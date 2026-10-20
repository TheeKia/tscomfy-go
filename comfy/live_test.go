package comfy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
)

// Live tests run against a real ComfyUI 0.42 when TSCOMFY_GO_LIVE_URL names it (spec §9). They
// queue model-free prompts only: empty images, blurs and previews to the temp directory, a small
// upload to the input directory. They cancel only the jobs they queued.

func live(t *testing.T) (*comfy.Client, context.Context) {
	t.Helper()
	url := os.Getenv("TSCOMFY_GO_LIVE_URL")
	if url == "" {
		t.Skip("set TSCOMFY_GO_LIVE_URL to run the live tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	c, err := comfy.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, ctx
}

// fresh is a color no earlier run used, so the server's cache doesn't skip the work.
func fresh() int { return rand.IntN(1 << 24) }

func small() comfy.Prompt {
	return comfy.Prompt(fmt.Sprintf(`{"1":{"class_type":"EmptyImage","inputs":{"width":16,"height":16,"batch_size":1,"color":%d}},"preview":{"class_type":"PreviewImage","inputs":{"images":["1",0]}}}`, fresh()))
}

// slow is a dozen blurs of a large image, one node each: long enough to act on mid-run.
func slow() comfy.Prompt {
	var b strings.Builder
	fmt.Fprintf(&b, `{"0":{"class_type":"EmptyImage","inputs":{"width":2048,"height":2048,"batch_size":1,"color":%d}}`, fresh())
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, `,"blur%d":{"class_type":"ImageBlur","inputs":{"image":["%s",0],"blur_radius":31,"sigma":10}}`, i, previous(i))
	}
	b.WriteString(`,"preview":{"class_type":"PreviewImage","inputs":{"images":["blur12",0]}}}`)
	return comfy.Prompt(b.String())
}

func previous(i int) string {
	if i == 1 {
		return "0"
	}
	return fmt.Sprintf("blur%d", i-1)
}

func TestLiveRunAndDownload(t *testing.T) {
	c, ctx := live(t)
	job, err := c.Submit(ctx, small(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var started, output bool
	for ev := range job.Events(ctx) {
		switch ev.(type) {
		case comfy.StartedEvent:
			started = true
		case comfy.OutputEvent:
			output = true
		}
	}
	res, err := job.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := res.Files("preview")
	if !started || !output || len(files) != 1 || files[0].Type != comfy.FolderTemp {
		t.Fatalf("started %v, output %v, files %+v", started, output, files)
	}
	f, err := c.Open(ctx, files[0].FileRef)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("\x89PNG")) || f.ContentType != "image/png" {
		t.Errorf("downloaded %q (%s), %v", head, f.ContentType, err)
	}
}

func TestLiveA63BitIntegerTravelsExactly(t *testing.T) {
	c, ctx := live(t)
	p := comfy.Prompt(`{"1":{"class_type":"PrimitiveInt","inputs":{"value":9223372036854775807}},"shown":{"class_type":"PreviewAny","inputs":{"source":["1",0]}}}`)
	res, err := c.Run(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := res.Output("shown"); !ok || len(out.Text) != 1 || out.Text[0] != "9223372036854775807" {
		t.Errorf("the node showed %+v", out)
	}
}

func TestLiveSaveImageWebsocketIsAPreview(t *testing.T) {
	c, ctx := live(t)
	p := comfy.Prompt(fmt.Sprintf(`{"1":{"class_type":"EmptyImage","inputs":{"width":8,"height":8,"batch_size":1,"color":%d}},"socket":{"class_type":"SaveImageWebsocket","inputs":{"images":["1",0]}}}`, fresh()))
	job, err := c.Submit(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	var previews []comfy.PreviewEvent
	for ev := range job.Events(ctx) {
		if p, ok := ev.(comfy.PreviewEvent); ok {
			previews = append(previews, p)
		}
	}
	if _, err := job.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 || previews[0].Node != "socket" || previews[0].MIME != "image/png" {
		t.Errorf("previews %+v", previews)
	}
}

func TestLiveTheServerRejects(t *testing.T) {
	c, ctx := live(t)
	p := comfy.Prompt(`{"1":{"class_type":"EmptyImage","inputs":{"width":8,"height":8,"batch_size":1,"color":0}},"2":{"class_type":"ImageBlur","inputs":{"image":["1",0],"blur_radius":99,"sigma":1}},"3":{"class_type":"PreviewImage","inputs":{"images":["2",0]}}}`)
	_, err := c.Submit(ctx, p, nil)
	rejected, ok := errors.AsType[*comfy.RejectedError](err)
	if !ok || len(rejected.NodeErrors) != 1 || rejected.NodeErrors[0].Node != "2" || rejected.NodeErrors[0].Input != "blur_radius" {
		t.Errorf("Submit = %v", err)
	}
}

func TestLiveANodeErrorFailsTheJob(t *testing.T) {
	c, ctx := live(t)
	p := comfy.Prompt(fmt.Sprintf(`{"1":{"class_type":"PrimitiveInt","inputs":{"value":%d}},"math":{"class_type":"ComfyMathExpression","inputs":{"expression":"a / 0","values.a":["1",0]}},"3":{"class_type":"PreviewAny","inputs":{"source":["math",0]}}}`, fresh()))
	_, err := c.Run(ctx, p, nil)
	execErr, ok := errors.AsType[*comfy.ExecutionError](err)
	if !ok || execErr.Node != "math" || execErr.ExceptionType != "ZeroDivisionError" {
		t.Errorf("Run = %v", err)
	}
}

func TestLiveCancelRunningAndQueued(t *testing.T) {
	c, ctx := live(t)
	running, err := c.Submit(ctx, slow(), nil)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := c.Submit(ctx, slow(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range running.Events(ctx) {
		if _, ok := ev.(comfy.ExecutingEvent); ok {
			break
		}
	}
	if err := queued.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queued.Wait(ctx); !errors.Is(err, comfy.ErrCancelled) {
		t.Errorf("the queued job: %v", err)
	}
	if _, err := running.Wait(ctx); !errors.Is(err, comfy.ErrCancelled) {
		t.Errorf("the running job: %v", err)
	}
}

func TestLiveAnUploadedImageFeedsLoadImage(t *testing.T) {
	c, ctx := live(t)
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(fresh())
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	uploaded, err := c.Upload(ctx, "tscomfy-go-live.png", &buf, &comfy.UploadOptions{Subfolder: "tscomfy-go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uploaded.Value, "tscomfy-go/tscomfy-go-live") {
		t.Errorf("uploaded as %+v", uploaded)
	}
	p := comfy.Prompt(fmt.Sprintf(`{"1":{"class_type":"LoadImage","inputs":{"image":%q}},"preview":{"class_type":"PreviewImage","inputs":{"images":["1",0]}}}`, uploaded.Value))
	res, err := c.Run(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files("preview")) != 1 {
		t.Errorf("files %+v", res.Files())
	}
}

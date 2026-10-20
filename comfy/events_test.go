package comfy_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

func TestSamplerProgressAndPreviews(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "sampler", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, &comfy.SubmitOptions{Previews: comfy.PreviewLatent2RGB})
		var progress, previews int
		var last comfy.ProgressEvent
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.ProgressEvent:
				progress++
				if ev.Max > 1 {
					last = ev
				}
			case comfy.PreviewEvent:
				previews++
				if ev.MIME != "image/jpeg" || len(ev.Data) == 0 || ev.Node == "" {
					t.Errorf("preview %+v", ev)
				}
			}
		}
		if progress == 0 || previews != 6 || last.Value != last.Max {
			t.Errorf("%d progress events (last %+v), %d previews", progress, last, previews)
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSaveImageWebsocketArrivesAsAPreview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "websocket", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var png, text bool
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.PreviewEvent:
				png = ev.MIME == "image/png" && bytes.HasPrefix(ev.Data, []byte("\x89PNG"))
			case comfy.OutputEvent:
				text = text || len(ev.Output.Text) > 0
			}
		}
		res, err := job.Wait(t.Context())
		if err != nil || !png || !text {
			t.Errorf("png %v, text %v, error %v", png, text, err)
		}
		var withText bool
		for out := range res.All() {
			withText = withText || len(out.Text) > 0
		}
		if !withText {
			t.Error("the result has no text output")
		}
	})
}

func TestProgressTextAndABlockedNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "blocked", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var texts []comfy.TextEvent
		var blocked []comfy.BlockedEvent
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.TextEvent:
				texts = append(texts, ev)
			case comfy.BlockedEvent:
				blocked = append(blocked, ev)
			}
		}
		res, err := job.Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(texts) != 1 || texts[0].Node == "" || texts[0].Text == "" {
			t.Errorf("text events %+v", texts)
		}
		if len(blocked) != 1 || blocked[0].Message == "" || !slices.Equal(res.Blocked(), blocked) {
			t.Errorf("blocked events %+v, result %+v", blocked, res.Blocked())
		}
	})
}

func TestALaggingReaderGetsTheLatestProgressAndPreviewPerNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "sampler", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, &comfy.SubmitOptions{Previews: comfy.PreviewAuto})
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Read only now: one preview and one progress per node are left of each run of them.
		perNode := map[string]int{}
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.ProgressEvent:
				perNode["progress "+string(ev.Node)]++
			case comfy.PreviewEvent:
				perNode["preview "+string(ev.Node)]++
			}
		}
		for k, n := range perNode {
			if n != 1 {
				t.Errorf("%s: %d unread events kept", k, n)
			}
		}
	})
}

func TestEventsContinueAfterABreakAndEndOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var first comfy.Event
		for ev := range job.Events(t.Context()) {
			first = ev
			break
		}
		if _, ok := first.(comfy.StartedEvent); !ok {
			t.Fatalf("first event %#v", first)
		}
		rest := events(t, job)
		if len(rest) == 0 {
			t.Fatal("nothing after the break")
		}
		if _, ok := rest[0].(comfy.CachedEvent); !ok {
			t.Errorf("the stream didn't continue where it stopped: %#v", rest[0])
		}
		if again := events(t, job); len(again) != 0 {
			t.Errorf("an ended stream yielded %d more", len(again))
		}
	})
}

func TestEventsFromTwoGoroutinesPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 2})
		job := submit(t, c, r, nil)
		reading := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range job.Events(t.Context()) {
				close(reading)
				<-time.After(time.Hour)
				return
			}
		}()
		<-reading
		func() {
			defer func() {
				if recover() == nil {
					t.Error("a second reader didn't panic")
				}
			}()
			for range job.Events(t.Context()) {
				t.Error("the second reader got an event")
			}
		}()
		<-done
	})
}

// A run whose node reports many progress texts while the reader waits.
func TestTextEventsAreCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		var extra []jsontext.Value
		for i := range 300 {
			extra = append(extra, jsontext.Value(fmt.Sprintf(`{"binary":3,"node":"1","text":"step %d"}`, i)))
		}
		// Right after execution_start, so the texts belong to the job whose prompt runs.
		r.Events = slices.Concat(r.Events[:1], extra, r.Events[1:])
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		var texts []string
		for ev := range job.Events(t.Context()) {
			if e, ok := ev.(comfy.TextEvent); ok {
				texts = append(texts, e.Text)
			}
		}
		if len(texts) != 256 || texts[0] != "step 44" || texts[255] != "step 299" {
			t.Errorf("%d texts kept, from %q to %q", len(texts), texts[0], texts[len(texts)-1])
		}
	})
}

func TestALargePreviewFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "websocket", 0)
		big := bytes.Repeat([]byte{0xab}, 8<<20)
		for i, e := range r.Events {
			var frame map[string]jsontext.Value
			if err := json.Unmarshal(e, &frame); err != nil {
				t.Fatal(err)
			}
			if string(frame["binary"]) != "4" {
				continue
			}
			frame["image"] = jsontext.Value(`"` + base64.StdEncoding.EncodeToString(big) + `"`)
			data, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			r.Events[i] = data
		}
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var size int
		for ev := range job.Events(t.Context()) {
			if p, ok := ev.(comfy.PreviewEvent); ok {
				size = len(p.Data)
			}
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if size != len(big) {
			t.Errorf("a preview of %d bytes arrived, want %d", size, len(big))
		}
	})
}

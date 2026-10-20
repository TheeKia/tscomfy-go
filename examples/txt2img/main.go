// Command txt2img serves POST /generate: it builds the txt2img prompt from the request with the
// Go that go generate wrote from workflows/txt2img.ts, runs it on ComfyUI, and streams the job's
// progress and previews to the caller as server-sent events, then the URLs of the saved images.
// When the caller goes away, the job is cancelled.
//
//	go run ./examples/txt2img -comfy http://127.0.0.1:8188
//	curl -N -d '{"prompt":"a fox in the snow","seed":42}' http://127.0.0.1:8080/generate
//
// The example's schema is a mock: run it against a server that has the checkpoint model-a.safetensors,
// or regenerate workflows/comfy/local from your own server with tscomfy generate --url.
package main

//go:generate go tool tscomfy-go -workflows ./workflows -out ./internal/workflows

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/examples/txt2img/internal/workflows"
)

func main() {
	comfyURL := flag.String("comfy", "http://127.0.0.1:8188", "the ComfyUI server")
	listen := flag.String("listen", "127.0.0.1:8080", "the address to serve on")
	flag.Parse()
	if err := serve(*comfyURL, *listen); err != nil {
		slog.Error("txt2img", "error", err)
		os.Exit(1)
	}
}

func serve(comfyURL, listen string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client, err := comfy.Dial(ctx, comfyURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }() // Close is always nil
	mux := http.NewServeMux()
	mux.Handle("POST /generate", generate(client))
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	slog.Info("txt2img: serving", "address", listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// request is the body of POST /generate. Steps is optional: the workflow's own value otherwise.
type request struct {
	Prompt string `json:"prompt"`
	Seed   uint64 `json:"seed"`
	Steps  *int   `json:"steps"`
}

// generate runs txt2img for each request and streams the job as server-sent events: progress
// (the sampler's steps), preview (a data URL), then image (a URL per saved image) or error.
func generate(client *comfy.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 1<<20), &req); err != nil {
			http.Error(w, "the body is not a request: "+err.Error(), http.StatusBadRequest)
			return
		}
		prompt, err := workflows.Txt2Img{Prompt: req.Prompt, Seed: req.Seed, Steps: req.Steps}.Build()
		if err != nil {
			// A value outside its input's bounds, such as 0 steps.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := client.Submit(r.Context(), prompt, &comfy.SubmitOptions{Previews: comfy.PreviewAuto})
		if rejected, ok := errors.AsType[*comfy.RejectedError](err); ok {
			http.Error(w, rejected.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// The job outlives the request unless it is cancelled: do that when the caller goes away.
		stopCancel := context.AfterFunc(r.Context(), func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = job.Cancel(ctx)
		})
		defer stopCancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		send := func(event string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
			_ = http.NewResponseController(w).Flush()
		}
		for ev := range job.Events(r.Context()) {
			switch ev := ev.(type) {
			case comfy.ProgressEvent:
				if ev.Node == workflows.Txt2ImgSampler {
					send("progress", map[string]float64{"value": ev.Value, "max": ev.Max})
				}
			case comfy.PreviewEvent:
				send("preview", "data:"+ev.MIME+";base64,"+base64.StdEncoding.EncodeToString(ev.Data))
			}
		}
		res, err := job.Wait(r.Context())
		if err != nil {
			send("error", err.Error())
			return
		}
		for _, f := range res.Files(workflows.Txt2ImgSave) {
			send("image", client.FileURL(f.FileRef))
		}
	})
}

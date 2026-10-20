package comfy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
)

// A prompt as generated code builds it; here, raw API JSON.
var prompt = comfy.Prompt(`{
  "1": {"class_type": "EmptyImage", "inputs": {"width": 64, "height": 64, "batch_size": 1, "color": 0}},
  "save": {"class_type": "SaveImage", "inputs": {"images": ["1", 0], "filename_prefix": "example"}}
}`)

// One client per ComfyUI server, shared by every job; Close it when the backend stops.
func ExampleDial() {
	ctx := context.Background()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err) // comfy.ErrUnsupportedServer for a server older than ComfyUI 0.42
		return
	}
	defer c.Close()
	fmt.Println(c.FileURL(comfy.FileRef{Filename: "a.png", Type: comfy.FolderOutput}))
}

// Run suits a synchronous handler: its context governs the job.
func ExampleClient_Run() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()
	res, err := c.Run(ctx, prompt, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, f := range res.Files("save") {
		fmt.Println(f.Filename)
	}
}

// Submit returns at once; the job goes on until it ends or Cancel. Its events stream progress
// and previews, and Wait gives the outcome.
func ExampleClient_Submit() {
	ctx := context.Background()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()
	job, err := c.Submit(ctx, prompt, &comfy.SubmitOptions{Previews: comfy.PreviewAuto})
	if err != nil {
		fmt.Println(err)
		return
	}
	for ev := range job.Events(ctx) {
		switch ev := ev.(type) {
		case comfy.ProgressEvent:
			fmt.Printf("%s: %.0f of %.0f\n", ev.Node, ev.Value, ev.Max)
		case comfy.PreviewEvent:
			fmt.Printf("%s: a %s preview of %d bytes\n", ev.Node, ev.MIME, len(ev.Data))
		}
	}
	res, err := job.Wait(ctx)
	var execErr *comfy.ExecutionError
	switch {
	case errors.As(err, &execErr):
		fmt.Println("node", execErr.Node, "failed:", execErr.Message)
	case errors.Is(err, comfy.ErrCancelled):
		fmt.Println("cancelled")
	case err != nil:
		fmt.Println(err)
	default:
		fmt.Println(len(res.Files()), "files")
	}
}

// A handler that streams events cancels the job when its request ends.
func ExampleJob_Cancel() {
	ctx := context.Background()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()
	job, err := c.Submit(ctx, prompt, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	request, done := context.WithTimeout(ctx, time.Second) // the request's context
	defer done()
	select {
	case <-job.Done():
	case <-request.Done():
		if err := job.Cancel(context.Background()); err != nil {
			fmt.Println(err)
		}
	}
}

func ExampleClient_Upload() {
	ctx := context.Background()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()
	f, err := os.Open("cat.png")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	uploaded, err := c.Upload(ctx, "cat.png", f, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(uploaded.Value) // for a LoadImage image parameter
}

func ExampleClient_Open() {
	ctx := context.Background()
	c, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()
	f, err := c.Open(ctx, comfy.FileRef{Filename: "example_00001_.png", Type: comfy.FolderOutput})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(os.Stdout, f); err != nil {
		fmt.Println(err)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

// The handler against the fake ComfyUI, which plays a recorded run whatever the prompt.
func TestGenerate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{})
		defer srv.Close()
		client, err := comfy.Dial(t.Context(), srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		srv.Expect(comfytest.Recording("sampler")[0], comfytest.Behavior{})

		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/generate", strings.NewReader(`{"prompt":"a fox","seed":18446744073709551615}`))
		generate(client).ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("%d %s: %s", w.Code, w.Header().Get("Content-Type"), body)
		}
		if !strings.Contains(body, "event: preview\ndata: \"data:image/jpeg;base64,") {
			t.Errorf("no preview in\n%s", body)
		}
		var post comfytest.Request
		for _, req := range srv.Received() {
			if req.Path == "/prompt" {
				post = req
			}
		}
		if !strings.Contains(string(post.Body), `"seed":18446744073709551615`) || !strings.Contains(string(post.Body), `"text":"a fox"`) {
			t.Errorf("the prompt sent: %s", post.Body)
		}
	})
}

func TestGenerateRefusesBadValues(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/generate", strings.NewReader(`{"prompt":"a fox","seed":1,"steps":0}`))
	generate(nil).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "steps") {
		t.Errorf("%d: %s", w.Code, w.Body.String())
	}
}

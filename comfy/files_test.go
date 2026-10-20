package comfy_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

func TestUpload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		data := []byte("\x89PNG not really")
		got, err := c.Upload(t.Context(), "cat.png", bytes.NewReader(data), &comfy.UploadOptions{Subfolder: "pets"})
		if err != nil {
			t.Fatal(err)
		}
		want := comfy.UploadedFile{Filename: "cat (1).png", Subfolder: "pets", Type: comfy.FolderInput, Value: "pets/cat (1).png"}
		if got != want {
			t.Errorf("Upload = %+v, want %+v", got, want)
		}
		posts := requests(srv, http.MethodPost, "/upload/image")
		if len(posts) != 1 || !bytes.Contains(posts[0].Body, data) || !strings.HasPrefix(posts[0].Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("the server got %+v", posts)
		}
		got, err = c.Upload(t.Context(), "cat.png", bytes.NewReader(data), &comfy.UploadOptions{Type: comfy.FolderTemp, Overwrite: true})
		if err != nil || got.Value != "cat.png [temp]" || got.Type != comfy.FolderTemp {
			t.Errorf("an overwriting upload to temp = %+v, %v", got, err)
		}
	})
}

func TestUploadRefusesBadInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		if _, err := c.Upload(t.Context(), "", strings.NewReader("x"), nil); err == nil {
			t.Error("an upload without a name")
		}
		if _, err := c.Upload(t.Context(), "a.png", strings.NewReader("x"), &comfy.UploadOptions{Type: "models"}); err == nil {
			t.Error("an upload to an unknown folder")
		}
		if n := len(requests(srv, http.MethodPost, "/upload/image")); n != 0 {
			t.Errorf("%d uploads were sent", n)
		}
		broken := io.MultiReader(strings.NewReader("half"), errReader{})
		if _, err := c.Upload(t.Context(), "a.png", broken, nil); err == nil || !strings.Contains(err.Error(), "broken reader") {
			t.Errorf("an upload whose reader fails = %v", err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken reader") }

func TestOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		f, err := c.Open(t.Context(), comfy.FileRef{Filename: "a.png", Subfolder: "s", Type: comfy.FolderOutput})
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG")) || f.ContentType != "image/png" {
			t.Errorf("Open gave %q (%s), %v", data, f.ContentType, err)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
		views := requests(srv, http.MethodGet, "/view")
		if len(views) != 1 || views[0].Query != "filename=a.png&subfolder=s&type=output" {
			t.Errorf("views %+v", views)
		}
		_, err = c.Open(t.Context(), comfy.FileRef{Filename: "missing.png", Type: comfy.FolderOutput})
		if httpErr, ok := errors.AsType[*comfy.HTTPError](err); !ok || httpErr.StatusCode != http.StatusNotFound {
			t.Errorf("Open of a missing file = %v", err)
		}
	})
}

func TestFileURLKeepsThePathAndQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{PathPrefix: "/comfy"})
		defer srv.Close()
		c, err := comfy.Dial(t.Context(), srv.URL()+"?token=t", &comfy.Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		got := c.FileURL(comfy.FileRef{Filename: "a b.png", Type: comfy.FolderTemp})
		if got != "http://comfy.test/comfy/view?filename=a+b.png&subfolder=&token=t&type=temp" {
			t.Errorf("FileURL = %s", got)
		}
	})
}

func TestFilesAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, c := fake(t)
		_ = c.Close()
		if _, err := c.Upload(t.Context(), "a.png", strings.NewReader("x"), nil); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Upload = %v", err)
		}
		if _, err := c.Open(t.Context(), comfy.FileRef{Filename: "a.png", Type: comfy.FolderOutput}); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Open = %v", err)
		}
	})
}

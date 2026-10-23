package comfy_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

// fake starts a fake server and a client of it; both close when the test ends. The test must run
// inside synctest.Test: the fake waits on fake time, and a goroutine left blocked fails the test.
func fake(t *testing.T) (*comfytest.Server, *comfy.Client) {
	t.Helper()
	srv := comfytest.New(comfytest.Options{})
	t.Cleanup(srv.Close)
	c, err := comfy.Dial(t.Context(), srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return srv, c
}

func run(t *testing.T, name string, i int) comfytest.Run {
	t.Helper()
	runs := comfytest.Recording(name)
	if i >= len(runs) {
		t.Fatalf("recording %s has %d runs", name, len(runs))
	}
	return runs[i]
}

func TestSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		res, err := job.Wait(t.Context())
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		files := res.Files()
		if len(files) != 2 {
			t.Fatalf("Files() = %+v, want 2 files", files)
		}
		if files[0].Type != comfy.FolderOutput || files[0].Key != "images" || !strings.HasPrefix(files[0].Filename, "tscomfy-capture/") && files[0].Subfolder == "" {
			t.Errorf("the saved file is %+v", files[0])
		}
		if ids := srv.JobIDs(); !slices.Equal(ids, []string{job.ID()}) {
			t.Errorf("the server queued %v, want %s", ids, job.ID())
		}
	})
}

func TestEventsOfARun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.StartedEvent:
				kinds = append(kinds, "started")
			case comfy.CachedEvent:
				kinds = append(kinds, "cached")
			case comfy.ExecutingEvent:
				kinds = append(kinds, "executing "+string(ev.Node))
			case comfy.ProgressEvent:
				kinds = append(kinds, "progress "+string(ev.Node))
			case comfy.OutputEvent:
				kinds = append(kinds, "output "+string(ev.Node))
			}
		}
		if len(kinds) < 4 || kinds[0] != "started" || kinds[1] != "cached" {
			t.Errorf("events: %v", kinds)
		}
		if !slices.Contains(kinds, "output 3") || !slices.Contains(kinds, "output 4") {
			t.Errorf("events have no outputs of nodes 3 and 4: %v", kinds)
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		for i, r := range comfytest.Recording("rejected") {
			srv.Expect(r, comfytest.Behavior{})
			job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
			rejected, ok := errors.AsType[*comfy.RejectedError](err)
			if job != nil || !ok {
				t.Fatalf("run %d: Submit = %v, %v; want a *RejectedError", i, job, err)
			}
			if rejected.Type == "" || rejected.Message == "" {
				t.Errorf("run %d: %+v", i, rejected)
			}
		}
	})
}

func TestDialChecksTheServer(t *testing.T) {
	tests := []struct {
		name string
		opts comfytest.Options
		want error
	}{
		{"an old version", comfytest.Options{Version: "0.41.3"}, comfy.ErrUnsupportedServer},
		{"no version", comfytest.Options{Version: "unknown"}, comfy.ErrUnsupportedServer},
		{"no feature flags", comfytest.Options{NoFeatureFlags: true}, comfy.ErrUnsupportedServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				srv := comfytest.New(tt.opts)
				defer srv.Close()
				c, err := comfy.Dial(t.Context(), srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
				if !errors.Is(err, tt.want) || c != nil {
					t.Errorf("Dial = %v, %v; want %v", c, err, tt.want)
				}
				synctest.Wait()
				if n := srv.OpenSockets(); n != 0 {
					t.Errorf("%d sockets stayed open", n)
				}
			})
		})
	}
}

func TestNewConnectsOnFirstSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{})
		defer srv.Close()
		c, err := comfy.New(srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = c.Close() }()
		synctest.Wait()
		if got := srv.Received(); len(got) != 0 {
			t.Fatalf("New sent %d requests, want none", len(got))
		}
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		var paths []string
		for _, req := range srv.Received() {
			paths = append(paths, req.Method+" "+req.Path)
		}
		if len(paths) < 3 || paths[0] != "GET /system_stats" || paths[1] != "GET /ws" || paths[2] != "POST /prompt" {
			t.Errorf("requests = %v, want the version check, the websocket, then the prompt", paths)
		}
	})
}

func TestNewChecksTheServerOnSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{Version: "0.41.3"})
		defer srv.Close()
		c, err := comfy.New(srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if _, err := c.Submit(t.Context(), comfy.Prompt(run(t, "success", 0).Prompt), nil); !errors.Is(err, comfy.ErrUnsupportedServer) {
			t.Errorf("Submit = %v, want ErrUnsupportedServer", err)
		}
		synctest.Wait()
		if n := len(srv.Sockets()); n != 0 {
			t.Errorf("%d sockets were opened", n)
		}
	})
}

func TestNewBeforeTheServerIsUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{})
		defer srv.Close()
		srv.RefuseSockets(true)
		c, err := comfy.New(srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		r := run(t, "success", 0)
		if _, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil); err == nil {
			t.Fatal("Submit succeeded while the server refused sockets")
		}
		srv.RefuseSockets(false)
		srv.Expect(r, comfytest.Behavior{})
		job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if err != nil {
			t.Fatalf("Submit once the server is up: %v", err)
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Errorf("Wait: %v", err)
		}
	})
}

func TestNewRefusesAnInvalidURL(t *testing.T) {
	for _, raw := range []string{"ftp://comfy.test", "http://user:pass@comfy.test", "http://comfy.test/#x"} {
		if c, err := comfy.New(raw, nil); err == nil || c != nil {
			t.Errorf("New(%q) = %v, %v; want an error", raw, c, err)
		}
	}
}

func TestPing(t *testing.T) {
	tests := []struct {
		name string
		opts comfytest.Options
		want error
	}{
		{"a supported server", comfytest.Options{}, nil},
		{"an old version", comfytest.Options{Version: "0.41.3"}, comfy.ErrUnsupportedServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				srv := comfytest.New(tt.opts)
				defer srv.Close()
				c, err := comfy.New(srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
				if err != nil {
					t.Fatal(err)
				}
				if err := c.Ping(t.Context()); !errors.Is(err, tt.want) {
					t.Errorf("Ping = %v, want %v", err, tt.want)
				}
				if n := len(srv.Sockets()); n != 0 {
					t.Errorf("Ping opened %d sockets", n)
				}
				_ = c.Close()
				if err := c.Ping(t.Context()); !errors.Is(err, comfy.ErrClosed) {
					t.Errorf("Ping after Close = %v, want ErrClosed", err)
				}
			})
		})
	}
}

func TestCloseEndsOpenJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 5})
		job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Wait after Close = %v", err)
		}
		if _, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Submit after Close = %v", err)
		}
	})
}

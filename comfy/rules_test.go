package comfy_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

// Tests of the settling rules (spec §8, rules 4, 5 and 14) and other paths the recordings don't
// take as they are.

// withoutStart is a run whose start events the client misses, as when they went to another socket.
func withoutStart(r comfytest.Run) comfytest.Run {
	r.Events = slices.DeleteFunc(slices.Clone(r.Events), func(e jsontext.Value) bool {
		var m struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e, &m)
		return m.Type == "execution_start" || m.Type == "execution_cached"
	})
	return r
}

const statusEvent = `{"type":"status","data":{"status":{"exec_info":{"queue_remaining":1}}}}`

func TestARecordThatVanishesAfterTheStartIsLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		// Forgotten after it started, the socket up all along: a restart, not a cancel.
		srv.Expect(r, comfytest.Behavior{ForgetBefore: 5})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); !errors.Is(err, comfy.ErrJobLost) {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestTheListReadSeesAJobStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := withoutStart(run(t, "success", 0))
		// A queue change while it runs: the list read finds it in progress.
		r.Events = slices.Insert(r.Events, 0, jsontext.Value(statusEvent))
		srv.Expect(r, comfytest.Behavior{Gap: 2 * time.Second, ForgetBefore: 4})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); !errors.Is(err, comfy.ErrJobLost) {
			t.Errorf("Wait = %v, want ErrJobLost: the list said the job had started", err)
		}
	})
}

func TestTheRecordSeesAJobStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := withoutStart(run(t, "success", 0))
		// Slow enough that the 30 s poll reads the record in progress before the job is forgotten.
		srv.Expect(r, comfytest.Behavior{Gap: 20 * time.Second, ForgetBefore: 3})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); !errors.Is(err, comfy.ErrJobLost) {
			t.Errorf("Wait = %v, want ErrJobLost: the record said the job had started", err)
		}
	})
}

func TestOwnCancelAfterADropIsACancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		srv.DropSockets()
		synctest.Wait()
		if err := job.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrCancelled) {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestTheListReadSettlesAJobCancelledWhileQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		time.Sleep(5 * time.Second)
		cancelElsewhere(t, srv, job.ID())
		start := time.Now()
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrCancelled) {
			t.Errorf("Wait = %v", err)
		}
		// Within the list read's second, not at the 30 s poll.
		if d := time.Since(start); d > time.Second {
			t.Errorf("settling took %v", d)
		}
	})
}

// cancelElsewhere cancels a job as another client would.
func cancelElsewhere(t *testing.T, srv *comfytest.Server, id string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/api/jobs/"+id+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestListReadsAreThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := withoutStart(run(t, "success", 0))
		var burst []jsontext.Value
		for range 20 {
			burst = append(burst, jsontext.Value(statusEvent))
		}
		r.Events = slices.Concat(burst, r.Events)
		srv.Expect(r, comfytest.Behavior{Gap: 10 * time.Millisecond})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Twenty changes in 0.2 s: a read at once and one at the end of the second, at most.
		var lists int
		for _, req := range requests(srv, http.MethodGet, "/api/jobs") {
			if req.Path == "/api/jobs" {
				lists++
			}
		}
		if lists < 1 || lists > 3 {
			t.Errorf("%d list reads", lists)
		}
	})
}

func TestNodesExpandedAtRunTimeAreShownAsTheirNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		output := `{"images":[{"filename":"a.png","subfolder":"","type":"output"}]}`
		r := comfytest.Run{
			Prompt: jsontext.Value(`{"5":{"class_type":"Group","inputs":{}}}`),
			Answer: comfytest.Exchange{Status: http.StatusOK, Body: jsontext.Value(`{"prompt_id":"PROMPT_ID","number":1,"node_errors":{}}`)},
			Events: []jsontext.Value{
				jsontext.Value(`{"type":"execution_start","data":{"prompt_id":"PROMPT_ID"}}`),
				jsontext.Value(`{"type":"progress_state","data":{"prompt_id":"PROMPT_ID","nodes":{"5.0.0.1":{"value":1,"max":2,"node_id":"5.0.0.1","display_node_id":"5"}}}}`),
				jsontext.Value(`{"type":"executed","data":{"node":"5.0.0.1","display_node":"5","output":` + output + `,"prompt_id":"PROMPT_ID"}}`),
				jsontext.Value(`{"binary":3,"node":"5.0.0.1","text":"done"}`),
				jsontext.Value(`{"type":"executing","data":{"node":null,"prompt_id":"PROMPT_ID"}}`),
			},
			Record: &comfytest.Exchange{Status: http.StatusOK, Body: jsontext.Value(`{"id":"PROMPT_ID","status":"completed","outputs":{"5.0.0.1":` + output + `}}`)},
		}
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var nodes []comfy.NodeID
		for ev := range job.Events(t.Context()) {
			switch ev := ev.(type) {
			case comfy.ProgressEvent:
				nodes = append(nodes, ev.Node)
			case comfy.OutputEvent:
				nodes = append(nodes, ev.Node)
			case comfy.TextEvent:
				nodes = append(nodes, ev.Node)
			}
		}
		res, err := job.Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(nodes, []comfy.NodeID{"5", "5", "5"}) {
			t.Errorf("events of %v", nodes)
		}
		if files := res.Files("5"); len(files) != 1 {
			t.Errorf("Files(5) = %+v", res.Files())
		}
	})
}

// loopRun is a loop of three iterations as ComfyUI 0.42's StartLoop expands it: each iteration
// runs its own copy of the save node, displayed as "save". The ids' byte order (0, 10, 2) isn't
// their execution order (0, 2, 10).
func loopRun(live bool) comfytest.Run {
	r := comfytest.Run{
		Prompt: jsontext.Value(`{"start":{"class_type":"StartLoop","inputs":{}},"save":{"class_type":"SaveImage","inputs":{}},"end":{"class_type":"EndLoop","inputs":{}}}`),
		Answer: comfytest.Exchange{Status: http.StatusOK, Body: jsontext.Value(`{"prompt_id":"PROMPT_ID","number":1,"node_errors":{}}`)},
		Events: []jsontext.Value{jsontext.Value(`{"type":"execution_start","data":{"prompt_id":"PROMPT_ID"}}`)},
	}
	var outputs []string
	for i, iteration := range []string{"0", "2", "10"} {
		id := "start.0.0." + iteration + "_save"
		output := `{"images":[{"filename":"` + string(rune('a'+i)) + `.png","subfolder":"","type":"output"}]}`
		if live {
			r.Events = append(r.Events, jsontext.Value(`{"type":"executed","data":{"node":"`+id+`","display_node":"save","output":`+output+`,"prompt_id":"PROMPT_ID"}}`))
		}
		outputs = append(outputs, `"`+id+`":`+output)
	}
	r.Events = append(r.Events, jsontext.Value(`{"type":"executing","data":{"node":null,"prompt_id":"PROMPT_ID"}}`))
	r.Record = &comfytest.Exchange{Status: http.StatusOK, Body: jsontext.Value(`{"id":"PROMPT_ID","status":"completed","outputs":{` + strings.Join(outputs, ",") + `}}`)}
	return r
}

func filenames(files []comfy.OutputFile) []string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Filename
	}
	return names
}

func TestEveryIterationOfALoopKeepsItsOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := loopRun(true)
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := filenames(res.Files("save")); !slices.Equal(got, []string{"a.png", "b.png", "c.png"}) {
			t.Errorf("Files(save) = %v, want every iteration's, in execution order", got)
		}
		if out, ok := res.Output("save"); !ok || !slices.Equal(filenames(out.Files), []string{"c.png"}) {
			t.Errorf("Output(save) = %+v, %v; want the last iteration's", out, ok)
		}
		var all int
		for out := range res.All() {
			if out.Node != "save" {
				t.Errorf("All yields node %q", out.Node)
			}
			all++
		}
		if all != 3 {
			t.Errorf("All yields %d outputs, want 3", all)
		}
	})
}

func TestALoopsOutputsMissedLiveKeepTheRecordsOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := loopRun(false) // the executed messages went to a socket that dropped
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := filenames(res.Files()); !slices.Equal(got, []string{"a.png", "b.png", "c.png"}) {
			t.Errorf("Files() = %v, want the record's order", got)
		}
	})
}

func TestSaveImageWebsocketImagesAreAllKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "websocket", 0)
		// A batch of three: three frames of one node.
		at := slices.IndexFunc(r.Events, func(e jsontext.Value) bool { return strings.Contains(string(e), `"binary"`) })
		if at < 0 {
			t.Fatal("the recording has no frame")
		}
		r.Events = slices.Insert(r.Events, at, r.Events[at], r.Events[at])
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		var images int
		for ev := range job.Events(t.Context()) {
			if p, ok := ev.(comfy.PreviewEvent); ok && p.MIME == "image/png" {
				images++
			}
		}
		if images != 3 {
			t.Errorf("a lagging reader got %d of 3 images", images)
		}
	})
}

func TestLoneSurrogatesFromPythonDecode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "websocket", 0)
		r.Record = &comfytest.Exchange{Status: http.StatusOK, Body: jsontext.Value(`{"id":"PROMPT_ID","status":"completed","outputs":{"9":{"text":["bad \udc80 byte"]}}}`)}
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if out, ok := res.Output("9"); !ok || len(out.Text) != 1 || !strings.Contains(out.Text[0], "�") {
			t.Errorf("Output(9) = %+v, %v", out, ok)
		}
	})
}

func TestTheReconnectLoopStopsWhenItsJobsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		srv.RefuseSockets(true)
		srv.DropSockets()
		synctest.Wait()
		if err := job.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrCancelled) {
			t.Fatalf("Wait = %v", err)
		}
		time.Sleep(5 * time.Minute)
		// The first socket, and at most the attempt that was due when the job ended.
		if n := len(srv.Sockets()); n > 3 {
			t.Errorf("%d handshakes: the reconnect went on without jobs", n)
		}
	})
}

func TestADeafNewSocketStillServesTheNextJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{DropBefore: 6, Hazard: true})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		// The hazard unregisters the next socket of the old client id; a client with a fresh id
		// per socket hears the next job's events.
		srv.Expect(r, comfytest.Behavior{})
		if got := events(t, submit(t, c, r, nil)); len(got) < 5 {
			t.Errorf("the next job got %d events", len(got))
		}
	})
}

func TestDialReportsItsOwnDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{NoFeatureFlags: true})
		defer srv.Close()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := comfy.Dial(ctx, srv.URL(), &comfy.Options{HTTPClient: srv.Client()})
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, comfy.ErrUnsupportedServer) {
			t.Errorf("Dial = %v", err)
		}
	})
}

func TestSubmitOnASilentIdleSocketEndsAtItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		srv.Silence()
		time.Sleep(time.Minute)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		if _, err := c.Submit(ctx, comfy.Prompt(`{}`), nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Second {
			t.Errorf("Submit = %v after %v", err, time.Since(start))
		}
	})
}

func TestCloseAbortsSubmitAndCancelReturnsErrClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 3})
		job := submit(t, c, r, nil)
		srv.Expect(r, comfytest.Behavior{AnswerDelay: time.Hour})
		errs := make(chan error, 1)
		go func() {
			_, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
			errs <- err
		}()
		time.Sleep(time.Second)
		_ = c.Close()
		if err := <-errs; !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("the Submit in flight = %v", err)
		}
		if err := job.Cancel(t.Context()); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Cancel after Close = %v", err)
		}
	})
}

func TestCancelOfAJobThatEndedUnsettledIsSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "cancel", 0)
		srv.Expect(r, comfytest.Behavior{HoldUntilCancel: true})
		job := submit(t, c, r, nil)
		srv.RefuseSockets(true)
		srv.DropSockets()
		// The reconnect budget ends the job; it still runs on the server.
		if _, err := job.Wait(t.Context()); err == nil || errors.Is(err, comfy.ErrCancelled) {
			t.Fatalf("Wait = %v", err)
		}
		if err := job.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := cancelsOf(srv); !slices.Equal(got, []string{job.ID()}) {
			t.Errorf("cancelled %v", got)
		}
	})
}

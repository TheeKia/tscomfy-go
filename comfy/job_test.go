package comfy_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

// requests returns the requests to method and a path starting with prefix.
func requests(srv *comfytest.Server, method, prefix string) []comfytest.Request {
	var out []comfytest.Request
	for _, r := range srv.Received() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// cancelsOf returns the ids the server was asked to cancel.
func cancelsOf(srv *comfytest.Server) []string {
	var ids []string
	for _, r := range requests(srv, http.MethodPost, "/api/jobs/") {
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(r.Path, "/api/jobs/"), "/cancel"))
	}
	return ids
}

func submit(t *testing.T, c *comfy.Client, r comfytest.Run, opts *comfy.SubmitOptions) *comfy.Job {
	t.Helper()
	job, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), opts)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return job
}

// events drains a job's events.
func events(t *testing.T, job *comfy.Job) []comfy.Event {
	t.Helper()
	var all []comfy.Event
	for ev := range job.Events(t.Context()) {
		all = append(all, ev)
	}
	return all
}

func TestSubmitSendsTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, &comfy.SubmitOptions{
			Previews:  comfy.PreviewAuto,
			Targets:   []comfy.NodeID{"3"},
			Front:     true,
			ExtraData: map[string]jsontext.Value{"api_key_comfy_org": jsontext.Value(`"k"`)},
		})
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		posts := requests(srv, http.MethodPost, "/prompt")
		if len(posts) != 1 {
			t.Fatalf("%d posts", len(posts))
		}
		var body struct {
			ClientID string                    `json:"client_id"`
			PromptID string                    `json:"prompt_id"`
			Prompt   jsontext.Value            `json:"prompt"`
			Extra    map[string]jsontext.Value `json:"extra_data"`
			Front    bool                      `json:"front"`
			Targets  []string                  `json:"partial_execution_targets"`
		}
		if err := json.Unmarshal(posts[0].Body, &body, json.RejectUnknownMembers(true)); err != nil {
			t.Fatal(err)
		}
		uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		if !uuid.MatchString(body.PromptID) || body.PromptID != job.ID() || !uuid.MatchString(body.ClientID) {
			t.Errorf("ids: prompt %q, client %q", body.PromptID, body.ClientID)
		}
		if !bytes.Equal(body.Prompt, r.Prompt) {
			t.Errorf("the prompt changed on the way:\n%s\n%s", body.Prompt, r.Prompt)
		}
		if string(body.Extra["preview_method"]) != `"auto"` || string(body.Extra["api_key_comfy_org"]) != `"k"` || !body.Front || !slices.Equal(body.Targets, []string{"3"}) {
			t.Errorf("the body is %s", posts[0].Body)
		}
		sockets := srv.Sockets()
		if len(sockets) != 1 || !strings.Contains(sockets[0], "clientId="+body.ClientID) {
			t.Errorf("the socket's query is %v, want the client id %s", sockets, body.ClientID)
		}
	})
}

func TestSubmitRefusesBadInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		tests := []struct {
			name   string
			prompt comfy.Prompt
			opts   *comfy.SubmitOptions
			want   string
		}{
			{"a list", comfy.Prompt(`[1]`), nil, "not a JSON object"},
			{"invalid JSON", comfy.Prompt(`{"a":`), nil, "not a JSON object"},
			{"client_id in ExtraData", comfy.Prompt(`{}`), &comfy.SubmitOptions{ExtraData: map[string]jsontext.Value{"client_id": jsontext.Value(`"x"`)}}, "can't set client_id"},
			{"preview_method in ExtraData", comfy.Prompt(`{}`), &comfy.SubmitOptions{ExtraData: map[string]jsontext.Value{"preview_method": jsontext.Value(`"x"`)}}, "can't set preview_method"},
			{"invalid ExtraData", comfy.Prompt(`{}`), &comfy.SubmitOptions{ExtraData: map[string]jsontext.Value{"x": jsontext.Value(`{`)}}, "ExtraData"},
		}
		for _, tt := range tests {
			if job, err := c.Submit(t.Context(), tt.prompt, tt.opts); job != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s: Submit = %v, %v; want an error about %q", tt.name, job, err, tt.want)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := c.Submit(ctx, comfy.Prompt(`{}`), nil); !errors.Is(err, context.Canceled) {
			t.Errorf("Submit with a done context = %v", err)
		}
		if posts := requests(srv, http.MethodPost, "/prompt"); len(posts) != 0 {
			t.Errorf("%d prompts were posted", len(posts))
		}
	})
}

func TestOutputsAreInPromptOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var nodes []comfy.NodeID
		for out := range res.All() {
			nodes = append(nodes, out.Node)
		}
		if !slices.Equal(nodes, []comfy.NodeID{"3", "4"}) {
			t.Errorf("outputs of %v, want 3 then 4", nodes)
		}
		if files := res.Files("4"); len(files) != 1 || files[0].Type != comfy.FolderTemp {
			t.Errorf("Files(4) = %+v", files)
		}
		if out, ok := res.Output("3"); !ok || len(out.Files) != 1 || !out.UI.IsValid() {
			t.Errorf("Output(3) = %+v, %v", out, ok)
		}
		if _, ok := res.Output("1"); ok {
			t.Error("node 1 reported an output")
		}
		if len(res.NodeErrors()) != 0 || len(res.Blocked()) != 0 {
			t.Errorf("node errors %v, blocked %v", res.NodeErrors(), res.Blocked())
		}
	})
}

func TestACachedRerunReportsItsOutputsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 1)
		srv.Expect(r, comfytest.Behavior{})
		job := submit(t, c, r, nil)
		var cached bool
		for ev := range job.Events(t.Context()) {
			if e, ok := ev.(comfy.CachedEvent); ok && len(e.Nodes) == 4 {
				cached = true
			}
		}
		res, err := job.Wait(t.Context())
		if err != nil || !cached || len(res.Files()) != 2 {
			t.Errorf("cached %v, files %v, error %v", cached, res.Files(), err)
		}
	})
}

func TestEventsBeforeTheAnswerAreKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{EventsBeforeAnswer: 3})
		job := submit(t, c, r, nil)
		all := events(t, job)
		if len(all) < 2 {
			t.Fatalf("events: %v", all)
		}
		if _, ok := all[0].(comfy.StartedEvent); !ok {
			t.Errorf("the first event is %#v", all[0])
		}
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPartialAcceptance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "partial", 0)
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		errs := res.NodeErrors()
		if len(errs) == 0 || errs[0].Node == "" || errs[0].Type == "" || errs[0].ClassType == "" {
			t.Errorf("node errors: %+v", errs)
		}
		if len(res.Files()) == 0 {
			t.Error("the valid output reported no files")
		}
	})
}

func TestRejectionsCarryTheServersErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		runs := comfytest.Recording("rejected")
		var got []*comfy.RejectedError
		for _, r := range runs {
			srv.Expect(r, comfytest.Behavior{})
			_, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
			rejected, ok := errors.AsType[*comfy.RejectedError](err)
			if !ok {
				t.Fatalf("Submit: %v", err)
			}
			got = append(got, rejected)
		}
		if got[0].Type != "prompt_no_outputs" {
			t.Errorf("no output node: %+v", got[0])
		}
		if got[1].Type != "missing_node_type" || got[1].Node == "" {
			t.Errorf("an unknown class: %+v", got[1])
		}
		if len(got[2].NodeErrors) == 0 || got[2].NodeErrors[0].Input == "" {
			t.Errorf("an input out of bounds: %+v", got[2])
		}
		if !strings.Contains(got[2].Error(), got[2].NodeErrors[0].Input) {
			t.Errorf("the message doesn't name the input: %v", got[2])
		}
		if len(srv.JobIDs()) != 0 || len(cancelsOf(srv)) != 0 {
			t.Error("a refused prompt was queued or cancelled")
		}
	})
}

func TestANodeErrorFailsTheJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "error", 0)
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		execErr, ok := errors.AsType[*comfy.ExecutionError](err)
		if !ok {
			t.Fatalf("Wait error = %v", err)
		}
		if execErr.Node == "" || execErr.ExceptionType == "" || execErr.Message == "" || len(execErr.Traceback) == 0 || len(execErr.Executed) == 0 {
			t.Errorf("%+v", execErr)
		}
		if res == nil || len(res.Files()) == 0 {
			t.Errorf("the result lacks the output that finished first: %+v", res)
		}
	})
}

func TestCancelARunningJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "cancel", 0)
		srv.Expect(r, comfytest.Behavior{HoldUntilCancel: true})
		job := submit(t, c, r, nil)
		for ev := range job.Events(t.Context()) {
			if _, ok := ev.(comfy.ExecutingEvent); ok {
				break
			}
		}
		if err := job.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		res, err := job.Wait(t.Context())
		if !errors.Is(err, comfy.ErrCancelled) || res == nil {
			t.Errorf("Wait = %v, %v; want a result and ErrCancelled", res, err)
		}
		if err := job.Cancel(t.Context()); err != nil {
			t.Errorf("Cancel of an ended job = %v", err)
		}
	})
}

func TestCancelAQueuedJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		if err := job.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		if res, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrCancelled) || res == nil {
			t.Errorf("Wait = %v, %v", res, err)
		}
	})
}

func TestAJobAnotherClientCancelsWhileQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		other := srv.Client()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/api/jobs/"+job.ID()+"/cancel", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := other.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		// The status broadcast leads to the list read; the job is missing from it, and its record
		// is gone.
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrCancelled) {
			t.Errorf("Wait = %v", err)
		}
		if lists := requests(srv, http.MethodGet, "/api/jobs"); !slices.ContainsFunc(lists, func(r comfytest.Request) bool {
			return r.Path == "/api/jobs" && strings.Contains(r.Query, "status=pending%2Cin_progress")
		}) {
			t.Errorf("no list read: %+v", lists)
		}
	})
}

func TestRunCancelsWhenItsContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "cancel", 0)
		srv.Expect(r, comfytest.Behavior{HoldUntilCancel: true})
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		res, err := c.Run(ctx, comfy.Prompt(r.Prompt), nil)
		if !errors.Is(err, context.DeadlineExceeded) || res != nil {
			t.Errorf("Run = %v, %v", res, err)
		}
		if ids := srv.JobIDs(); len(ids) != 1 || !slices.Equal(cancelsOf(srv), ids) {
			t.Errorf("queued %v, cancelled %v", ids, cancelsOf(srv))
		}
	})
}

func TestSubmitReturnsAtItsContextAndCancelsTheJobOnceAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{AnswerDelay: time.Hour})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		job, err := c.Submit(ctx, comfy.Prompt(r.Prompt), nil)
		if job != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Second {
			t.Fatalf("Submit = %v, %v after %v", job, err, time.Since(start))
		}
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if ids := srv.JobIDs(); len(ids) != 1 || !slices.Equal(cancelsOf(srv), ids) {
			t.Errorf("queued %v, cancelled %v", ids, cancelsOf(srv))
		}
	})
}

func TestAServerThatIgnoresThePromptID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		r.Answer.Body = jsontext.Value(`{"prompt_id":"0e7d6c4a-3b1f-4c2e-9a8d-1f2e3d4c5b6a","number":1,"node_errors":{}}`)
		srv.Expect(r, comfytest.Behavior{HangBefore: 1})
		_, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		if _, ok := errors.AsType[*comfy.ProtocolError](err); !ok {
			t.Fatalf("Submit error = %v", err)
		}
		synctest.Wait()
		if got := cancelsOf(srv); !slices.Equal(got, []string{"0e7d6c4a-3b1f-4c2e-9a8d-1f2e3d4c5b6a"}) {
			t.Errorf("cancelled %v", got)
		}
	})
}

func TestA200WhoseBodyIsNotJSONStillQueues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		r.Answer.Body = jsontext.Value(`queued`)
		srv.Expect(r, comfytest.Behavior{})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestAnUnexpectedAnswerIsAnHTTPErrorAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		r.Answer = comfytest.Exchange{Status: http.StatusBadGateway, Body: jsontext.Value(`{"error":"upstream down"}`)}
		srv.Expect(r, comfytest.Behavior{})
		_, err := c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil)
		httpErr, ok := errors.AsType[*comfy.HTTPError](err)
		if !ok || httpErr.StatusCode != http.StatusBadGateway || !strings.Contains(string(httpErr.Body), "upstream down") {
			t.Fatalf("Submit error = %v", err)
		}
		synctest.Wait()
		if len(cancelsOf(srv)) != 1 {
			t.Errorf("cancelled %v", cancelsOf(srv))
		}
	})
}

func TestAJobWhoseRecordVanishesAfterItStartedIsLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{DropBefore: 4, ForgetBefore: 4})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if !errors.Is(err, comfy.ErrJobLost) || res != nil {
			t.Errorf("Wait = %v, %v", res, err)
		}
	})
}

func TestLiveOutputsWinOverTheRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		// The record lists node 4's preview under another name: the live one wins.
		r.Record.Body = jsontext.Value(bytes.ReplaceAll(r.Record.Body, []byte("ComfyUI_temp_vkgbr_00001_.png"), []byte("rewritten.png")))
		srv.Expect(r, comfytest.Behavior{})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if files := res.Files("4"); len(files) != 1 || files[0].Filename != "ComfyUI_temp_vkgbr_00001_.png" {
			t.Errorf("Files(4) = %+v", files)
		}
	})
}

func TestWaitIsBoundedByItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 3})
		job := submit(t, c, r, nil)
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		if _, err := job.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Wait = %v", err)
		}
		select {
		case <-job.Done():
			t.Error("the job ended with the wait")
		default:
		}
	})
}

func TestTheBaseURLsPrefixQueryAndHeadersGoEverywhere(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{PathPrefix: "/comfy"})
		defer srv.Close()
		header := http.Header{"X-Token": {"secret"}}
		c, err := comfy.Dial(t.Context(), srv.URL()+"/?token=abc", &comfy.Options{HTTPClient: srv.Client(), Header: header})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		if _, err := c.Run(t.Context(), comfy.Prompt(r.Prompt), nil); err != nil {
			t.Fatal(err)
		}
		for _, req := range srv.Received() {
			if !strings.HasPrefix(req.Path, "/comfy/") || !strings.Contains(req.Query, "token=abc") || req.Header.Get("X-Token") != "secret" {
				t.Errorf("%s %s?%s lacks the prefix, query or header", req.Method, req.Path, req.Query)
			}
			if req.Header.Get("Origin") != "" || req.Header.Get("Sec-Fetch-Site") != "" {
				t.Errorf("%s %s sent an Origin", req.Method, req.Path)
			}
		}
	})
}

func TestDialRefusesBadURLs(t *testing.T) {
	for _, raw := range []string{"ftp://h", "http://user:pass@h", "http://h/#frag", "::", "http://"} {
		if _, err := comfy.Dial(t.Context(), raw, nil); err == nil || strings.Contains(err.Error(), "pass") {
			t.Errorf("Dial(%q) = %v", raw, err)
		}
	}
}

func TestErrorsDoNotShowTheQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := comfytest.New(comfytest.Options{RefuseWebsocket: true})
		defer srv.Close()
		_, err := comfy.Dial(t.Context(), srv.URL()+"?token=secret", &comfy.Options{HTTPClient: srv.Client()})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("Dial error = %v", err)
		}
	})
}

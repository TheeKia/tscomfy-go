package comfy_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

// clientIDs returns the client id of each socket the server saw opened.
func clientIDs(srv *comfytest.Server) []string {
	var ids []string
	for _, q := range srv.Sockets() {
		for part := range strings.SplitSeq(q, "&") {
			if id, ok := strings.CutPrefix(part, "clientId="); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func TestADroppedSocketReconnectsWithAFreshIDAndTheJobSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{DropBefore: 6})
		res, err := submit(t, c, r, nil).Wait(t.Context())
		if err != nil || len(res.Files()) != 2 {
			t.Fatalf("Wait = %v, %v", res, err)
		}
		ids := clientIDs(srv)
		if len(ids) != 2 || ids[0] == ids[1] {
			t.Errorf("client ids %v, want two different ones", ids)
		}
	})
}

func TestTheJobSettlesThoughTheNewSocketIsDeaf(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		// The old socket's late close unregisters the new one: it gets nothing targeted.
		srv.Expect(r, comfytest.Behavior{DropBefore: 6, Hazard: true})
		start := time.Now()
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Settled by the record polls after the reconnect, not by the 30 s poll.
		if d := time.Since(start); d > 20*time.Second {
			t.Errorf("settling took %v", d)
		}
	})
}

func TestASocketTheServerClosesBetweenJobsReopensForTheNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		srv.DropSockets()
		synctest.Wait()
		if n := len(srv.Sockets()); n != 1 {
			t.Fatalf("%d sockets opened with no job open", n)
		}
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if ids := clientIDs(srv); len(ids) != 2 || ids[0] == ids[1] {
			t.Errorf("client ids %v", ids)
		}
	})
}

func TestSubmitsThatFindTheSocketDownShareOneReopening(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		srv.DropSockets()
		synctest.Wait()
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		srv.Expect(r, comfytest.Behavior{})
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				if _, err := c.Run(t.Context(), comfy.Prompt(r.Prompt), nil); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if n := len(srv.Sockets()); n != 2 {
			t.Errorf("%d sockets opened, want 2: the first and one reopening", n)
		}
	})
}

func TestASilentSocketIsFoundByItsPings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{Gap: 10 * time.Second})
		job := submit(t, c, r, nil)
		time.Sleep(5 * time.Second)
		srv.Silence()
		start := time.Now()
		if _, err := job.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if n := len(srv.Sockets()); n != 2 {
			t.Errorf("%d sockets opened, want a reconnect", n)
		}
		// A ping within 30 s, its 10 s timeout, a reconnect, and polls until the run ends.
		if d := time.Since(start); d > 3*time.Minute {
			t.Errorf("settling took %v", d)
		}
	})
}

func TestAnIdleSilentSocketIsPingedBeforeSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		srv.Silence()
		time.Sleep(time.Minute)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		if _, err := c.Run(t.Context(), comfy.Prompt(r.Prompt), nil); err != nil {
			t.Fatal(err)
		}
		if n := len(srv.Sockets()); n != 2 {
			t.Errorf("%d sockets opened, want the silent one replaced", n)
		}
	})
}

func TestTheReconnectBudgetEndsOpenJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 4})
		job := submit(t, c, r, nil)
		srv.RefuseSockets(true)
		srv.DropSockets()
		start := time.Now()
		_, err := job.Wait(t.Context())
		if err == nil || errors.Is(err, comfy.ErrClosed) || !strings.Contains(err.Error(), "couldn't reopen it") {
			t.Fatalf("Wait = %v", err)
		}
		if d := time.Since(start); d < time.Minute || d > 3*time.Minute {
			t.Errorf("the budget lasted %v, want about two minutes", d)
		}
		// Submit then opens a socket of its own, once.
		srv.RefuseSockets(false)
		srv.Expect(r, comfytest.Behavior{})
		if _, err := c.Run(t.Context(), comfy.Prompt(r.Prompt), nil); err != nil {
			t.Errorf("Run after the server came back = %v", err)
		}
	})
}

func TestSubmitWaitsForAReconnectInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 4})
		submit(t, c, r, nil)
		srv.DropSockets()
		srv.Expect(r, comfytest.Behavior{})
		if _, err := c.Run(t.Context(), comfy.Prompt(r.Prompt), nil); err != nil {
			t.Fatal(err)
		}
		if n := len(srv.Sockets()); n != 2 {
			t.Errorf("%d sockets opened", n)
		}
	})
}

func TestCloseWhileReconnecting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 4})
		job := submit(t, c, r, nil)
		srv.RefuseSockets(true)
		srv.DropSockets()
		time.Sleep(3 * time.Second)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Wait = %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("a second Close = %v", err)
		}
	})
}

func TestRecordReadsAreRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		srv.BreakRecordReads(3, 0)
		start := time.Now()
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if n := len(requests(srv, http.MethodGet, "/api/jobs/")); n != 4 {
			t.Errorf("%d record reads, want 3 broken and 1 good", n)
		}
		// The run's events take a few milliseconds, the retries 0.5, 1 and 2 s.
		if d := time.Since(start); d < 3500*time.Millisecond || d > 3600*time.Millisecond {
			t.Errorf("settling took %v, want 3.5 s of retries", d)
		}
	})
}

func TestFailedRecordReadsLeaveTheJobToItsNextPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{})
		srv.BreakRecordReads(6, 0)
		if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
			t.Fatalf("Wait = %v, want the job settled by a later poll", err)
		}
		if n := len(requests(srv, http.MethodGet, "/api/jobs/")); n != 7 {
			t.Errorf("%d record reads, want 6 failed (a read and its five retries) and 1 good", n)
		}
	})
}

func TestBusyRecordAnswersLeaveTheJobToItsNextPoll(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		synctest.Test(t, func(t *testing.T) {
			srv, c := fake(t)
			r := run(t, "success", 0)
			srv.Expect(r, comfytest.Behavior{})
			srv.BreakRecordReads(2, status)
			if _, err := submit(t, c, r, nil).Wait(t.Context()); err != nil {
				t.Errorf("status %d: Wait = %v", status, err)
			}
		})
	}
}

func TestUnexpectedRecordAnswersEndTheJob(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		synctest.Test(t, func(t *testing.T) {
			srv, c := fake(t)
			r := run(t, "success", 0)
			srv.Expect(r, comfytest.Behavior{})
			// A 404 that isn't ComfyUI's JSON one: a proxy that doesn't route the job API.
			srv.BreakRecordReads(1, status)
			_, err := submit(t, c, r, nil).Wait(t.Context())
			if httpErr, ok := errors.AsType[*comfy.HTTPError](err); !ok || httpErr.StatusCode != status {
				t.Errorf("status %d: Wait = %v", status, err)
			}
		})
	}
}

func TestAnUnknownJobStatusIsAProtocolError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		r.Record = &comfytest.Exchange{Status: http.StatusOK, Body: []byte(`{"id":"x","status":"paused"}`)}
		srv.Expect(r, comfytest.Behavior{})
		_, err := submit(t, c, r, nil).Wait(t.Context())
		if _, ok := errors.AsType[*comfy.ProtocolError](err); !ok || !strings.Contains(err.Error(), "paused") {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestAnOutageShorterThanTheBudgetDoesNotEndTheJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{Gap: 10 * time.Second})
		job := submit(t, c, r, nil)
		time.Sleep(15 * time.Second)
		// A minute with neither the socket nor the job API.
		srv.RefuseSockets(true)
		srv.BreakRecordReads(1000, 0)
		srv.DropSockets()
		time.Sleep(time.Minute)
		srv.RefuseSockets(false)
		srv.BreakRecordReads(0, 0)
		if _, err := job.Wait(t.Context()); err != nil {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestA404AfterADropIsLostNotCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "pending", 1)
		srv.Expect(r, comfytest.Behavior{Queued: true})
		job := submit(t, c, r, nil)
		srv.DropSockets()
		synctest.Wait()
		// Cancelled while queued by someone else, or lost in a restart: after a drop the
		// client can't tell.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/api/jobs/"+job.ID()+"/cancel", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrJobLost) {
			t.Errorf("Wait = %v", err)
		}
	})
}

func TestNoGoroutineOutlivesClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, c := fake(t)
		r := run(t, "success", 0)
		srv.Expect(r, comfytest.Behavior{HangBefore: 4})
		srv.Expect(r, comfytest.Behavior{AnswerDelay: time.Hour})
		job := submit(t, c, r, nil)
		go func() { _, _ = c.Submit(t.Context(), comfy.Prompt(r.Prompt), nil) }()
		time.Sleep(time.Minute)
		srv.DropSockets()
		time.Sleep(time.Second)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Wait(t.Context()); !errors.Is(err, comfy.ErrClosed) {
			t.Errorf("Wait = %v", err)
		}
		// synctest.Test fails if a goroutine of the client is still blocked when it returns.
	})
}

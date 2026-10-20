package comfy

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Record polls (rule 5): every pollInterval while the job is open; after a reconnect, at once and
// then after each of afterReconnect, the last one repeating, until it settles.
var (
	pollInterval   = 30 * time.Second
	afterReconnect = [...]time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
	// readRetries are the waits before retrying a record read that couldn't reach the server;
	// one more attempt follows the last.
	readRetries = [...]time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
)

// SubmitOptions configures Submit and Run. nil means the defaults.
type SubmitOptions struct {
	// Previews asks the server for live previews of this job. Zero: not sent, so the server's
	// default applies, which is none unless it was started with --preview-method.
	Previews PreviewMethod
	// Targets runs only these output nodes (partial_execution_targets); nil runs every output.
	Targets []NodeID
	// Front queues the job ahead of the jobs waiting.
	Front bool
	// ExtraData is more extra_data for the server, such as api_key_comfy_org for partner nodes.
	// The keys the client sets itself, client_id and preview_method, are refused.
	ExtraData map[string]jsontext.Value
}

// PreviewMethod is how the server draws live previews: extra_data.preview_method.
type PreviewMethod string

// The preview methods ComfyUI 0.42 takes.
const (
	PreviewAuto       PreviewMethod = "auto" // latent2rgb
	PreviewLatent2RGB PreviewMethod = "latent2rgb"
	PreviewTAESD      PreviewMethod = "taesd" // needs the TAESD decoder model; latent2rgb without it
	PreviewNone       PreviewMethod = "none"
)

// Job is a prompt the server accepted. It is safe for concurrent use.
type Job struct {
	c        *Client
	id       string
	order    []NodeID       // the prompt's nodes, in order
	position map[NodeID]int // each node's index in order

	done   chan struct{} // closed when the job ends
	wakeCh chan struct{} // asks the job's goroutine to read the record
	events eventBuffer

	// socketDropped is set when the client's socket drops while the job is open: a 404 of its
	// record may then mean a restart rather than a cancel (rule 4).
	socketDropped atomic.Bool

	mu         sync.Mutex
	ended      bool
	result     *Result
	err        error
	started    bool
	cancelled  bool // this client asked the server to cancel it
	reconnects int  // reconnects since the job began, for its poll schedule
	nodeErrors []NodeError
	blocked    []BlockedEvent
	live       map[string]NodeOutput // outputs seen in executed messages, by server node id
	liveOrder  []string              // the server node ids of live, in the order they ran
	display    map[string]NodeID     // the node each server node id is displayed as
	progress   map[NodeID][2]float64
}

// ID returns the job's prompt id, which the client minted.
func (j *Job) ID() string { return j.id }

// Done returns a channel that is closed when the job ends: settled, lost, closed or unreachable.
func (j *Job) Done() <-chan struct{} { return j.done }

// Submit queues prompt on the server and returns the job. ctx bounds the submission only: once
// Submit returns a job, it runs until it ends or Cancel.
//
// A prompt the server refuses is a *RejectedError, and no job. When ctx ends while the POST is in
// flight, Submit returns context.Cause(ctx) at once; unless the server refused the prompt, any
// failure after the POST may have reached the server ends with a best-effort cancel of the job,
// so a prompt nobody waits for doesn't run.
func (c *Client) Submit(ctx context.Context, prompt Prompt, opts *SubmitOptions) (*Job, error) {
	if opts == nil {
		opts = &SubmitOptions{}
	}
	order, err := promptNodes(prompt)
	if err != nil {
		return nil, err
	}
	extra, err := extraData(opts)
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	j := c.newJob(order)
	var s *socket
	for {
		if s, err = c.ready(ctx); err != nil {
			return nil, err
		}
		// Registered on the socket that is open now: one that dropped since ready would give the
		// job a client id nobody listens on.
		if registered, err := c.register(j, s); err != nil {
			return nil, err
		} else if registered {
			break
		}
	}
	body := requestBody(j.id, s.clientID, prompt, extra, opts)
	answers := make(chan postAnswer, 1)
	if !c.spawn(func() { answers <- c.post(body) }) {
		c.unregister(j)
		return nil, ErrClosed
	}
	select {
	case a := <-answers:
		if err := c.accept(j, a, false); err != nil {
			return nil, err
		}
		return j, nil
	case <-ctx.Done():
		// The POST goes on; its answer decides whether the job needs a cancel (D3).
		c.spawn(func() { _ = c.accept(j, <-answers, true) })
		return nil, context.Cause(ctx)
	}
}

// Run submits prompt and waits for its result. Unlike Submit and Wait, ctx governs the job, as in
// exec.CommandContext: when it ends first, Run cancels the job on the server, best effort, and
// returns context.Cause(ctx).
func (c *Client) Run(ctx context.Context, prompt Prompt, opts *SubmitOptions) (*Result, error) {
	j, err := c.Submit(ctx, prompt, opts)
	if err != nil {
		return nil, err
	}
	res, err := j.Wait(ctx)
	if ctx.Err() != nil && errors.Is(err, context.Cause(ctx)) {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCancelTimeout)
		defer cancel()
		_ = j.Cancel(cancelCtx)
	}
	return res, err
}

// Wait waits for the job to end and returns its result; ctx bounds only the wait. The result is
// non-nil exactly when the server settled the job:
//
//   - it succeeded: the result, nil;
//   - a node failed: the result, with the outputs of the nodes that finished first, and an
//     *ExecutionError;
//   - it was cancelled or interrupted, by anyone: the result so far and ErrCancelled;
//   - its record vanished: ErrJobLost;
//   - the client closed: ErrClosed;
//   - the socket stayed down through the reconnect budget: the transport error;
//   - the server answered its record in a way the protocol doesn't expect: an *HTTPError or a
//     *ProtocolError.
//
// A record read that fails for a while (the server restarting, a proxy's 502) doesn't end the
// job: the next poll reads again.
//
// When ctx ends first, Wait returns context.Cause(ctx) and the job goes on; a later Wait can
// still get its outcome. Once the job has ended, every Wait returns the same.
func (j *Job) Wait(ctx context.Context) (*Result, error) {
	select {
	case <-j.done:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.err
}

// Cancel asks the server to cancel the job, queued or running. The job then settles from its
// record, normally with ErrCancelled; one that was finishing may still succeed. Cancelling a job
// the server settled does nothing; a job that ended otherwise (ErrClosed, a transport error) may
// still run, so the cancel is sent. After Close, Cancel returns ErrClosed.
func (j *Job) Cancel(ctx context.Context) error {
	j.mu.Lock()
	settled := j.result != nil
	j.mu.Unlock()
	if settled {
		return nil
	}
	ctx, cancel, err := j.c.callContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	// Set before the request: a 404 read meanwhile is this cancel's.
	j.mu.Lock()
	j.cancelled = true
	j.mu.Unlock()
	if err := j.c.cancelJob(ctx, j.id); err != nil {
		j.mu.Lock()
		j.cancelled = false
		j.mu.Unlock()
		return j.c.closedOr(err)
	}
	j.wake()
	return nil
}

// Events returns the job's events, from the moment it was submitted: those buffered so far, then
// new ones as they come. It ends when the job has ended and the buffer is drained, or when ctx is
// done. It returns a single-use iterator: called again after a break it continues the stream,
// and after it ended it yields nothing. Iterating it from two goroutines at once panics.
//
// The events never say how the job ended; that is Wait's. While the reader lags, a newer
// ProgressEvent, or sampler's JPEG PreviewEvent, of a node replaces its unread one where it
// stands; of TextEvents and SaveImageWebsocket's PNG PreviewEvents at most 256 and 64 are kept,
// the oldest dropped first. The other events come at most once per node the server runs, so the
// buffer grows with the run, not with the wait: a loop's body runs once per iteration.
func (j *Job) Events(ctx context.Context) iter.Seq[Event] {
	return j.events.seq(ctx)
}

func (c *Client) newJob(order []NodeID) *Job {
	position := make(map[NodeID]int, len(order))
	for i, id := range order {
		position[id] = i
	}
	j := &Job{
		c:        c,
		id:       newUUID(),
		order:    order,
		position: position,
		done:     make(chan struct{}),
		wakeCh:   make(chan struct{}, 1),
		live:     map[string]NodeOutput{},
		display:  map[string]NodeID{},
		progress: map[NodeID][2]float64{},
	}
	j.events.init()
	return j
}

// register adds j to the open jobs while s is the client's socket; it reports false when s is no
// longer the socket.
func (c *Client) register(j *Job, s *socket) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.closed:
		return false, ErrClosed
	case c.sock != s:
		return false, nil
	}
	c.jobs[j.id] = j
	return true, nil
}

func (c *Client) unregister(j *Job) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jobs[j.id] == j {
		delete(c.jobs, j.id)
	}
	if c.running == j {
		c.running = nil
	}
}

// promptNodes checks that prompt is a JSON object and returns its keys, the node ids, in order.
func promptNodes(prompt Prompt) ([]NodeID, error) {
	v := jsontext.Value(prompt)
	if v.Kind() != '{' || !v.IsValid(lenient) {
		return nil, errors.New("comfy: the prompt is not a JSON object")
	}
	var order []NodeID
	dec := jsontext.NewDecoder(bytes.NewReader(v), lenient)
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("comfy: the prompt: %w", err)
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("comfy: the prompt: %w", err)
		}
		order = append(order, NodeID(tok.String()))
		if err := dec.SkipValue(); err != nil {
			return nil, fmt.Errorf("comfy: the prompt: %w", err)
		}
	}
	return order, nil
}

// extraData is the request's extra_data: the caller's, and the preview method.
func extraData(opts *SubmitOptions) (jsontext.Value, error) {
	extra := make(map[string]jsontext.Value, len(opts.ExtraData)+1)
	for k, v := range opts.ExtraData {
		if k == "client_id" || k == "preview_method" {
			return nil, fmt.Errorf("comfy: SubmitOptions.ExtraData can't set %s", k)
		}
		extra[k] = v
	}
	if opts.Previews != "" {
		quoted, err := jsontext.AppendQuote(nil, string(opts.Previews))
		if err != nil {
			return nil, fmt.Errorf("comfy: SubmitOptions.Previews: %w", err)
		}
		extra["preview_method"] = quoted
	}
	data, err := json.Marshal(extra, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("comfy: SubmitOptions.ExtraData: %w", err)
	}
	return data, nil
}

// requestBody is the body of POST /prompt, with the prompt byte for byte as given.
func requestBody(id, clientID string, prompt Prompt, extra jsontext.Value, opts *SubmitOptions) []byte {
	var b bytes.Buffer
	b.WriteString(`{"client_id":"`)
	b.WriteString(clientID) // both ids are UUIDs: nothing to escape
	b.WriteString(`","prompt_id":"`)
	b.WriteString(id)
	b.WriteString(`","prompt":`)
	b.Write(prompt)
	b.WriteString(`,"extra_data":`)
	b.Write(extra)
	if opts.Front {
		b.WriteString(`,"front":true`)
	}
	if opts.Targets != nil {
		targets, _ := json.Marshal(opts.Targets) // a list of strings always marshals
		b.WriteString(`,"partial_execution_targets":`)
		b.Write(targets)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// postAnswer is the answer to POST /prompt: its status and body, or the transport error.
type postAnswer struct {
	status int
	body   jsontext.Value
	err    error
}

const postOp = "POST /prompt"

// post sends POST /prompt within the client's lifetime, not the caller's context (D3).
func (c *Client) post(body []byte) postAnswer {
	resp, err := c.do(c.life, http.MethodPost, "prompt", nil, bytes.NewReader(body), "application/json")
	if err != nil {
		return postAnswer{err: err}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		// Queued, whatever the body says: an unreadable one only loses its detail.
		data, _ := readJSON(resp, postOp)
		return postAnswer{status: resp.StatusCode, body: data}
	case http.StatusBadRequest:
		data, err := readJSON(resp, postOp)
		return postAnswer{status: resp.StatusCode, body: data, err: err}
	default:
		return postAnswer{status: resp.StatusCode, err: unexpected(resp, postOp)}
	}
}

// accept handles the answer to a job's POST. abandoned is a Submit whose caller gave up: a
// queued job is then cancelled. It returns the error Submit reports.
func (c *Client) accept(j *Job, a postAnswer, abandoned bool) error {
	switch a.status {
	case http.StatusOK:
	case http.StatusBadRequest:
		// Refused: nothing was queued.
		c.unregister(j)
		if a.err != nil {
			return a.err
		}
		return rejected(a.body, j.position)
	default:
		// It may have been queued: Close is the one failure that leaves it running.
		c.unregister(j)
		if c.life.Err() != nil {
			return ErrClosed
		}
		c.cancelBestEffort(j.id)
		return a.err
	}
	var answer struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors jsontext.Value `json:"node_errors"`
	}
	if a.body != nil {
		_ = json.Unmarshal(a.body, &answer, lenient) // a body of another shape only loses its detail
	}
	if answer.PromptID != "" && answer.PromptID != j.id {
		// A proxy that ignores the client's id: the minted one was never queued.
		c.unregister(j)
		c.cancelBestEffort(answer.PromptID)
		return &ProtocolError{Op: postOp, Err: fmt.Errorf("the server queued the prompt as %s, not as %s", answer.PromptID, j.id)}
	}
	if abandoned {
		c.unregister(j)
		c.cancelBestEffort(j.id)
		return nil
	}
	j.mu.Lock()
	j.nodeErrors = nodeErrors(answer.NodeErrors, j.position)
	j.mu.Unlock()
	if !c.spawn(j.watch) {
		j.end(nil, ErrClosed)
		return ErrClosed
	}
	return nil
}

// cancelBestEffort cancels a job for a caller who has given up, in the background.
func (c *Client) cancelBestEffort(id string) {
	c.spawn(func() {
		ctx, cancel := context.WithTimeout(c.life, runCancelTimeout)
		defer cancel()
		_ = c.cancelJob(ctx, id)
	})
}

// cancelJob posts /api/jobs/{id}/cancel, which answers 200 for any id.
func (c *Client) cancelJob(ctx context.Context, id string) error {
	op := "POST /api/jobs/" + id + "/cancel"
	resp, err := c.do(ctx, http.MethodPost, "api/jobs/"+id+"/cancel", nil, bytes.NewReader([]byte("{}")), "application/json")
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return unexpected(resp, op)
	}
	drain(resp)
	return nil
}

// watch is the job's goroutine: it reads the record when woken (the end event, a queue change, a
// cancel, a reconnect) and on its poll schedule, until the job ends or the client closes.
func (j *Job) watch() {
	t := time.NewTimer(pollInterval)
	defer t.Stop()
	reconnects, step := 0, 0
	for {
		select {
		case <-j.done:
			return
		case <-j.c.life.Done():
			return
		case <-j.wakeCh:
		case <-t.C:
		}
		j.readRecord()
		j.mu.Lock()
		if j.reconnects != reconnects {
			reconnects, step = j.reconnects, 0
		}
		j.mu.Unlock()
		delay := pollInterval
		if reconnects > 0 {
			delay = afterReconnect[min(step, len(afterReconnect)-1)]
			step++
		}
		t.Reset(delay)
	}
}

// wake asks the job's goroutine to read the record; requests made during a read are read after.
func (j *Job) wake() {
	select {
	case j.wakeCh <- struct{}{}:
	default:
	}
}

// reconnected is called after the client's socket reopened: the job's events went to the old
// client id, so it polls its record until it settles.
func (j *Job) reconnected() {
	j.mu.Lock()
	j.reconnects++
	j.mu.Unlock()
	j.wake()
}

func (j *Job) hasEnded() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ended
}

func (j *Job) isStarted() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.started
}

func (j *Job) markStarted() {
	j.mu.Lock()
	j.started = true
	j.mu.Unlock()
}

// end ends the job once: settled with a result, or not, with an error.
func (j *Job) end(res *Result, err error) {
	j.mu.Lock()
	if j.ended {
		j.mu.Unlock()
		return
	}
	j.ended = true
	j.result, j.err = res, err
	j.mu.Unlock()
	close(j.done)
	j.events.end()
	j.c.unregister(j)
}

// record is a job record, GET /api/jobs/{id}.
type record struct {
	Status         string         `json:"status"`
	Outputs        jsontext.Value `json:"outputs"` // by server node id, in the order they ran
	ExecutionError *recordError   `json:"execution_error"`
}

type recordError struct {
	NodeID           string   `json:"node_id"`
	NodeType         string   `json:"node_type"`
	ExceptionType    string   `json:"exception_type"`
	ExceptionMessage string   `json:"exception_message"`
	Traceback        []string `json:"traceback"`
	Executed         []string `json:"executed"`
}

// readRecord reads the job record and settles the job from it (rule 4). A read that can't reach
// the server, or that a busy or restarting server answers with 429 or 5xx, leaves the job to its
// next poll: only the reconnect budget ends a job for an unreachable server.
func (j *Job) readRecord() {
	if j.hasEnded() {
		return
	}
	const op = "GET /api/jobs/{id}"
	resp, err := j.fetchRecord()
	if err != nil {
		j.c.log.Debug("comfy: reading a job record failed; the next poll reads again", "job", j.id, "error", err)
		return
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// ComfyUI's 404 is JSON; another one is a proxy that doesn't route the job API.
		if _, err := readJSON(resp, op); err != nil {
			j.end(nil, &HTTPError{Op: op, StatusCode: http.StatusNotFound})
			return
		}
		j.mu.Lock()
		cancelled := j.cancelled || !j.started && !j.socketDropped.Load()
		j.mu.Unlock()
		if cancelled {
			j.end(j.liveResult(), ErrCancelled)
		} else {
			j.end(nil, ErrJobLost)
		}
		return
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		drain(resp)
		return
	case resp.StatusCode != http.StatusOK:
		j.end(nil, unexpected(resp, op))
		return
	}
	var r record
	if err := decodeJSON(resp, op, &r); err != nil {
		if j.c.life.Err() == nil {
			j.end(nil, err)
		}
		return
	}
	switch r.Status {
	case "pending":
	case "in_progress":
		j.markStarted()
	case "completed":
		j.end(j.recordResult(r), nil)
	case "failed":
		e := r.ExecutionError
		if e == nil {
			e = &recordError{}
		}
		j.end(j.recordResult(r), &ExecutionError{
			Node:          NodeID(e.NodeID),
			ClassType:     e.NodeType,
			ExceptionType: e.ExceptionType,
			Message:       e.ExceptionMessage,
			Traceback:     e.Traceback,
			Executed:      nodeIDs(e.Executed),
		})
	case "cancelled":
		j.end(j.recordResult(r), ErrCancelled)
	default:
		j.end(nil, &ProtocolError{Op: op, Err: fmt.Errorf("unknown job status %q", r.Status)})
	}
}

// fetchRecord sends GET /api/jobs/{id}, each attempt within recordTimeout, retrying one that
// can't reach the server after each of readRetries.
func (j *Job) fetchRecord() (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(j.c.life, recordTimeout)
		resp, err := j.c.do(ctx, http.MethodGet, "api/jobs/"+j.id, nil, nil, "")
		if err == nil {
			// The body is read under the same timeout.
			resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}
		cancel()
		if attempt == len(readRetries) || j.c.life.Err() != nil {
			return nil, err
		}
		select {
		case <-time.After(readRetries[attempt]):
		case <-j.done:
			return nil, err
		case <-j.c.life.Done():
			return nil, err
		}
	}
}

func nodeIDs(ids []string) []NodeID {
	out := make([]NodeID, len(ids))
	for i, id := range ids {
		out[i] = NodeID(id)
	}
	return out
}

// recordResult is the result from a record's outputs, in the order the nodes ran, a node's live
// output winning: the record rewrites some entries (rule 4 and §7.8). A loop's iterations each
// have an output of their own, displayed as the same node.
func (j *Job) recordResult(r record) *Result {
	var byID map[string]jsontext.Value
	_ = json.Unmarshal(r.Outputs, &byID, lenient) // outputs of another shape are none
	j.mu.Lock()
	defer j.mu.Unlock()
	var outputs []NodeOutput
	for _, id := range objectKeys(r.Outputs) {
		if live, ok := j.live[id]; ok {
			outputs = append(outputs, live)
			continue
		}
		node, ok := j.display[id]
		if !ok {
			node = NodeID(id)
		}
		if out, ok := parseNodeOutput(node, byID[id]); ok {
			outputs = append(outputs, out)
		}
	}
	return j.newResult(outputs)
}

// liveResult is the result from the outputs seen live, for a job whose record is gone.
func (j *Job) liveResult() *Result {
	j.mu.Lock()
	defer j.mu.Unlock()
	outputs := make([]NodeOutput, len(j.liveOrder))
	for i, id := range j.liveOrder {
		outputs[i] = j.live[id]
	}
	return j.newResult(outputs)
}

// newResult orders outputs as the prompt does, keeping the order they ran within a node and among
// nodes the prompt doesn't have, which come last; j.mu is held.
func (j *Job) newResult(outputs []NodeOutput) *Result {
	slices.SortStableFunc(outputs, func(a, b NodeOutput) int { return comparePositions(j.position, a.Node, b.Node) })
	return &Result{outputs: outputs, nodeErrors: slices.Clone(j.nodeErrors), blocked: slices.Clone(j.blocked)}
}

// cancelOnClose cancels a request's context when its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

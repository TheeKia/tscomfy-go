package comfy

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Timing and limits of the protocol (docs/spec.md §7, §8).
const (
	// handshakeTimeout bounds opening a socket: the websocket handshake and the server's answer
	// to the feature flags.
	handshakeTimeout = 10 * time.Second
	// readLimit is the largest websocket message the client takes: previews are small, but
	// SaveImageWebsocket sends a whole PNG (rule 12).
	readLimit = 128 << 20
	// recordTimeout bounds one read of a job record, so a hung request can't stall settling.
	recordTimeout = 10 * time.Second
	// runCancelTimeout bounds the cancel Run sends when its context ends.
	runCancelTimeout = 10 * time.Second
	// listInterval is the least time between two list reads for jobs that haven't started.
	listInterval = time.Second
	// pingInterval, pingTimeout and idleThreshold keep a socket honest (rule 13).
	pingInterval  = 30 * time.Second
	pingTimeout   = 10 * time.Second
	idleThreshold = 30 * time.Second
)

// backoff is the wait before each reconnect attempt, before jitter: about two minutes in all.
var backoff = [...]time.Duration{
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
	8 * time.Second, 15 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
}

// minVersion is the oldest ComfyUI the client supports.
var minVersion = [3]int{0, 42, 0}

// clientFlags is the first message on every socket: previews that say which prompt and node
// they belong to.
var clientFlags = []byte(`{"type":"feature_flags","data":{"supports_preview_metadata":true}}`)

// Options configures a Client. The zero value, and nil, are the defaults.
type Options struct {
	// HTTPClient sends every request and opens the websocket. Default: a client of the package's
	// own, whose idle connections Close closes.
	HTTPClient *http.Client
	// Header is sent with every HTTP request and the websocket handshake, for a proxy's token.
	Header http.Header
	// Logger reports socket drops and reconnects. Default: discard.
	Logger *slog.Logger
}

// Client is a connection to one ComfyUI server. Any number of jobs share its websocket. It is
// safe for concurrent use.
type Client struct {
	base      *url.URL
	http      *http.Client
	transport *http.Transport // the package's own, nil for a caller's HTTPClient
	header    http.Header
	log       *slog.Logger

	//nolint:containedctx // the client's lifetime, for its own goroutines: Close cancels it
	life context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	// queue is signaled by status messages, for the list read of jobs that haven't started.
	queue chan struct{}

	// checked is set once the server's version passed: by Dial, Ping or a first Submit.
	checked atomic.Bool

	mu        sync.Mutex
	closed    bool
	sock      *socket
	opening   *flight // a socket being opened for Submit, nil when none
	reconnect *flight // a reconnect in progress, nil when none
	jobs      map[string]*Job
	running   *Job // the job whose prompt the server is running, for progress text
}

// socket is one websocket and its client id.
type socket struct {
	conn     *websocket.Conn
	clientID string
	// lastHeard is when the socket last received a message or a pong, in Unix nanoseconds.
	lastHeard atomic.Int64
	done      chan struct{} // closed when its reader ends
	pinging   *flight       // an idle check in progress, guarded by Client.mu
}

// touch records that the socket was heard from.
func (s *socket) touch() { s.lastHeard.Store(time.Now().UnixNano()) }

func (s *socket) idle() time.Duration { return time.Since(time.Unix(0, s.lastHeard.Load())) }

// flight is an operation that callers wait for together.
type flight struct {
	done chan struct{}
	err  error // set before done closes
}

// New makes a client of the ComfyUI server at rawURL, its http(s) base URL, without contacting
// the server. A path prefix and a query are kept on every request. The first Submit checks the
// server's version and opens the websocket, as Dial does, so a backend can start before ComfyUI;
// Ping reports whether the server is reachable meanwhile. New fails only for an invalid URL.
func New(rawURL string, opts *Options) (*Client, error) {
	base, err := parseBaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &Options{}
	}
	c := &Client{
		base:   base,
		http:   opts.HTTPClient,
		header: opts.Header.Clone(),
		log:    opts.Logger,
		queue:  make(chan struct{}, 1),
		jobs:   map[string]*Job{},
	}
	if c.http == nil {
		c.transport = &http.Transport{}
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			c.transport = t.Clone()
		}
		c.http = &http.Client{Transport: c.transport}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	c.life, c.stop = context.WithCancel(context.Background())
	c.wg.Go(c.watchQueue)
	return c, nil
}

// Dial connects to the ComfyUI server at rawURL, as New does, and contacts it at once: it opens
// the websocket, exchanges feature flags, and checks the server's version. A server that isn't
// ComfyUI 0.42 or later is ErrUnsupportedServer. ctx bounds Dial only. On an error nothing stays
// open.
func Dial(ctx context.Context, rawURL string, opts *Options) (*Client, error) {
	c, err := New(rawURL, opts)
	if err != nil {
		return nil, err
	}
	if err := c.dial(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) dial(ctx context.Context) error {
	s, err := c.open(ctx)
	if err != nil {
		return err
	}
	if !c.install(s) {
		return ErrClosed
	}
	return c.checkVersion(ctx)
}

// checkVersion reads /system_stats: ComfyUI 0.42 or later. A pass is remembered, so the first
// socket of a client made with New is opened after one.
func (c *Client) checkVersion(ctx context.Context) error {
	const op = "GET /system_stats"
	resp, err := c.do(ctx, http.MethodGet, "system_stats", nil, nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return unexpected(resp, op)
	}
	var stats struct {
		System struct {
			Version string `json:"comfyui_version"`
		} `json:"system"`
	}
	if err := decodeJSON(resp, op, &stats); err != nil {
		return err
	}
	v, ok := parseVersion(stats.System.Version)
	if !ok {
		return fmt.Errorf("%w: it reports no ComfyUI version", ErrUnsupportedServer)
	}
	if compareVersions(v, minVersion) < 0 {
		return fmt.Errorf("%w: it runs ComfyUI %s; the client needs %d.%d.%d or later", ErrUnsupportedServer,
			stats.System.Version, minVersion[0], minVersion[1], minVersion[2])
	}
	c.checked.Store(true)
	return nil
}

// Ping checks the server as Dial does, without the websocket: that it answers /system_stats and
// runs ComfyUI 0.42 or later, else ErrUnsupportedServer. It opens nothing, so a backend can call
// it to report the server's health. ctx bounds it.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel, err := c.callContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return c.closedOr(c.checkVersion(ctx))
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// parseVersion reads the major.minor.patch at the start of a version; the rest is ignored.
func parseVersion(s string) ([3]int, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Close stops everything the client started: its sockets, goroutines, timers, record reads and
// in-flight POSTs. Open jobs' Wait and Run return ErrClosed; the jobs keep running on the server.
// Later calls of the client's methods return ErrClosed. Close is idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	s := c.sock
	c.sock = nil
	jobs := c.jobs
	c.jobs = map[string]*Job{}
	c.mu.Unlock()

	// The jobs end before the lifetime does, so none ends with the error a cut request gives.
	for _, j := range jobs {
		j.end(nil, ErrClosed)
	}
	c.stop()
	if s != nil {
		_ = s.conn.CloseNow()
	}
	c.wg.Wait()
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

// spawn runs f in a goroutine Close waits for, unless the client is closed.
func (c *Client) spawn(f func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.wg.Go(f)
	return true
}

// errNoFlags is the cause of a handshake whose flags the server didn't answer in time.
var errNoFlags = errors.New("no answer to the feature flags")

// open opens a socket with a fresh client id and exchanges feature flags: the websocket handshake
// and the server's answer to the flags each get handshakeTimeout. A reused id would go deaf when
// the server closes the old socket late (rule 6). It returns ErrClosed once the client closed,
// context.Cause(ctx) when ctx ends first, and ErrUnsupportedServer when the flags go unanswered.
func (c *Client) open(ctx context.Context) (*socket, error) {
	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.life, cancel)
	defer stop()
	fail := func(err error) error {
		switch {
		case c.life.Err() != nil:
			return ErrClosed
		case ctx.Err() != nil:
			return context.Cause(ctx)
		default:
			return err
		}
	}

	id := newUUID()
	u := endpoint(c.base, "ws", url.Values{"clientId": {id}})
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	dialCtx, cancelDial := context.WithTimeout(inner, handshakeTimeout)
	defer cancelDial()
	conn, resp, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: c.header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fail(fmt.Errorf("comfy: opening the websocket to %s: %w", shown(u), redact(err, u)))
	}
	conn.SetReadLimit(readLimit)
	flagsCtx, cancelFlags := context.WithTimeoutCause(inner, handshakeTimeout, errNoFlags)
	defer cancelFlags()
	if err := conn.Write(flagsCtx, websocket.MessageText, clientFlags); err != nil {
		_ = conn.CloseNow()
		return nil, fail(fmt.Errorf("comfy: sending the feature flags: %w", err))
	}
	// The server answers the flags after its first status message; anything else before the
	// answer can't concern a job of this client id yet.
	for {
		typ, data, err := conn.Read(flagsCtx)
		if err != nil {
			_ = conn.CloseNow()
			if errors.Is(context.Cause(flagsCtx), errNoFlags) {
				return nil, fmt.Errorf("%w: %w in %v", ErrUnsupportedServer, errNoFlags, handshakeTimeout)
			}
			return nil, fail(fmt.Errorf("comfy: waiting for the feature flags: %w", err))
		}
		if typ == websocket.MessageText && messageType(data) == "feature_flags" {
			break
		}
	}
	c.log.Debug("comfy: websocket open", "url", shown(u), "client_id", id)
	s := &socket{conn: conn, clientID: id, done: make(chan struct{})}
	s.touch()
	return s, nil
}

// install makes s the client's socket and starts its reader and keepalive. It reports false,
// closing s, when the client closed meanwhile.
func (c *Client) install(s *socket) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = s.conn.CloseNow()
		return false
	}
	c.sock = s
	c.wg.Go(func() { c.read(s) })
	c.wg.Go(func() { c.keepalive(s) })
	return true
}

// read reads s until it fails, dispatching each message.
func (c *Client) read(s *socket) {
	defer close(s.done)
	for {
		typ, data, err := s.conn.Read(c.life)
		if err != nil {
			c.dropped(s, err)
			return
		}
		s.touch()
		if typ == websocket.MessageText {
			c.dispatch(data)
		} else {
			c.dispatchFrame(data)
		}
	}
}

// keepalive pings s while jobs are open; a ping that fails drops the socket (rule 13).
func (c *Client) keepalive(s *socket) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if c.openJobs() == 0 {
				continue
			}
			if err := c.ping(s); err != nil {
				c.log.Warn("comfy: the websocket didn't answer a ping", "error", err)
			}
		}
	}
}

// ping pings s within the client's lifetime and pingTimeout, never a caller's context: coder/
// websocket closes a connection whose write context ends. A failed ping closes s, which makes its
// reader drop it.
func (c *Client) ping(s *socket) error {
	ctx, cancel := context.WithTimeout(c.life, pingTimeout)
	defer cancel()
	if err := s.conn.Ping(ctx); err != nil {
		_ = s.conn.CloseNow()
		return err
	}
	s.touch()
	return nil
}

func (c *Client) openJobs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.jobs)
}

// dropped handles a socket's end: a reconnect while jobs are open (rule 6).
func (c *Client) dropped(s *socket, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sock != s {
		return
	}
	c.sock = nil
	c.running = nil
	if c.closed {
		return
	}
	for _, j := range c.jobs {
		j.socketDropped.Store(true)
	}
	if len(c.jobs) == 0 {
		// With no job open it stays closed; the next Submit reopens it.
		return
	}
	c.log.Warn("comfy: lost the websocket; reconnecting", "error", err)
	f := &flight{done: make(chan struct{})}
	c.reconnect = f
	c.wg.Go(func() { c.reconnectLoop(f) })
}

// reconnectLoop reopens the socket with growing waits; when the budget is spent, open jobs end
// with the transport error.
func (c *Client) reconnectLoop(f *flight) {
	var last error
	for _, delay := range backoff {
		if !sleep(c.life, time.Duration(float64(delay)*(0.5+rand.Float64()/2))) {
			c.finishReconnect(f, ErrClosed)
			return
		}
		if c.openJobs() == 0 {
			// Every job ended meanwhile: the socket stays closed until the next Submit.
			c.finishReconnect(f, nil)
			return
		}
		s, err := c.open(c.life)
		if err == nil {
			if !c.install(s) {
				c.finishReconnect(f, ErrClosed)
				return
			}
			c.log.Info("comfy: websocket reopened")
			c.finishReconnect(f, nil)
			for _, j := range c.openJobList() {
				j.reconnected()
			}
			return
		}
		last = err
	}
	err := fmt.Errorf("comfy: lost the websocket to %s and couldn't reopen it: %w", shown(c.base), last)
	c.finishReconnect(f, err)
	for _, j := range c.openJobList() {
		j.end(nil, err)
	}
}

func (c *Client) finishReconnect(f *flight, err error) {
	c.mu.Lock()
	if c.reconnect == f {
		c.reconnect = nil
	}
	c.mu.Unlock()
	f.err = err
	close(f.done)
}

func (c *Client) openJobList() []*Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	jobs := make([]*Job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	return jobs
}

// ready waits until the socket is open, within ctx. While a reconnect is in progress it waits
// for it, and returns its error if its budget runs out. With no socket and no reconnect, it opens
// one, once for every caller waiting. A socket that has been idle is pinged first, once for every
// caller, within the client's lifetime: a caller's context that ends doesn't cut the ping.
func (c *Client) ready(ctx context.Context) (*socket, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClosed
		}
		if s := c.sock; s != nil {
			if s.idle() < idleThreshold {
				c.mu.Unlock()
				return s, nil
			}
			f := s.pinging
			if f == nil {
				f = &flight{done: make(chan struct{})}
				s.pinging = f
				c.wg.Go(func() { c.checkIdle(s, f) })
			}
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			if f.err == nil {
				return s, nil
			}
			// The reader drops the socket; the next round reconnects or opens another.
			select {
			case <-s.done:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			continue
		}
		f := c.reconnect
		if f == nil {
			if f = c.opening; f == nil {
				f = &flight{done: make(chan struct{})}
				c.opening = f
				c.wg.Go(func() { c.openForSubmit(f) })
			}
		}
		c.mu.Unlock()
		select {
		case <-f.done:
			if f.err != nil {
				return nil, f.err
			}
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// checkIdle pings an idle socket for the callers of ready.
func (c *Client) checkIdle(s *socket, f *flight) {
	err := c.ping(s)
	c.mu.Lock()
	if s.pinging == f {
		s.pinging = nil
	}
	c.mu.Unlock()
	f.err = err
	close(f.done)
}

// openForSubmit opens a socket for the callers of ready, flags first, after checking the
// server's version unless that passed already.
func (c *Client) openForSubmit(f *flight) {
	var s *socket
	var err error
	if !c.checked.Load() {
		err = c.closedOr(c.checkVersion(c.life))
	}
	if err == nil {
		if s, err = c.open(c.life); err == nil && !c.install(s) {
			err = ErrClosed
		}
	}
	c.mu.Lock()
	if c.opening == f {
		c.opening = nil
	}
	c.mu.Unlock()
	f.err = err
	close(f.done)
}

// sleep waits d, and reports false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// message is a JSON websocket message.
type message struct {
	Type string         `json:"type"`
	Data jsontext.Value `json:"data"`
}

func messageType(data []byte) string {
	var m message
	if json.Unmarshal(data, &m, lenient) != nil {
		return ""
	}
	return m.Type
}

// dispatch routes a JSON message to its job by prompt id; status messages go to the list read.
func (c *Client) dispatch(data []byte) {
	var m message
	if json.Unmarshal(data, &m, lenient) != nil {
		return
	}
	if m.Type == "status" {
		select {
		case c.queue <- struct{}{}:
		default:
		}
		return
	}
	var head struct {
		PromptID string  `json:"prompt_id"`
		Node     *string `json:"node"`
	}
	if json.Unmarshal(m.Data, &head, lenient) != nil || head.PromptID == "" {
		return
	}
	c.mu.Lock()
	j := c.jobs[head.PromptID]
	switch {
	case m.Type == "execution_start":
		c.running = j // nil for another client's prompt: it isn't running ours
	case m.Type == "executing" && head.Node == nil && c.running == j:
		c.running = nil
	}
	c.mu.Unlock()
	if j != nil {
		j.message(m.Type, m.Data)
	}
}

// dispatchFrame routes a binary frame (rule 7): a preview by its prompt id, progress text to the
// job whose prompt is running.
func (c *Client) dispatchFrame(data []byte) {
	f, ok := decodeFrame(data)
	if !ok {
		return
	}
	c.mu.Lock()
	j := c.running
	if f.promptID != "" {
		j = c.jobs[f.promptID]
	}
	c.mu.Unlock()
	if j != nil {
		j.frame(f)
	}
}

// watchQueue does the list read for jobs that haven't started (rule 5): after a status message,
// at most once per listInterval, it reads the pending and running jobs, and has each job missing
// from the list read its own record.
func (c *Client) watchQueue() {
	var last time.Time
	for {
		select {
		case <-c.life.Done():
			return
		case <-c.queue:
		}
		if !sleep(c.life, time.Until(last.Add(listInterval))) {
			return
		}
		last = time.Now()
		waiting := c.unstarted()
		if len(waiting) == 0 {
			continue
		}
		listed, err := c.listJobs()
		if err != nil {
			c.log.Debug("comfy: reading the job list failed", "error", err)
			continue
		}
		for _, j := range waiting {
			status, ok := listed[j.id]
			switch {
			case !ok:
				j.wake()
			case status == "in_progress":
				j.markStarted()
			}
		}
	}
}

func (c *Client) unstarted() []*Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	var jobs []*Job
	for _, j := range c.jobs {
		if !j.isStarted() {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

// listJobs reads the server's pending and running jobs: their statuses by id.
func (c *Client) listJobs() (map[string]string, error) {
	const op = "GET /api/jobs"
	ctx, cancel := context.WithTimeout(c.life, recordTimeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "api/jobs", url.Values{"status": {"pending,in_progress"}}, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unexpected(resp, op)
	}
	var list struct {
		Jobs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if err := decodeJSON(resp, op, &list); err != nil {
		return nil, err
	}
	statuses := make(map[string]string, len(list.Jobs))
	for _, j := range list.Jobs {
		statuses[j.ID] = j.Status
	}
	return statuses, nil
}

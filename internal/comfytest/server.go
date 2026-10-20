package comfytest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Behavior says how the fake server plays a run. The zero value plays it as recorded.
type Behavior struct {
	// EventsBeforeAnswer events go out before the answer to POST /prompt: a fast server.
	EventsBeforeAnswer int
	// DropBefore closes the client's socket before event number DropBefore, counting from 1; later
	// events go nowhere, as on a real drop. 0: never.
	DropBefore int
	// Hazard, with DropBefore, unregisters the next socket that registers under the job's client
	// id as soon as it does, as when the old socket's close reaches the server late (the same-id
	// hazard). A client that reuses its id goes deaf; one with a fresh id per socket doesn't.
	Hazard bool
	// ForgetBefore has the server forget the job before event number ForgetBefore, counting from
	// 1, and play nothing more, as when it restarts: its record answers 404. 0: never.
	ForgetBefore int
	// HoldUntilCancel waits for a cancel request before the first execution_interrupted.
	HoldUntilCancel bool
	// Queued plays nothing until Start: the job waits in the queue. A cancel removes it (404).
	Queued bool
	// HangBefore stops before event number HangBefore, counting from 1, and never finishes: a
	// dead worker. 0: never.
	HangBefore int
	// Gap is the time between events; default 1 ms.
	Gap time.Duration
	// AnswerDelay delays the answer to POST /prompt; the job is queued already.
	AnswerDelay time.Duration
}

// Options configures a Server. The zero value serves ComfyUI 0.42.0.
type Options struct {
	Version         string // what /system_stats reports; default "0.42.0"
	PathPrefix      string // serve under this path, such as "/comfy", as behind a proxy
	NoFeatureFlags  bool   // never answer the feature flags
	RefuseWebsocket bool   // answer the websocket handshake with 403
}

// Request is a request the fake server received.
type Request struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

// Server is a fake ComfyUI. It serves over in-memory connections: Client returns an *http.Client
// whose transport reaches it, for the URL that URL returns. A test runs it inside synctest.Test,
// so its waits are fake time. Close it at the end of the test.
type Server struct {
	opts     Options
	listener *pipeListener
	server   *http.Server
	done     chan struct{}
	wg       sync.WaitGroup

	mu       sync.Mutex
	runs     []expected
	jobs     map[string]*job
	order    []string
	routes   map[string]*conn // by client id: where events go
	all      map[*conn]bool
	hazard   map[string]bool // client ids whose next registration the hazard removes
	received []Request
	sockets  []string // each handshake's query
	refuse   bool     // answer handshakes with 403
	broken   int      // record reads still to break
	breakAs  int      // how: 0 drops the connection, else this status with a plain body
}

type expected struct {
	run      Run
	behavior Behavior
}

type job struct {
	id, clientID string
	run          Run
	state        string // "pending", "in_progress", "done", "gone"
	release      chan struct{}
	releaseOnce  sync.Once
}

func (j *job) free() { j.releaseOnce.Do(func() { close(j.release) }) }

// conn is a client's socket. Messages to it go through a queue its writer drains, as a real
// server's do: over an unbuffered in-memory connection, a write that waits for the client's
// read could otherwise deadlock with the client's own write.
type conn struct {
	ws       *websocket.Conn
	net      *quietConn // the connection under it
	clientID string
	wake     chan struct{}

	mu    sync.Mutex
	queue [][]byte
}

func (c *conn) enqueue(message []byte) {
	c.mu.Lock()
	c.queue = append(c.queue, message)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// write sends what is queued until ctx ends.
func (c *conn) write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		}
		c.mu.Lock()
		queue := c.queue
		c.queue = nil
		c.mu.Unlock()
		for _, message := range queue {
			typ := websocket.MessageText
			if len(message) > 0 && message[0] != '{' {
				typ = websocket.MessageBinary
			}
			if c.ws.Write(ctx, typ, message) != nil {
				return
			}
		}
	}
}

// New starts a fake server.
func New(opts Options) *Server {
	if opts.Version == "" {
		opts.Version = "0.42.0"
	}
	s := &Server{
		opts:     opts,
		listener: newPipeListener(),
		done:     make(chan struct{}),
		jobs:     map[string]*job{},
		routes:   map[string]*conn{},
		all:      map[*conn]bool{},
		hazard:   map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.websocket)
	mux.HandleFunc("POST /prompt", s.prompt)
	mux.HandleFunc("GET /system_stats", s.systemStats)
	mux.HandleFunc("GET /api/jobs", s.list)
	mux.HandleFunc("GET /api/jobs/{id}", s.record)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancel)
	mux.HandleFunc("GET /view", s.view)
	mux.HandleFunc("POST /upload/image", s.upload)
	var handler http.Handler = mux
	if opts.PathPrefix != "" {
		handler = http.StripPrefix(opts.PathPrefix, mux)
	}
	s.server = &http.Server{
		Handler:           s.log(handler),
		ReadHeaderTimeout: time.Minute,
		// The websocket handler finds its connection here, for Silence.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}
	s.wg.Go(func() { _ = s.server.Serve(s.listener) })
	return s
}

// URL is the server's base URL, with its path prefix; only Client's transport reaches it.
func (s *Server) URL() string { return "http://comfy.test" + s.opts.PathPrefix }

// Client returns an HTTP client that reaches the server. It opens a connection per request, so
// counts of requests are exact: http.Transport retries a GET on a reused connection by itself.
func (s *Server) Client() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: s.listener.dial, DisableKeepAlives: true}}
}

// Close stops the server: its sockets, its connections and the runs it plays.
func (s *Server) Close() {
	close(s.done)
	s.mu.Lock()
	for c := range s.all {
		_ = c.ws.CloseNow()
	}
	s.mu.Unlock()
	// Shutdown waits for the connections, which can linger after an error answer (net/http
	// waits 500 ms before closing one whose request body went unread).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
	_ = s.server.Close()
	s.wg.Wait()
}

// Expect plays run for the next POST /prompt.
func (s *Server) Expect(run Run, behavior Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, expected{run: run, behavior: behavior})
}

// Start releases a job that Behavior.Queued holds.
func (s *Server) Start(id string) {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j != nil {
		j.free()
	}
}

// JobIDs returns the prompt ids the server queued, in order.
func (s *Server) JobIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// Received returns the requests the server received, in order.
func (s *Server) Received() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.received...)
}

// Sockets returns the query of each websocket handshake, in order.
func (s *Server) Sockets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sockets...)
}

// OpenSockets returns how many client sockets are open on the server's side.
func (s *Server) OpenSockets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.all)
}

// DropSockets closes every client socket from the server's side.
func (s *Server) DropSockets() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.all {
		_ = c.ws.CloseNow()
	}
}

// Silence makes the server stop sending and stop reading on the sockets open now, without closing
// them: sockets that died behind a proxy. Pings go unanswered. Later sockets work.
func (s *Server) Silence() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.all {
		if c.net != nil {
			c.net.silence()
		}
	}
}

// RefuseSockets makes the server answer websocket handshakes with 403, or stop doing so.
func (s *Server) RefuseSockets(refuse bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = refuse
}

// BreakRecordReads makes the next n reads of a job record fail: with status 0 as a dropped
// connection would, otherwise answered with that status and a plain-text body, as a proxy does.
func (s *Server) BreakRecordReads(n, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken, s.breakAs = n, status
}

func (s *Server) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Method == http.MethodPost {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		s.mu.Lock()
		s.received = append(s.received, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.sockets = append(s.sockets, r.URL.RawQuery)
	refuse := s.refuse || s.opts.RefuseWebsocket
	s.mu.Unlock()
	if refuse {
		http.Error(w, "403: Forbidden", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	netConn, _ := r.Context().Value(connKey{}).(*quietConn)
	c := &conn{ws: ws, net: netConn, clientID: r.URL.Query().Get("clientId"), wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(r.Context())
	writer := make(chan struct{})
	go func() {
		defer close(writer)
		c.write(ctx)
	}()
	s.mu.Lock()
	s.all[c] = true
	s.routes[c.clientID] = c
	if s.hazard[c.clientID] {
		// The old socket's late close removes the entry: this socket gets nothing targeted.
		delete(s.hazard, c.clientID)
		delete(s.routes, c.clientID)
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.all, c)
		// As ComfyUI does: whatever is registered under the id goes, this socket or not.
		delete(s.routes, c.clientID)
		s.mu.Unlock()
		_ = ws.CloseNow()
		cancel()
		<-writer
	}()
	c.enqueue([]byte(`{"type":"status","data":{"status":{"exec_info":{"queue_remaining":0}},"sid":"` + c.clientID + `"}}`))
	first := true
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if first && typ == websocket.MessageText && bytes.Contains(data, []byte(`"feature_flags"`)) && !s.opts.NoFeatureFlags {
			c.enqueue([]byte(`{"type":"feature_flags","data":{"supports_preview_metadata":true,"max_upload_size":104857600}}`))
		}
		first = false
	}
}

func (s *Server) broadcast(message []byte) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.all))
	for c := range s.all {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.enqueue(message)
	}
}

func (s *Server) systemStats(w http.ResponseWriter, _ *http.Request) {
	body, _ := json.Marshal(map[string]any{"system": map[string]any{"comfyui_version": s.opts.Version}})
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var request struct {
		PromptID string `json:"prompt_id"`
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(body, &request)
	s.mu.Lock()
	if len(s.runs) == 0 {
		s.mu.Unlock()
		http.Error(w, "no run expected", http.StatusInternalServerError)
		return
	}
	next := s.runs[0]
	s.runs = s.runs[1:]
	run := substitute(next.run, request.PromptID)
	if run.Answer.Status != http.StatusOK {
		s.mu.Unlock()
		writeJSON(w, run.Answer.Status, run.Answer.Body)
		return
	}
	j := &job{id: request.PromptID, clientID: request.ClientID, run: run, state: "pending", release: make(chan struct{})}
	s.jobs[j.id] = j
	s.order = append(s.order, j.id)
	s.mu.Unlock()
	s.broadcast(status(1))

	sent := make(chan struct{})
	answered := make(chan struct{})
	s.wg.Go(func() { s.play(j, next.behavior, sent, answered) })
	if next.behavior.EventsBeforeAnswer > 0 {
		select {
		case <-sent:
		case <-s.done:
		}
	}
	if d := next.behavior.AnswerDelay; d > 0 {
		select {
		case <-time.After(d):
		case <-s.done:
		}
	}
	writeJSON(w, http.StatusOK, run.Answer.Body)
	// The events after these go out once the answer has.
	time.AfterFunc(5*time.Millisecond, func() { close(answered) })
}

func status(queueRemaining int) []byte {
	return []byte(`{"type":"status","data":{"status":{"exec_info":{"queue_remaining":` + strconv.Itoa(queueRemaining) + `}}}}`)
}

// substitute puts the minted prompt id in for PROMPT_ID.
func substitute(run Run, id string) Run {
	replace := func(v jsontext.Value) jsontext.Value {
		return bytes.ReplaceAll(v, []byte("PROMPT_ID"), []byte(id))
	}
	out := Run{Prompt: run.Prompt, Answer: Exchange{Status: run.Answer.Status, Body: replace(run.Answer.Body)}}
	for _, e := range run.Events {
		out.Events = append(out.Events, replace(e))
	}
	if run.Record != nil {
		out.Record = &Exchange{Status: run.Record.Status, Body: replace(run.Record.Body)}
	}
	return out
}

// play sends a job's events as the behavior says.
func (s *Server) play(j *job, b Behavior, sent chan<- struct{}, answered <-chan struct{}) {
	signalSent := sync.OnceFunc(func() { close(sent) })
	defer signalSent()
	wait := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		case <-s.done:
			return false
		}
	}
	if b.EventsBeforeAnswer == 0 && !wait(answered) {
		return
	}
	if b.Queued && !wait(j.release) {
		return
	}
	s.mu.Lock()
	if j.state == "gone" {
		s.mu.Unlock()
		return
	}
	j.state = "in_progress"
	j.release = make(chan struct{})
	j.releaseOnce = sync.Once{}
	release := j.release
	s.mu.Unlock()
	gap := b.Gap
	if gap == 0 {
		gap = time.Millisecond
	}
	for i, event := range j.run.Events {
		n := i + 1
		if i == b.EventsBeforeAnswer && i > 0 {
			signalSent()
			if !wait(answered) {
				return
			}
		}
		if n == b.HangBefore {
			<-s.done
			return
		}
		if n == b.DropBefore {
			s.mu.Lock()
			c := s.routes[j.clientID]
			if b.Hazard {
				s.hazard[j.clientID] = true
			}
			s.mu.Unlock()
			if c != nil {
				_ = c.ws.CloseNow()
			}
		}
		if n == b.ForgetBefore {
			s.mu.Lock()
			j.state = "gone"
			s.mu.Unlock()
			return
		}
		if b.HoldUntilCancel && eventType(event) == "execution_interrupted" && !wait(release) {
			return
		}
		if isFinal(event) {
			s.mu.Lock()
			j.state = "done"
			s.mu.Unlock()
			s.broadcast(status(0))
		}
		select {
		case <-time.After(gap):
		case <-s.done:
			return
		}
		s.send(j.clientID, event)
	}
	s.mu.Lock()
	j.state = "done"
	s.mu.Unlock()
}

func eventType(event jsontext.Value) string {
	var e struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(event, &e)
	return e.Type
}

func isFinal(event jsontext.Value) bool {
	var e struct {
		Type string `json:"type"`
		Data struct {
			Node *string `json:"node"`
		} `json:"data"`
	}
	return json.Unmarshal(event, &e) == nil && e.Type == "executing" && e.Data.Node == nil
}

// send sends one recorded event: progress text to every socket, the rest to the client id's.
func (s *Server) send(clientID string, event jsontext.Value) {
	var e struct {
		Binary   int            `json:"binary"`
		Node     string         `json:"node"`
		Text     string         `json:"text"`
		Metadata jsontext.Value `json:"metadata"`
		Image    string         `json:"image"`
	}
	_ = json.Unmarshal(event, &e)
	if e.Binary == 3 {
		s.broadcast(TextFrame(e.Node, e.Text))
		return
	}
	s.mu.Lock()
	c := s.routes[clientID]
	s.mu.Unlock()
	if c == nil {
		return
	}
	if e.Binary == 4 {
		image, _ := base64.StdEncoding.DecodeString(e.Image)
		c.enqueue(PreviewFrame(e.Metadata, image))
		return
	}
	c.enqueue(event)
}

// PreviewFrame is a binary type-4 frame: a preview with metadata.
func PreviewFrame(metadata []byte, image []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, 4)
	b = binary.BigEndian.AppendUint32(b, uint32(len(metadata)))
	b = append(b, metadata...)
	return append(b, image...)
}

// TextFrame is a binary type-3 frame: progress text of a node.
func TextFrame(node, text string) []byte {
	b := binary.BigEndian.AppendUint32(nil, 3)
	b = binary.BigEndian.AppendUint32(b, uint32(len(node)))
	b = append(b, node...)
	return append(b, text...)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	statuses := strings.Split(r.URL.Query().Get("status"), ",")
	type entry struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	jobs := []entry{}
	s.mu.Lock()
	for _, id := range s.order {
		j := s.jobs[id]
		if (j.state == "pending" || j.state == "in_progress") && (statuses[0] == "" || slices.Contains(statuses, j.state)) {
			jobs = append(jobs, entry{ID: id, Status: j.state})
		}
	}
	s.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"jobs": jobs, "pagination": map[string]any{"offset": 0, "limit": nil, "total": len(jobs), "has_more": false}})
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	if s.broken > 0 {
		s.broken--
		status := s.breakAs
		s.mu.Unlock()
		if status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	j := s.jobs[id]
	var state string
	if j != nil {
		state = j.state
	}
	s.mu.Unlock()
	switch {
	case j == nil || state == "gone":
		writeJSON(w, http.StatusNotFound, []byte(`{"error":"Job not found"}`))
	case state != "done" || j.run.Record == nil:
		writeJSON(w, http.StatusOK, []byte(`{"id":"`+id+`","status":"`+state+`"}`))
	default:
		writeJSON(w, j.run.Record.Status, j.run.Record.Body)
	}
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.state == "done" || j.state == "gone" {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, []byte(`{"cancelled":false}`))
		return
	}
	pending := j.state == "pending"
	if pending {
		j.state = "gone"
	}
	s.mu.Unlock()
	j.free()
	if pending {
		s.broadcast(status(0))
	}
	writeJSON(w, http.StatusOK, []byte(`{"cancelled":true}`))
}

// tinyPNG is the PNG signature: enough for a file to download.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n")

func (s *Server) view(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("filename") == "missing.png" {
		http.Error(w, "404: Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(tinyPNG)
}

// upload stores nothing: it answers as ComfyUI does for a name that is taken, renamed.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	folder := r.FormValue("type")
	if folder == "" {
		folder = "input"
	}
	name := header.Filename
	if r.FormValue("overwrite") != "true" {
		ext := ""
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name, ext = name[:i], name[i:]
		}
		name += " (1)" + ext
	}
	body, _ := json.Marshal(map[string]string{"name": name, "subfolder": r.FormValue("subfolder"), "type": folder})
	writeJSON(w, http.StatusOK, body)
}

// pipeListener is a net.Listener of in-memory connections: inside a synctest bubble, waits on
// them are durable, so fake time advances.
type pipeListener struct {
	conns  chan net.Conn
	done   chan struct{}
	closed sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- newQuietConn(server):
		return client, nil
	case <-l.done:
		return nil, errors.New("comfytest: the server is closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "comfy.test" }

type connKey struct{}

// quietConn is the server's side of an in-memory connection, which Silence can make go quiet:
// it stops reading and swallows what is written, as a connection that died behind a proxy.
type quietConn struct {
	net.Conn
	quiet     chan struct{}
	closed    chan struct{}
	quietOnce sync.Once
	closeOnce sync.Once
}

func newQuietConn(c net.Conn) *quietConn {
	return &quietConn{Conn: c, quiet: make(chan struct{}), closed: make(chan struct{})}
}

func (c *quietConn) silence() { c.quietOnce.Do(func() { close(c.quiet) }) }

func (c *quietConn) isQuiet() bool {
	select {
	case <-c.quiet:
		return true
	default:
		return false
	}
}

func (c *quietConn) Read(b []byte) (int, error) {
	if !c.isQuiet() {
		n, err := c.Conn.Read(b)
		if !c.isQuiet() {
			return n, err
		}
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *quietConn) Write(b []byte) (int, error) {
	if c.isQuiet() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *quietConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

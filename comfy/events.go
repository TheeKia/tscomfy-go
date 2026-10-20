package comfy

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"slices"
	"sync"
)

// The unread events a job keeps of the kinds it doesn't coalesce; older ones go first.
const (
	maxTextEvents  = 256
	maxImageEvents = 64 // PNG previews: SaveImageWebsocket's images, each a whole image
)

// Event is something a job reports while it runs: a StartedEvent, CachedEvent, ExecutingEvent,
// ProgressEvent, PreviewEvent, OutputEvent, TextEvent or BlockedEvent, as a value. Node ids are the
// prompt's: the ids the workflow's author gave.
type Event interface{ isEvent() }

// StartedEvent is the server starting the job (execution_start).
type StartedEvent struct{}

// CachedEvent lists nodes whose cached results the run reuses (execution_cached).
type CachedEvent struct {
	Nodes []NodeID
}

// ExecutingEvent is a node starting to run (executing).
type ExecutingEvent struct {
	Node NodeID
}

// ProgressEvent is a running node's progress: Value of Max, such as a sampler's steps. ComfyUI
// sends them as floats (progress_state).
type ProgressEvent struct {
	Node       NodeID
	Value, Max float64
}

// PreviewEvent is a live image from a running node: a sampler's latent preview (a JPEG, when the
// job asks for previews with SubmitOptions.Previews), or a SaveImageWebsocket image (a PNG, one
// per image of the batch, which exists nowhere else).
type PreviewEvent struct {
	Node NodeID
	MIME string // "image/jpeg", "image/png"
	Data []byte
}

// OutputEvent is a node that finished and reported output (executed).
type OutputEvent struct {
	Node   NodeID
	Output NodeOutput
}

// TextEvent is progress text from a running node: partner nodes report their status this way.
type TextEvent struct {
	Node NodeID
	Text string
}

// BlockedEvent is a node that blocked execution with a message. The run goes on without what
// depends on it; the result lists it too.
type BlockedEvent struct {
	Node    NodeID
	Message string
}

func (StartedEvent) isEvent()   {}
func (CachedEvent) isEvent()    {}
func (ExecutingEvent) isEvent() {}
func (ProgressEvent) isEvent()  {}
func (PreviewEvent) isEvent()   {}
func (OutputEvent) isEvent()    {}
func (TextEvent) isEvent()      {}
func (BlockedEvent) isEvent()   {}

// eventBuffer holds a job's unread events for one reader at a time (see Job.Events).
type eventBuffer struct {
	notify chan struct{} // signaled after each push and at the end

	mu      sync.Mutex
	items   []Event
	texts   int // TextEvents in items
	images  int // PNG PreviewEvents in items
	ended   bool
	reading bool
}

func (b *eventBuffer) init() { b.notify = make(chan struct{}, 1) }

// push adds e: a ProgressEvent, or a JPEG PreviewEvent, replaces an unread one of the same node
// where it stands; beyond maxTextEvents the oldest TextEvent goes, and beyond maxImageEvents the
// oldest PNG PreviewEvent.
func (b *eventBuffer) push(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return
	}
	defer b.signal()
	switch e := e.(type) {
	case ProgressEvent:
		for i, queued := range b.items {
			if q, ok := queued.(ProgressEvent); ok && q.Node == e.Node {
				b.items[i] = e
				return
			}
		}
	case PreviewEvent:
		if isImage(e) {
			if b.images == maxImageEvents {
				b.dropOldest(isImage)
			}
			b.images++
			break
		}
		for i, queued := range b.items {
			if q, ok := queued.(PreviewEvent); ok && q.Node == e.Node && !isImage(q) {
				b.items[i] = e
				return
			}
		}
	case TextEvent:
		if b.texts == maxTextEvents {
			b.dropOldest(isText)
		}
		b.texts++
	}
	b.items = append(b.items, e)
}

// isImage is a PNG PreviewEvent: an image a node sent whole, not a sampler's preview.
func isImage(e Event) bool {
	p, ok := e.(PreviewEvent)
	return ok && p.MIME == "image/png"
}

func isText(e Event) bool {
	_, ok := e.(TextEvent)
	return ok
}

// dropOldest removes the oldest unread event of a kind, and its count.
func (b *eventBuffer) dropOldest(kind func(Event) bool) {
	for i, queued := range b.items {
		if kind(queued) {
			b.items = append(b.items[:i], b.items[i+1:]...)
			b.count(queued, -1)
			return
		}
	}
}

func (b *eventBuffer) count(e Event, n int) {
	switch {
	case isText(e):
		b.texts += n
	case isImage(e):
		b.images += n
	}
}

func (b *eventBuffer) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// end stops the buffer taking events; what is buffered can still be read.
func (b *eventBuffer) end() {
	b.mu.Lock()
	b.ended = true
	b.mu.Unlock()
	b.signal()
}

// next takes the oldest event; ended reports that no more will come.
func (b *eventBuffer) next() (e Event, ok, ended bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil, false, b.ended
	}
	e = b.items[0]
	b.items[0] = nil
	b.items = b.items[1:]
	b.count(e, -1)
	return e, true, false
}

func (b *eventBuffer) seq(ctx context.Context) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		b.mu.Lock()
		if b.reading {
			b.mu.Unlock()
			panic("comfy: Job.Events iterated from two goroutines at once")
		}
		b.reading = true
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			b.reading = false
			b.mu.Unlock()
		}()
		for {
			e, ok, ended := b.next()
			switch {
			case ok:
				if !yield(e) {
					return
				}
			case ended:
				return
			default:
				select {
				case <-b.notify:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// message handles a JSON message about the job (§7.7). The end of the run, executing with no
// node, has the job read its record.
func (j *Job) message(typ string, data jsontext.Value) {
	if j.hasEnded() {
		return
	}
	switch typ {
	case "execution_start":
		j.markStarted()
		j.events.push(StartedEvent{})
	case "execution_cached":
		var m struct {
			Nodes []string `json:"nodes"`
		}
		_ = json.Unmarshal(data, &m, lenient)
		j.markStarted()
		j.events.push(CachedEvent{Nodes: nodeIDs(m.Nodes)})
	case "executing":
		var m struct {
			Node        *string `json:"node"`
			DisplayNode *string `json:"display_node"`
		}
		if json.Unmarshal(data, &m, lenient) != nil {
			return
		}
		switch {
		case m.Node == nil:
			j.wake()
		case m.DisplayNode != nil:
			j.events.push(ExecutingEvent{Node: NodeID(*m.DisplayNode)})
		default:
			j.events.push(ExecutingEvent{Node: NodeID(*m.Node)})
		}
	case "progress_state":
		j.progressState(data)
	case "executed":
		j.executed(data)
	case "execution_error":
		j.executionError(data)
	}
	// execution_success and execution_interrupted decide nothing: the record does (rule 3).
}

// progressState turns progress_state into a ProgressEvent per node whose value or max changed.
func (j *Job) progressState(data jsontext.Value) {
	var m struct {
		Nodes map[string]struct {
			Value         *float64 `json:"value"`
			Max           *float64 `json:"max"`
			NodeID        string   `json:"node_id"`
			DisplayNodeID string   `json:"display_node_id"`
		} `json:"nodes"`
	}
	if json.Unmarshal(data, &m, lenient) != nil {
		return
	}
	var changed []ProgressEvent
	j.mu.Lock()
	for key, n := range m.Nodes {
		if n.Value == nil || n.Max == nil {
			continue
		}
		node := NodeID(key)
		switch {
		case n.DisplayNodeID != "":
			node = NodeID(n.DisplayNodeID)
		case n.NodeID != "":
			node = NodeID(n.NodeID)
		}
		seen := [2]float64{*n.Value, *n.Max}
		if old, ok := j.progress[node]; ok && old == seen {
			continue
		}
		j.progress[node] = seen
		changed = append(changed, ProgressEvent{Node: node, Value: seen[0], Max: seen[1]})
	}
	j.mu.Unlock()
	// A map has no order: the prompt's is a stable one.
	slices.SortFunc(changed, func(a, b ProgressEvent) int { return compareNodes(j.position, a.Node, b.Node) })
	for _, e := range changed {
		j.events.push(e)
	}
}

// executed records a node's output, seen live, and maps the server's node id to the displayed
// one for nodes expanded at run time. A cached output node reports a null output.
func (j *Job) executed(data jsontext.Value) {
	var m struct {
		Node        string         `json:"node"`
		DisplayNode string         `json:"display_node"`
		Output      jsontext.Value `json:"output"`
	}
	if json.Unmarshal(data, &m, lenient) != nil || m.Node == "" {
		return
	}
	display := NodeID(m.Node)
	if m.DisplayNode != "" {
		display = NodeID(m.DisplayNode)
	}
	out, ok := parseNodeOutput(display, m.Output)
	j.mu.Lock()
	j.display[m.Node] = display
	if ok {
		if _, seen := j.live[m.Node]; !seen {
			j.liveOrder = append(j.liveOrder, m.Node)
		}
		j.live[m.Node] = out
	}
	j.mu.Unlock()
	if ok {
		j.events.push(OutputEvent{Node: display, Output: out})
	}
}

// executionError reports a blocked node (rule 9); a real error ends the run, and the record has
// it.
func (j *Job) executionError(data jsontext.Value) {
	var m struct {
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
		NodeID           string `json:"node_id"`
	}
	if json.Unmarshal(data, &m, lenient) != nil || m.ExceptionType != "ExecutionBlocked" || m.NodeID == "" {
		return
	}
	msg := m.ExceptionMessage
	if msg == "" {
		msg = "execution blocked"
	}
	e := BlockedEvent{Node: NodeID(m.NodeID), Message: msg}
	j.mu.Lock()
	j.blocked = append(j.blocked, e)
	j.mu.Unlock()
	j.events.push(e)
}

// frame handles a binary frame of the job: a preview or progress text.
func (j *Job) frame(f frame) {
	if j.hasEnded() {
		return
	}
	j.mu.Lock()
	node, ok := j.display[string(f.node)]
	j.mu.Unlock()
	if !ok {
		node = f.node
	}
	if f.text {
		j.events.push(TextEvent{Node: node, Text: string(f.data)})
		return
	}
	j.events.push(PreviewEvent{Node: node, MIME: f.mime, Data: f.data})
}

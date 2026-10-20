package comfy

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrClosed is returned by a Client's methods after Close, and by Wait and Run for a job
	// that was open when the client closed. The job keeps running on the server.
	ErrClosed = errors.New("comfy: the client is closed")

	// ErrUnsupportedServer is returned by Dial for a server that isn't ComfyUI 0.42 or later: one
	// that doesn't answer the feature flags, or reports an older or no version.
	ErrUnsupportedServer = errors.New("comfy: unsupported server")

	// ErrJobLost is returned by Wait when the server lost the job's record and the client can't
	// tell whether the job was cancelled or the server restarted. The job's outcome is unknown.
	ErrJobLost = errors.New("comfy: the server has no record of the job")

	// ErrCancelled is returned by Wait for a job that was cancelled or interrupted, by this client
	// or anyone else. Wait returns the result too: the outputs of the nodes that finished.
	ErrCancelled = errors.New("comfy: the job was cancelled")
)

// RejectedError is a prompt the server refused (POST /prompt answered 400): nothing ran.
type RejectedError struct {
	// Type, Message and Details are the server's top-level error, such as
	// "prompt_outputs_failed_validation".
	Type, Message, Details string
	// Node is the node the top-level error names, for "missing_node_type"; "" otherwise.
	Node NodeID
	// NodeErrors are the errors of each node, in prompt order.
	NodeErrors []NodeError
}

// Error describes the refusal: the server's error, then each node error.
func (e *RejectedError) Error() string {
	var b strings.Builder
	b.WriteString("comfy: the server refused the prompt: ")
	b.WriteString(describe(e.Type, e.Message, e.Details))
	for _, n := range e.NodeErrors {
		b.WriteString("; ")
		b.WriteString(n.String())
	}
	return b.String()
}

// NodeError is the server's error about one node of a prompt: an input that failed validation,
// or the node as a whole.
type NodeError struct {
	Node      NodeID
	ClassType string
	Input     string // extra_info.input_name; "" when the error is about the whole node
	Type      string // the server's error type, such as "value_not_in_list"
	Message   string
	Details   string
}

// String describes the error with its node and input.
func (e NodeError) String() string {
	where := string(e.Node)
	if e.Input != "" {
		where += "." + e.Input
	}
	return where + ": " + describe(e.Type, e.Message, e.Details)
}

// ExecutionError is a node that raised an exception while the job ran. Wait returns it with the
// result: the outputs of the nodes that finished first.
type ExecutionError struct {
	Node          NodeID
	ClassType     string
	ExceptionType string // the exception's class, qualified: "torch.OutOfMemoryError"
	Message       string
	Traceback     []string
	Executed      []NodeID // the nodes that had finished
}

// Error names the node, its class and the exception.
func (e *ExecutionError) Error() string {
	return fmt.Sprintf("comfy: node %s (%s) raised %s: %s", e.Node, e.ClassType, e.ExceptionType, e.Message)
}

// HTTPError is an answer the protocol doesn't expect, such as a 500 or a proxy's 502.
type HTTPError struct {
	Op         string // the request: "POST /prompt", "GET /api/jobs/{id}"
	StatusCode int
	Body       []byte // the start of the answer's body, at most 4 KiB
}

// Error names the request and the status, with the body.
func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("comfy: %s answered %d", e.Op, e.StatusCode)
	if body := strings.TrimSpace(string(e.Body)); body != "" {
		msg += ": " + body
	}
	return msg
}

// ProtocolError is a server that broke the protocol: an answer of the wrong shape, or a prompt
// queued under another id than the one the client minted.
type ProtocolError struct {
	Op  string
	Err error
}

// Error names the request and what was wrong.
func (e *ProtocolError) Error() string { return fmt.Sprintf("comfy: %s: %v", e.Op, e.Err) }

// Unwrap returns the underlying error.
func (e *ProtocolError) Unwrap() error { return e.Err }

// describe joins an error's type, message and details, leaving out empty and repeated parts.
func describe(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p == "" || (len(kept) > 0 && kept[len(kept)-1] == p) {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return "no description"
	}
	return strings.Join(kept, ": ")
}

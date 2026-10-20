package comfy

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"slices"
	"strings"
)

// Result is what a settled job produced: each node's output, in prompt order, and what the
// server reported besides. A node the server ran more than once, such as the body of a loop, has
// one output per run, in the order they ran.
type Result struct {
	outputs    []NodeOutput
	nodeErrors []NodeError
	blocked    []BlockedEvent
}

// Files returns the files of the given nodes, or of every node when none are given, in prompt
// order.
func (r *Result) Files(nodes ...NodeID) []OutputFile {
	var files []OutputFile
	for _, out := range r.outputs {
		if len(nodes) == 0 || slices.Contains(nodes, out.Node) {
			files = append(files, out.Files...)
		}
	}
	return files
}

// Output returns a node's output, and whether it reported one: the last, for a node that ran more
// than once. All has every run's.
func (r *Result) Output(node NodeID) (NodeOutput, bool) {
	for _, out := range slices.Backward(r.outputs) {
		if out.Node == node {
			return out, true
		}
	}
	return NodeOutput{}, false
}

// All yields every node's output, in prompt order: one per run of a node that ran more than once.
func (r *Result) All() iter.Seq[NodeOutput] {
	return slices.Values(r.outputs)
}

// NodeErrors returns the errors of the outputs the server refused while it ran the others: a
// partial acceptance.
func (r *Result) NodeErrors() []NodeError { return slices.Clone(r.nodeErrors) }

// Blocked returns the nodes that blocked execution, as the job's socket reported them. ComfyUI
// doesn't record them, so one reported while the socket was down is missing.
func (r *Result) Blocked() []BlockedEvent { return slices.Clone(r.blocked) }

// NodeOutput is what one node reported when it ran.
type NodeOutput struct {
	Node  NodeID
	Files []OutputFile // every file entry, under any key, in the node's order
	Text  []string     // the strings under "text"
	// UI is the node's output as the server sent it, raw: lists by key, with the server's
	// metadata (kind, width, height) and animated flags.
	UI jsontext.Value
}

// OutputFile is a file a node produced.
type OutputFile struct {
	FileRef
	Key string // the output key it was listed under: "images", "audio", "gifs", …
}

// FileRef names a file on the server, as outputs list it and /view serves it.
type FileRef struct {
	Filename  string
	Subfolder string // "" for none
	Type      FolderType
}

// FolderType is one of the server's directories.
type FolderType string

// The server's directories.
const (
	FolderOutput FolderType = "output"
	FolderTemp   FolderType = "temp"
	FolderInput  FolderType = "input"
)

func (t FolderType) valid() bool {
	return t == FolderOutput || t == FolderTemp || t == FolderInput
}

// parseNodeOutput reads a node's UI output, leniently: files are recognized by their shape under
// any key, an object with a non-empty filename and a known type. A null or non-object output is
// no output.
func parseNodeOutput(node NodeID, ui jsontext.Value) (NodeOutput, bool) {
	var lists map[string]jsontext.Value
	if ui.Kind() != '{' || json.Unmarshal(ui, &lists, lenient) != nil {
		return NodeOutput{}, false
	}
	out := NodeOutput{Node: node, UI: bytes.Clone(ui)}
	for _, key := range objectKeys(ui) {
		var items []jsontext.Value
		if json.Unmarshal(lists[key], &items, lenient) != nil {
			continue
		}
		for _, item := range items {
			if key == "text" {
				var s string
				if item.Kind() == '"' && json.Unmarshal(item, &s, lenient) == nil {
					out.Text = append(out.Text, s)
				}
				continue
			}
			var f struct {
				Filename  string     `json:"filename"`
				Subfolder string     `json:"subfolder"`
				Type      FolderType `json:"type"`
			}
			if item.Kind() != '{' || json.Unmarshal(item, &f, lenient) != nil || f.Filename == "" || !f.Type.valid() {
				continue
			}
			out.Files = append(out.Files, OutputFile{FileRef: FileRef(f), Key: key})
		}
	}
	return out, true
}

// objectKeys returns a JSON object's member names in order.
func objectKeys(v jsontext.Value) []string {
	dec := jsontext.NewDecoder(bytes.NewReader(v), lenient)
	if _, err := dec.ReadToken(); err != nil {
		return nil
	}
	var keys []string
	for dec.PeekKind() == '"' {
		tok, err := dec.ReadToken()
		if err != nil {
			return keys
		}
		keys = append(keys, tok.String())
		if dec.SkipValue() != nil {
			return keys
		}
	}
	return keys
}

// serverError is an error entry of the server's answer to a prompt.
type serverError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Details   string `json:"details"`
	ExtraInfo struct {
		InputName string `json:"input_name"`
		NodeID    string `json:"node_id"`
	} `json:"extra_info"`
}

// nodeErrors reads node_errors, {node: {errors: […], class_type}}, one NodeError per error, in
// the prompt's order of nodes (position).
func nodeErrors(v jsontext.Value, position map[NodeID]int) []NodeError {
	var byNode map[string]struct {
		Errors    []serverError `json:"errors"`
		ClassType string        `json:"class_type"`
	}
	if len(v) == 0 || json.Unmarshal(v, &byNode, lenient) != nil {
		return nil
	}
	ids := make([]NodeID, 0, len(byNode))
	for id := range byNode {
		ids = append(ids, NodeID(id))
	}
	slices.SortFunc(ids, func(a, b NodeID) int { return compareNodes(position, a, b) })
	var errs []NodeError
	for _, id := range ids {
		n := byNode[string(id)]
		for _, e := range n.Errors {
			errs = append(errs, NodeError{
				Node:      id,
				ClassType: n.ClassType,
				Input:     e.ExtraInfo.InputName,
				Type:      e.Type,
				Message:   e.Message,
				Details:   e.Details,
			})
		}
	}
	return errs
}

// rejected is the *RejectedError of a 400 answer: {error, node_errors}. The error is an object
// or, from some paths, a string.
func rejected(body jsontext.Value, position map[NodeID]int) error {
	var answer struct {
		Error      jsontext.Value `json:"error"`
		NodeErrors jsontext.Value `json:"node_errors"`
	}
	if err := json.Unmarshal(body, &answer, lenient); err != nil {
		return &ProtocolError{Op: postOp, Err: err}
	}
	e := &RejectedError{NodeErrors: nodeErrors(answer.NodeErrors, position)}
	var top serverError
	switch answer.Error.Kind() {
	case '"':
		_ = json.Unmarshal(answer.Error, &e.Message, lenient)
	case '{':
		if json.Unmarshal(answer.Error, &top, lenient) == nil {
			e.Type, e.Message, e.Details = top.Type, top.Message, top.Details
			e.Node = NodeID(top.ExtraInfo.NodeID)
		}
	default:
		// No description: the type of error is all there is.
	}
	return e
}

// compareNodes compares two node ids by their position in the prompt; unknown ones come last,
// in byte order.
func compareNodes(position map[NodeID]int, a, b NodeID) int {
	if c := comparePositions(position, a, b); c != 0 {
		return c
	}
	return strings.Compare(string(a), string(b))
}

// comparePositions compares two node ids by their position in the prompt; unknown ones come last
// and compare equal.
func comparePositions(position map[NodeID]int, a, b NodeID) int {
	pa, oka := position[a]
	pb, okb := position[b]
	switch {
	case oka && okb:
		return pa - pb
	case oka:
		return -1
	case okb:
		return 1
	default:
		return 0
	}
}

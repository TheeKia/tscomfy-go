package format

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
)

// Version is the template format this package reads.
const Version = 1

// VersionError is a document of another format than Version.
type VersionError struct {
	Version int // the document's
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("format %d, want %d", e.Version, Version)
}

// Kind is the kind of a parameter's input.
type Kind string

// The kinds a parameter can have.
const (
	KindInt     Kind = "int"
	KindFloat   Kind = "float"
	KindString  Kind = "string"
	KindBoolean Kind = "boolean"
	KindCombo   Kind = "combo"
)

var (
	templateName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	paramName    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// Template is a template document, checked.
type Template struct {
	Name   string
	Params []Param
	// Prompt is the prompt, compacted: byte for byte what tscomfy's stringify wrote.
	Prompt jsontext.Value
	// Nodes are the prompt's node ids, in prompt order.
	Nodes []string
}

// Param is one parameter of a template, checked against its prompt.
type Param struct {
	Name     string
	Node     string
	Input    string
	Kind     Kind
	Optional bool
	// Default is the input's value in the prompt; Offset is where it starts in Template.Prompt.
	Default jsontext.Value
	Offset  int
	// Min and Max are the input's bounds, for kinds int and float.
	Min, Max Bound
}

// Bound is a bound of a parameter's input: its exact literal, and its value as the kind compares
// it. The zero Bound is absent.
type Bound struct {
	Literal jsontext.Value // nil when the schema gives none
	Int     *big.Int       // kind int
	Float   float64        // kind float
}

// Set reports whether the bound is present.
func (b Bound) Set() bool { return b.Literal != nil }

// The document as written. Optional members are omitted when zero.
type document struct {
	Format int            `json:"format"`
	Name   string         `json:"name"`
	Params []paramMember  `json:"params"`
	Prompt jsontext.Value `json:"prompt"`
}

type paramMember struct {
	Name     string         `json:"name"`
	Node     string         `json:"node"`
	Input    string         `json:"input"`
	Kind     Kind           `json:"kind"`
	Min      jsontext.Value `json:"min,omitzero"`
	Max      jsontext.Value `json:"max,omitzero"`
	Optional bool           `json:"optional,omitzero"`
}

// Parse reads a template document and checks it against §4: the format, the names, each
// parameter's input present in the prompt with a value of its kind, and the bounds. Unknown
// members are an error.
func Parse(data []byte) (*Template, error) {
	if err := checkVersion(data); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	t, err := parse(doc)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", doc.Name, err)
	}
	return t, nil
}

// checkVersion reads only the format member, so a document of another version is reported as
// such rather than by the first member this version doesn't know.
func checkVersion(data []byte) error {
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if head.Format != Version {
		return &VersionError{Version: head.Format}
	}
	return nil
}

type nodeInput struct{ node, input string }

func parse(doc document) (*Template, error) {
	if !templateName.MatchString(doc.Name) {
		return nil, fmt.Errorf("the name must match %s", templateName)
	}
	if doc.Prompt == nil {
		return nil, errors.New("no prompt")
	}
	t := &Template{Name: doc.Name, Prompt: slices.Clone(doc.Prompt)}
	if err := t.Prompt.Compact(jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false)); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	names := make(map[string]bool, len(doc.Params))
	byInput := make(map[nodeInput]int, len(doc.Params))
	t.Params = make([]Param, len(doc.Params))
	for i, m := range doc.Params {
		if !paramName.MatchString(m.Name) {
			return nil, fmt.Errorf("parameter %q: the name must match %s", m.Name, paramName)
		}
		if names[m.Name] {
			return nil, fmt.Errorf("parameter %q: declared twice", m.Name)
		}
		names[m.Name] = true
		key := nodeInput{m.Node, m.Input}
		if j, taken := byInput[key]; taken {
			return nil, fmt.Errorf("parameter %q: %s.%s is parameter %q already", m.Name, m.Node, m.Input, doc.Params[j].Name)
		}
		byInput[key] = i
		t.Params[i] = Param{Name: m.Name, Node: m.Node, Input: m.Input, Kind: m.Kind, Optional: m.Optional}
	}
	nodes, err := scanPrompt(t.Prompt, byInput, t.Params)
	if err != nil {
		return nil, err
	}
	t.Nodes = nodes
	for i := range t.Params {
		p := &t.Params[i]
		if err := p.check(doc.Params[i]); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.Name, err)
		}
	}
	return t, nil
}

// scanPrompt walks the compacted prompt: an object of node objects, each with an "inputs"
// object. It returns the node ids in order and records each parameter's value and offset.
func scanPrompt(prompt jsontext.Value, byInput map[nodeInput]int, params []Param) ([]string, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(prompt))
	if err := readDelim(dec, '{', "the prompt is not an object"); err != nil {
		return nil, err
	}
	var nodes []string
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("prompt: %w", err)
		}
		node := tok.String()
		nodes = append(nodes, node)
		if err := readDelim(dec, '{', fmt.Sprintf("node %q is not an object", node)); err != nil {
			return nil, err
		}
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return nil, fmt.Errorf("prompt: %w", err)
			}
			if tok.String() != "inputs" {
				if err := dec.SkipValue(); err != nil {
					return nil, fmt.Errorf("prompt: %w", err)
				}
				continue
			}
			if err := scanInputs(dec, prompt, node, byInput, params); err != nil {
				return nil, err
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return nil, fmt.Errorf("prompt: %w", err)
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("prompt: data after the object")
	}
	for _, p := range params {
		if p.Default == nil {
			return nil, fmt.Errorf("parameter %q: the prompt has no input %s.%s", p.Name, p.Node, p.Input)
		}
	}
	return nodes, nil
}

func scanInputs(dec *jsontext.Decoder, prompt jsontext.Value, node string, byInput map[nodeInput]int, params []Param) error {
	if err := readDelim(dec, '{', fmt.Sprintf(`node %q: "inputs" is not an object`, node)); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		input := tok.String() // the token is void after the next read
		value, err := dec.ReadValue()
		if err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		if i, ok := byInput[nodeInput{node, input}]; ok {
			// The prompt is compact, so the value's bytes end where the decoder is. The
			// decoder's own copy is void after the next read.
			end := int(dec.InputOffset())
			start := end - len(value)
			params[i].Default = prompt[start:end:end]
			params[i].Offset = start
		}
	}
	_, err := dec.ReadToken()
	return err
}

func readDelim(dec *jsontext.Decoder, delim jsontext.Kind, message string) error {
	if dec.PeekKind() != delim {
		return errors.New(message)
	}
	_, err := dec.ReadToken()
	return err
}

// check checks the parameter's kind, its default and its bounds.
func (p *Param) check(m paramMember) error {
	switch p.Kind {
	case KindInt, KindFloat:
	case KindString, KindCombo:
		if p.Default.Kind() != '"' {
			return fmt.Errorf("the value %s is not a string", p.Default)
		}
	case KindBoolean:
		if k := p.Default.Kind(); k != 't' && k != 'f' {
			return fmt.Errorf("the value %s is not a boolean", p.Default)
		}
	default:
		return fmt.Errorf("unknown kind %q", p.Kind)
	}
	if p.Kind != KindInt && p.Kind != KindFloat {
		if m.Min != nil || m.Max != nil {
			return fmt.Errorf("a %s input has no bounds", p.Kind)
		}
		return nil
	}
	def, err := p.bound(p.Default)
	if err != nil {
		return fmt.Errorf("the value %w", err)
	}
	if p.Min, err = p.bound(m.Min); err != nil {
		return fmt.Errorf("min %w", err)
	}
	if p.Max, err = p.bound(m.Max); err != nil {
		return fmt.Errorf("max %w", err)
	}
	if p.Min.Set() && p.Max.Set() && p.Min.compare(p.Max) > 0 {
		return fmt.Errorf("min %s is above max %s", p.Min.Literal, p.Max.Literal)
	}
	if !p.InRange(def) {
		return fmt.Errorf("the value %s is outside the bounds", p.Default)
	}
	return nil
}

// bound reads a literal of the parameter's numeric kind; a nil literal is an absent bound.
func (p *Param) bound(literal jsontext.Value) (Bound, error) {
	if literal == nil {
		return Bound{}, nil
	}
	b := Bound{Literal: literal}
	if p.Kind == KindInt {
		n, ok := parseInt(literal)
		if !ok {
			return Bound{}, fmt.Errorf("%s is not an integer", literal)
		}
		b.Int = n
		return b, nil
	}
	f, ok := parseFloat(literal)
	if !ok {
		return Bound{}, fmt.Errorf("%s is not a finite number", literal)
	}
	b.Float = f
	return b, nil
}

// InRange reports whether v, a bound of the parameter's kind, is within its bounds.
func (p *Param) InRange(v Bound) bool {
	return (!p.Min.Set() || p.Min.compare(v) <= 0) && (!p.Max.Set() || v.compare(p.Max) <= 0)
}

func (b Bound) compare(other Bound) int {
	if b.Int != nil {
		return b.Int.Cmp(other.Int)
	}
	switch {
	case b.Float < other.Float:
		return -1
	case b.Float > other.Float:
		return 1
	default:
		return 0
	}
}

// parseInt reads an integer literal exactly: digits, with an optional minus sign.
func parseInt(literal jsontext.Value) (*big.Int, bool) {
	if literal.Kind() != '0' || bytes.ContainsAny(literal, ".eE") {
		return nil, false
	}
	return new(big.Int).SetString(string(literal), 10)
}

// parseFloat reads a number literal as the float64 Python's float() would give.
func parseFloat(literal jsontext.Value) (float64, bool) {
	if literal.Kind() != '0' {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(literal), 64)
	return f, err == nil && !math.IsInf(f, 0)
}

// ParseExport reads the export command's output, {"format":1,"templates":[…]}, and returns the
// templates, raw and as written.
func ParseExport(data []byte) ([]jsontext.Value, error) {
	if err := checkVersion(data); err != nil {
		return nil, err
	}
	var export struct {
		Format    int              `json:"format"`
		Templates []jsontext.Value `json:"templates"`
	}
	if err := json.Unmarshal(data, &export, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	return export.Templates, nil
}

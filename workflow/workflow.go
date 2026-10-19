package workflow

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/internal/format"
)

// Kind is the kind of a parameter's input, which decides the Go values it takes (see
// Template.Fill).
type Kind string

// The kinds a parameter can have.
const (
	KindInt     Kind = Kind(format.KindInt)
	KindFloat   Kind = Kind(format.KindFloat)
	KindString  Kind = Kind(format.KindString)
	KindBoolean Kind = Kind(format.KindBoolean)
	KindCombo   Kind = Kind(format.KindCombo)
)

// The errors a *ParamError wraps.
var (
	// ErrRequired is a required parameter given nil.
	ErrRequired = errors.New("the parameter is required")
	// ErrOutOfRange is a value outside its input's bounds.
	ErrOutOfRange = errors.New("the value is outside the input's bounds")
	// ErrWrongType is a value of a type the parameter's kind doesn't take, a string that isn't
	// valid UTF-8, or a float that isn't finite.
	ErrWrongType = errors.New("the value has the wrong type")
)

// ParamError is a value that Template.Fill refused.
type ParamError struct {
	Workflow string // the template's name
	Param    string // the parameter's name
	Err      error  // ErrRequired, ErrOutOfRange or ErrWrongType, wrapped with detail
}

func (e *ParamError) Error() string {
	return fmt.Sprintf("workflow %s: parameter %s: %v", e.Workflow, e.Param, e.Err)
}

func (e *ParamError) Unwrap() error { return e.Err }

// Param is one parameter of a template: an input of a node, whose value Fill sets.
type Param struct {
	Name     string
	Node     comfy.NodeID
	Input    string
	Kind     Kind
	Optional bool           // nil keeps the default
	Default  jsontext.Value // the value in the prompt
	Min, Max jsontext.Value // exact literals; nil when the schema gives none
}

// Template is a workflow's prompt with its parameter slots. It is immutable and safe for
// concurrent use.
type Template struct {
	t      *format.Template
	params []Param
	// The parameters' indexes in the order their values appear in the prompt.
	order []int
}

// Load reads a template file and checks it: its format, every parameter's input present in the
// prompt with a value of its kind, and the value within the input's bounds.
func Load(data []byte) (*Template, error) {
	t, err := format.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	params := make([]Param, len(t.Params))
	for i, p := range t.Params {
		params[i] = Param{
			Name:     p.Name,
			Node:     comfy.NodeID(p.Node),
			Input:    p.Input,
			Kind:     Kind(p.Kind),
			Optional: p.Optional,
			Default:  p.Default,
			Min:      p.Min.Literal,
			Max:      p.Max.Literal,
		}
	}
	order := make([]int, len(t.Params))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return t.Params[a].Offset - t.Params[b].Offset })
	return &Template{t: t, params: params, order: order}, nil
}

// MustLoad is Load for templates known to be valid, such as those the generator wrote and
// embedded. It panics on an error.
func MustLoad(data []byte) *Template {
	t, err := Load(data)
	if err != nil {
		panic(err)
	}
	return t
}

// Name returns the template's name.
func (t *Template) Name() string { return t.t.Name }

// Params returns the template's parameters, in declaration order: the order Fill takes values
// in. They are copies; changing them doesn't change the template.
func (t *Template) Params() []Param {
	params := slices.Clone(t.params)
	for i := range params {
		params[i].Default = slices.Clone(params[i].Default)
		params[i].Min = slices.Clone(params[i].Min)
		params[i].Max = slices.Clone(params[i].Max)
	}
	return params
}

// Fill returns the template's prompt with one value per parameter, in Params order, replacing
// each parameter's value in the prompt. Every other byte is the template's, so the result is what
// tscomfy would build with the same values.
//
// Values by kind:
//
//   - KindInt: int, int64, uint64, or a pointer to one, within the input's bounds;
//   - KindFloat: float64 or *float64, finite and within the bounds;
//   - KindBoolean: bool or *bool;
//   - KindString, KindCombo: string or *string, valid UTF-8. The server checks combo values.
//
// nil, untyped or a nil pointer, keeps the default; for a required parameter it is ErrRequired.
// A value Fill refuses is a *ParamError. Fill never panics.
func (t *Template) Fill(values ...any) (comfy.Prompt, error) {
	if len(values) != len(t.params) {
		return nil, fmt.Errorf("workflow %s: Fill takes %d values, got %d", t.t.Name, len(t.params), len(values))
	}
	encoded := make([][]byte, len(values))
	size := len(t.t.Prompt)
	for i, v := range values {
		p := &t.t.Params[i]
		b, err := encode(p, v)
		if err != nil {
			return nil, &ParamError{Workflow: t.t.Name, Param: p.Name, Err: err}
		}
		encoded[i] = b
		size += len(b) - len(p.Default)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, i := range t.order {
		p := &t.t.Params[i]
		out = append(out, t.t.Prompt[last:p.Offset]...)
		out = append(out, encoded[i]...)
		last = p.Offset + len(p.Default)
	}
	return comfy.Prompt(append(out, t.t.Prompt[last:]...)), nil
}

// encode returns the JSON of v for parameter p: its default when v is nil.
func encode(p *format.Param, v any) ([]byte, error) {
	var (
		b     []byte
		unset bool
		err   error
	)
	switch p.Kind {
	case format.KindInt:
		b, unset, err = encodeInt(p, v)
	case format.KindFloat:
		b, unset, err = encodeFloat(p, v)
	case format.KindBoolean:
		b, unset, err = encodeBool(v)
	case format.KindString, format.KindCombo:
		b, unset, err = encodeString(v)
	}
	switch {
	case err != nil:
		return nil, err
	case unset && !p.Optional:
		return nil, ErrRequired
	case unset:
		return p.Default, nil
	default:
		return b, nil
	}
}

func encodeInt(p *format.Param, v any) (b []byte, unset bool, err error) {
	var n big.Int
	switch v := v.(type) {
	case int:
		n.SetInt64(int64(v))
	case int64:
		n.SetInt64(v)
	case uint64:
		n.SetUint64(v)
	case *int:
		if v == nil {
			return nil, true, nil
		}
		n.SetInt64(int64(*v))
	case *int64:
		if v == nil {
			return nil, true, nil
		}
		n.SetInt64(*v)
	case *uint64:
		if v == nil {
			return nil, true, nil
		}
		n.SetUint64(*v)
	case nil:
		return nil, true, nil
	default:
		return nil, false, wrongType(v, "int, int64 or uint64")
	}
	if !p.InRange(format.Bound{Int: &n}) {
		return nil, false, outOfRange(p, n.String())
	}
	return n.Append(nil, 10), false, nil
}

func encodeFloat(p *format.Param, v any) (b []byte, unset bool, err error) {
	var f float64
	switch v := v.(type) {
	case float64:
		f = v
	case *float64:
		if v == nil {
			return nil, true, nil
		}
		f = *v
	case nil:
		return nil, true, nil
	default:
		return nil, false, wrongType(v, "float64")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false, fmt.Errorf("%w: %v is not finite", ErrWrongType, f)
	}
	if !p.InRange(format.Bound{Float: f}) {
		return nil, false, outOfRange(p, strconv.FormatFloat(f, 'g', -1, 64))
	}
	return appendFloat(nil, f), false, nil
}

// appendFloat writes f as tscomfy's stringify does: as JavaScript's JSON.stringify, except that
// -0 is 0 and an integer beyond 2^53 (every float64 there is one) is in exponent form.
func appendFloat(b []byte, f float64) []byte {
	if f == 0 {
		return append(b, '0')
	}
	if math.Abs(f) < 1<<53 {
		return jsontext.AppendFloat(b, f, 64)
	}
	// Go writes the exponent with at least two digits; from 2^53 on it has two or three, as
	// JavaScript's toExponential writes them.
	return strconv.AppendFloat(b, f, 'e', -1, 64)
}

func encodeBool(v any) (b []byte, unset bool, err error) {
	var x bool
	switch v := v.(type) {
	case bool:
		x = v
	case *bool:
		if v == nil {
			return nil, true, nil
		}
		x = *v
	case nil:
		return nil, true, nil
	default:
		return nil, false, wrongType(v, "bool")
	}
	return strconv.AppendBool(nil, x), false, nil
}

func encodeString(v any) (b []byte, unset bool, err error) {
	var s string
	switch v := v.(type) {
	case string:
		s = v
	case *string:
		if v == nil {
			return nil, true, nil
		}
		s = *v
	case nil:
		return nil, true, nil
	default:
		return nil, false, wrongType(v, "string")
	}
	if !utf8.ValidString(s) {
		return nil, false, fmt.Errorf("%w: the string isn't valid UTF-8", ErrWrongType)
	}
	b, err = jsontext.AppendQuote(nil, s)
	return b, false, err
}

func wrongType(v any, want string) error {
	return fmt.Errorf("%w: got %T, want %s", ErrWrongType, v, want)
}

func outOfRange(p *format.Param, value string) error {
	switch {
	case p.Min.Set() && p.Max.Set():
		return fmt.Errorf("%w: %s is not within %s to %s", ErrOutOfRange, value, p.Min.Literal, p.Max.Literal)
	case p.Min.Set():
		return fmt.Errorf("%w: %s is below %s", ErrOutOfRange, value, p.Min.Literal)
	default:
		return fmt.Errorf("%w: %s is above %s", ErrOutOfRange, value, p.Max.Literal)
	}
}

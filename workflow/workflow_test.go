package workflow_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/TheeKia/tscomfy-go/workflow"
)

// A template on a node "n" with one parameter of each kind.
const base = `{
  "format": 1,
  "name": "sample",
  "params": [
    { "name": "seed", "node": "n", "input": "seed", "kind": "int", "min": 0, "max": 18446744073709551615 },
    { "name": "steps", "node": "n", "input": "steps", "kind": "int", "min": 1, "max": 100, "optional": true },
    { "name": "cfg", "node": "n", "input": "cfg", "kind": "float", "min": 0, "max": 30 },
    { "name": "text", "node": "n", "input": "text", "kind": "string" },
    { "name": "tiled", "node": "n", "input": "tiled", "kind": "boolean", "optional": true },
    { "name": "sampler", "node": "n", "input": "sampler", "kind": "combo", "optional": true }
  ],
  "prompt": {
    "n": {
      "inputs": { "text": "a cat", "seed": 0, "steps": 20, "cfg": 7.5, "tiled": false, "sampler": "euler", "model": ["m", 0] },
      "class_type": "Sampler"
    },
    "m": { "inputs": {}, "class_type": "Model" }
  }
}`

func mustLoad(t *testing.T, doc string) *workflow.Template {
	t.Helper()
	tmpl, err := workflow.Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tmpl
}

func TestFillMatchesTscomfy(t *testing.T) {
	data, err := os.ReadFile("testdata/cross-language.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Template jsontext.Value `json:"template"`
		Cases    []struct {
			Name   string           `json:"name"`
			Values []jsontext.Value `json:"values"`
			Prompt string           `json:"prompt"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	tmpl, err := workflow.Load(fixture.Template)
	if err != nil {
		t.Fatal(err)
	}
	params := tmpl.Params()
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			values := make([]any, len(params))
			for i, p := range params {
				values[i] = goValue(t, p.Kind, c.Values[i])
			}
			got, err := tmpl.Fill(values...)
			if err != nil {
				t.Fatalf("Fill: %v", err)
			}
			if string(got) != c.Prompt {
				t.Errorf("Fill gave\n%s\ntscomfy built\n%s", got, c.Prompt)
			}
		})
	}
}

// goValue decodes a fixture value as the Go value a backend would pass: integers as int64 or
// uint64, floats from JavaScript's shortest text.
func goValue(t *testing.T, kind workflow.Kind, v jsontext.Value) any {
	t.Helper()
	if string(v) == "null" {
		return nil
	}
	switch kind {
	case workflow.KindInt:
		if strings.HasPrefix(string(v), "-") {
			n, err := strconv.ParseInt(string(v), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		n, err := strconv.ParseUint(string(v), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	case workflow.KindFloat:
		var text string
		if err := json.Unmarshal(v, &text); err != nil {
			t.Fatal(err)
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		return f
	case workflow.KindBoolean:
		return string(v) == "true"
	default:
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func TestLoad(t *testing.T) {
	tmpl := mustLoad(t, base)
	if tmpl.Name() != "sample" {
		t.Errorf("Name() = %q", tmpl.Name())
	}
	params := tmpl.Params()
	want := []struct {
		name, kind, def, min, max string
		optional                  bool
	}{
		{"seed", "int", "0", "0", "18446744073709551615", false},
		{"steps", "int", "20", "1", "100", true},
		{"cfg", "float", "7.5", "0", "30", false},
		{"text", "string", `"a cat"`, "", "", false},
		{"tiled", "boolean", "false", "", "", true},
		{"sampler", "combo", `"euler"`, "", "", true},
	}
	if len(params) != len(want) {
		t.Fatalf("got %d params, want %d", len(params), len(want))
	}
	for i, w := range want {
		p := params[i]
		if p.Name != w.name || string(p.Kind) != w.kind || string(p.Default) != w.def ||
			string(p.Min) != w.min || string(p.Max) != w.max || p.Optional != w.optional || p.Node != "n" {
			t.Errorf("param %d = %+v, want %+v", i, p, w)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, old, new, want string
	}{
		{"another format", `"format": 1`, `"format": 2`, "format 2, want 1"},
		{"no format", `"format": 1,`, ``, "format 0, want 1"},
		{"an unknown member", `"name": "sample",`, `"name": "sample", "extra": 1,`, "unknown object member"},
		{"a bad name", `"name": "sample"`, `"name": "Sample"`, "the name must match"},
		{"a bad parameter name", `"name": "seed"`, `"name": "1seed"`, `parameter "1seed": the name must match`},
		{"a parameter twice", `"name": "steps"`, `"name": "seed"`, `parameter "seed": declared twice`},
		{"an input twice", `"input": "steps"`, `"input": "seed"`, "n.seed is parameter \"seed\" already"},
		{"a missing node", `"node": "n", "input": "text"`, `"node": "x", "input": "text"`, "the prompt has no input x.text"},
		{"a missing input", `"input": "text"`, `"input": "texts"`, "the prompt has no input n.texts"},
		{"a link", `"input": "text"`, `"input": "model"`, `the value ["m",0] is not a string`},
		{"an unknown kind", `"kind": "boolean"`, `"kind": "bool"`, `unknown kind "bool"`},
		{"a string for an int", `"seed": 0,`, `"seed": "0",`, `the value "0" is not an integer`},
		{"a float for an int", `"seed": 0,`, `"seed": 0.5,`, "the value 0.5 is not an integer"},
		{"an exponent for an int", `"seed": 0,`, `"seed": 1e3,`, "the value 1e3 is not an integer"},
		{"a string for a float", `"cfg": 7.5`, `"cfg": "7.5"`, `the value "7.5" is not a finite number`},
		{"an infinite float", `"cfg": 7.5`, `"cfg": 1e999`, "the value 1e999 is not a finite number"},
		{"a number for a boolean", `"tiled": false`, `"tiled": 0`, "the value 0 is not a boolean"},
		{"a default below min", `"steps": 20`, `"steps": 0`, "the value 0 is outside the bounds"},
		{"a default above max", `"cfg": 7.5`, `"cfg": 31`, "the value 31 is outside the bounds"},
		{"min above max", `"min": 1, "max": 100`, `"min": 101, "max": 100`, "min 101 is above max 100"},
		{"bounds on a string", `"kind": "string" }`, `"kind": "string", "min": 0 }`, "a string input has no bounds"},
		{"a float bound on an int", `"min": 1, "max": 100`, `"min": 1.5, "max": 100`, "min 1.5 is not an integer"},
		{"a node that isn't an object", `"m": { "inputs": {}, "class_type": "Model" }`, `"m": 1`, `node "m" is not an object`},
		{"inputs that aren't an object", `"m": { "inputs": {},`, `"m": { "inputs": [],`, `node "m": "inputs" is not an object`},
		{"a duplicate node", `"m": { "inputs": {}, "class_type": "Model" }`, `"m": {}, "m": {}`, "duplicate object member"},
		{"no prompt", `"prompt": {`, `"other": {`, "unknown object member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(base, tt.old) {
				t.Fatalf("the base template has no %q", tt.old)
			}
			_, err := workflow.Load([]byte(strings.Replace(base, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadPromptShape(t *testing.T) {
	for _, prompt := range []string{`[]`, `"n"`, `null`} {
		_, err := workflow.Load([]byte(`{"format":1,"name":"x","params":[],"prompt":` + prompt + `}`))
		if err == nil || !strings.Contains(err.Error(), "the prompt is not an object") {
			t.Errorf("prompt %s: Load error = %v", prompt, err)
		}
	}
	if _, err := workflow.Load([]byte(`{"format":1,"name":"x","params":[]}`)); err == nil || !strings.Contains(err.Error(), "no prompt") {
		t.Errorf("no prompt: Load error = %v", err)
	}
}

func TestLoadRejectsInvalidUTF8(t *testing.T) {
	_, err := workflow.Load([]byte(strings.Replace(base, `"a cat"`, "\"a \xff cat\"", 1)))
	if err == nil {
		t.Error("Load accepted invalid UTF-8")
	}
}

func TestFill(t *testing.T) {
	tmpl := mustLoad(t, base)
	steps, tiled, sampler := 30, true, "dpmpp_2m"
	tests := []struct {
		name   string
		values []any
		want   string
	}{
		{
			"every value",
			[]any{uint64(math.MaxUint64), &steps, 0.1, "a \"dog\"\n", &tiled, &sampler},
			`{"n":{"inputs":{"text":"a \"dog\"\n","seed":18446744073709551615,"steps":30,"cfg":0.1,"tiled":true,"sampler":"dpmpp_2m","model":["m",0]},"class_type":"Sampler"},"m":{"inputs":{},"class_type":"Model"}}`,
		},
		{
			"defaults for nil and nil pointers",
			[]any{int64(5), nil, 7.5, "a cat", (*bool)(nil), (*string)(nil)},
			`{"n":{"inputs":{"text":"a cat","seed":5,"steps":20,"cfg":7.5,"tiled":false,"sampler":"euler","model":["m",0]},"class_type":"Sampler"},"m":{"inputs":{},"class_type":"Model"}}`,
		},
		{
			"an int and negative zero",
			[]any{1, nil, math.Copysign(0, -1), "", false, "x"},
			`{"n":{"inputs":{"text":"","seed":1,"steps":20,"cfg":0,"tiled":false,"sampler":"x","model":["m",0]},"class_type":"Sampler"},"m":{"inputs":{},"class_type":"Model"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tmpl.Fill(tt.values...)
			if err != nil {
				t.Fatalf("Fill: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Fill =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestFillErrors(t *testing.T) {
	tmpl := mustLoad(t, base)
	valid := func() []any { return []any{0, nil, 7.5, "a cat", nil, nil} }
	with := func(i int, v any) []any {
		values := valid()
		values[i] = v
		return values
	}
	big, small := uint64(101), 0
	tests := []struct {
		name   string
		values []any
		param  string
		err    error
		detail string
	}{
		{"a required nil", with(0, nil), "seed", workflow.ErrRequired, ""},
		{"a required nil pointer", with(3, (*string)(nil)), "text", workflow.ErrRequired, ""},
		{"a negative seed", with(0, -1), "seed", workflow.ErrOutOfRange, "-1 is not within 0 to 18446744073709551615"},
		{"steps above max", with(1, &big), "steps", workflow.ErrOutOfRange, "101 is not within 1 to 100"},
		{"steps below min", with(1, &small), "steps", workflow.ErrOutOfRange, "0 is not within 1 to 100"},
		{"cfg above max", with(2, 30.5), "cfg", workflow.ErrOutOfRange, "30.5 is not within 0 to 30"},
		{"NaN", with(2, math.NaN()), "cfg", workflow.ErrWrongType, "NaN is not finite"},
		{"infinity", with(2, math.Inf(1)), "cfg", workflow.ErrWrongType, "+Inf is not finite"},
		{"a float32", with(2, float32(0.5)), "cfg", workflow.ErrWrongType, "got float32, want float64"},
		{"an int for a float", with(2, 7), "cfg", workflow.ErrWrongType, "got int, want float64"},
		{"an int32", with(0, int32(1)), "seed", workflow.ErrWrongType, "got int32, want int, int64 or uint64"},
		{"a float for an int", with(0, 1.0), "seed", workflow.ErrWrongType, "got float64"},
		{"a string pointer for a bool", with(4, new("x")), "tiled", workflow.ErrWrongType, "got *string, want bool"},
		{"invalid UTF-8", with(3, "\xff"), "text", workflow.ErrWrongType, "isn't valid UTF-8"},
		{"a byte slice for a string", with(3, []byte("x")), "text", workflow.ErrWrongType, "got []uint8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tmpl.Fill(tt.values...)
			pe, ok := errors.AsType[*workflow.ParamError](err)
			if !ok {
				t.Fatalf("Fill error = %v, want a *ParamError", err)
			}
			if pe.Workflow != "sample" || pe.Param != tt.param || !errors.Is(err, tt.err) {
				t.Errorf("Fill error = %#v, want parameter %q and %v", pe, tt.param, tt.err)
			}
			if !strings.Contains(err.Error(), tt.detail) {
				t.Errorf("Fill error = %q, want it to contain %q", err, tt.detail)
			}
		})
	}
	if _, err := tmpl.Fill(0); err == nil || !strings.Contains(err.Error(), "Fill takes 6 values, got 1") {
		t.Errorf("Fill with one value: error = %v", err)
	}
}

func TestFillOneSidedBounds(t *testing.T) {
	doc := strings.Replace(base, `"min": 1, "max": 100`, `"min": 1`, 1)
	doc = strings.Replace(doc, `"min": 0, "max": 30`, `"max": 30`, 1)
	tmpl := mustLoad(t, doc)
	low, high := 0, 31.0
	if _, err := tmpl.Fill(0, &low, 1.0, "", nil, nil); err == nil || !strings.Contains(err.Error(), "0 is below 1") {
		t.Errorf("steps 0: error = %v", err)
	}
	if _, err := tmpl.Fill(0, nil, high, "", nil, nil); err == nil || !strings.Contains(err.Error(), "31 is above 30") {
		t.Errorf("cfg 31: error = %v", err)
	}
}

func TestBoundsBeyond64Bits(t *testing.T) {
	doc := strings.Replace(base, `"min": 0, "max": 18446744073709551615`, `"min": -100000000000000000000, "max": 100000000000000000000`, 1)
	tmpl := mustLoad(t, doc)
	for _, v := range []any{int64(math.MinInt64), uint64(math.MaxUint64)} {
		if _, err := tmpl.Fill(v, nil, 1.0, "", nil, nil); err != nil {
			t.Errorf("Fill(%v): %v", v, err)
		}
	}
}

func TestParamsAreCopies(t *testing.T) {
	tmpl := mustLoad(t, base)
	params := tmpl.Params()
	params[0].Name = "changed"
	params[3].Default[1] = 'X'
	params[0].Max[0] = '9'
	again := tmpl.Params()
	if again[0].Name != "seed" || string(again[3].Default) != `"a cat"` || string(again[0].Max) != "18446744073709551615" {
		t.Errorf("changing Params changed the template: %+v", again)
	}
	got, err := tmpl.Fill(0, nil, 7.5, nil, nil, nil)
	if err == nil {
		t.Fatalf("Fill with a required nil: got %s", got)
	}
}

func TestFillConcurrently(t *testing.T) {
	tmpl := mustLoad(t, base)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				got, err := tmpl.Fill(i*1000+j, nil, 1.5, "x", nil, nil)
				if err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(got), `"seed":`+strconv.Itoa(i*1000+j)+",") {
					t.Errorf("Fill gave %s", got)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestMustLoadPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustLoad didn't panic on an invalid template")
		}
	}()
	workflow.MustLoad([]byte(`{"format":2}`))
}

func FuzzLoad(f *testing.F) {
	f.Add([]byte(base))
	data, err := os.ReadFile("testdata/cross-language.json")
	if err != nil {
		f.Fatal(err)
	}
	var fixture struct {
		Template jsontext.Value `json:"template"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(fixture.Template))
	f.Fuzz(func(t *testing.T, data []byte) {
		tmpl, err := workflow.Load(data)
		if err != nil {
			return
		}
		// Filling the defaults gives valid JSON; leaving every parameter unset gives the template's
		// prompt back, compacted, byte for byte. (A required parameter's default is re-encoded:
		// 1E2 becomes 100.)
		values := make([]any, len(tmpl.Params()))
		unset := true
		for i, p := range tmpl.Params() {
			if !p.Optional {
				values[i] = defaultValue(t, p)
				unset = false
			}
		}
		got, err := tmpl.Fill(values...)
		if err != nil {
			t.Fatalf("Fill with the defaults: %v", err)
		}
		if !jsontext.Value(got).IsValid() {
			t.Fatalf("Fill gave invalid JSON: %s", got)
		}
		if !unset {
			return
		}
		var doc struct {
			Prompt jsontext.Value `json:"prompt"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if err := doc.Prompt.Compact(); err != nil {
			t.Fatal(err)
		}
		if string(got) != string(doc.Prompt) {
			t.Fatalf("Fill with the defaults gave\n%s\nnot the prompt\n%s", got, doc.Prompt)
		}
	})
}

func FuzzFill(f *testing.F) {
	tmpl, err := workflow.Load([]byte(base))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint64(0), int64(20), 7.5, "a cat", true, "euler")
	f.Add(uint64(math.MaxUint64), int64(-1), math.Copysign(0, -1), " \x00\"", false, "")
	f.Fuzz(func(t *testing.T, seed uint64, steps int64, cfg float64, text string, tiled bool, sampler string) {
		got, err := tmpl.Fill(seed, steps, cfg, text, tiled, sampler)
		if err != nil {
			if _, ok := errors.AsType[*workflow.ParamError](err); !ok {
				t.Fatalf("Fill error %v is not a *ParamError", err)
			}
			return
		}
		var prompt map[string]struct {
			Inputs map[string]jsontext.Value `json:"inputs"`
		}
		if err := json.Unmarshal(got, &prompt); err != nil {
			t.Fatalf("Fill gave invalid JSON %s: %v", got, err)
		}
		var gotText string
		if err := json.Unmarshal(prompt["n"].Inputs["text"], &gotText); err != nil || gotText != text {
			t.Fatalf("text %q came back as %q (%v)", text, gotText, err)
		}
		if string(prompt["n"].Inputs["seed"]) != strconv.FormatUint(seed, 10) {
			t.Fatalf("seed %d came back as %s", seed, prompt["n"].Inputs["seed"])
		}
		var gotCfg float64
		if err := json.Unmarshal(prompt["n"].Inputs["cfg"], &gotCfg); err != nil || gotCfg != cfg {
			t.Fatalf("cfg %v came back as %s (%v)", cfg, prompt["n"].Inputs["cfg"], err)
		}
	})
}

// defaultValue returns a parameter's default as the Go value Fill takes.
func defaultValue(t *testing.T, p workflow.Param) any {
	t.Helper()
	switch p.Kind {
	case workflow.KindInt:
		if n, err := strconv.ParseInt(string(p.Default), 10, 64); err == nil {
			return n
		}
		n, err := strconv.ParseUint(string(p.Default), 10, 64)
		if err != nil {
			t.Skip("a default beyond 64 bits")
		}
		return n
	case workflow.KindFloat:
		f, err := strconv.ParseFloat(string(p.Default), 64)
		if err != nil {
			t.Fatal(err)
		}
		return f
	case workflow.KindBoolean:
		return string(p.Default) == "true"
	default:
		var s string
		if err := json.Unmarshal(p.Default, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
}

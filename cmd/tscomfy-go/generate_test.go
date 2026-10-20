package main

import (
	"bytes"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestGenerateGolden(t *testing.T) {
	export, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := generate("golden", export)
	if err != nil {
		t.Fatal(err)
	}
	// The golden package is compiled with the module and imported by TestGoldenBuilds.
	dir := filepath.Join("internal", "golden")
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := writeOutput(dir, files); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := slices.Sorted(maps.Keys(files)); !slices.Equal(names, want) {
		t.Errorf("golden files %v, generated %v", names, want)
	}
	for _, name := range names {
		golden, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(files[name], golden) {
			t.Errorf("%s differs from the golden file (run go test ./cmd/tscomfy-go -update):\n%s", name, files[name])
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	export, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := generate("workflows", export)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := generate("workflows", export)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.EqualFunc(first, again, bytes.Equal) {
			t.Fatal("two runs on the same export differ")
		}
	}
}

// exportOf builds an export document from templates, each given as its name, params and prompt.
func exportOf(templates ...string) []byte {
	return []byte(`{"format":1,"templates":[` + strings.Join(templates, ",") + `]}`)
}

func tmpl(name, params, prompt string) string {
	return `{"format":1,"name":"` + name + `","params":[` + params + `],"prompt":` + prompt + `}`
}

// intParam is an int parameter on input input of node "n".
func intParam(name, input, bounds string) string {
	return `{"name":"` + name + `","node":"n","input":"` + input + `","kind":"int"` + bounds + `}`
}

func TestGenerateErrors(t *testing.T) {
	prompt := func(node string, inputs ...string) string {
		var members []string
		for _, input := range inputs {
			members = append(members, `"`+input+`":1`)
		}
		return `{"` + node + `":{"inputs":{` + strings.Join(members, ",") + `},"class_type":"X"}}`
	}
	tests := []struct {
		name   string
		export []byte
		want   string
	}{
		{
			"another format",
			[]byte(`{"format":2,"templates":[]}`),
			"the export wrote template format 2, want 1: install the @tscomfy/go version that matches",
		},
		{
			"an unknown member in the export",
			[]byte(`{"format":1,"templates":[],"version":"0.1.0"}`),
			"the export's output",
		},
		{
			"a template Load refuses",
			exportOf(tmpl("a", intParam("x", "x", ""), `{}`)),
			`template "a": parameter "x": the prompt has no input n.x`,
		},
		{
			"two templates of one name",
			exportOf(tmpl("a", "", prompt("n")), tmpl("a", "", prompt("n"))),
			`two templates are named "a"`,
		},
		{
			"a type and a constant of one name",
			exportOf(tmpl("a", "", prompt("bc")), tmpl("a_bc", "", prompt("n"))),
			`the constant of node "bc" of template "a" and the type of template "a_bc" are both named ABc`,
		},
		{
			"two constants of one name",
			exportOf(tmpl("a", "", `{"b-c":{"inputs":{},"class_type":"X"},"b_c":{"inputs":{},"class_type":"X"}}`)),
			`the constant of node "b-c" of template "a" and the constant of node "b_c" of template "a" are both named ABC`,
		},
		{
			"two fields of one name",
			exportOf(tmpl("a", intParam("a_b", "x", "")+","+intParam("aB", "y", ""), prompt("n", "x", "y"))),
			`template "a": the field of parameter "a_b" and the field of parameter "aB" are both named AB`,
		},
		{
			"a field named Build",
			exportOf(tmpl("a", intParam("build", "x", ""), prompt("n", "x"))),
			`template "a": the method Build and the field of parameter "build" are both named Build`,
		},
		{
			"int bounds beyond uint64",
			exportOf(tmpl("a", intParam("x", "x", `,"min":0,"max":18446744073709551616`), prompt("n", "x"))),
			`template "a", parameter "x": the bounds 0 to 18446744073709551616 fit neither int64 nor uint64`,
		},
		{
			"int bounds across int64 and uint64",
			exportOf(tmpl("a", intParam("x", "x", `,"min":-1,"max":18446744073709551615`), prompt("n", "x"))),
			"the bounds -1 to 18446744073709551615 fit neither int64 nor uint64",
		},
		{
			"an unbounded max beyond int64",
			exportOf(tmpl("a", intParam("x", "x", `,"max":9223372036854775808`), prompt("n", "x"))),
			"the bounds up to 9223372036854775808 fit neither",
		},
		{
			"a template name go build takes for a test file",
			exportOf(tmpl("txt_test", "", prompt("n"))),
			`template "txt_test": go build would take txt_test.go for a test file`,
		},
		{
			"a template name with a GOOS suffix",
			exportOf(tmpl("up_windows", "", prompt("n"))),
			`template "up_windows": go build would compile up_windows.go for some systems or architectures only`,
		},
		{
			"a template name with a GOOS and GOARCH suffix",
			exportOf(tmpl("up_linux_arm64", "", prompt("n"))),
			"for some systems or architectures only",
		},
		{
			"a template name with a GOARCH suffix",
			exportOf(tmpl("x_amd64", "", prompt("n"))),
			"for some systems or architectures only",
		},
		{
			"a node id without an identifier",
			exportOf(tmpl("a", "", `{"--":{"inputs":{},"class_type":"X"}}`)),
			`template "a": node id "--" gives no Go identifier`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := generate("workflows", tt.export)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("generate error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestFieldCommentsQuoteUnprintableNames(t *testing.T) {
	export := exportOf(tmpl("a", `{"name":"x","node":"n\nvar Injected = 1\n//","input":"in\ty","kind":"int"}`,
		`{"n\nvar Injected = 1\n//":{"inputs":{"in\ty":1},"class_type":"X"}}`))
	files, err := generate("w", export)
	if err != nil {
		t.Fatal(err)
	}
	code := string(files["a.go"])
	if !strings.Contains(code, `X int64 // "n\nvar Injected = 1\n//"."in\ty"`) {
		t.Errorf("the field comment doesn't quote the names:\n%s", code)
	}
	if strings.Contains(code, "\nvar Injected") {
		t.Errorf("a node id broke out of its comment:\n%s", code)
	}
}

func TestFieldCommentsEscapeUnprintableDefaults(t *testing.T) {
	// A byte-order mark is valid in JSON, unescaped, but not in Go source past its start.
	for _, text := range []string{"a\ufeffb", "\ufeff" + strings.Repeat("x", 45)} {
		export := exportOf(tmpl("a", `{"name":"x","node":"n","input":"text","kind":"string","optional":true}`,
			`{"n":{"inputs":{"text":"`+text+`"},"class_type":"X"}}`))
		files, err := generate("w", export)
		if err != nil {
			t.Fatalf("default %q: %v", text, err)
		}
		if !strings.Contains(string(files["a.go"]), `\ufeff`) {
			t.Errorf("default %q: the field comment doesn't escape it:\n%s", text, files["a.go"])
		}
	}
}

func TestGoName(t *testing.T) {
	tests := map[string]string{
		"txt2img":         "Txt2Img",
		"filename_prefix": "FilenamePrefix",
		"blur0":           "Blur0",
		"vae-decode":      "VaeDecode",
		"a1b2":            "A1B2",
		"x_1":             "X1",
		"HTTPServer":      "HTTPServer",
		"ünï":             "N",
		"__":              "",
		"2nd":             "2Nd",
	}
	for in, want := range tests {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIntType(t *testing.T) {
	tests := []struct{ bounds, want string }{
		{`,"min":0,"max":100`, "int"},
		{`,"min":-2147483648,"max":2147483647`, "int"},
		{`,"min":0,"max":2147483648`, "int64"},
		{`,"min":0,"max":18446744073709551615`, "uint64"},
		{`,"min":0,"max":9223372036854775808`, "uint64"},
		{`,"min":0,"max":9223372036854775807`, "int64"},
		{`,"min":1`, "int64"},
		{`,"max":1`, "int64"},
		{``, "int64"},
		{`,"min":-9223372036854775808,"max":9223372036854775807`, "int64"},
	}
	for _, tt := range tests {
		files, err := generate("w", exportOf(tmpl("a", intParam("x", "x", tt.bounds), `{"n":{"inputs":{"x":1},"class_type":"X"}}`)))
		if err != nil {
			t.Errorf("bounds %s: %v", tt.bounds, err)
			continue
		}
		if !strings.Contains(string(files["a.go"]), "\tX "+tt.want+" //") {
			t.Errorf("bounds %s: want a field of type %s in\n%s", tt.bounds, tt.want, files["a.go"])
		}
	}
}

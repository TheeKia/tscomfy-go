package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func generatedGo(body string) []byte { return []byte(header + "\npackage w\n" + body) }

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteOutputCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "workflows")
	if err := writeOutput(dir, map[string][]byte{"a.go": generatedGo(""), "a.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{"a.go", "a.json"}) {
		t.Errorf("the directory holds %v", got)
	}
}

func TestWriteOutputReplacesAndDeletes(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"old.go":    string(generatedGo("// old")),
		"old.json":  "{}",
		"keep.go":   string(generatedGo("// keep")),
		"keep.json": "{}\n",
	})
	unchanged := filepath.Join(dir, "keep.json")
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(unchanged, past, past); err != nil {
		t.Fatal(err)
	}
	err := writeOutput(dir, map[string][]byte{
		"keep.go":   generatedGo("// new"),
		"keep.json": []byte("{}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{"keep.go", "keep.json"}) {
		t.Errorf("the directory holds %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "keep.go"))
	if err != nil || !strings.HasSuffix(string(data), "// new") {
		t.Errorf("keep.go = %q, %v", data, err)
	}
	info, err := os.Stat(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Error("an unchanged file was rewritten")
	}
}

func TestWriteOutputRemovesLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{tempPrefix + "123": "half a file"})
	if err := writeOutput(dir, map[string][]byte{"a.go": generatedGo(""), "a.json": []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{"a.go", "a.json"}) {
		t.Errorf("the directory holds %v", got)
	}
}

func TestWriteOutputRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(target, generatedGo(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "x.go")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := writeOutput(dir, map[string][]byte{}); err == nil || !strings.Contains(err.Error(), "holds x.go") {
		t.Errorf("writeOutput error = %v", err)
	}
}

func TestWriteOutputRefusesForeignFiles(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a hand-written Go file", map[string]string{"doc.go": "package w\n"}, "holds doc.go"},
		{"a JSON file without its Go file", map[string]string{"a.json": "{}"}, "holds a.json"},
		{"a JSON file next to a hand-written Go file", map[string]string{"a.go": "package w\n", "a.json": "{}"}, "holds a.go"},
		{"another file", map[string]string{"README.md": "hi"}, "holds README.md"},
		{"a directory", map[string]string{"sub/x.txt": "hi"}, "holds sub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			before := listDir(t, dir)
			err := writeOutput(dir, map[string][]byte{"new.go": generatedGo(""), "new.json": []byte("{}")})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "didn't write") {
				t.Errorf("writeOutput error = %v, want one containing %q", err, tt.want)
			}
			if after := listDir(t, dir); !slices.Equal(before, after) {
				t.Errorf("the directory changed from %v to %v", before, after)
			}
		})
	}
}

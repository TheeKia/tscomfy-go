package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"-workflows", "w"}, {"-out", "o"}, {"-workflows", "w", "-out", "o", "extra"}, {"-nope"}} {
		var stderr bytes.Buffer
		if err := run(t.Context(), args, &stderr); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
		if !strings.Contains(stderr.String(), "usage: tscomfy-go -workflows dir -out dir") && !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Errorf("run(%q) printed %q", args, stderr.String())
		}
	}
	if err := run(t.Context(), []string{"-h"}, &bytes.Buffer{}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("run(-h) = %v", err)
	}
}

func TestRunRefusesABadPackageName(t *testing.T) {
	for _, name := range []string{"my-workflows", "_"} {
		out := filepath.Join(t.TempDir(), name)
		err := run(t.Context(), []string{"-workflows", t.TempDir(), "-out", out}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), `"`+name+`" is not a Go package name`) {
			t.Errorf("run with -out %s: error = %v", name, err)
		}
	}
}

func TestRunNeedsAnEntry(t *testing.T) {
	err := run(t.Context(), []string{"-workflows", t.TempDir(), "-out", filepath.Join(t.TempDir(), "w")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "has no index.ts") {
		t.Errorf("run error = %v", err)
	}
}

// fakeBun puts a bun on PATH that runs script, a shell script, after checking that it was asked
// for the export.
func fakeBun(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake bun is a shell script")
	}
	bin := t.TempDir()
	body := "#!/bin/sh\n" +
		`[ "$*" = "x --bun --no-install tscomfy-go-export index.ts" ] || { echo "unexpected arguments: $*" >&2; exit 9; }` + "\n" +
		script + "\n"
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func workflowsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunWritesTheExport(t *testing.T) {
	export, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	exportFile := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(exportFile, export, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeBun(t, "cat "+exportFile)
	out := filepath.Join(t.TempDir(), "workflows")
	if err := run(t.Context(), []string{"-workflows", workflowsDir(t), "-out", out}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, out); !slices.Equal(got, []string{"bare.go", "bare.json", "every_kind.go", "every_kind.json"}) {
		t.Errorf("the output holds %v", got)
	}
}

func TestRunReportsOutputBesidesTheExport(t *testing.T) {
	// bun init writes an index.ts that logs this; a debug log does the same.
	fakeBun(t, `echo 'Hello via Bun!'; echo '{"format":1,"templates":[]}'`)
	out := filepath.Join(t.TempDir(), "workflows")
	err := run(t.Context(), []string{"-workflows", workflowsDir(t), "-out", out}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "besides its JSON") || !strings.Contains(err.Error(), `"Hello via Bun!"`) {
		t.Errorf("run error = %v, want one that names the stray output", err)
	}
}

func TestRunReportsTheExportsError(t *testing.T) {
	fakeBun(t, `echo 'tscomfy-go-export: template "a": the name must match' >&2; exit 1`)
	out := filepath.Join(t.TempDir(), "workflows")
	err := run(t.Context(), []string{"-workflows", workflowsDir(t), "-out", out}, &bytes.Buffer{})
	if err == nil || err.Error() != `the export failed: tscomfy-go-export: template "a": the name must match` {
		t.Errorf("run error = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed export wrote the output directory")
	}
}

func TestRunWithoutBun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := run(t.Context(), []string{"-workflows", workflowsDir(t), "-out", filepath.Join(t.TempDir(), "w")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "bun is not on PATH") {
		t.Errorf("run error = %v", err)
	}
}

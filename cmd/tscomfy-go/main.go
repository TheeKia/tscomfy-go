// Command tscomfy-go turns TypeScript workflows written with @tscomfy/go into Go. For each
// template the workflows project exports, it writes the template file and a Go file with a typed
// struct, whose Build method fills the template, and a constant per named node.
//
// go generate runs it:
//
//	//go:generate go tool tscomfy-go -workflows ./workflows -out ./internal/workflows
//
// Flags:
//
//	-workflows dir  the TypeScript workflows project; its index.ts exports the templates
//	-out dir        the Go package directory to write; its base name is the package name
//
// It runs the project's export command with Bun (1.4 or later), so the project's own installed
// @tscomfy packages are used. It needs no network and no ComfyUI. The output directory belongs to
// it: it holds only the files it wrote, and stale ones are deleted.
package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stderr)
	stop()
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "tscomfy-go: %v\n", err)
		os.Exit(1)
	}
}

// errUsage is a command line the flag set already reported.
var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("tscomfy-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workflows := flags.String("workflows", "", "the TypeScript workflows project; its index.ts exports the templates")
	out := flags.String("out", "", "the Go package directory to write; its base name is the package name")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: tscomfy-go -workflows dir -out dir")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	if *workflows == "" || *out == "" || flags.NArg() > 0 {
		flags.Usage()
		return errUsage
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	pkg := filepath.Base(dir)
	if !token.IsIdentifier(pkg) || pkg == "_" {
		return fmt.Errorf("the output directory's name %q is not a Go package name", pkg)
	}
	export, err := runExport(ctx, *workflows)
	if err != nil {
		return err
	}
	files, err := generate(pkg, export)
	if err != nil {
		return err
	}
	return writeOutput(dir, files)
}

// runExport runs the workflows project's export command and returns its output: the export
// document (docs/spec.md §3.5).
func runExport(ctx context.Context, workflows string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(workflows, "index.ts")); err != nil {
		return nil, fmt.Errorf("the workflows project %s has no index.ts", workflows)
	}
	// bun x finds the bin of the @tscomfy/go the project installed; --no-install keeps it from
	// fetching one when there is none.
	cmd := exec.CommandContext(ctx, "bun", "x", "--bun", "--no-install", "tscomfy-go-export", "index.ts")
	cmd.Dir = workflows
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("bun is not on PATH: the generator runs the workflows with Bun 1.4 or later (https://bun.sh)")
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("the export failed: %s", message)
		}
		return nil, fmt.Errorf("the export failed: %w", err)
	}
	// The export writes one JSON document; anything else on stdout is the workflows' own output.
	if out := bytes.TrimSpace(stdout.Bytes()); len(out) > 0 && !jsontext.Value(out).IsValid() {
		line, _, _ := strings.Cut(strings.TrimSpace(stdout.String()), "\n")
		return nil, fmt.Errorf("the export printed something besides its JSON on stdout, such as a console.log in the workflows: %q", line)
	}
	return stdout.Bytes(), nil
}

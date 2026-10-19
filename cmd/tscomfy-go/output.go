package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// writeOutput makes dir hold exactly files (docs/spec.md §6.5). The directory belongs to the
// generator: it may hold only Go files with the generated header, the template file next to
// each, and temporary files a killed run left. When it holds anything else, nothing is written.
// Files whose content is unchanged are left alone; the rest of what the generator wrote before is
// deleted.
func writeOutput(dir string, files map[string][]byte) error {
	generated, err := generatedFiles(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := writeFile(filepath.Join(dir, name), files[name]); err != nil {
			return err
		}
	}
	for _, name := range generated {
		if _, keep := files[name]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// generatedFiles lists what the generator wrote in dir, and fails on anything else. A missing
// directory holds nothing.
func generatedFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	goFiles := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(name, ".go") {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			goFiles[name] = bytes.HasPrefix(data, []byte(header))
		}
	}
	var generated []string
	for _, e := range entries {
		name := e.Name()
		ok := false
		switch {
		case !e.Type().IsRegular():
		case strings.HasSuffix(name, ".go"):
			ok = goFiles[name]
		case strings.HasPrefix(name, tempPrefix):
			ok = true // a run killed between writing and renaming
		case strings.HasSuffix(name, ".json"):
			ok = goFiles[strings.TrimSuffix(name, ".json")+".go"]
		}
		if !ok {
			return nil, fmt.Errorf("%s holds %s, which tscomfy-go didn't write: the directory belongs to the generator, so move it out", dir, name)
		}
		generated = append(generated, name)
	}
	return generated, nil
}

// tempPrefix starts the names of the generator's temporary files.
const tempPrefix = ".tscomfy-go-"

// writeFile replaces path with data, through a temporary file, unless it holds data already.
func writeFile(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once renamed
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

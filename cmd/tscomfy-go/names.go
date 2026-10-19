package main

import "strings"

// goName converts a workflow name, parameter name or node id to an exported Go identifier part
// (docs/spec.md §6.3): split at every character that isn't an ASCII letter or digit and before
// every letter that follows a digit, capitalize each part, join them. It returns "" when nothing
// is left.
func goName(name string) string {
	var b strings.Builder
	start, afterDigit := true, false
	for i := range len(name) {
		c := name[i]
		switch {
		case isDigit(c):
			b.WriteByte(c)
			start, afterDigit = false, true
		case isLetter(c):
			if start || afterDigit {
				c = upper(c)
			}
			b.WriteByte(c)
			start, afterDigit = false, false
		default:
			start, afterDigit = true, false
		}
	}
	return b.String()
}

// unexported is the unexported form of an exported identifier.
func unexported(name string) string {
	return string(lower(name[0])) + name[1:]
}

func isDigit(c byte) bool  { return '0' <= c && c <= '9' }
func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func upper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c - 'A' + 'a'
	}
	return c
}

// automatic reports whether a node id is one tscomfy gives automatically: digits only. Such ids
// get no constant (§6.4).
func automatic(id string) bool {
	if id == "" {
		return false
	}
	for i := range len(id) {
		if !isDigit(id[i]) {
			return false
		}
	}
	return true
}

package mcp

import (
	"strings"
	"testing"
)

// Reported 2026-09-06: with context=1, matches on lines 5 and 7 of one file
// printed line 6 twice with a "--" between, and matches on lines 3 and 4
// printed both lines twice — half the result was a repeat.
func TestRenderContextHits_MergesOverlappingWindows(t *testing.T) {
	hits := []any{
		map[string]any{"line": 5, "text": "five", "before": []any{"four"}, "after": []any{"six"}},
		map[string]any{"line": 7, "text": "seven", "before": []any{"six"}, "after": []any{"eight"}},
	}
	var b strings.Builder
	if _, ok := renderContextHits(&b, "pkg/api.go", hits, nil); !ok {
		t.Fatal("render failed")
	}
	// Path once, then Read-shaped numbered lines: the path repeated on every
	// context line was most of the bytes of a context result.
	want := "pkg/api.go:\n  4- four\n  5: five\n  6- six\n  7: seven\n  8- eight\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestRenderContextHits_SeparatesDistantRuns(t *testing.T) {
	hits := []any{
		map[string]any{"line": 3, "text": "three", "before": []any{"two"}, "after": []any{"four"}},
		map[string]any{"line": 4, "text": "four", "before": []any{"three"}, "after": []any{"five"}},
		map[string]any{"line": 40, "text": "forty", "before": []any{"thirtynine"}, "after": []any{"fortyone"}},
	}
	var b strings.Builder
	renderContextHits(&b, "f.go", hits, nil)
	got := b.String()
	if strings.Count(got, "f.go:") != 1 {
		t.Errorf("path must appear once as a header:\n%s", got)
	}
	if strings.Count(got, "  3: ") != 1 || strings.Count(got, "  4: ") != 1 {
		t.Errorf("adjacent matches must each print once as a match line:\n%s", got)
	}
	if strings.Count(got, "  --\n") != 1 {
		t.Errorf("want exactly one separator between the two runs:\n%s", got)
	}
	if strings.Contains(got, "  4- ") {
		t.Errorf("a line that is a match must not also appear as context:\n%s", got)
	}
}

func TestRenderContextHits_NoContextNoSeparator(t *testing.T) {
	hits := []any{map[string]any{"line": 1, "text": "x\n"}}
	var b strings.Builder
	renderContextHits(&b, "f.go", hits, nil)
	if b.String() != "f.go:1: x\n" {
		t.Errorf("plain hit, trailing newline trimmed, no separator; got %q", b.String())
	}
}

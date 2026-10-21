package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOversized(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("package p\n"+strings.Repeat(" ", 11*1024*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Audit 2026-10-20 #1: Grove reports a per-file failure in
// IndexResult.Errors with a nil Go error and keeps the file's old facts.
// The refresh must say so, on every answer while the failure lasts.
func TestRefreshReportsFileLevelIndexErrors(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	h := evidenceHandler(t, map[string]string{"a.go": "package p\nfunc Target() {}\nfunc Caller() { Target() }\n"})
	writeOversized(t, h.Root, "a.go")
	for i := 1; i <= 2; i++ {
		note := h.refreshIndexBestEffort(context.Background())
		t.Logf("refresh %d warning=%q", i, note)
		if !strings.Contains(note, "a.go") || !strings.Contains(note, "exceeds") {
			t.Fatalf("refresh %d: per-file index error not surfaced: %q", i, note)
		}
		if !strings.Contains(note, "stale") {
			t.Fatalf("refresh %d: retained facts must be labelled stale: %q", i, note)
		}
	}
	// Fixed on disk: the warning goes away.
	if err := os.WriteFile(filepath.Join(h.Root, "a.go"), []byte("package p\nfunc Target() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if note := h.refreshIndexBestEffort(context.Background()); note != "" {
		t.Fatalf("resolved failure still reported: %q", note)
	}
}

// A file that has no indexed facts is missing, not stale: said once while
// the failure persists, and again only if it recurs after clearing.
func TestRefreshReportsUnindexedFileOnce(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	h := evidenceHandler(t, map[string]string{"a.go": "package p\nfunc Target() {}\n"})
	writeOversized(t, h.Root, "big.go")
	first := h.refreshIndexBestEffort(context.Background())
	if !strings.Contains(first, "big.go") || !strings.Contains(first, "missing") {
		t.Fatalf("first refresh must name the unindexed file: %q", first)
	}
	if again := h.refreshIndexBestEffort(context.Background()); again != "" {
		t.Fatalf("persistent failure without stale facts must not tag every answer: %q", again)
	}
	if err := os.Remove(filepath.Join(h.Root, "big.go")); err != nil {
		t.Fatal(err)
	}
	if note := h.refreshIndexBestEffort(context.Background()); note != "" {
		t.Fatalf("cleared failure reported: %q", note)
	}
	writeOversized(t, h.Root, "big.go")
	if note := h.refreshIndexBestEffort(context.Background()); !strings.Contains(note, "big.go") {
		t.Fatalf("recurring failure must be said again: %q", note)
	}
}

func TestIndexErrorWarningIsBounded(t *testing.T) {
	h := newTestHandler(t)
	h.Grove = nil
	var errs []string
	for i := 0; i < 7; i++ {
		errs = append(errs, fmt.Sprintf("f%d.go: %s", i, strings.Repeat("x", 500)))
	}
	note := h.indexErrorWarning(context.Background(), errs)
	if !strings.Contains(note, "7 file(s)") || !strings.Contains(note, "+4 more") || strings.Contains(note, "f3.go") {
		t.Fatalf("warning must show the first 3 failures and a count: %q", note)
	}
	if len(note) > 600 {
		t.Fatalf("warning not bounded (%d bytes): %q", len(note), note)
	}
}

// Every index-backed text answer carries the warning, not only
// change_impact's renderer.
func TestCompactAnswersCarryIndexErrorWarning(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	files := map[string]string{
		"go.mod": "module p\ngo 1.22\n",
		"a.go":   "package p\nfunc Target() {}\nfunc Caller() { Target() }\n",
		"b.go":   "package p\nfunc Other() { Target() }\n",
	}
	srv := compactFixture(t, files)
	callCompact(t, srv, "lookup", map[string]any{"name": "Target"}) // index while a.go is good
	writeOversized(t, srv.handler.Root, "a.go")
	for _, tc := range []struct {
		op   string
		args map[string]any
	}{
		{"lookup", map[string]any{"name": "Other"}},
		{"search", map[string]any{"terms": []any{"Other"}}},
		{"query", map[string]any{"terms": []any{"Other"}}},
		{"change_impact", map[string]any{"name": "Other"}},
	} {
		text := callCompact(t, srv, tc.op, tc.args)
		if !strings.Contains(text, "a.go (file exceeds") {
			t.Errorf("%s answer lacks the index failure warning:\n%.800s", tc.op, text)
		}
	}
}

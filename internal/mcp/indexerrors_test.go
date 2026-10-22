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
// IndexResult.Errors with a nil Go error. Since Grove v0.63.25 it also prunes
// the file's old facts, so the refresh names the file as missing from the
// result (once while the failure lasts) instead of serving it unlabelled.
func TestRefreshReportsFileLevelIndexErrors(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	h := evidenceHandler(t, map[string]string{"a.go": "package p\nfunc Target() {}\nfunc Caller() { Target() }\n"})
	writeOversized(t, h.Root, "a.go")
	note := h.refreshIndexBestEffort(context.Background())
	if !strings.Contains(note, "a.go") || !strings.Contains(note, "exceeds") || !strings.Contains(note, "missing") {
		t.Fatalf("per-file index error not surfaced as missing: %q", note)
	}
	if syms, err := h.Grove.FileSymbols(context.Background(), "a.go"); err == nil && len(syms) > 0 {
		t.Fatalf("removed Target still indexed after its file failed: %v", syms)
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
// change_impact's renderer. Each op is checked as the first answer after a
// fresh failure, since a missing file is said once per occurrence.
func TestCompactAnswersCarryIndexErrorWarning(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	good := "package p\nfunc Target() {}\nfunc Caller() { Target() }\n"
	files := map[string]string{
		"go.mod": "module p\ngo 1.22\n",
		"a.go":   good,
		"b.go":   "package p\nfunc Other() { Target() }\n",
	}
	srv := compactFixture(t, files)
	for _, tc := range []struct {
		op   string
		args map[string]any
	}{
		{"lookup", map[string]any{"name": "Other"}},
		{"search", map[string]any{"terms": []any{"Other"}}},
		{"query", map[string]any{"terms": []any{"Other"}}},
		{"change_impact", map[string]any{"name": "Other"}},
	} {
		if err := os.WriteFile(filepath.Join(srv.handler.Root, "a.go"), []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		callCompact(t, srv, "lookup", map[string]any{"name": "Target"}) // index while a.go is good
		writeOversized(t, srv.handler.Root, "a.go")
		text := callCompact(t, srv, tc.op, tc.args)
		if !strings.Contains(text, "a.go (file exceeds") {
			t.Errorf("%s answer lacks the index failure warning:\n%.800s", tc.op, text)
		}
		// The warning must not push the answer into the JSON fallback
		// (a lookup's JSON carries the body twice): text, warning once.
		if strings.Contains(text, `"staleWarning"`) || strings.HasPrefix(strings.TrimSpace(text), "{") {
			t.Errorf("%s answer fell back to JSON because of the warning:\n%.800s", tc.op, text)
		}
		if n := strings.Count(text, "a.go (file exceeds"); n != 1 {
			t.Errorf("%s answer prints the warning %d times:\n%.800s", tc.op, n, text)
		}
	}
}

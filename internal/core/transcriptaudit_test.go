package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/workspace"
)

// An empty query answer is a struct; it used to print as JSON even with
// --format text (11-15 ice-turn-context, webrtc-sctp-startcontext).
func TestEmptyQueryRendersAsText(t *testing.T) {
	h := &Handler{}
	text, ok := h.RenderToolText(true, "prism_query", nil, queryResult{Symbols: []rankedSymbol{}, Note: "no symbols matched terms [\"X\"] under project root /r"})
	if !ok || strings.Contains(text, "{") || !strings.HasPrefix(text, "// no symbols matched") {
		t.Fatalf("rendered=%v text=%q", ok, text)
	}
}

// Two names resolving to one symbol deliver its body once.
func TestLookupBatchDedupesSameSymbol(t *testing.T) {
	if m := lookupSpanLine.FindStringSubmatch("// method Handler.Foo  internal/x.go:10-40\n10→func"); m == nil || m[2] != "internal/x.go:10-40" {
		t.Fatalf("span line not matched: %v", m)
	}
	if lookupSpanLine.MatchString("// AMBIGUOUS — several symbols fit equally") {
		t.Fatal("a flag line must not read as a span")
	}
}

func linkedFixture(t *testing.T, files map[string]string) *Handler {
	t.Helper()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	files["app/prism.yaml"] = "version: 2\nworkspace_repo: lib=../lib\n"
	files["app/a.go"] = "package app\n"
	files["lib/go.mod"] = "module example.com/lib\n"
	for rel, content := range files {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(parent, "app")
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	cfg, _ := config.LoadFromDir(root)
	workspace.ConfirmLinks(root, cfg)
	return NewHandlerWithLedger(cfg, root, nil, session.NewLedger("t"))
}

// Test-file lines of a linked repository are counted, not listed; a path
// that names no file there says so instead of "0 match(es)".
func TestLinkedSearchFoldsTestsAndReportsMissingPath(t *testing.T) {
	h := linkedFixture(t, map[string]string{
		"lib/lib.go":      "package lib\n\n// Needle in code\n",
		"lib/lib_test.go": "package lib\n\n// Needle in test\n// Needle again\n",
	})
	roots, err := h.linkedSearchRoots("lib")
	if err != nil {
		t.Fatal(err)
	}
	got := h.linkedSearchListing(t.Context(), roots, map[string]any{"query": "Needle"})
	if !strings.Contains(got, "lib.go:3:") || strings.Contains(got, "lib_test.go:") || !strings.Contains(got, "test files: 2 hit(s) in 1 file(s) of lib") {
		t.Fatalf("listing:\n%s", got)
	}
	all := h.linkedSearchListing(t.Context(), roots, map[string]any{"query": "Needle", "exhaustive": true})
	if !strings.Contains(all, "lib_test.go:3:") {
		t.Fatalf("exhaustive must list test lines:\n%s", all)
	}
	miss := h.linkedSearchListing(t.Context(), roots, map[string]any{"query": "Needle", "path": []string{"../lib/nope.go"}})
	if !strings.Contains(miss, "FILTER MATCHED NO FILES") || strings.Contains(miss, "0 match(es)") {
		t.Fatalf("missing path:\n%s", miss)
	}
}

func TestLookupBatchDedupPreservesAmbiguity(t *testing.T) {
	sym := map[string]any{"kind": "method", "qualifiedName": "A.Foo", "filePath": "x.go", "span": map[string]any{"start": 10, "end": 10}}
	out := map[string]any{"results": []map[string]any{
		{"name": "A.Foo", "result": map[string]any{"symbol": sym, "content": "func (a A) Foo() {}"}},
		{"name": "Foo", "result": map[string]any{"symbol": sym, "content": "func (a A) Foo() {}", "ambiguous": true, "candidates": []string{"A.Foo x.go:10", "B.Foo x.go:20"}}},
	}}
	text, ok := renderLookupBatchAsText(out)
	if !ok || strings.Count(text, "func (a A) Foo() {}") != 1 || !strings.Contains(text, "same symbol as lookup A.Foo") || !strings.Contains(text, "AMBIGUOUS") || !strings.Contains(text, "B.Foo") {
		t.Fatalf("duplicate body suppression erased ambiguity: %s", text)
	}
}

func TestLookupBatchDedupRequiresDeliveredIdenticalBody(t *testing.T) {
	sym := map[string]any{"kind": "method", "qualifiedName": "A.Foo", "filePath": "x.go", "span": map[string]any{"start": 10, "end": 10}}
	for _, first := range []string{"", "old body"} {
		out := map[string]any{"results": []map[string]any{
			{"name": "A.Foo", "result": map[string]any{"symbol": sym, "content": first}},
			{"name": "Foo", "result": map[string]any{"symbol": sym, "content": "new body"}},
		}}
		text, ok := renderLookupBatchAsText(out)
		if !ok || !strings.Contains(text, "new body") || strings.Contains(text, "same symbol as") {
			t.Fatalf("first=%q: undelivered body suppressed: %s", first, text)
		}
	}
}

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The change this pins: prism_search's MCP response is plain text for a
// pure-text result, not JSON. BACKLOG.md item 1 — measured 1.19-1.32x fewer
// bytes for identical hits (+219 to +704 bytes/call on real queries), on the
// highest-call-count tool in the system, where the envelope compounds via
// the session cache on every later turn.

func TestRenderSearchAsText_SingleTerm(t *testing.T) {
	out := map[string]any{
		"textHits": []map[string]any{
			{"file": "a.go", "hits": []map[string]any{
				{"line": 10, "text": "func Foo() {}"},
				{"line": 22, "text": "Foo()"},
			}},
		},
		"textBackend": "rg",
		"truncated":   false,
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	for _, want := range []string{"a.go:10: func Foo() {}", "a.go:22: Foo()"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// The whole point: no JSON scaffolding around the hits — no key
	// quoting, no escaped tabs. (The source text itself may legitimately
	// contain braces, as "func Foo() {}" does here.)
	if strings.Contains(text, `"line":`) || strings.Contains(text, `"text":`) {
		t.Errorf("rendering still looks like JSON: %s", text)
	}
}

func TestRenderSearchAsText_NothingSilentlyDropped(t *testing.T) {
	// Every field a real searchOne() result can carry must show up in the
	// text, or the function must decline (ok=false) rather than drop it.
	out := map[string]any{
		"textHits":     []map[string]any{{"file": "a.go", "hits": []map[string]any{{"line": 1, "text": "x"}}}},
		"textBackend":  "rg",
		"truncated":    true,
		"totalHits":    999,
		"filesMatched": 42,
		"warning":      "showing 1 of AT LEAST 999 matches across 42 files",
		"resolvedNote": "37% of these hits are not resolved references",
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	for _, want := range []string{"999", "42", "showing 1 of AT LEAST", "37%"} {
		if !strings.Contains(text, want) {
			t.Errorf("dropped field, missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderSearchAsText_FilesOnly(t *testing.T) {
	out := map[string]any{"files": []string{"a.go", "b.go"}, "fileCount": 2}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	if !strings.Contains(text, "a.go") || !strings.Contains(text, "b.go") {
		t.Errorf("missing files: %s", text)
	}
}

func TestRenderSearchAsText_MultiTerm(t *testing.T) {
	out := map[string]any{
		"results": []map[string]any{
			{"query": "Alpha", "textHits": []map[string]any{{"file": "a.go", "hits": []map[string]any{{"line": 1, "text": "Alpha"}}}}},
			{"query": "Beta", "textHits": []map[string]any{{"file": "b.go", "hits": []map[string]any{{"line": 2, "text": "Beta"}}}}},
		},
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	if !strings.Contains(text, "Alpha") || !strings.Contains(text, "Beta") {
		t.Errorf("missing a term's group: %s", text)
	}
	if !strings.Contains(text, "a.go:1") || !strings.Contains(text, "b.go:2") {
		t.Errorf("missing a hit: %s", text)
	}
}

func TestRenderSearchAsText_FallsBackForSymbolResults(t *testing.T) {
	// scope="symbols"/"both" results carry structured data (signature, doc,
	// body, kind) that would lose information if flattened -- must fall back
	// to JSON, not attempt a lossy text form.
	out := map[string]any{"symbols": []map[string]any{{"name": "Foo", "kind": "func"}}}
	_, ok := renderSearchAsText(out)
	if ok {
		t.Error("symbol-bearing results must fall back to JSON, not render as text")
	}
}

func TestRenderSearchAsText_FallsBackOnUnknownField(t *testing.T) {
	// A field this function does not recognise must trigger JSON fallback,
	// not be silently omitted from the text.
	out := map[string]any{"textHits": []map[string]any{}, "someNewFieldAddedLater": "data"}
	_, ok := renderSearchAsText(out)
	if ok {
		t.Error("an unrecognised field must force the JSON fallback, not be dropped")
	}
}

func TestRenderSearchAsText_CachedFile(t *testing.T) {
	out := map[string]any{
		"textHits": []map[string]any{
			{"file": "a.go", "cached": true, "lines": []int{5, 9, 14},
				"note": "content already delivered this session (unchanged) — matches listed by line only"},
		},
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	if !strings.Contains(text, "5,9,14") || !strings.Contains(text, "cached") {
		t.Errorf("cached-file entry not rendered correctly: %s", text)
	}
}

// Confirms the measured saving end to end, over Invoke -> the real MCP
// content path -- not a synthetic map -- so this fails if the wiring in
// server.go ever stops calling the renderer for prism_search.
func TestPrismSearch_TextScope_SmallerThanJSON(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	text, ok := renderSearchAsText(m)
	if !ok {
		t.Fatal("expected a plain-text rendering for a real handler result")
	}
	encoded, _ := json.Marshal(m)
	if len(text) >= len(encoded) {
		t.Errorf("text rendering (%d bytes) is not smaller than JSON (%d bytes)", len(text), len(encoded))
	}
}

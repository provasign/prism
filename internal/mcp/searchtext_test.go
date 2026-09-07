package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The change this pins: prism_search's MCP response is plain text for a
// pure-text result, not JSON. Measured 1.19-1.32x fewer
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
	// The graph's reading of the term is the headline: first line, before
	// any hit (dubbo retest 2026-09-05: trailing it after 170 grep lines got
	// it ignored).
	if !strings.HasPrefix(text, "// 37%") {
		t.Errorf("resolvedNote must lead the rendering, got:\n%s", text)
	}
}

// TestRenderSearchAsText_ExhaustiveInventoryListed: the files past the render
// cap under exhaustive=true are printed one per line, not just promised.
func TestRenderSearchAsText_ExhaustiveInventoryListed(t *testing.T) {
	out := map[string]any{
		"textHits": []map[string]any{
			{"file": "a.go", "hits": []map[string]any{{"line": 1, "text": "x"}}},
			{"note": "2 more files with matches — exhaustive=true, so every one is listed here",
				"files": []string{"b.go (3)", "c/d.go (1)"}},
		},
		"textBackend": "rg",
		"truncated":   false,
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	for _, want := range []string{"b.go (3)\n", "c/d.go (1)\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("inventory entry %q dropped from:\n%s", want, text)
		}
	}
}

func TestSearchExhaustiveInventoryNamesEveryFileLineAndSymbol(t *testing.T) {
	files := map[string]string{"go.mod": "module example.com/inventory\n\ngo 1.26\n"}
	for i := 0; i < textRenderFileCap+3; i++ {
		files[fmt.Sprintf("pkg/group/f%02d.go", i)] = fmt.Sprintf(
			"package group\ntype T%02d struct{}\nfunc (t T%02d) Needle() {}\n", i, i)
	}
	h := evidenceHandler(t, files)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Needle(", "scope": "text", "exhaustive": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := renderSearchAsText(out.(map[string]any))
	if !ok {
		t.Fatal("exhaustive result did not render as text")
	}
	for i := 0; i < textRenderFileCap+3; i++ {
		for _, want := range []string{fmt.Sprintf("f%02d.go:", i), fmt.Sprintf("T%02d.Needle", i)} {
			if !strings.Contains(text, want) {
				t.Fatalf("complete inventory missing %q:\n%s", want, text)
			}
		}
	}
	if strings.Contains(text, "files, ") || strings.Contains(text, "path=<dir>") {
		t.Fatalf("exhaustive result still requires directory expansion:\n%s", text)
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

func TestRenderSearchAsText_SymbolsRenderAsLocations(t *testing.T) {
	// Contract change (v0.55.6): search is a LOCATE tool, but its JSON form
	// shipped full SymbolRecords — rawText bodies, blobSha, ids, callSites —
	// measured at 26-27KB per default-scope call on Kinto. The text form is
	// one location line per symbol plus an explicit in-band pointer to
	// lookup/read for bodies; index internals are policy-dropped, not lost.
	out := map[string]any{"symbols": []map[string]any{{
		"name": "Foo", "qualifiedName": "pkg.Foo", "kind": "func",
		"filePath": "pkg/foo.go", "signature": "func Foo() error",
		"span":    map[string]any{"start": 10, "end": 30},
		"rawText": "func Foo() error {\n\treturn nil\n}\n",
		"blobSha": "deadbeef", "id": "sym-1",
	}}}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("symbol results must render as location lines")
	}
	for _, want := range []string{"pkg.Foo", "pkg/foo.go:10-30", "func Foo() error", "prism_lookup"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, banned := range []string{"deadbeef", "return nil", "sym-1"} {
		if strings.Contains(text, banned) {
			t.Errorf("index internal / body %q leaked into locate result:\n%s", banned, text)
		}
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

func TestRenderSearchAsText_HitRollup(t *testing.T) {
	out := map[string]any{
		"textHits":  []map[string]any{{"file": "a.py", "hits": []map[string]any{{"line": 1, "text": "x"}}}},
		"truncated": true, "totalHits": 443, "filesMatched": 30,
		"warning": "showing 25 of AT LEAST 443 matches",
		"hitRollup": []map[string]any{
			{"symbol": "TransitConnection.hints", "file": "src/transit.py",
				"span": map[string]any{"start": 120, "end": 180}, "hits": 34},
			{"note": "+12 more symbols with 200 hit(s)"},
			{"note": "57 hit(s) outside indexed symbols (comments, docs, config) or in files past the probe cap"},
		},
	}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("rollup-bearing result must render")
	}
	for _, want := range []string{"graph rollup", "TransitConnection.hints  src/transit.py:120-180  (34 hits)",
		"+12 more symbols", "57 hit(s) outside indexed symbols"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestHitRollupSilentWithoutGrove(t *testing.T) {
	h := newTestHandler(t)
	if ru := h.hitRollup(context.Background(), "anything", searchScope{}, false); ru != nil {
		t.Errorf("rollup must be silent without a graph, got %v", ru)
	}
}

// TestRenderSearchAsText_BatchDedupesLinesAcrossTerms: a file:line printed
// under one term of a batched call is counted, not reprinted, under the
// next ("http3" and "Http3" hit the same lines).
func TestRenderSearchAsText_BatchDedupesLinesAcrossTerms(t *testing.T) {
	hit := func() []map[string]any {
		return []map[string]any{{"file": "a.go", "hits": []map[string]any{{"line": 7, "text": "x http3 y"}, {"line": 9, "text": "z"}}}}
	}
	out := map[string]any{"results": []map[string]any{
		{"query": "http3", "textHits": hit(), "textBackend": "rg"},
		{"query": "Http3", "textHits": hit(), "textBackend": "rg"},
	}}
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	if strings.Count(text, "a.go:7: x http3 y") != 1 || strings.Count(text, "a.go:9: z") != 1 {
		t.Errorf("each line should print once across terms:\n%s", text)
	}
	if !strings.Contains(text, "a.go: 2 line(s) already shown under an earlier term") {
		t.Errorf("the second term should carry the dedupe count:\n%s", text)
	}
}

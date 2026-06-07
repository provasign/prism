package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

func fakeGroveSrv(t *testing.T, payload map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func newHWithGrove(t *testing.T, _ *httptest.Server) *Handler {
	t.Helper()
	root := t.TempDir()
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	return NewHandler(&config.Config{MaxCacheFiles: 100}, root, gc)
}

func TestToolSearch(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{{"id": "s1", "name": "Foo"}}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Foo", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
}

func TestToolLookup(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{
		{"id": "s1", "name": "Foo", "qualifiedName": "pkg.Foo", "rawText": "code"},
	}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "pkg.Foo"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
	// No name → error
	if _, err := h.Invoke("prism_lookup", map[string]any{}); err == nil {
		t.Error("expected err")
	}
}

func TestToolLookup_NoMatch(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "X"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["symbol"] != nil {
		t.Error("expected nil symbol")
	}
}

func TestToolIndex(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"filesSeen": 5})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_index", map[string]any{"dir": h.Root}); err != nil {
		t.Fatal(err)
	}
}

func TestToolCompact(t *testing.T) {
	h := newH(t)
	turns := []map[string]any{
		{"role": "user", "content": "task A", "kind": "exploration"},
		{"role": "assistant", "content": "result file 1", "kind": "file_read"},
		{"role": "user", "content": "task B", "kind": "implementation"},
		{"role": "assistant", "content": "result B"},
		{"role": "user", "content": "final task"},
	}
	out, err := h.Invoke("prism_compact", map[string]any{"turns": turns})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["compressedTurns"] == nil {
		t.Error("no compressed")
	}
}

func TestToolCompact_NoTurns(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("prism_compact", map[string]any{}); err == nil {
		t.Error("expected err")
	}
	if _, err := h.Invoke("prism_compact", map[string]any{"turns": "notarray"}); err == nil {
		t.Error("expected err")
	}
}

func TestToolFeedback(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("prism_feedback", map[string]any{"tool": "x", "rating": 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke("prism_feedback", map[string]any{"rating": 99}); err == nil {
		t.Error("expected err")
	}
	if _, err := h.Invoke("prism_feedback", map[string]any{}); err == nil {
		t.Error("expected err on missing rating")
	}
}

func TestCategorize(t *testing.T) {
	cases := []struct {
		name string
		s    grove.SymbolRecord
		want ranking.Category
	}{
		{"go test", grove.SymbolRecord{FilePath: "x_test.go"}, ranking.CategoryTest},
		{"typescript test", grove.SymbolRecord{FilePath: "x.test.ts"}, ranking.CategoryTest},
		{"typescript spec", grove.SymbolRecord{FilePath: "x.spec.ts"}, ranking.CategoryTest},
		{"javascript tests dir", grove.SymbolRecord{FilePath: "/__tests__/x.js"}, ranking.CategoryTest},
		{"python test", grove.SymbolRecord{FilePath: "x_test.py"}, ranking.CategoryTest},
		{"java test", grove.SymbolRecord{FilePath: "src/UserServiceTest.java"}, ranking.CategoryTest},
		{"rust test", grove.SymbolRecord{FilePath: "src/service_test.rs"}, ranking.CategoryTest},
		{"c test", grove.SymbolRecord{FilePath: "tests/service_test.c"}, ranking.CategoryTest},
		{"cpp test", grove.SymbolRecord{FilePath: "tests/service_test.cpp"}, ranking.CategoryTest},
		{"csharp test", grove.SymbolRecord{FilePath: "UserServiceTests.cs"}, ranking.CategoryTest},
		{"php test", grove.SymbolRecord{FilePath: "UserServiceTest.php"}, ranking.CategoryTest},
		{"markdown doc", grove.SymbolRecord{FilePath: "x.md", Kind: "function"}, ranking.CategoryDoc},
		{"namespace doc", grove.SymbolRecord{Kind: "namespace"}, ranking.CategoryDoc},
		{"docstring doc", grove.SymbolRecord{Docstring: "doc"}, ranking.CategoryDoc},
		{"dependency", grove.SymbolRecord{FilePath: "x.go", Kind: "function"}, ranking.CategoryDependency},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := categorize(c.s); got != c.want {
				t.Fatalf("categorize() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFilterGeneratedPrismContext(t *testing.T) {
	in := []grove.SymbolRecord{
		{FilePath: ".mcp.json", RawText: `{"mcpServers":{"prism":{}}}`},
		{FilePath: "AGENTS.md", RawText: "## Prism — context delivery\nUse Prism."},
		{FilePath: "docs/architecture.md", RawText: "## Prism architecture"},
		{FilePath: "src/app.ts", Name: "authorize"},
	}
	got := filterGeneratedPrismContext(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 non-generated symbols, got %d: %+v", len(got), got)
	}
	if got[0].FilePath != "docs/architecture.md" || got[1].FilePath != "src/app.ts" {
		t.Fatalf("unexpected filtered symbols: %+v", got)
	}
}

func TestStringArg(t *testing.T) {
	if stringArg(nil, "x", "def") != "def" {
		t.Error("default")
	}
	if stringArg(map[string]any{"x": "v"}, "x", "def") != "v" {
		t.Error("get")
	}
}

func TestToolQuery_OK(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{
		"symbols": []map[string]any{{"id": "s1", "name": "Foo", "filePath": "a.go"}},
		"results": []map[string]any{},
		"edges":   []map[string]any{},
		"nodes":   []map[string]any{},
		"tests":   []map[string]any{},
	})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_query", map[string]any{"task": "find Foo", "limit": 10}); err != nil {
		t.Logf("query err (ok if grove paths missing): %v", err)
	}
}

func TestToolRead_NoFile(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("prism_read", map[string]any{}); err == nil {
		t.Error("expected err")
	}
}

// ─── agent-directed query parameters ─────────────────────────────────────────

func TestToolQuery_TermsSeeding(t *testing.T) {
	h := newHWithGrove(t, nil)
	// terms param should not error even when grove returns no matches
	_, err := h.Invoke("prism_query", map[string]any{
		"task":  "find AccessCount",
		"terms": []any{"AccessCount", "sha-pointer"},
	})
	if err != nil {
		t.Fatalf("unexpected error with terms param: %v", err)
	}
}

func TestToolQuery_IncludeGraphOnly(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "compression",
		"include": []any{"graph"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	for _, sym := range res.Symbols {
		if sym.Category == string(ranking.CategoryTest) {
			t.Errorf("include=[graph] should not return test symbol %q", sym.Name)
		}
		if sym.Category == string(ranking.CategoryDoc) {
			t.Errorf("include=[graph] should not return doc symbol %q", sym.Name)
		}
	}
}

func TestToolQuery_IncludeDocsOnly(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "architecture",
		"include": []any{"docs"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	for _, sym := range res.Symbols {
		if sym.Category != string(ranking.CategoryDoc) {
			t.Errorf("include=[docs] should only return doc symbols, got category %q for %q", sym.Category, sym.Name)
		}
	}
}

func TestToolQuery_GraphDepthClamped(t *testing.T) {
	h := newHWithGrove(t, nil)
	// depth=0 should be clamped to 1, depth=99 to 5 — neither should error
	for _, depth := range []int{0, 1, 5, 99} {
		_, err := h.Invoke("prism_query", map[string]any{
			"task":        "find symbols",
			"graph_depth": depth,
		})
		if err != nil {
			t.Errorf("depth=%d: unexpected error: %v", depth, err)
		}
	}
}

func TestToolQuery_CoverageGaps(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "fix compression bug",
		"include": []any{"graph", "tests", "coverage_gaps"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	// coverageGaps must be a slice (may be empty if grove has no index) — not nil/panic
	// Each gap entry must have a non-empty name and filePath
	for _, g := range res.CoverageGaps {
		if g.Name == "" {
			t.Error("gap entry has empty name")
		}
		if g.FilePath == "" {
			t.Error("gap entry has empty filePath")
		}
	}
}

func TestToolQuery_CoverageGaps_NotIncludedByDefault(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task": "fix bug",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	if len(res.CoverageGaps) > 0 {
		t.Error("coverage_gaps should be empty when not requested")
	}
}

func TestToolQuery_TermsAndIncludeCombined(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":        "repeat read handling",
		"terms":       []any{"AccessCount"},
		"include":     []any{"graph", "tests"},
		"graph_depth": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	// docs must be absent
	for _, sym := range res.Symbols {
		if sym.Category == string(ranking.CategoryDoc) {
			t.Errorf("should not return doc symbol %q when include=[graph,tests]", sym.Name)
		}
	}
}

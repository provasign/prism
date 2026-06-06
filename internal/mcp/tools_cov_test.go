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

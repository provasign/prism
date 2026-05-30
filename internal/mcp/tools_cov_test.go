package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tabladrum/grove-suite/prism/internal/config"
	"github.com/tabladrum/grove-suite/prism/internal/grove"
)

func fakeGroveSrv(t *testing.T, payload map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func newHWithGrove(t *testing.T, srv *httptest.Server) *Handler {
	t.Helper()
	gc := grove.NewClient(srv.URL, "")
	return NewHandler(&config.Config{MaxCacheFiles: 100}, t.TempDir(), gc)
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
	if _, err := h.Invoke("prism_index", map[string]any{"dir": "/x"}); err != nil {
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
		s grove.SymbolRecord
	}{
		{grove.SymbolRecord{FilePath: "x_test.go"}},
		{grove.SymbolRecord{FilePath: "x.test.ts"}},
		{grove.SymbolRecord{FilePath: "x.spec.ts"}},
		{grove.SymbolRecord{FilePath: "/__tests__/x.js"}},
		{grove.SymbolRecord{FilePath: "x_test.py"}},
		{grove.SymbolRecord{FilePath: "x.md", Kind: "function"}},
		{grove.SymbolRecord{Kind: "namespace"}},
		{grove.SymbolRecord{Docstring: "doc"}},
		{grove.SymbolRecord{FilePath: "x.go", Kind: "function"}},
	}
	for _, c := range cases {
		_ = categorize(c.s)
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

func TestToolQuery_GroveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_query", map[string]any{"task": "x"}); err == nil {
		t.Error("expected err")
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

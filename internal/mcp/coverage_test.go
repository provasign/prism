package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func newH(t *testing.T) *Handler {
	t.Helper()
	root := t.TempDir()
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	return NewHandler(&config.Config{MaxCacheFiles: 100}, root, gc)
}

func TestNewHandler(t *testing.T) {
	h := newH(t)
	if h.Cfg == nil || h.Session == nil || h.Ledger == nil || h.Signals == nil {
		t.Error("nil field")
	}
}

func TestSemanticAdapter_NoScoresLoaded(t *testing.T) {
	h := newH(t)
	a := semanticAdapter{h: h}
	if got := a.Similarity("q", grove.SymbolRecord{ID: "x"}); got != 0 {
		t.Errorf("got %v", got)
	}
}

func TestSemanticAdapter_LoadedScores(t *testing.T) {
	h := newH(t)
	h.semScores = map[string]float64{"sym1": 0.7}
	a := semanticAdapter{h: h}
	if got := a.Similarity("q", grove.SymbolRecord{ID: "sym1"}); got != 0.7 {
		t.Errorf("want 0.7, got %v", got)
	}
	if got := a.Similarity("q", grove.SymbolRecord{ID: "other"}); got != 0 {
		t.Errorf("unscored symbol must be 0, got %v", got)
	}
}

func TestInvoke_UnknownTool(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("nope", nil); err == nil {
		t.Error("expected err")
	}
}

func TestInvoke_Savings(t *testing.T) {
	h := newH(t)
	out, err := h.Invoke("prism_savings", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil out")
	}
}

func TestInvoke_DirMismatchRejected(t *testing.T) {
	h := newH(t)
	other := t.TempDir()
	_, err := h.Invoke("prism_query", map[string]any{"task": "x", "dir": other})
	if err == nil {
		t.Fatal("expected error for dir outside server root")
	}
	if !strings.Contains(err.Error(), h.Root) || !strings.Contains(err.Error(), other) {
		t.Errorf("error must name both roots, got: %v", err)
	}
}

func TestInvoke_DirMatchingRootAccepted(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("prism_query", map[string]any{"task": "x", "dir": h.Root}); err != nil {
		t.Errorf("dir equal to server root must pass, got: %v", err)
	}
	// prism_index keeps its own dir semantics and is exempt from the guard.
	if _, err := h.Invoke("prism_index", map[string]any{"dir": h.Root}); err != nil {
		t.Errorf("prism_index with dir must pass, got: %v", err)
	}
}

func TestSameRoot(t *testing.T) {
	dir := t.TempDir()
	if !sameRoot(dir, dir+string(filepath.Separator)) {
		t.Error("trailing separator must not break equality")
	}
	if sameRoot(dir, t.TempDir()) {
		t.Error("distinct dirs must not compare equal")
	}
}

func TestQueryEmptyResultCarriesNote(t *testing.T) {
	h := newH(t)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":  "find callers",
		"terms": []any{"noSuchSymbolAnywhere"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(queryResult)
	if !ok {
		t.Fatalf("want queryResult, got %T", out)
	}
	if len(res.Symbols) != 0 {
		t.Fatalf("expected no symbols, got %d", len(res.Symbols))
	}
	if res.Note == "" {
		t.Error("empty result must carry a diagnostic note")
	}
	if !strings.Contains(res.Note, "noSuchSymbolAnywhere") || !strings.Contains(res.Note, h.Root) {
		t.Errorf("note must name the terms and the root, got: %q", res.Note)
	}
}

func TestDirRemovedFromNonIndexSchemas(t *testing.T) {
	for _, name := range []string{"prism_query", "prism_read", "prism_search", "prism_lookup"} {
		props := toolSchema(name)["properties"].(map[string]any)
		if _, has := props["dir"]; has {
			t.Errorf("%s schema must not advertise dir; only prism_index honors it", name)
		}
	}
	props := toolSchema("prism_index")["properties"].(map[string]any)
	if _, has := props["dir"]; !has {
		t.Error("prism_index schema must keep dir")
	}
}

func TestDispatch_Initialize(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	res, e := s.dispatch("initialize", nil)
	if e != nil {
		t.Fatal(e)
	}
	if m, ok := res.(map[string]any); !ok || m["protocolVersion"] == nil {
		t.Errorf("bad resp: %+v", res)
	}
}

func TestDispatch_ToolsList(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	res, e := s.dispatch("tools/list", nil)
	if e != nil {
		t.Fatal(e)
	}
	if m, ok := res.(map[string]any); !ok || m["tools"] == nil {
		t.Errorf("bad resp")
	}
}

func TestDispatch_ToolsCall_BadJSON(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	_, e := s.dispatch("tools/call", json.RawMessage("{not json"))
	if e == nil {
		t.Error("expected err")
	}
}

func TestDispatch_ToolsCall_InvokeError(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	_, e := s.dispatch("tools/call", json.RawMessage(`{"name":"nope"}`))
	if e == nil {
		t.Error("expected err")
	}
}

func TestDispatch_ToolsCall_OK(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	res, e := s.dispatch("tools/call", json.RawMessage(`{"name":"prism_savings"}`))
	if e != nil {
		t.Fatal(e)
	}
	if res == nil {
		t.Error("nil")
	}
}

func TestDispatch_Unknown(t *testing.T) {
	h := newH(t)
	s := NewServer(h)
	_, e := s.dispatch("nope", nil)
	if e == nil || e.Code != -32601 {
		t.Error("expected -32601")
	}
}

func TestReadMessage_LineDelimited(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(`{"a":1}` + "\n"))
	got, err := readMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("got %q", got)
	}
}

func TestReadMessage_ContentLength(t *testing.T) {
	body := `{"a":1}`
	msg := "Content-Length: 7\r\n\r\n" + body
	r := bufio.NewReader(strings.NewReader(msg))
	got, err := readMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("got %q", got)
	}
}

func TestReadMessage_BadContentLength(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Content-Length: abc\r\n\r\n"))
	if _, err := readMessage(r); err == nil {
		t.Error("expected err")
	}
}

func TestReadMessage_EOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	if _, err := readMessage(r); err == nil {
		t.Error("expected EOF")
	}
}

func TestWriteMessage(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMessage(&buf, 1, map[string]string{"ok": "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	// MCP stdio transport: newline-delimited compact JSON, no Content-Length header.
	out := buf.String()
	if strings.Contains(out, "Content-Length:") {
		t.Errorf("unexpected Content-Length header (stdio must be newline-delimited): %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("response not newline-terminated: %q", out)
	}
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("response contains embedded newlines (must be one compact object per line): %q", out)
	}
}

func TestWriteMessage_WithError(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMessage(&buf, 1, nil, &rpcError{Code: -1, Message: "boo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "boo") {
		t.Error("missing err msg")
	}
}

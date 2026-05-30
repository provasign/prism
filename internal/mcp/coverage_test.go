package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tabladrum/grove-suite/prism/internal/config"
	"github.com/tabladrum/grove-suite/prism/internal/grove"
)

func newH(t *testing.T) *Handler {
	t.Helper()
	gc := grove.NewClient("http://127.0.0.1:1", "")
	return NewHandler(&config.Config{MaxCacheFiles: 100}, t.TempDir(), gc)
}

func TestNewHandler(t *testing.T) {
	h := newH(t)
	if h.Cfg == nil || h.Session == nil || h.Ledger == nil || h.Signals == nil {
		t.Error("nil field")
	}
}

func TestMarkCorpusStale(t *testing.T) {
	h := newH(t)
	h.dirty = false
	h.MarkCorpusStale()
	if !h.dirty {
		t.Error("not marked")
	}
}

func TestSemanticAdapter_NilBackend(t *testing.T) {
	h := newH(t)
	a := semanticAdapter{h: h}
	if got := a.Similarity("q", grove.SymbolRecord{}); got != 0 {
		t.Errorf("got %v", got)
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

func TestEnsureEmbeddings_GroveError(t *testing.T) {
	h := newH(t)
	if err := h.ensureEmbeddings(t.Context()); err == nil {
		t.Error("expected grove error")
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
	if !strings.Contains(buf.String(), "Content-Length:") {
		t.Errorf("no header: %q", buf.String())
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

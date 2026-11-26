package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/core"
)

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
	if err := writeMessage(&buf, 1, nil, &RPCError{Code: -1, Message: "boo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "boo") {
		t.Error("missing err msg")
	}
}

func TestServerInitializeHandshake(t *testing.T) {
	h := newTestHandler(t)
	srv := NewServer(h)

	in := strings.NewReader(rpcLine(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "test", "version": "1"},
		"capabilities":    map[string]any{},
	}))
	var out bytes.Buffer
	// Serve returns on EOF which happens after the single message.
	_ = srv.Serve(in, &out)

	resp := readRPCResponse(t, &out)
	if resp["error"] != nil {
		t.Fatalf("initialize returned error: %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion: got %v", result["protocolVersion"])
	}
}

func TestServerToolsList(t *testing.T) {
	h := newTestHandler(t)
	srv := NewServer(h)

	in := strings.NewReader(rpcLine(2, "tools/list", map[string]any{}))
	var out bytes.Buffer
	_ = srv.Serve(in, &out)

	resp := readRPCResponse(t, &out)
	if resp["error"] != nil {
		t.Fatalf("tools/list error: %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools field missing or wrong type: %T", result["tools"])
	}
	if len(tools) != len(core.ToolSchemas()) {
		t.Errorf("tools/list: got %d tools, want %d", len(tools), len(core.ToolSchemas()))
	}
}

func TestServerUnknownMethod(t *testing.T) {
	h := newTestHandler(t)
	srv := NewServer(h)

	in := strings.NewReader(rpcLine(3, "not/a/method", nil))
	var out bytes.Buffer
	_ = srv.Serve(in, &out)

	resp := readRPCResponse(t, &out)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %v", resp["error"])
	}
	if code, _ := errObj["code"].(float64); code != -32601 {
		t.Errorf("error code: got %v want -32601", errObj["code"])
	}
}

func TestServerNotificationNoResponse(t *testing.T) {
	h := newTestHandler(t)
	srv := NewServer(h)

	// A notification has no "id" field — server must not send a response.
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n"
	in := strings.NewReader(notification)
	var out bytes.Buffer
	_ = srv.Serve(in, &out)

	if out.Len() > 0 {
		t.Errorf("server sent response to notification: %q", out.String())
	}
}

// ─── Server framing + dispatch ────────────────────────────────────────────

func rpcLine(id int, method string, params any) string {
	p, _ := json.Marshal(params)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)}
	b, _ := json.Marshal(msg)
	return string(b) + "\n"
}

func readRPCResponse(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	// MCP stdio transport: one newline-delimited compact JSON object per line.
	payload := strings.TrimSpace(buf.String())
	if payload == "" {
		t.Fatalf("empty response")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("unmarshal response: %v (payload: %q)", err, payload)
	}
	return m
}

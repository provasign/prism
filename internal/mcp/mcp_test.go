package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/session"
)

// newTestHandler builds a Handler with no Grove client — suitable for
// testing tools that don't touch the network (feedback, savings, compact).
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := config.Default()
	ledger := session.NewLedger("test-session")
	return NewHandlerWithLedger(cfg, t.TempDir(), nil, ledger)
}

// ─── Handler.Invoke dispatch ──────────────────────────────────────────────

func TestInvokeUnknownToolReturnsError(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Invoke("prism_does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error %q should mention 'unknown tool'", err.Error())
	}
}

// ─── toolFeedback ─────────────────────────────────────────────────────────

func TestToolFeedbackAcceptsValidRating(t *testing.T) {
	h := newTestHandler(t)
	for _, rating := range []int{0, 1, 3, 5} {
		out, err := h.Invoke("prism_feedback", map[string]any{
			"tool":   "prism_query",
			"rating": rating,
		})
		if err != nil {
			t.Fatalf("rating %d: unexpected error: %v", rating, err)
		}
		m, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("rating %d: got %T want map", rating, out)
		}
		if m["totalRatings"] == nil {
			t.Errorf("rating %d: totalRatings missing from response", rating)
		}
	}
}

func TestToolFeedbackRejectsOutOfRangeRating(t *testing.T) {
	h := newTestHandler(t)
	for _, bad := range []int{-1, 6, 100} {
		_, err := h.Invoke("prism_feedback", map[string]any{
			"tool":   "prism_query",
			"rating": bad,
		})
		if err == nil {
			t.Errorf("rating %d: expected error, got nil", bad)
		}
	}
}

func TestToolFeedbackAccumulatesEntries(t *testing.T) {
	h := newTestHandler(t)
	for i := 0; i < 3; i++ {
		out, err := h.Invoke("prism_feedback", map[string]any{"tool": "prism_read", "rating": 4})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		m := out.(map[string]any)
		got := int(m["totalRatings"].(int))
		if got != i+1 {
			t.Errorf("call %d: totalRatings = %d, want %d", i, got, i+1)
		}
	}
}

// ─── toolSavings ──────────────────────────────────────────────────────────

func TestToolSavingsReflectsLedger(t *testing.T) {
	h := newTestHandler(t)
	h.Ledger.Record("prism_query", 1000, 250)
	h.Ledger.Record("prism_read", 500, 500)

	out, err := h.Invoke("prism_savings", nil)
	if err != nil {
		t.Fatalf("prism_savings: %v", err)
	}
	snap, ok := out.(session.Summary)
	if !ok {
		t.Fatalf("expected session.Summary, got %T", out)
	}
	if snap.TotalOriginal != 1500 {
		t.Errorf("TotalOriginal: got %d want 1500", snap.TotalOriginal)
	}
	if snap.TotalDelivered != 750 {
		t.Errorf("TotalDelivered: got %d want 750", snap.TotalDelivered)
	}
	wantSavings := 50.0
	if snap.SavingsPercent != wantSavings {
		t.Errorf("SavingsPercent: got %v want %v", snap.SavingsPercent, wantSavings)
	}
}

// ─── toolCompact ──────────────────────────────────────────────────────────

func TestToolCompactRequiresTurns(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Invoke("prism_compact", map[string]any{})
	if err == nil {
		t.Fatal("expected error when turns missing")
	}
}

func TestToolCompactPreservesLastThreeTurnsFull(t *testing.T) {
	h := newTestHandler(t)
	turns := make([]map[string]any, 6)
	for i := range turns {
		turns[i] = map[string]any{"role": "user", "content": strings.Repeat("x", 200)}
	}
	out, err := h.Invoke("prism_compact", map[string]any{"turns": turns})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	m := out.(map[string]any)
	compressed := m["compressedTurns"].([]map[string]any)
	// Last 3 turns must survive at full content length.
	n := len(compressed)
	if n < 3 {
		t.Fatalf("got %d compressed turns, want at least 3", n)
	}
	for i, turn := range compressed[n-3:] {
		if len(turn["content"].(string)) != 200 {
			t.Errorf("last-3 turn %d was truncated (len=%d)", i, len(turn["content"].(string)))
		}
	}
}

func TestToolCompactRecordsLedger(t *testing.T) {
	h := newTestHandler(t)
	turns := []map[string]any{
		{"role": "user", "content": strings.Repeat("a", 500)},
	}
	_, err := h.Invoke("prism_compact", map[string]any{"turns": turns})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	snap := h.Ledger.Snapshot()
	if snap.ByTool["prism_compact"].Calls != 1 {
		t.Error("prism_compact not recorded in ledger")
	}
}

// ─── safePathWithinRoot ───────────────────────────────────────────────────

func TestSafePathWithinRoot(t *testing.T) {
	root := t.TempDir()
	absOutside := filepath.Join(t.TempDir(), "outside.go")
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"internal/foo.go", false},
		{"./internal/../internal/foo.go", false}, // canonicalize, still in root
		{"../outside.go", true},                  // escape attempt
		{absOutside, true},                       // absolute outside root
	}
	for _, tc := range cases {
		_, _, err := safePathWithinRoot(root, tc.path)
		if (err != nil) != tc.wantErr {
			t.Errorf("safePathWithinRoot(%q): err=%v, wantErr=%v", tc.path, err, tc.wantErr)
		}
	}
}

func TestSafePathWithinRoot_EquivalentSymlinkRoots(t *testing.T) {
	realRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(realRoot, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	inputPath := filepath.Join(realRoot, "x.go")
	wantAbs, err := filepath.EvalSymlinks(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	abs, sessionPath, err := safePathWithinRoot(linkRoot, inputPath)
	if err != nil {
		t.Fatalf("equivalent symlink path rejected: %v", err)
	}
	if abs != wantAbs {
		t.Fatalf("abs = %q, want %q", abs, wantAbs)
	}
	if sessionPath != "x.go" {
		t.Fatalf("sessionPath = %q, want x.go", sessionPath)
	}
}

// TestSafePathWithinRoot_RelativeSymlinkEscape guards the security-review
// finding: a RELATIVE path (the shape prism_read/CLI callers actually pass,
// e.g. "leak.go") that is itself a symlink whose target resolves outside
// root previously skipped EvalSymlinks entirely — only the filepath.IsAbs
// branch resolved symlinks, so the join-then-containment-check saw the
// unresolved in-root path, passed, and os.ReadFile then followed the link
// at read time, serving the external file's content under the in-repo
// name (the same symlink-escape failure class reported against other
// code-indexing tools — attacker-controlled repo content could leak
// signatures/source from outside the indexed tree).
func TestSafePathWithinRoot_RelativeSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n\nfunc TopSecret() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	link := filepath.Join(root, "leak.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := safePathWithinRoot(root, "leak.go"); err == nil {
		t.Fatal("relative path to a symlink escaping root was accepted; expected rejection")
	}
}

// ─── ToolSchemas ──────────────────────────────────────────────────────────

// Routing-critical tools must carry anthropic/alwaysLoad so Claude Code never
// defers their schemas behind a ToolSearch hop (cheap tiers don't make the
// hop — measured haiku 0 calls deferred vs 5 loaded). Long-tail graph ops
// must stay deferrable to keep the always-on schema cost low.
func TestToolSchemasAlwaysLoadOnRoutingCriticalTools(t *testing.T) {
	always := map[string]bool{
		"prism_search": true, "prism_query": true, "prism_read": true,
		"prism_lookup": true, "prism_change_impact": true,
	}
	for _, s := range ToolSchemas() {
		name := s["name"].(string)
		meta, hasMeta := s["_meta"].(map[string]any)
		if always[name] {
			if !hasMeta || meta["anthropic/alwaysLoad"] != true {
				t.Errorf("%s must set _meta anthropic/alwaysLoad=true", name)
			}
		} else if hasMeta && meta["anthropic/alwaysLoad"] == true {
			t.Errorf("%s must stay deferrable (unexpected alwaysLoad)", name)
		}
	}
}


func TestToolSchemasReturnsAdvertisedTools(t *testing.T) {
	schemas := ToolSchemas()
	if len(schemas) != 14 {
		t.Fatalf("want 14 tool schemas, got %d", len(schemas))
	}
	names := make(map[string]bool)
	for _, s := range schemas {
		name, ok := s["name"].(string)
		if !ok || name == "" {
			t.Error("schema missing name field")
		}
		if s["description"] == nil {
			t.Errorf("schema %q missing description", name)
		}
		if s["inputSchema"] == nil {
			t.Errorf("schema %q missing inputSchema", name)
		}
		names[name] = true
	}
	for _, want := range []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_change_impact", "prism_missing_implementations",
		"prism_dead_code", "prism_rename_plan", "prism_node",
		"prism_verify", "prism_arch_check",
		"prism_index", "prism_references", "prism_map",
	} {
		if !names[want] {
			t.Errorf("ToolSchemas missing %q", want)
		}
	}
	// Demoted from the agent surface (CLI-only): never re-advertise without
	// a deliberate decision — every extra tool is a routing error candidate.
	for _, gone := range []string{
		"prism_resolve", "prism_edges", "prism_cycles", "prism_drift",
		"prism_compact", "prism_savings", "prism_feedback", "prism_evidence",
		// The unified prism(task) tool was REMOVED (2026-08-09): natural
		// language was its sole retrieval key, which contradicts the
		// deterministic-anchor contract every other surface honors.
		"prism",
	} {
		if names[gone] {
			t.Errorf("ToolSchemas advertises %q, which was removed from the agent surface", gone)
		}
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
	if len(tools) != 14 {
		t.Errorf("tools/list: got %d tools, want 14", len(tools))
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

// TestDispatchableToolsAllDispatch pins DispatchableTools to Invoke: every
// listed name must route somewhere (any error but "unknown tool" is fine —
// most tools need Grove), and dispatchable-but-unlisted names cannot happen
// silently because the HTTP server derives its routes from this list.
func TestDispatchableToolsAllDispatch(t *testing.T) {
	h := newTestHandler(t)
	for _, name := range DispatchableTools() {
		_, err := h.Invoke(name, map[string]any{})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("%s is listed as dispatchable but Invoke does not know it", name)
		}
	}
}

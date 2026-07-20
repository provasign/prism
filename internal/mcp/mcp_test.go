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

// ─── toolEvidence ────────────────────────────────────────────────────────

func TestToolEvidenceRequiresClaims(t *testing.T) {
	h := newTestHandler(t)
	_, err := h.Invoke("prism_evidence", map[string]any{})
	if err == nil {
		t.Fatal("expected error when claims are missing")
	}
}

func TestToolEvidenceReturnsTypedPackets(t *testing.T) {
	h := newTestHandler(t)
	out, err := h.Invoke("prism_evidence", map[string]any{
		"claims": []map[string]any{
			{
				"claim":     "negotiateProtocolVersion echoes a supported client version",
				"file":      "internal/mcp/server.go",
				"lineStart": 84,
				"lineEnd":   92,
			},
		},
	})
	if err != nil {
		t.Fatalf("prism_evidence: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", out)
	}
	evidence, ok := m["evidence"].([]EvidencePacket)
	if !ok {
		t.Fatalf("evidence type: got %T", m["evidence"])
	}
	if len(evidence) != 1 {
		t.Fatalf("expected 1 evidence packet, got %d", len(evidence))
	}
	if evidence[0].Claim == "" || evidence[0].File == "" {
		t.Fatalf("expected non-empty typed packet: %+v", evidence[0])
	}
}

func TestToolEvidenceTokenSavingsCrossover(t *testing.T) {
	h := newTestHandler(t)

	makeClaims := func(n int, claimLen int) []map[string]any {
		claims := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			claims = append(claims, map[string]any{
				"claim":     strings.Repeat("evidence detail ", claimLen) + "#" + string(rune('A'+(i%26))),
				"file":      "internal/mcp/server.go",
				"lineStart": 60 + i,
				"lineEnd":   61 + i,
			})
		}
		return claims
	}

	// Small packet can be neutral/negative due to typed envelope overhead.
	smallOut, err := h.Invoke("prism_evidence", map[string]any{"claims": makeClaims(2, 1)})
	if err != nil {
		t.Fatalf("small prism_evidence: %v", err)
	}
	small := smallOut.(map[string]any)
	smallSavings, ok := small["savingsPercent"].(float64)
	if !ok {
		t.Fatalf("small savingsPercent type: %T", small["savingsPercent"])
	}
	if smallSavings > 50 {
		t.Fatalf("unexpectedly high small-packet savings: %.2f", smallSavings)
	}

	// Large packet should strongly benefit from typed compact representation.
	largeOut, err := h.Invoke("prism_evidence", map[string]any{"claims": makeClaims(40, 25)})
	if err != nil {
		t.Fatalf("large prism_evidence: %v", err)
	}
	large := largeOut.(map[string]any)
	largeSavings, ok := large["savingsPercent"].(float64)
	if !ok {
		t.Fatalf("large savingsPercent type: %T", large["savingsPercent"])
	}
	if !(largeSavings > smallSavings) {
		t.Fatalf("expected large packet to improve over small packet: small=%.2f large=%.2f", smallSavings, largeSavings)
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

// ─── ToolSchemas ──────────────────────────────────────────────────────────

func TestToolSchemasReturnsAdvertisedTools(t *testing.T) {
	schemas := ToolSchemas()
	if len(schemas) != 17 {
		t.Fatalf("want 17 tool schemas, got %d", len(schemas))
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
		"prism_untested_surface", "prism_dead_code", "prism_rename_plan",
		"prism_index", "prism_drift", "prism_references", "prism_resolve", "prism_edges",
		"prism_change_impact", "prism_map",
	} {
		if !names[want] {
			t.Errorf("ToolSchemas missing %q", want)
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
	if len(tools) != 17 {
		t.Errorf("tools/list: got %d tools, want 17", len(tools))
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

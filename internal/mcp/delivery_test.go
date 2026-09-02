package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

// ─── symbolWindows (pure) ─────────────────────────────────────────────────

func bs(start, end int, lvl ranking.DisclosureLevel) ranking.BudgetedSymbol {
	return ranking.BudgetedSymbol{
		Symbol:     grove.SymbolRecord{Span: grove.SpanInfo{Start: start, End: end}},
		Disclosure: lvl,
	}
}

func TestSymbolWindowsMergesAdjacentSpans(t *testing.T) {
	// Spans 10-14 and 18-22 with pad 2 become 8-16 and 16-24 → gap ≤ merge → one window.
	wins := symbolWindows([]ranking.BudgetedSymbol{
		bs(10, 14, ranking.DisclosureFull),
		bs(18, 22, ranking.DisclosureFull),
	}, 100)
	if len(wins) != 1 {
		t.Fatalf("expected 1 merged window, got %v", wins)
	}
	if wins[0].start != 8 || wins[0].end != 24 {
		t.Errorf("merged window = %+v, want {8 24}", wins[0])
	}
}

func TestSymbolWindowsKeepsDistantSpansSeparate(t *testing.T) {
	wins := symbolWindows([]ranking.BudgetedSymbol{
		bs(5, 8, ranking.DisclosureFull),
		bs(50, 60, ranking.DisclosureFull),
	}, 100)
	if len(wins) != 2 {
		t.Fatalf("expected 2 windows, got %v", wins)
	}
}

func TestSymbolWindowsClampsToFileBounds(t *testing.T) {
	wins := symbolWindows([]ranking.BudgetedSymbol{bs(1, 30, ranking.DisclosureFull)}, 20)
	if len(wins) != 1 || wins[0].start != 1 || wins[0].end != 20 {
		t.Fatalf("expected clamped {1 20}, got %v", wins)
	}
}

func TestSymbolWindowsSignatureDisclosureCapsSpan(t *testing.T) {
	// A dependency at signature disclosure with a 100-line body contributes
	// only its head, not the whole body.
	wins := symbolWindows([]ranking.BudgetedSymbol{bs(10, 110, ranking.DisclosureSignature)}, 200)
	if len(wins) != 1 {
		t.Fatalf("expected 1 window, got %v", wins)
	}
	wantEnd := 10 + signatureWindowLines - 1 + windowPad
	if wins[0].end != wantEnd {
		t.Errorf("signature window end = %d, want %d", wins[0].end, wantEnd)
	}
}

func TestSymbolWindowsSkipsInvalidSpans(t *testing.T) {
	wins := symbolWindows([]ranking.BudgetedSymbol{bs(0, 0, ranking.DisclosureFull)}, 100)
	if len(wins) != 0 {
		t.Fatalf("expected no windows for zero span, got %v", wins)
	}
}

// ─── toolExplore E2E over an indexed fixture ──────────────────────────────

func newDeliveryFixture(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "util.go"), []byte(`package p

// FormatGreeting builds the greeting shown on login.
func FormatGreeting(name string) string {
	if name == "" {
		return "hello, stranger"
	}
	return "hello, " + name
}
`), 0o644)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte(`package p

func run() string {
	return FormatGreeting("topo")
}
`), 0o644)
	os.WriteFile(filepath.Join(dir, "util_test.go"), []byte(`package p

import "testing"

func TestFormatGreeting(t *testing.T) {
	if FormatGreeting("") == "" {
		t.Fatal("empty")
	}
}
`), 0o644)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

func queryContent(t *testing.T, h *Handler, args map[string]any) string {
	t.Helper()
	out, err := h.Invoke("prism_query", args)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map", out)
	}
	content, _ := m["content"].(string)
	return content
}

func TestToolQuery_SourceDelivery_E2E(t *testing.T) {
	h := newDeliveryFixture(t)
	// "wrong" + "fix" phrasing -> debug phase -> source delivery by default.
	content := queryContent(t, h, map[string]any{
		"task":  "fix the bug: greeting is wrong for empty names",
		"terms": []string{"FormatGreeting"},
	})
	if content == "" {
		t.Fatal("empty source-delivery content")
	}
	// Verbatim, line-numbered source window for the anchor.
	if !strings.Contains(content, "util.go") {
		t.Errorf("content should include util.go section:\n%s", content)
	}
	if !strings.Contains(content, "\tfunc FormatGreeting(name string) string {") {
		t.Errorf("content should include line-numbered source:\n%s", content)
	}
	// Anchor summary names the caller relationship and covering test.
	if !strings.Contains(content, "Anchors") {
		t.Errorf("content should include anchor summary section:\n%s", content)
	}
	if !strings.Contains(content, "caller") {
		t.Errorf("anchor summary should mention callers:\n%s", content)
	}
	// Steering framing that makes the delivery edit-ready.
	if !strings.Contains(content, "Read you have already performed") {
		t.Errorf("content should carry the already-read steering:\n%s", content)
	}
}

func TestToolQuery_SourceRepeatDeliveryUsesCachedPointer(t *testing.T) {
	h := newDeliveryFixture(t)
	args := map[string]any{
		"task":     "fix the bug: greeting is wrong for empty names",
		"terms":    []string{"FormatGreeting"},
		"delivery": "source",
	}
	// "// [prism:cached]" is the pointer line itself; the steering preamble
	// mentions the bare "[prism:cached]" token, so match the line prefix.
	const pointerLine = "// [prism:cached]"
	first := queryContent(t, h, args)
	if strings.Contains(first, pointerLine) {
		t.Fatalf("first delivery must be full, not cached:\n%s", first)
	}
	second := queryContent(t, h, args)
	if !strings.Contains(second, pointerLine) {
		t.Errorf("second delivery of unchanged small files should be a pointer:\n%s", second)
	}
	// The pointer must never replace content the agent hasn't seen: only
	// full-file deliveries are recorded, and this fixture's files are tiny.
	if strings.Contains(second, "func FormatGreeting") && strings.Contains(second, pointerLine+" util.go") {
		t.Errorf("cached pointer and full body for the same file:\n%s", second)
	}
}

func TestToolQuery_SymbolsDeliveryIsExplicitOnly(t *testing.T) {
	// The compact symbols delivery is now something you ASK for. It used to be
	// inferred from the task reading like review or orientation, which meant
	// the same seeds returned different shapes depending on wording.
	h := newDeliveryFixture(t)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":     "review the greeting code",
		"terms":    []string{"FormatGreeting"},
		"delivery": "symbols",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, isMap := out.(map[string]any); isMap {
		t.Fatalf("delivery=symbols should return the symbols struct, got map: %v", out)
	}
}

// The point of the change: phrasing is not a control surface. Same terms,
// wildly different task wording — including wordings that used to select
// different phases, profiles and budgets — must produce the same delivery.
func TestToolQuery_PhrasingDoesNotChangeTheResult(t *testing.T) {
	h := newDeliveryFixture(t)
	phrasings := []string{
		"fix the crash in the greeting code", // was PhaseDebug   -> source
		"review the greeting code",           // was PhaseReview  -> symbols
		"write tests for the greeting code",  // was test-writing -> +25% budget
		"understand how greeting works",      // was orientation
		"greeting",                           // no phase signal at all
	}
	var first map[string]any
	for _, task := range phrasings {
		out, err := h.Invoke("prism_query", map[string]any{
			"task": task, "terms": []string{"FormatGreeting"},
		})
		if err != nil {
			t.Fatalf("query(%q): %v", task, err)
		}
		m, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("query(%q): want the source delivery for every phrasing, got %T", task, out)
		}
		if first == nil {
			first = m
			continue
		}
		// "content" legitimately differs: its header echoes the task back as
		// a label. What must not differ is the SELECTION — which files and
		// how many symbols were chosen.
		for _, k := range []string{"files", "symbolCount", "delivery"} {
			if fmt.Sprint(first[k]) != fmt.Sprint(m[k]) {
				t.Errorf("query(%q): %s = %v, but the first phrasing got %v — the task string is still steering the result",
					task, k, m[k], first[k])
			}
		}
	}
}

// TestToolQuery_TestedByPointer verifies the restored test-coverage signal
// (2026-09-02): prism_query's own tool description promises "callers and
// covering tests", but the delivery path unconditionally dropped every
// CategoryTest symbol (pr3493: an unrelated, lexically-task-matched test
// earned a whole source window), and the supporting hasTestEdgeID/
// testFilePaths maps that should have gated a SAFE version of that were
// declared and never populated -- so the promised behavior was silently
// dead. Fixed as a pointer only (file:line, never a body), which cannot
// repeat the pr3493 failure since it never enters the budget/disclosure
// pipeline at all.
func TestToolQuery_TestedByPointer(t *testing.T) {
	h := newDeliveryFixture(t)
	// newDeliveryFixture's util_test.go already has TestFormatGreeting
	// calling FormatGreeting directly -- a real verified test caller.
	out, err := h.Invoke("prism_query", map[string]any{
		"task": "how does the greeting work", "terms": []string{"FormatGreeting"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("want map delivery, got %T", out)
	}
	content, _ := m["content"].(string)
	if !strings.Contains(content, "tested by") || !strings.Contains(content, "TestFormatGreeting") {
		t.Errorf("expected a 'tested by' pointer naming TestFormatGreeting, got:\n%s", content)
	}
	// Pointer only: the test's body ("if FormatGreeting(...") must not
	// appear -- that would mean it leaked into a source window instead of
	// staying a location-only pointer.
	if strings.Contains(content, `if FormatGreeting("")`) {
		t.Error("test body leaked into delivery -- this must stay pointer-only (file:line), not a source window")
	}
}

// TestToolChangeImpact_LabelsTestCallers verifies change_impact's caller
// list marks which callers are verified tests (isTest: true) instead of
// silently mixing them with production call sites -- the caller data was
// always present (change_impact never filtered test files), only the
// distinction was missing.
func TestToolChangeImpact_LabelsTestCallers(t *testing.T) {
	h := newDeliveryFixture(t)
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "FormatGreeting"})
	if err != nil {
		t.Fatalf("change_impact: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", out)
	}
	callers, _ := m["callers"].([]map[string]any)
	if len(callers) == 0 {
		t.Fatal("expected at least one caller")
	}
	found := false
	for _, c := range callers {
		if c["name"] == "TestFormatGreeting" {
			found = true
			if c["isTest"] != true {
				t.Errorf("TestFormatGreeting caller missing isTest:true, got %v", c)
			}
		}
		if c["name"] == "run" && c["isTest"] == true {
			t.Errorf("run() is production code, must not be marked isTest: %v", c)
		}
	}
	if !found {
		t.Error("TestFormatGreeting not found among callers")
	}
}

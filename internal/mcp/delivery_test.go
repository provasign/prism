package mcp

import (
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

func TestToolQuery_SymbolsDeliveryStillDefault(t *testing.T) {
	// A review-phase task must keep the compact symbols delivery: the
	// response carries a symbols array, not a rendered content string.
	h := newDeliveryFixture(t)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":  "review the greeting code",
		"terms": []string{"FormatGreeting"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, isMap := out.(map[string]any); isMap {
		t.Fatalf("review-phase query should return the symbols struct, got map: %v", out)
	}
}

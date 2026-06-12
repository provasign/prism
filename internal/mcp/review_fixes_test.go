package mcp

// Regression tests for the 2026-06-12 review fixes
// (docs/prism-review-2026-06-12.md): GraphDiff-backed drift, the inexact
// lookup flag, the measured prism_query savings baseline, and the
// context_used confidence hint.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// A function rename after delivery must surface as one "renamed" drift entry
// (Grove GraphDiff pairs it), not an unrelated removal + addition.
func TestDrift_RenameDetectedViaGraphDiff(t *testing.T) {
	h := newHWithGrove(t, nil)
	src := `package x

func OldName(a, b int) int {
	total := a + b
	total *= 3
	return total - 1
}
`
	if err := os.WriteFile(h.Root+"/r.go", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}
	// Deliver the file so the session tracker + drift baseline see it.
	if _, err := h.Invoke("prism_read", map[string]any{"file": "r.go"}); err != nil {
		t.Fatal(err)
	}
	// Rename the function on disk.
	renamed := strings.ReplaceAll(src, "OldName", "NewName")
	if err := os.WriteFile(h.Root+"/r.go", []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := out.(DriftReport)
	if !ok {
		t.Fatalf("unexpected drift response type %T", out)
	}
	if report.ChangedFiles != 1 || len(report.Files) != 1 {
		t.Fatalf("expected exactly one drifted file, got %+v", report)
	}
	var rename *DriftSymbol
	for i := range report.Files[0].Symbols {
		if report.Files[0].Symbols[i].Change == "renamed" {
			rename = &report.Files[0].Symbols[i]
		}
		if c := report.Files[0].Symbols[i].Change; c == "added" || c == "removed" {
			t.Errorf("rename leaked through as %s (%s)", c, report.Files[0].Symbols[i].Name)
		}
	}
	if rename == nil {
		t.Fatalf("expected a renamed entry, got %+v", report.Files[0].Symbols)
	}
	if rename.Name != "OldName" || rename.RenamedTo != "NewName" {
		t.Errorf("rename pair wrong: %+v", *rename)
	}
	if !rename.Breaking {
		t.Errorf("exported rename must be flagged breaking: %+v", *rename)
	}
}

// Without a same-session baseline (warm cache only), drift degrades to the
// SHA comparison and still reports the file as changed.
func TestDrift_FallbackWithoutBaseline(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.WriteFile(h.Root+"/f.go", []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke("prism_read", map[string]any{"file": "f.go"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a fresh session that warm-loaded the cache: no drift baseline.
	h.driftMu.Lock()
	h.driftBase = map[string][]grove.SymbolRecord{}
	h.driftMu.Unlock()

	if err := os.WriteFile(h.Root+"/f.go", []byte("package x\n\nfunc F() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.ChangedFiles != 1 {
		t.Fatalf("fallback path must still detect the change, got %+v", report)
	}
}

// prism_lookup must flag a non-exact fallback instead of silently handing
// over the closest hit.
func TestToolLookup_InexactFallbackFlagged(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.WriteFile(h.Root+"/l.go", []byte("package x\n\nfunc DoLookupExtra() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "DoLookupEx"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["symbol"] == nil {
		t.Fatal("expected a fallback symbol")
	}
	if matched, ok := m["matched"].(bool); !ok || matched {
		t.Errorf("inexact fallback must carry matched=false, got %v", m["matched"])
	}
	if cands, ok := m["candidates"].([]string); !ok || len(cands) == 0 {
		t.Errorf("inexact fallback must list candidates, got %v", m["candidates"])
	}
}

// Exact matches must not pay for the fallback fields.
func TestToolLookup_ExactMatchHasNoFallbackFields(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.WriteFile(h.Root+"/l.go", []byte("package x\n\nfunc DoLookup() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "DoLookup"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, present := m["matched"]; present {
		t.Errorf("exact match must not carry matched/candidates, got %v", m)
	}
}

// The prism_query savings baseline is measured from containing-file sizes
// and never drops below the delivered count (no invented savings).
func TestQueryBaselineTokens(t *testing.T) {
	h := newHWithGrove(t, nil)
	content := strings.Repeat("// filler line for size\n", 50)
	if err := os.WriteFile(h.Root+"/a.go", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	picked := []ranking.BudgetedSymbol{
		{Symbol: grove.SymbolRecord{FilePath: "a.go"}},
		{Symbol: grove.SymbolRecord{FilePath: "a.go"}},       // same file counted once
		{Symbol: grove.SymbolRecord{FilePath: "missing.go"}}, // stat fails → contributes 0
	}
	want := len(content) / 4
	if got := h.queryBaselineTokens(picked, 0); got != want {
		t.Errorf("baseline: want %d, got %d", want, got)
	}
	// Delivered above the measurable baseline → baseline clamps to delivered.
	if got := h.queryBaselineTokens(picked, want+500); got != want+500 {
		t.Errorf("clamp: want %d, got %d", want+500, got)
	}
}

// confidenceFor takes the larger of the ledger delta and the agent-reported
// context delta, so external token flow degrades confidence.
func TestConfidenceFor_ContextUsedHint(t *testing.T) {
	h := newH(t)
	entry := &session.Entry{TokenDistanceAtSend: 0, ContextUsedAtSend: 1_000}
	window := 10_000

	// No hint, no ledger movement → high confidence.
	if got := h.confidenceFor(entry, 0, window); got != session.High {
		t.Errorf("no hint: want high, got %s", got)
	}
	// Agent reports 9k tokens since send (80% of window) → low confidence,
	// even though Prism's own ledger saw nothing.
	if got := h.confidenceFor(entry, 9_000, window); got != session.Low {
		t.Errorf("hint: want low, got %s", got)
	}
}

// The advertised schemas must be real (typed properties), not open objects.
func TestToolSchemas_AllTyped(t *testing.T) {
	for _, tool := range ToolSchemas() {
		schema := tool["inputSchema"].(map[string]any)
		if open, ok := schema["additionalProperties"].(bool); ok && open {
			t.Errorf("%s still publishes an open schema", tool["name"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%s has no properties", tool["name"])
		}
	}
	// Round-trips as JSON.
	if _, err := json.Marshal(ToolSchemas()); err != nil {
		t.Fatal(err)
	}
}

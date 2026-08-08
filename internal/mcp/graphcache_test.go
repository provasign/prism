package mcp

import (
	"os"
	"strings"
	"testing"
)

// fatResult builds a change_impact-shaped payload comfortably above the
// pointer threshold.
func fatResult(sites int) map[string]any {
	callers := make([]any, 0, sites)
	for i := 0; i < sites; i++ {
		callers = append(callers, map[string]any{
			"name": "Caller" + strings.Repeat("x", 20), "file": "internal/pkg/file.go", "line": i,
		})
	}
	return map[string]any{
		"query":        "Service.QueryData",
		"completeness": "closed",
		"declarations": []any{map[string]any{"name": "QueryData"}},
		"callers":      callers,
	}
}

func TestGraphDedupeFirstDeliveryIsFull(t *testing.T) {
	h := newTestHandler(t)
	in := fatResult(80)
	out := h.graphDedupe("prism_change_impact", map[string]any{"query": "Service.QueryData"}, in)
	if m, ok := out.(map[string]any); !ok || m["cached"] == true {
		t.Fatalf("first delivery must be the full result, got %v", out)
	}
}

func TestGraphDedupeIdenticalRepeatIsPointer(t *testing.T) {
	h := newTestHandler(t)
	args := map[string]any{"query": "Service.QueryData"}
	h.graphDedupe("prism_change_impact", args, fatResult(80))
	out := h.graphDedupe("prism_change_impact", args, fatResult(80))
	m, ok := out.(map[string]any)
	if !ok || m["cached"] != true {
		t.Fatalf("identical repeat should be a cached pointer, got %v", out)
	}
	note, _ := m["note"].(string)
	if !strings.Contains(note, "[prism:cached]") || !strings.Contains(note, "NOT an error") {
		t.Errorf("pointer note missing contract language: %q", note)
	}
	if m["seen"] != 2 {
		t.Errorf("second delivery must report seen=2 (Lookup aliases the live entry; "+
			"Record's increment must not double-count), got %v", m["seen"])
	}
	summary, ok := m["summary"].(map[string]any)
	if !ok {
		t.Fatalf("pointer should carry a structural summary: %v", m)
	}
	if summary["callers"] != 80 || summary["completeness"] != "closed" {
		t.Errorf("summary should carry counts and identity scalars: %v", summary)
	}
}

func TestGraphDedupeChangedResultIsFull(t *testing.T) {
	h := newTestHandler(t)
	args := map[string]any{"query": "Service.QueryData"}
	h.graphDedupe("prism_change_impact", args, fatResult(80))
	changed := fatResult(81) // one new site — must never be pointered
	out := h.graphDedupe("prism_change_impact", args, changed)
	if m, ok := out.(map[string]any); !ok || m["cached"] == true {
		t.Fatalf("changed result must be delivered in full, got %v", out)
	}
}

func TestGraphDedupeDifferentArgsAreDifferentDeliveries(t *testing.T) {
	h := newTestHandler(t)
	h.graphDedupe("prism_change_impact", map[string]any{"query": "A.foo"}, fatResult(80))
	out := h.graphDedupe("prism_change_impact", map[string]any{"query": "B.bar"}, fatResult(80))
	if m, ok := out.(map[string]any); !ok || m["cached"] == true {
		t.Fatalf("different query args must not share a cache entry, got %v", out)
	}
	// But context_used/model must NOT be part of the identity.
	out = h.graphDedupe("prism_change_impact",
		map[string]any{"query": "A.foo", "context_used": 12345, "model": "claude-opus-5"}, fatResult(80))
	if m, ok := out.(map[string]any); !ok || m["cached"] != true {
		t.Fatalf("context_used/model must not change delivery identity, got %v", out)
	}
}

func TestGraphDedupeLowConfidenceRepeatEscalatesToFull(t *testing.T) {
	h := newTestHandler(t)
	args := map[string]any{"query": "Service.QueryData"}
	h.graphDedupe("prism_change_impact", args, fatResult(80)) // 1st: full
	h.graphDedupe("prism_change_impact", args, fatResult(80)) // 2nd: pointer
	// Push the session far past the attention window: the ledger's delivered
	// total is what confidenceFor measures distance with.
	window := h.Cfg.ContextWindow()
	h.Ledger.Record("prism_read", window, window) // 100% of window delivered since
	out := h.graphDedupe("prism_change_impact", args, fatResult(80))
	if m, ok := out.(map[string]any); !ok || m["cached"] == true {
		t.Fatalf("3rd+ repeat at low confidence must re-deliver in full, got %v", out)
	}
}

func TestGraphDedupeSmallResultNeverPointered(t *testing.T) {
	h := newTestHandler(t)
	small := map[string]any{"query": "A.foo", "callers": []any{map[string]any{"name": "one"}}}
	args := map[string]any{"query": "A.foo"}
	h.graphDedupe("prism_change_impact", args, small)
	out := h.graphDedupe("prism_change_impact", args, small)
	if m, ok := out.(map[string]any); !ok || m["cached"] == true {
		t.Fatalf("small results must always be delivered in full, got %v", out)
	}
}

// TestGateToolsAreNotWiredThroughDedupe pins the design decision that
// verify/arch_check (gates) never flow through delivery dedupe: their
// dispatch lines in Invoke must call the tool directly, not graphDelivery.
func TestGateToolsAreNotWiredThroughDedupe(t *testing.T) {
	src, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"toolVerify", "toolArchCheck"} {
		if strings.Contains(string(src), "graphDelivery(name, args)(h."+gate) {
			t.Errorf("%s is a GATE and must not be wired through graphDelivery — "+
				"its verdict is always delivered in full", gate)
		}
	}
}

// TestPointerKeepsTopLevelCountsForProgrammaticCallers: library consumers
// (mason's compactMeta) read the response by KEY. A pointer that replaced
// declarations/family/callers with a nested summary read as an empty result
// and sent the agent back to grep — measured, guava went 1.7k -> 91-186k
// input tokens for an identical answer.
func TestPointerKeepsTopLevelCountsForProgrammaticCallers(t *testing.T) {
	h := newTestHandler(t)
	args := map[string]any{"query": "Service.QueryData"}
	h.graphDedupe("prism_change_impact", args, fatResult(80))
	out := h.graphDedupe("prism_change_impact", args, fatResult(80))
	m, ok := out.(map[string]any)
	if !ok || m["cached"] != true {
		t.Fatalf("expected a cached pointer, got %v", out)
	}
	for _, k := range []string{"callers", "declarations", "completeness"} {
		if _, present := m[k]; !present {
			t.Errorf("pointer dropped top-level %q — key-matching consumers see an empty result", k)
		}
	}
	if m["callers"] != 80 {
		t.Errorf("callers count = %v, want 80", m["callers"])
	}
}

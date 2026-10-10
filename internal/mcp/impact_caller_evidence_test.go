package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// A complete compiler-backed answer keeps its completeness; the doubt moves
// to the one site found by name instead of labelling the whole answer.
func TestMarkNameMatchedSitesKeepsCompleteAnswer(t *testing.T) {
	r := &grove.ChangeImpactResult{
		Callers: []grove.SymbolRecord{
			{ID: "a", Name: "dispatch", FilePath: "src/hono-base.ts", Span: grove.SpanInfo{Start: 408}},
			{ID: "b", Name: "createHonoRouter", FilePath: "benchmarks/routers-deno/src/hono.mts", Span: grove.SpanInfo{Start: 8}},
		},
		HasHeuristicRefs:   true,
		NameMatchedCallers: []string{"b"},
	}
	out := map[string]any{
		"safeToClaimComplete": true,
		"callerCoverage":      "heuristic",
		"hasHeuristicRefs":    true,
		"relaySites": []string{
			"benchmarks/routers-deno/src/hono.mts:8:createHonoRouter [function]",
			"src/hono-base.ts:408:dispatch [method]",
		},
	}
	markNameMatchedSites(out, r)
	if _, ok := out["callerCoverage"]; ok {
		t.Fatal("whole-answer coverage label kept on a complete answer")
	}
	if _, ok := out["hasHeuristicRefs"]; ok {
		t.Fatal("whole-answer heuristic flag kept on a complete answer")
	}
	relay := out["relaySites"].([]string)
	if !strings.Contains(relay[0], "found by name") || strings.Contains(relay[1], "found by name") {
		t.Fatalf("only the name-found site must be marked: %v", relay)
	}
	if ev, _ := out["callerEvidence"].(string); !strings.Contains(ev, "1 resolved") || !strings.Contains(ev, "1 found by name") {
		t.Fatalf("callerEvidence = %q", ev)
	}
}

// Without a compiler-backed complete answer, nothing is rewritten.
func TestMarkNameMatchedSitesLeavesUnprovenAnswers(t *testing.T) {
	r := &grove.ChangeImpactResult{Callers: []grove.SymbolRecord{{ID: "b"}}, HasHeuristicRefs: true, NameMatchedCallers: []string{"b"}}
	out := map[string]any{"safeToClaimComplete": false, "callerCoverage": "heuristic"}
	markNameMatchedSites(out, r)
	if out["callerCoverage"] != "heuristic" {
		t.Fatal("unproven answer lost its coverage label")
	}
}

func TestBareCallCannotBeMethod(t *testing.T) {
	for _, tc := range []struct {
		lang, callee string
		want         bool
	}{
		{"typescript", "match", true},
		{"typescript", "router.match", false},
		{"python", "save", true},
		{"go", "pkg.Name", false},
		{"rust", "Type::new", false},
		{"java", "match", false}, // implicit this
		{"csharp", "Save", false},
	} {
		if got := bareCallCannotBeMethod(tc.lang, tc.callee); got != tc.want {
			t.Errorf("bareCallCannotBeMethod(%q, %q) = %v, want %v", tc.lang, tc.callee, got, tc.want)
		}
	}
}

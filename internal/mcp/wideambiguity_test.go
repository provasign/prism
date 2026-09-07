package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// grafana-querydata-impact transcript (2026-09-06): "QueryData" was ambiguous
// across 41 candidates; the agent read grove's bare "re-run with one of
// these", issued one change_impact call per guessed receiver, and still
// missed 6 of 51 required sites reachable only by text search (an external
// interface with no local declaration to anchor a family query on). Past
// wideMemberAmbiguityThreshold candidates, the error must redirect to
// exhaustive text search instead of inviting per-candidate looping.
func TestChangeImpact_WideAmbiguityRedirectsToTextSearch(t *testing.T) {
	files := map[string]string{"go.mod": "module example.com/wide\n\ngo 1.26\n"}
	for i := 0; i < wideMemberAmbiguityThreshold+1; i++ {
		files[fmt.Sprintf("impl%02d.go", i)] = fmt.Sprintf(
			"package wide\ntype T%02d struct{}\nfunc (t *T%02d) QueryData() int { return %d }\n", i, i, i)
	}
	h := evidenceHandler(t, files)
	_, err := h.Invoke("prism_change_impact", map[string]any{"query": "QueryData"})
	if err == nil {
		t.Fatal("a bare name with many receivers must still be ambiguous, not silently pick one")
	}
	msg := err.Error()
	if !strings.Contains(msg, "WIDE MEMBER") {
		t.Fatalf("wide ambiguity must be called out, not left as a bare candidate list: %s", msg)
	}
	if !strings.Contains(msg, "one change_impact call per") {
		t.Fatalf("must explicitly say not to loop over candidates: %s", msg)
	}
	if !strings.Contains(msg, `scope="text"`) || !strings.Contains(msg, "exhaustive=true") {
		t.Fatalf("must name the actual next call: %s", msg)
	}
	if !strings.Contains(msg, "candidates:") {
		t.Fatalf("original candidate list must survive (small-N cases still use it): %s", msg)
	}
}

// A handful of candidates is cheap to query one at a time; the redirect
// must not fire and drown out the useful candidate list grove already built.
func TestChangeImpact_FewCandidatesKeepsOriginalError(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"go.mod": "module example.com/narrow\n\ngo 1.26\n",
		"a.go":   "package narrow\ntype A struct{}\nfunc (a *A) Get() int { return 1 }\n",
		"b.go":   "package narrow\ntype B struct{}\nfunc (b *B) Get() int { return 2 }\n",
	})
	_, err := h.Invoke("prism_change_impact", map[string]any{"query": "Get"})
	if err == nil {
		t.Fatal("expected ambiguity for two same-named methods")
	}
	if strings.Contains(err.Error(), "WIDE MEMBER") {
		t.Fatalf("two candidates is cheap to query directly; must not redirect: %s", err)
	}
}

func TestEnrichAmbiguousImpactError_MalformedMessageFallsBackToNil(t *testing.T) {
	orig := fmt.Errorf(`change-impact: "X" is ambiguous — not-a-number candidates: a, b`)
	if got := enrichAmbiguousImpactError("X", orig); got != nil {
		t.Fatalf("unparseable candidate count must not synthesize a threshold decision: %v", got)
	}
}

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/core"
)

func TestImpactTextPreservesCoverageAndWiderNote(t *testing.T) {
	m := map[string]any{
		"query": "Child.run", "declarations": []any{}, "totalSites": 2, "completeness": "partial",
		"familyCompleteness": "closed", "callerCoverage": "partial",
		"coverageNote": "dynamic dispatch is unresolved", "safeToClaimComplete": false,
		"relaySites": []any{"run.go:8:Child.run [method]"}, "relayNote": "copy the canonical inventory",
		"widerAnchor": map[string]any{"qualifiedName": "Base.run", "totalSites": 4, "completeness": "closed", "note": "Base.run is related"},
	}
	out := captureStdout(func() { fmt.Print(core.RenderFallbackText("prism_change_impact", m)) })
	for _, want := range []string{"familyCompleteness: closed", "callerCoverage: partial", "dynamic dispatch is unresolved", "safeToClaimComplete: false", "relaySites (1; copy this inventory)", "run.go:8:Child.run [method]", "copy the canonical inventory", "// wider anchor: Base.run (4 sites, closed) — Base.run is related"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
	if strings.Contains(out, "<nil>") {
		t.Fatalf("wrong wider-anchor key: %s", out)
	}
}

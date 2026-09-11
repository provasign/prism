package cli

import (
	"strings"
	"testing"
)

func TestImpactTextPreservesCoverageAndWiderNote(t *testing.T) {
	m := map[string]any{
		"query": "Child.run", "totalSites": 2, "completeness": "partial",
		"familyCompleteness": "closed", "callerCoverage": "partial",
		"coverageNote": "dynamic dispatch is unresolved",
		"widerAnchor":  map[string]any{"note": "Base.run is related"},
	}
	out := captureStdout(func() { printChangeImpactText(m) })
	for _, want := range []string{"familyCompleteness: closed", "callerCoverage: partial", "dynamic dispatch is unresolved", "widerAnchor: Base.run is related"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
	if strings.Contains(out, "<nil>") {
		t.Fatalf("wrong wider-anchor key: %s", out)
	}
}

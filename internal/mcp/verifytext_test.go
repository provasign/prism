package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// The text form of prism_verify is what the agent actually reads. Until
// 2026-09-06 a REAL result (not the hand-built maps the renderer tests used)
// carried missedSites as a struct slice the renderer could not iterate, so
// the agent saw "verify: incomplete" with no MISSED SITES section at all.
func TestRenderVerifyAsText_ShowsMissedSitesFromRealResult(t *testing.T) {
	h, dir, sites := verifyFixture(t)
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 3: true})
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	txt, ok := renderVerifyAsText(m)
	if !ok {
		t.Fatal("verify text renderer fell back to JSON on a real result")
	}
	if !strings.Contains(txt, "MISSED SITES (3)") {
		t.Fatalf("missed sites absent from the text the agent reads:\n%s", txt)
	}
	for _, id := range []int{2, 4, 5} {
		loc := fmt.Sprintf("%s:%d", sites[id].file, sites[id].line)
		if !strings.Contains(txt, loc) {
			t.Errorf("missed site %s not listed:\n%s", loc, txt)
		}
	}
}

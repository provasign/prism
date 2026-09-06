package cli

import (
	"strings"
	"testing"
)

func TestSteeringRoutesKnownNamesDirectly(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"mcp__prism__prism_lookup",
		"Known symbol, affected sites or signature change:",
		"Known methods, need their behavior:",
		`prism_lookup(name=["A","B"])`,
		"Unknown location:",
		"Do not search merely to locate a symbol already named",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing direct route: %s", want)
		}
	}
	if strings.Contains(got, "FIRST discovery call a prism one (search/query)") {
		t.Error("known names must not require a preliminary search")
	}
	if len(got) > 3000 {
		t.Errorf("always-loaded steering grew beyond 3000 bytes: %d", len(got))
	}
	t.Logf("steering UTF-8 bytes: %d (not a model-token measurement)", len(got))
}

func TestSteeringRetainsEvidenceAndEditSafeguards(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"Before editing an existing symbol:",
		"Relay that set as-is",
		"Before declaring a multi-site change done:",
		`prism_verify(removed_symbols=["A","B"])`,
		"omitted evidence, ambiguous receivers, stale/incomplete",
		"Preserve reported sites while resolving uncertainty",
		"report them instead of claiming completeness",
		"inspect partial-result warnings",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing accuracy safeguard: %s", want)
		}
	}
}

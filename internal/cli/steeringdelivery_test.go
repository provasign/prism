package cli

import (
	"strings"
	"testing"
)

func TestSteeringRoutesKnownNamesDirectly(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"mcp__prism__prism_lookup",
		"affected sites, signature change, or pre-edit check",
		"known bodies",
		`prism_lookup(name=["A","B"])`,
		"known file/range",
		"unknown location/text",
		"related implementations/callers/tests",
		"Known symbol: use impact/lookup directly, not search",
		"external interfaces or incomplete wide scope",
		`prism_search(scope="text", exhaustive=true)`,
		"text matches do not prove implementation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing direct route: %s", want)
		}
	}
	for _, conflict := range []string{
		"FIRST discovery call a prism one (search/query)",
		"For a local bug or small feature",
		"use prism_lookup only for one known body",
		"guess ONE keyword",
	} {
		if strings.Contains(got, conflict) {
			t.Errorf("steering contains conflicting route %q", conflict)
		}
	}
	if strings.Contains(got, "Known symbol, affected sites") {
		t.Error("a named symbol alone must not trigger impact")
	}
	for _, want := range []string{"Read-only inspection alone needs no impact", "fetch bodies only", "site\nenumeration"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing task-depth distinction: %s", want)
		}
	}
	if len(got) > 3000 {
		t.Errorf("always-loaded steering grew beyond 3000 bytes: %d", len(got))
	}
	t.Logf("steering UTF-8 bytes: %d (not a model-token measurement)", len(got))
}

func TestSteeringRetainsEvidenceAndEditSafeguards(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"Before editing an existing symbol",
		"relay its sites as-is",
		"before finishing multi-site",
		`prism_verify(removed_symbols=["A","B"])`,
		"unclear evidence",
		"Preserve reported sites, resolve uncertainty",
		"report remaining gaps",
		"inspect partial-result warnings",
		"not every\nsmall local edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing accuracy safeguard: %s", want)
		}
	}
}

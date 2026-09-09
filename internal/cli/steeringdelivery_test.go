package cli

import (
	"strings"
	"testing"
)

func TestSteeringRoutesKnownNamesDirectly(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"mcp__prism__prism_lookup",
		"Affected sites or signature change:",
		"Known symbol bodies:",
		`prism_lookup(name=["A","B"])`,
		"Known file/range:",
		"Unknown code/text location:",
		"Related implementations, callers, and tests:",
		"Known symbol: use impact/lookup directly, not search",
		"External/unresolved interface or undersized closure for a wide task?",
		`prism_search(scope="text", exhaustive=true)`,
		"text matches do not prove implementation",
		"not guessed type names",
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
	for _, want := range []string{"Read-only inspection alone needs no impact", "do not fetch bodies by default", "site enumeration"} {
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

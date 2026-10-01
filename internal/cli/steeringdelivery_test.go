package cli

import (
	"strings"
	"testing"
)

func TestSteeringRoutesDiscoveryAndBatchesKnownInputs(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"Use Prism for each repository-discovery step",
		"cat/head/sed", "grep/rg/find/git log",
		"shell tools over Read/Edit/Write does not apply",
		"mcp__prism__prism",
		`ToolSearch("select:mcp__prism__prism")`,
		"Prism not being listed does not mean it is absent",
		`prism query --terms X`,
		"For each discovery step, pick the Prism op",
		"known symbol      -> lookup",
		"known file/range  -> read",
		"unknown location/text -> search",
		"callers/related       -> query",
		"tests naming known symbols -> search with test path/glob, files_only",
		"indirect tests    -> query",
		"Put every symbol and term you already know into ONE call",
		"Two lookups in a row is",
		"<!-- prism:end -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("steering omits %q", want)
		}
	}
	if strings.Count(got, "<!-- prism:end -->") != 1 {
		t.Error("bounded steering marker must occur exactly once")
	}
	// Per-language verify policy lives in the MCP instructions only; the
	// steering copy repeated it on every request (2026-09-30 trim).
	if strings.Contains(got, "unchecked JavaScript") {
		t.Error("steering repeats the MCP verify policy")
	}
	if len(got) > 2300 {
		t.Errorf("always-loaded steering grew beyond 2300 bytes: %d", len(got))
	}
	if strings.Contains(got, "callers/tests/related") {
		t.Error("steering collapsed narrow direct-test search into broad query routing")
	}
	t.Logf("steering UTF-8 bytes: %d", len(got))
}

func TestSteeringKeepsImpactMandatoryAndVerificationOptional(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"change_impact before editing a signature, public contract, override",
		"symbol whose callers you have not enumerated",
		"Relay its sites as-is",
		"Report gaps; never narrow scope to fit what was found",
		"verify is optional",
		"removed_symbols after a removal",
		"build/typecheck cannot cover the callers",
		"It is never a required closing step",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("steering omits %q", want)
		}
	}
	for _, mandate := range []string{"verify({}) before finishing", "verify({removed_symbols:[...]}) before a removal"} {
		if strings.Contains(got, mandate) {
			t.Errorf("steering still mandates optional verification: %q", mandate)
		}
	}
	if strings.Contains(got, "change_impact before editing every symbol") {
		t.Error("steering made impact unconditional")
	}
}

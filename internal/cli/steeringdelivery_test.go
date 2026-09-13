package cli

import (
	"strings"
	"testing"
)

func TestSteeringRoutesDiscoveryAndBatchesKnownInputs(t *testing.T) {
	got := steeringBlock()
	for _, want := range []string{
		"Repository discovery starts with Prism",
		"cat/head/sed", "grep/rg/find/git log",
		"shell tools over Read/Edit/Write does not apply",
		"mcp__prism__prism",
		`ToolSearch("select:mcp__prism__prism")`,
		"Prism not being listed does not mean it is absent",
		`prism query "<task>" --terms X`,
		"known symbol      -> lookup",
		"known file/range  -> read",
		"unknown location/text -> search",
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
	if len(got) > 3000 {
		t.Errorf("always-loaded steering grew beyond 3000 bytes: %d", len(got))
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
		"Optional checks:",
		"verify({removed_symbols:[...]}) after a removal",
		"Consider verify({}) for Python, unchecked JavaScript, or PHP",
		"For TypeScript or checked JavaScript",
		"For Go, Java,",
		"Rust, C/C++, or C#, skip it after a complete build/typecheck",
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

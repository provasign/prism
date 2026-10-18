package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/mcp"
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
		"start with change_impact on the named symbol, before deciding",
		mcp.ImpactRequiredFor,
		"Relay change_impact sites as-is",
		mcp.ImpactOptionalFor + " can",
		"proceed from the source you have read",
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
	// The blanket obligation contradicted the MCP instructions (2026-10-17):
	// impact is required for the triggers in mcp.ImpactRequiredFor and
	// optional for a local body-only edit, in every surface.
	for _, blanket := range []string{"any other symbol whose callers", "whose callers you have not enumerated"} {
		if strings.Contains(got, blanket) {
			t.Errorf("steering still carries the blanket impact obligation: %q", blanket)
		}
	}
}

// Installed instruction files written by an older Prism carry the blanket
// obligation inside the Prism-owned block; init must replace the block and
// leave the user's own text alone.
func TestSteeringRefreshReplacesTheBlanketImpactObligationAndKeepsUserText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	old := "# My rules\nAlways run the linter.\n\n## Prism — context delivery\n\nObligations:\n" +
		"  - Before editing any other symbol whose callers you have not enumerated, run\n    change_impact. Relay its sites as-is.\n\n<!-- prism:end -->\n\n## Mine\nKeep PRs small.\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSteeringInstructions(dir, []string{"claude"}, true)
	raw, _ := os.ReadFile(path)
	got := string(raw)
	if strings.Contains(got, "any other symbol whose callers") {
		t.Errorf("stale blanket obligation survived the refresh:\n%s", got)
	}
	for _, want := range []string{"# My rules", "Always run the linter.", "## Mine", "Keep PRs small.", mcp.ImpactRequiredFor, "Relay change_impact sites as-is"} {
		if !strings.Contains(got, want) {
			t.Errorf("refreshed file lost %q:\n%s", want, got)
		}
	}
}

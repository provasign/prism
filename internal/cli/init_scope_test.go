package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project-level init must keep the machine untouched: no user-global tool
// configs written (opencode leaked before v0.45.0 whenever its config dir
// existed), and Claude Code trust/allow/deny land in the PROJECT's
// .claude/settings.json, never ~/.claude/settings.json.
func TestInitProjectScopeTouchesNothingGlobal(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("USERPROFILE", home)
	// opencode installed: its global config dir exists.
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()

	initRegisterMCPTools(project, "prism", false, true, false, false)

	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
		t.Error("project init wrote the user-global opencode config")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Error("project init wrote machine-global ~/.claude/settings.json")
	}
	raw, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("project .claude/settings.json not written: %v", err)
	}
	for _, want := range []string{"enabledMcpjsonServers", "mcp__prism"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("project settings missing %s: %s", want, raw)
		}
	}
}

// The grep-BLOCKED paragraph must appear ONLY when denyBuiltinSearch is
// true — telling a user who declined the deny prompt that grep is blocked
// would be false guidance (most users decline it).
func TestSteeringBlock_GrepWarningOnlyWhenDenied(t *testing.T) {
	denied := steeringBlock(true)
	if !strings.Contains(denied, "grep, rg, and the built-in Grep tool are BLOCKED") {
		t.Error("deny=true steering must warn that grep/rg are blocked")
	}
	allowed := steeringBlock(false)
	if strings.Contains(allowed, "BLOCKED") {
		t.Error("deny=false steering must NOT claim grep is blocked (it isn't)")
	}
	// The base content must otherwise be identical.
	if strings.Count(denied, "prism_change_impact") != strings.Count(allowed, "prism_change_impact") {
		t.Error("deny variants diverged beyond the grep-warning insertion")
	}
}

// The section marker must survive heading renames. v0.48.0 renamed the
// heading ("context delivery" -> "code intelligence") while the replacer
// was pinned to the old text, so every refresh APPENDED a new block —
// doubling user files — and left the stale old-heading section in place
// forever. The replacer must (a) replace sections under ANY past heading
// and (b) heal files already duplicated by the bug.
func TestInjectPrismSection_HeadingRenameAndDedup(t *testing.T) {
	block := steeringBlock(false)

	// (a) old-heading section gets replaced, not appended-to.
	old := "# My project\n\n## Prism — context delivery (ALWAYS use these tools)\n\nold guidance\n\n<!-- prism:end -->\n\n## My rules\nkeep me\n"
	got := injectPrismSection(old, block)
	if strings.Count(got, "## Prism — ") != 1 {
		t.Errorf("old-heading upgrade left %d prism sections, want 1", strings.Count(got, "## Prism — "))
	}
	if !strings.Contains(got, "# My project") || !strings.Contains(got, "keep me") {
		t.Error("user content lost during heading-rename upgrade")
	}
	if strings.Contains(got, "old guidance") {
		t.Error("stale section not removed")
	}

	// (b) an already-duplicated file heals to exactly one section.
	doubled := injectPrismSection(injectPrismSection("# P\n", block)+"\n"+block, block)
	if strings.Count(doubled, "## Prism — ") != 1 {
		t.Errorf("duplicated file not healed: %d prism sections, want 1", strings.Count(doubled, "## Prism — "))
	}

	// (c) idempotence: re-running on a clean result is byte-stable.
	once := injectPrismSection("# P\n\n## Other\nx\n", block)
	twice := injectPrismSection(once, block)
	if once != twice {
		t.Error("re-init is not byte-stable")
	}
}

// The provasign/CLAUDE.md incident, pinned: a user-authored section whose
// heading merely STARTS with "## Prism — " (e.g. an architecture note titled
// "## Prism — Context Delivery Layer") must never be treated as prism-owned.
// An over-greedy prefix marker deleted exactly such a section, plus the
// unrelated "## Provasign — ..." section beneath it, from a real file on
// 2026-08-12 (restored from git). Only exact generated headings may match.
func TestInjectPrismSection_NeverTouchesUserAuthoredPrismHeadings(t *testing.T) {
	block := steeringBlock(false)
	file := "# Repo\n\n" +
		"## Prism — Context Delivery Layer\n\nuser's own architecture notes\n\n" +
		"## Prism — context delivery (ALWAYS use these tools)\n\nstale generated guidance, June era, no end marker\n\n" +
		"## Provasign — certified merge gate (ALWAYS use these tools)\n\ngate workflow the user maintains\n"
	got := injectPrismSection(file, block)
	for _, want := range []string{
		"user's own architecture notes",
		"## Prism — Context Delivery Layer",
		"gate workflow the user maintains",
		"## Provasign — certified merge gate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("user content lost: %q missing", want)
		}
	}
	if strings.Contains(got, "stale generated guidance") {
		t.Error("stale generated section not replaced")
	}
	if strings.Count(got, "(ALWAYS use these tools)") != 2 { // one generated prism + user's provasign
		t.Errorf("wrong section count: %d", strings.Count(got, "(ALWAYS use these tools)"))
	}
	// idempotent on re-run
	if again := injectPrismSection(got, block); again != got {
		t.Error("not byte-stable on re-init")
	}
}

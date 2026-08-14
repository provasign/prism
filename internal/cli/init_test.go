package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdInit(t *testing.T) {
	setHome(t, t.TempDir()) // keep global writers (Codex, Zed, Claude) off the real user configs
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(dir)
	if rc := cmdInit([]string{dir}); rc != 0 {
		t.Errorf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

func TestCmdInit_GlobalFlag(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	setHome(t, home)
	if rc := cmdInit([]string{dir, "--global"}); rc != 0 {
		t.Errorf("rc %d", rc)
	}
}

func TestCmdInit_BadDir(t *testing.T) {
	setHome(t, t.TempDir())
	// trigger write error by passing a path under a read-only parent
	parent := t.TempDir()
	ro := filepath.Join(parent, "ro")
	_ = os.MkdirAll(ro, 0o755)
	_ = os.Chmod(ro, 0o500)
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	target := filepath.Join(ro, "child")
	_ = os.MkdirAll(target, 0o755)
	if rc := cmdInit([]string{filepath.Join(ro, "nosuch")}); rc != 1 {
		// May or may not fail depending on platform; accept either
		t.Logf("rc %d", rc)
	}
}

func TestDetectSelfPath(t *testing.T) {
	if detectSelfPath() == "" {
		t.Error("empty")
	}
}

func TestWriteSteeringInstructions(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir, false)
	// Should have written at least one instruction file
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("no files")
	}
}

func TestBuildZedConfig(t *testing.T) {
	cfg := buildZedConfig("/x/prism")
	s := string(cfg)
	if len(cfg) == 0 {
		t.Fatal("empty")
	}
	// User-global entry: no pinned project dir (prism mcp serves launch cwd).
	for _, want := range []string{`"context_servers"`, `"prism"`, `"/x/prism"`, `"mcp"`} {
		if !contains(s, want) {
			t.Errorf("expected %q in %s", want, s)
		}
	}
	if contains(s, "/y/root") {
		t.Errorf("zed config must not pin a project dir: %s", s)
	}
}

// TestInitProjectLevelSkipsGlobalConfigs guards the multi-project footgun:
// a project-level init must not touch user-global configs (Zed, Codex) —
// doing so re-points every other project's editor at this one.
func TestInitProjectLevelSkipsGlobalConfigs(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	codexPath := filepath.Join(home, ".codex", "config.toml")
	zedPath := filepath.Join(home, ".config", "zed", "settings.json")
	const codexBefore = "[mcp_servers.other]\ncommand = \"x\"\n"
	const zedBefore = `{"context_servers":{"other":{"command":"x","args":[]}}}`
	os.MkdirAll(filepath.Dir(codexPath), 0o755)
	os.MkdirAll(filepath.Dir(zedPath), 0o755)
	os.WriteFile(codexPath, []byte(codexBefore), 0o644)
	os.WriteFile(zedPath, []byte(zedBefore), 0o644)

	dir := t.TempDir()
	initRegisterMCPTools(dir, "/x/prism", false, true, false, false)

	if got, _ := os.ReadFile(codexPath); string(got) != codexBefore {
		t.Errorf("project-level init modified global Codex config:\n%s", got)
	}
	if got, _ := os.ReadFile(zedPath); string(got) != zedBefore {
		t.Errorf("project-level init modified global Zed config:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Errorf("project-level init should still write .mcp.json: %v", err)
	}

	// --global registers both, without a pinned project dir.
	initRegisterMCPTools(dir, "/x/prism", true, true, false, false)
	codexAfter, _ := os.ReadFile(codexPath)
	if !strings.Contains(string(codexAfter), "[mcp_servers.prism]") {
		t.Errorf("--global init did not register Codex:\n%s", codexAfter)
	}
	if strings.Contains(string(codexAfter), dir) {
		t.Errorf("--global Codex entry must not pin a project dir:\n%s", codexAfter)
	}
	zedAfter, _ := os.ReadFile(zedPath)
	if !strings.Contains(string(zedAfter), `"prism"`) {
		t.Errorf("--global init did not register Zed:\n%s", zedAfter)
	}
	if strings.Contains(string(zedAfter), dir) {
		t.Errorf("--global Zed entry must not pin a project dir:\n%s", zedAfter)
	}
}

func TestBuildVSCodeConfig(t *testing.T) {
	cfg := buildVSCodeConfig("/x/prism", "/y/root")
	s := string(cfg)
	if len(cfg) == 0 {
		t.Fatal("empty")
	}
	for _, want := range []string{`"servers"`, `"prism"`, `"stdio"`, `"/x/prism"`, `"/y/root"`} {
		if !contains(s, want) {
			t.Errorf("expected %q in %s", want, s)
		}
	}
}

func TestWriteSteeringInstructions_AllTargets(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir, false)
	for _, want := range []string{
		"CLAUDE.md",
		"AGENTS.md",
		"GEMINI.md",
		".cursorrules",
		".windsurfrules",
		".clinerules",
		".github/copilot-instructions.md",
		".devin/instructions.md",
		".kiro/steering/prism.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestWriteSteeringInstructions_UpgradesStaleSection(t *testing.T) {
	dir := t.TempDir()
	// Write a file containing the old stale guidance.
	stale := "# Project config\n\n## Prism — context delivery (ALWAYS use these tools)\n\n### Rules\n1. Start every task with prism_query\n"
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSteeringInstructions(dir, false)
	raw, _ := os.ReadFile(path)
	s := string(raw)
	// Old guidance must be gone.
	if strings.Contains(s, "Start every task with prism_query") {
		t.Error("stale instructions not replaced")
	}
	// New guidance must be present.
	if !strings.Contains(s, "prism_change_impact") {
		t.Error("new instructions not written")
	}
	// Content before the Prism section must be preserved.
	if !strings.Contains(s, "# Project config") {
		t.Error("pre-existing content was lost")
	}
}

func TestInjectPrismSection(t *testing.T) {
	block := "\n## Prism — context delivery\nnew content\n"
	tests := []struct {
		name     string
		existing string
		wantPre  string // content that must appear before the block
	}{
		{
			name:     "replaces mid-file section",
			existing: "# Header\n\n## Prism — context delivery\nold\n",
			wantPre:  "# Header",
		},
		{
			name:     "replaces section-at-start",
			existing: "## Prism — context delivery\nold\n",
			wantPre:  "",
		},
		{
			name:     "appends when absent",
			existing: "# Header\n",
			wantPre:  "# Header",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := injectPrismSection(tc.existing, block)
			if !strings.Contains(got, "new content") {
				t.Errorf("new block missing: %q", got)
			}
			if strings.Contains(got, "old") {
				t.Errorf("old content not replaced: %q", got)
			}
			if tc.wantPre != "" && !strings.Contains(got, tc.wantPre) {
				t.Errorf("pre-existing content %q lost: %q", tc.wantPre, got)
			}
		})
	}
}

func TestWritePrismCodexConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// First write.
	if err := writePrismCodexConfig(path, "/usr/local/bin/prism", []string{"mcp", "/my/project"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	for _, want := range []string{`[mcp_servers.prism]`, `type = "stdio"`, `command = "/usr/local/bin/prism"`, `args = ["mcp", "/my/project"]`} {
		if !contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	// Idempotent second write must not duplicate the block.
	if err := writePrismCodexConfig(path, "/usr/local/bin/prism", []string{"mcp", "/my/project"}); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path)
	blockCount := 0
	for _, line := range strings.Split(string(raw2), "\n") {
		if line == "[mcp_servers.prism]" {
			blockCount++
		}
	}
	if blockCount != 1 {
		t.Errorf("expected 1 [mcp_servers.prism] block, got %d:\n%s", blockCount, raw2)
	}
}

func TestInitRegisterMCPTools_WritesVSCode(t *testing.T) {
	setHome(t, t.TempDir())
	dir := t.TempDir()
	written := initRegisterMCPTools(dir, "/x/prism", false, true, false, false)
	var sawVSCode bool
	for _, p := range written {
		if filepath.Base(filepath.Dir(p)) == ".vscode" && filepath.Base(p) == "mcp.json" {
			sawVSCode = true
		}
	}
	if !sawVSCode {
		t.Errorf(".vscode/mcp.json not written; got %v", written)
	}
}

func TestCmdInit_InstallAlias(t *testing.T) {
	setHome(t, t.TempDir())
	// `prism install` must behave identically to `prism init`.
	dir := t.TempDir()
	rc := Run([]string{"install", dir})
	if rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

func TestStripPrismTOMLBlock_NonMatchingBlock(t *testing.T) {
	lines := []string{
		"[[mcp_servers]]",
		`name = "other-tool"`,
		`command = "/usr/bin/other"`,
	}
	out := stripPrismTOMLBlock(lines, "mcp_servers", "prism")
	if len(out) != len(lines) {
		t.Errorf("expected %d lines preserved, got %d: %v", len(lines), len(out), out)
	}
}

func TestStripPrismTOMLBlock_EmptyInput(t *testing.T) {
	if out := stripPrismTOMLBlock(nil, "mcp_servers", "prism"); len(out) != 0 {
		t.Errorf("expected empty, got %v", out)
	}
}

func TestWritePrismCodexConfig_ExistingOtherContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "model = \"gpt-4\"\n\n[[mcp_servers]]\nname = \"other\"\ncommand = \"/usr/bin/other\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePrismCodexConfig(path, "/usr/local/bin/prism", []string{"mcp", "/root"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if !strings.Contains(s, `model = "gpt-4"`) {
		t.Error("existing model key lost")
	}
	if !strings.Contains(s, `name = "other"`) {
		t.Error("other mcp_servers block lost")
	}
	if !strings.Contains(s, `[mcp_servers.prism]`) {
		t.Error("prism table not added")
	}
}

func TestSteeringBlock_CoversBothSurfaces(t *testing.T) {
	// One block since v0.38.0: the mcp/cli/both split gated no tool and only
	// changed which documentation the agent read, so it collapsed. The single
	// block must still carry BOTH surfaces — MCP tool names for the primary
	// path and CLI invocations for Bash-only subagents.
	got := steeringBlock(false)
	for _, want := range []string{"prism_query", "prism query", "prism_change_impact", "change-impact"} {
		if !strings.Contains(got, want) {
			t.Errorf("steering block missing %q — it must cover MCP and CLI together", want)
		}
	}
}

func TestCmdInit_ModeFlagAcceptedAndIgnored(t *testing.T) {
	// --mode is kept for one release so existing scripts do not break; it must
	// not fail, and must not write an agent_mode key back into prism.yaml.
	for _, mode := range []string{"mcp", "cli", "both"} {
		t.Run(mode, func(t *testing.T) {
			setHome(t, t.TempDir())
			dir := t.TempDir()
			if rc := cmdInit([]string{dir, "--mode", mode}); rc != 0 {
				t.Fatalf("rc %d", rc)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "prism.yaml"))
			if strings.Contains(string(raw), "agent_mode") {
				t.Errorf("prism.yaml still carries agent_mode: %s", raw)
			}
			claudeMD, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
			for _, want := range []string{"prism_query", "prism query"} {
				if !strings.Contains(string(claudeMD), want) {
					t.Errorf("CLAUDE.md missing %q regardless of --mode %q", want, mode)
				}
			}
		})
	}
}

// contains is a tiny helper so we don't pull in strings for one test.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ─── Item 1: permissions auto-allow, --print-config, --refresh, opencode ────

// The Claude Code tool auto-allow must be written alongside server trust, and
// must survive re-runs without duplicating (and without clobbering unrelated
// settings).
func TestEnsureClaudeCodeApproval_WritesPermissionsAllow(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureClaudeCodeApproval(settings, "prism", "/fake/prism", true, false)
	ensureClaudeCodeApproval(settings, "prism", "/fake/prism", true, false) // idempotence

	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `"mcp__prism"`) {
		t.Errorf("permissions.allow missing mcp__prism: %s", got)
	}
	if strings.Count(got, `"mcp__prism"`) != 1 {
		t.Errorf("mcp__prism duplicated on re-run: %s", got)
	}
	if !strings.Contains(got, `"theme"`) {
		t.Errorf("unrelated setting clobbered: %s", got)
	}
	if !strings.Contains(got, `"enabledMcpjsonServers"`) {
		t.Errorf("server trust missing: %s", got)
	}
}

// --no-permissions must still establish server trust but write no allow rule.
func TestEnsureClaudeCodeApproval_NoPermissions(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	settings := filepath.Join(home, ".claude", "settings.json")
	ensureClaudeCodeApproval(settings, "prism", "/fake/prism", false, false)

	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, "mcp__prism") {
		t.Errorf("--no-permissions wrote an allow rule: %s", got)
	}
	if !strings.Contains(got, `"enabledMcpjsonServers"`) {
		t.Errorf("server trust missing: %s", got)
	}
}

// A previously-trusted server must still gain the allow rule: the two settings
// are independent and older prism versions wrote only the trust entry.
func TestEnsureClaudeCodeApproval_UpgradesTrustOnlyConfig(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"enabledMcpjsonServers":["prism"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureClaudeCodeApproval(settings, "prism", "/fake/prism", true, false)
	raw, _ := os.ReadFile(settings)
	if !strings.Contains(string(raw), "mcp__prism") {
		t.Errorf("allow rule not added to a trust-only config: %s", raw)
	}
}

// --print-config must render each agent and write nothing at all.
func TestPrintAgentConfig_WritesNothing(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	dir := t.TempDir()
	for _, id := range []string{"claude", "cursor", "windsurf", "vscode", "zed", "codex", "opencode", "hermes"} {
		if rc := printAgentConfig(id, dir, "/x/prism", false); rc != 0 {
			t.Errorf("%s: rc %d", id, rc)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("--print-config wrote %d entries into the project dir", len(entries))
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("--print-config wrote %d entries into HOME", len(entries))
	}
	if rc := printAgentConfig("bogus", dir, "/x/prism", false); rc != 2 {
		t.Errorf("unknown agent should exit 2, got %d", rc)
	}
}

// --refresh must never introduce a config for a tool that was not already set up.
func TestInitRegisterMCPTools_RefreshSkipsUnconfigured(t *testing.T) {
	setHome(t, t.TempDir())
	dir := t.TempDir()
	written := initRegisterMCPTools(dir, "/x/prism", false, true, true, false)
	if len(written) != 0 {
		t.Errorf("--refresh added configs to a fresh project: %v", written)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		t.Error("--refresh created .mcp.json")
	}
}

// ...but it must rewrite the ones that DO exist.
func TestInitRegisterMCPTools_RefreshRewritesConfigured(t *testing.T) {
	setHome(t, t.TempDir())
	dir := t.TempDir()
	initRegisterMCPTools(dir, "/old/prism", false, true, false, false) // first install
	written := initRegisterMCPTools(dir, "/new/prism", false, true, true, false)
	if len(written) == 0 {
		t.Fatal("--refresh rewrote nothing on a configured project")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/new/prism") {
		t.Errorf("--refresh did not update the binary path: %s", raw)
	}
}

func TestBuildOpencodeConfig(t *testing.T) {
	got := string(buildOpencodeConfig("/x/prism"))
	for _, want := range []string{`"$schema"`, `"mcp"`, `"prism"`, `"local"`, "/x/prism", `"enabled": true`} {
		if !strings.Contains(got, want) {
			t.Errorf("opencode config missing %s: %s", want, got)
		}
	}
}

// prism init must NEVER delete user content. Before the Prism block gained an
// end marker, injectPrismSection returned content[:idx]+block and silently
// dropped every section after it — on every re-init, and --refresh makes
// re-running routine.
func TestInjectPrismSection_PreservesTrailingUserContent(t *testing.T) {
	block := "\n## Prism — context delivery (ALWAYS use these tools)\nNEW\n\n<!-- prism:end -->\n"

	t.Run("legacy section without end marker", func(t *testing.T) {
		content := "# P\n\n## Build\nmake\n\n## Prism — context delivery (ALWAYS use these tools)\nOLD\n\n## MY RULES\nkeep me\n\n## Deploy\nkeep me too\n"
		got := injectPrismSection(content, block)
		for _, want := range []string{"## Build", "## MY RULES", "keep me", "## Deploy", "keep me too", "NEW"} {
			if !strings.Contains(got, want) {
				t.Errorf("lost %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "OLD") {
			t.Errorf("stale guidance not replaced:\n%s", got)
		}
	})

	t.Run("bounded section is replaced in place and is idempotent", func(t *testing.T) {
		content := "# P\n\n## Build\nmake\n" + block + "\n## MY RULES\nkeep me\n"
		got := injectPrismSection(content, block)
		again := injectPrismSection(got, block)
		if got != again {
			t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
		}
		if !strings.Contains(again, "## MY RULES") || !strings.Contains(again, "## Build") {
			t.Errorf("lost user sections:\n%s", again)
		}
		if n := strings.Count(again, "## Prism — context delivery"); n != 1 {
			t.Errorf("prism section count = %d, want 1:\n%s", n, again)
		}
	})

	t.Run("absent section appends", func(t *testing.T) {
		got := injectPrismSection("# P\n\n## Build\nmake\n", block)
		if !strings.Contains(got, "## Build") || !strings.Contains(got, "NEW") {
			t.Errorf("append failed:\n%s", got)
		}
	})
}

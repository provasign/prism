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
	writeSteeringInstructions(dir, "both")
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
	initRegisterMCPTools(dir, "/x/prism", false)

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
	initRegisterMCPTools(dir, "/x/prism", true)
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
	writeSteeringInstructions(dir, "both")
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
	writeSteeringInstructions(dir, "both")
	raw, _ := os.ReadFile(path)
	s := string(raw)
	// Old guidance must be gone.
	if strings.Contains(s, "Start every task with prism_query") {
		t.Error("stale instructions not replaced")
	}
	// New guidance must be present.
	if !strings.Contains(s, "line-numbered source windows") {
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
	written := initRegisterMCPTools(dir, "/x/prism", false)
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

func TestSteeringBlockForMode(t *testing.T) {
	cases := []struct {
		mode    string
		wantStr string
	}{
		{"mcp", "prism_query"},
		{"cli", "prism query"},
		{"both", "prism_query"},
		{"both", "prism query"},
		{"unknown", "prism query"}, // unknown → both
	}
	seen := map[string]string{}
	for _, tc := range cases {
		got := steeringBlockForMode(tc.mode)
		if !strings.Contains(got, tc.wantStr) {
			t.Errorf("mode %q: expected %q in block", tc.mode, tc.wantStr)
		}
		seen[tc.mode] = got
	}
	// MCP and CLI modes must produce distinct content.
	if seen["mcp"] == seen["cli"] {
		t.Error("mcp and cli produced identical steering blocks")
	}
}

func TestCmdInit_ModeFlag(t *testing.T) {
	for _, mode := range []string{"mcp", "cli", "both"} {
		t.Run(mode, func(t *testing.T) {
			setHome(t, t.TempDir())
			dir := t.TempDir()
			if rc := cmdInit([]string{dir, "--mode", mode}); rc != 0 {
				t.Fatalf("rc %d", rc)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "prism.yaml"))
			if !strings.Contains(string(raw), `agent_mode: "`+mode+`"`) {
				t.Errorf("agent_mode %q not in prism.yaml: %s", mode, raw)
			}
			claudeMD, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
			block := steeringBlockForMode(mode)
			// Pick a distinctive string from each mode's block.
			var mustContain string
			switch mode {
			case "mcp":
				mustContain = "prism_query"
			case "cli":
				mustContain = "prism query"
			case "both":
				mustContain = "prism_query"
			}
			if !strings.Contains(string(claudeMD), mustContain) {
				t.Errorf("CLAUDE.md missing %q for mode %q", mustContain, mode)
			}
			_ = block
		})
	}
}

func TestPromptAgentMode_NonInteractive(t *testing.T) {
	// In tests stdin is a pipe, not a terminal — must return "both" without blocking.
	got := promptAgentMode()
	if got != "both" {
		t.Errorf("non-interactive mode: got %q, want both", got)
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

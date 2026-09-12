package cli

import (
	"encoding/json"
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

	initRegisterMCPTools(project, "prism", supportedHarnesses, true, false, false)

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
	codexRaw, err := os.ReadFile(filepath.Join(project, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("project .codex/config.toml not written: %v", err)
	}
	if !strings.Contains(string(codexRaw), "[mcp_servers.prism]") {
		t.Errorf("project Codex config missing prism registration: %s", codexRaw)
	}
}

// --deny-builtin-search must land only in the project's Claude settings.
func TestInitDenyBuiltinSearch_LandsInProjectSettings(t *testing.T) {
	denyRules := []string{"Grep", "Bash(" + "grep:*)", "Bash(" + "rg:*)"}

	t.Run("project scope", func(t *testing.T) {
		home := t.TempDir()
		setHome(t, home)
		t.Setenv("USERPROFILE", home)
		project := t.TempDir()

		initRegisterMCPTools(project, "prism", []string{"claude"}, true, false, true)

		raw, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("project settings not written: %v", err)
		}
		for _, want := range denyRules {
			if !strings.Contains(string(raw), want) {
				t.Errorf("project settings missing deny rule %q: %s", want, raw)
			}
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
			t.Error("project-scoped deny leaked into machine-global settings")
		}
	})
}

func TestCmdInit_ConfiguresOnlySelectedHarnesses(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()

	if rc := cmdInit([]string{"--harness", "codex", project}); rc != 0 {
		t.Fatalf("cmdInit rc = %d", rc)
	}
	for _, want := range []string{"prism.yaml", "AGENTS.md", filepath.Join(".codex", "config.toml")} {
		if _, err := os.Stat(filepath.Join(project, want)); err != nil {
			t.Errorf("selected Codex file %s missing: %v", want, err)
		}
	}
	for _, unwanted := range []string{"CLAUDE.md", ".mcp.json", ".cursorrules", filepath.Join(".cursor", "mcp.json"), "GEMINI.md", "opencode.json"} {
		if _, err := os.Stat(filepath.Join(project, unwanted)); err == nil {
			t.Errorf("unselected harness file %s was written", unwanted)
		}
	}
}

func TestParseHarnesses_AliasesOrderAndValidation(t *testing.T) {
	got, err := parseHarnesses([]string{"VS-Code,claude-code", "codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "codex", "vscode"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseHarnesses = %v, want %v", got, want)
	}
	if _, err := parseHarnesses([]string{"unknown"}); err == nil {
		t.Fatal("unknown harness was accepted")
	}
}

func TestParseHarnessSelectionAcceptsNumbersNamesAndWhitespace(t *testing.T) {
	got, err := parseHarnessSelection("1, 2   vscode")
	if err != nil {
		t.Fatal(err)
	}
	if want := "claude,codex,vscode"; strings.Join(got, ",") != want {
		t.Fatalf("selection = %v, want %s", got, want)
	}
	if _, err := parseHarnessSelection("99"); err == nil {
		t.Fatal("out-of-range harness number was accepted")
	}
}

func TestPromptHarnessesReadsWholeLineAndConfirms(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("1, 2, 5\ny\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldIn
		_ = r.Close()
	})
	got, err := promptHarnesses(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "claude,codex,vscode"; strings.Join(got, ",") != want {
		t.Fatalf("prompt selection = %v, want %s", got, want)
	}
}

func TestCmdInitNonInteractiveRequiresHarness(t *testing.T) {
	setHome(t, t.TempDir())
	project := t.TempDir()
	if rc := cmdInit([]string{project}); rc != 2 {
		t.Fatalf("cmdInit without harness = %d, want 2", rc)
	}
	if fileExists(filepath.Join(project, "prism.yaml")) {
		t.Fatal("rejected init wrote prism.yaml")
	}
}

func TestCmdInitYesReusesRecordedHarnesses(t *testing.T) {
	setHome(t, t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "prism.yaml"), []byte("version: 2\nharnesses: \"codex\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := cmdInit([]string{"--yes", project}); rc != 0 {
		t.Fatalf("cmdInit --yes = %d", rc)
	}
	if !fileExists(filepath.Join(project, ".codex", "config.toml")) {
		t.Fatal("recorded Codex selection was not reused")
	}
	if fileExists(filepath.Join(project, ".mcp.json")) {
		t.Fatal("unrecorded Claude harness was configured")
	}
}

func TestRemoveLegacyGlobalMCPRegistrations_PreservesUnrelatedConfig(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("USERPROFILE", home)

	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, []byte(`{"theme":"dark","mcpServers":{"prism":{"command":"old"},"other":{"command":"keep"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	codex := "model = \"keep\"\n\n[mcp_servers.prism]\ncommand = \"old\"\nargs = [\"mcp\"]\n\n[mcp_servers.other]\ncommand = \"keep\"\n"
	if err := os.WriteFile(codexPath, []byte(codex), 0o644); err != nil {
		t.Fatal(err)
	}
	opencodePath := filepath.Join(home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(opencodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	opencode := `{"mcp":{"prism":{"command":["old"]},"other":{"command":["keep"]},"servers":{"prism":{"command":["old2"]},"other2":{"command":["keep2"]}}}}`
	if err := os.WriteFile(opencodePath, []byte(opencode), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"enabledMcpjsonServers":["prism","keep"],"permissions":{"allow":["mcp__prism","mcp__other"],"deny":["Grep","Bash(grep:*)","Bash(rg:*)","keep-deny"]}}`
	if err := os.WriteFile(claudeSettings, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeLegacyGlobalMCPRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 4 {
		t.Fatalf("changed = %v, want four global config files", changed)
	}
	for _, path := range []string{claudePath, codexPath, opencodePath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "prism") || !strings.Contains(string(raw), "keep") {
			t.Errorf("migration did not remove only Prism from %s:\n%s", path, raw)
		}
	}
	settingsRaw, err := os.ReadFile(claudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settingsRaw), "mcp__prism") || strings.Contains(string(settingsRaw), `"prism"`) || strings.Contains(string(settingsRaw), `"Grep"`) {
		t.Fatalf("legacy global Claude approval survived:\n%s", settingsRaw)
	}
	for _, want := range []string{"mcp__other", "keep-deny", `"keep"`} {
		if !strings.Contains(string(settingsRaw), want) {
			t.Fatalf("unrelated global Claude setting %q was removed:\n%s", want, settingsRaw)
		}
	}
}

func TestWritePrismCodexConfigRemovesNestedLegacySubtree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	existing := `[mcp_servers.prism.tools.search]
enabled = false

[mcp_servers."prism".tools.lookup]
enabled = true

[mcp_servers.other]
command = "keep"
`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePrismCodexConfig(path, "/new/prism", []string{"mcp", "--compact"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	if strings.Contains(got, "tools.search") || strings.Contains(got, "tools.lookup") {
		t.Fatalf("legacy Prism subtree survived:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.other]") || !strings.Contains(got, `args = ["mcp", "--compact"]`) {
		t.Fatalf("unrelated/current config missing:\n%s", got)
	}
}

func TestMergeOrCreatePreservesInvalidJSONAndWritesBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	bad := []byte(`{"mcpServers":`)
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeOrCreate(path, buildMCPConfig("prism", mcpEntry{Command: "prism"}), []string{"mcpServers"}); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if got, _ := os.ReadFile(path); string(got) != string(bad) {
		t.Fatalf("invalid user config was overwritten: %q", got)
	}
	if got, err := os.ReadFile(path + ".prism-backup"); err != nil || string(got) != string(bad) {
		t.Fatalf("backup = %q, %v", got, err)
	}
}

func TestWindsurfSelectionRemovesUnsupportedLegacyProjectEntry(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".windsurf", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"prism":{"command":"old"},"keep":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	initRegisterMCPTools(project, "prism", []string{"windsurf"}, false, false, false)
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"prism"`) || !strings.Contains(string(raw), `"keep"`) {
		t.Fatalf("legacy Windsurf cleanup was not scoped:\n%s", raw)
	}
}

func TestOpenCodeV2SchemaIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"servers":{"other":{"type":"local","command":["x"]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := buildOpencodeConfigForPath("/x/prism", path)
	merged, err := mergeOrCreate(path, overlay, []string{"mcp"}, []string{"mcp", "servers"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatal(err)
	}
	mcpMap := doc["mcp"].(map[string]any)
	servers := mcpMap["servers"].(map[string]any)
	if servers["prism"] == nil || servers["other"] == nil || mcpMap["prism"] != nil {
		t.Fatalf("OpenCode v2 merge shape is wrong: %s", merged)
	}
}

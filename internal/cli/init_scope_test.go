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

	changed := removeLegacyGlobalMCPRegistrations()
	if len(changed) != 3 {
		t.Fatalf("changed = %v, want three global config files", changed)
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
}

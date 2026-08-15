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

// --deny-builtin-search must land its rules SOMEWHERE in both scopes. The
// approval step used to be keyed on the config file name (".mcp.json"), which
// is only how Claude Code is registered at project scope — under --global it
// registers via ~/.claude.json, so the whole approval/permissions step was
// skipped and `init --global --deny-builtin-search` wrote the deny rules to
// no file at all. Silent, and invisible to every existing test because they
// only covered the project path. Keyed on the writer now; this pins both.
func TestInitDenyBuiltinSearch_LandsInTheScopedSettingsFile(t *testing.T) {
	denyRules := []string{"Grep", "Bash(" + "grep:*)", "Bash(" + "rg:*)"}

	t.Run("project scope", func(t *testing.T) {
		home := t.TempDir()
		setHome(t, home)
		t.Setenv("USERPROFILE", home)
		project := t.TempDir()

		initRegisterMCPTools(project, "prism", false, true, false, true) // global=false, deny=true

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

	t.Run("global scope", func(t *testing.T) {
		home := t.TempDir()
		setHome(t, home)
		t.Setenv("USERPROFILE", home)
		project := t.TempDir()

		initRegisterMCPTools(project, "prism", true, true, false, true) // global=true, deny=true

		raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("global settings not written under --global: %v", err)
		}
		for _, want := range denyRules {
			if !strings.Contains(string(raw), want) {
				t.Errorf("global settings missing deny rule %q: %s", want, raw)
			}
		}
	})
}

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

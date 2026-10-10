package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bsdSed(t *testing.T) {
	t.Helper()
	if exec.Command("sed", "--version").Run() == nil {
		t.Skip("sed is GNU here; the guard only acts on BSD sed")
	}
}

func (h *hookEnv) sedDecision(command string) string {
	h.t.Helper()
	out := h.run("prism_sed_guard.py", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	if out == "" {
		return ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.HookSpecificOutput.PermissionDecision != "deny" {
		h.t.Fatalf("sed guard output: %q", out)
	}
	return doc.HookSpecificOutput.PermissionDecisionReason
}

// GNU escapes silently match nothing in BSD sed: the 2026-10-10 benchmark
// lost a task to `sed -i ” 's/\bget_command\b/.../g'` changing no file.
func TestSedGuard_DeniesCommandsBSDSedMisreads(t *testing.T) {
	bsdSed(t)
	h := newHookEnv(t)
	for _, cmd := range []string{
		`sed -i '' 's/\bget_command\b/lookup_command/g' src/click/core.py`,
		`grep -rl get_headers src | xargs sed -i '' -E 's/\bget_headers\(/response_headers(/g'`,
		`find . -name '*.go' -exec sed -i '' 's/\<Old\>/New/g' {} \;`,
		`sed 's/fo\+/x/' a.txt`,
		`sed -i 's/a/b/' f.go`,
		`sed -i -e 's/a/b/' f.go`,
	} {
		if reason := h.sedDecision(cmd); !strings.Contains(reason, "BSD") {
			t.Errorf("not denied: %s (reason %q)", cmd, reason)
		}
	}
}

func TestSedGuard_AllowsWorkingCommands(t *testing.T) {
	h := newHookEnv(t)
	for _, cmd := range []string{
		`sed -i '' 's/[[:<:]]foo[[:>:]]/bar/g' a.py`,
		`sed -E -i '' 's/fo+/x/' a.txt`,
		`sed -i '' -e 's/a/b/' f.go`,
		`sed -i.bak 's/a/b/' f.go`,
		`sed -n 10,20p file.go`,
		`grep -rn sed docs/README.md`,
		"python3 - <<'E'\nopen('f','w').write('x')\nE",
	} {
		if reason := h.sedDecision(cmd); reason != "" {
			t.Errorf("denied a working command: %s (%s)", cmd, reason)
		}
	}
}

func TestSedGuard_InstalledAndRemovedWithReadGuard(t *testing.T) {
	h := newHookEnv(t)
	script := filepath.Join(h.project, filepath.FromSlash(sedGuardPath))
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("sed guard not installed: %v", err)
	}
	settings, _ := os.ReadFile(filepath.Join(h.project, ".claude", "settings.json"))
	if !strings.Contains(string(settings), sedGuardCmd()) {
		t.Fatalf("settings.json lacks the sed guard entry:\n%s", settings)
	}
	if err := uninstallReadGuard(h.project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("sed guard script left behind: %v", err)
	}
	settings, _ = os.ReadFile(filepath.Join(h.project, ".claude", "settings.json"))
	if strings.Contains(string(settings), "prism_sed_guard") {
		t.Fatalf("settings.json still references the sed guard:\n%s", settings)
	}
}

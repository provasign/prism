package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func settingsHookCommands(t *testing.T, project string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, entries := range doc.Hooks {
		for _, e := range entries {
			for _, h := range e.Hooks {
				cmds = append(cmds, h.Command)
			}
		}
	}
	return cmds
}

// The 2026-10-10 bug: hooks registered as `python3 .claude/hooks/x.py` ran in
// the agent's current directory, so after `cd src/...` python exited 2 (file
// not found) and Claude Code blocked every Bash and Read call. Run each
// registered command the way Claude Code does, from a subdirectory.
func TestReadGuardHooksRunFromSubdirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	h := newHookEnv(t)
	sub := filepath.Join(h.project, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{}}`
	for _, cmd := range settingsHookCommands(t, h.project) {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = sub
		c.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+h.project)
		c.Stdin = strings.NewReader(payload)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("%s from a subdirectory: %v\n%s", cmd, err, out)
		}
	}
}

func TestReadGuardInstallReplacesLegacyRelativeCommands(t *testing.T) {
	project := t.TempDir()
	legacy := `{"hooks":{
	  "PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"python3 .claude/hooks/prism_read_guard.py"}]},
	                {"matcher":"Bash","hooks":[{"type":"command","command":"python3 .claude/hooks/prism_sed_guard.py"},
	                                           {"type":"command","command":"./my-own-hook.sh"}]}],
	  "PostToolUse":[{"matcher":"mcp__prism__prism","hooks":[{"type":"command","command":"python3 .claude/hooks/prism_read_tracker.py"}]}]}}`
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installReadGuard(project); err != nil {
		t.Fatal(err)
	}
	cmds := settingsHookCommands(t, project)
	joined := strings.Join(cmds, "\n")
	if strings.Contains(joined, "python3 .claude/") {
		t.Errorf("legacy relative command left behind:\n%s", joined)
	}
	if !strings.Contains(joined, "./my-own-hook.sh") {
		t.Errorf("user's own hook dropped:\n%s", joined)
	}
	if n := strings.Count(joined, "$CLAUDE_PROJECT_DIR"); n != 4 {
		t.Errorf("want 4 prism hook entries, got %d:\n%s", n, joined)
	}

	if err := uninstallReadGuard(project); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(settingsHookCommands(t, project), "\n"); got != "./my-own-hook.sh" {
		t.Errorf("uninstall should leave only the user's hook, got:\n%s", got)
	}
}

// brew upgrades the binary, not the scripts copied into the project: any
// init run upgrades an existing read guard.
func TestInitUpgradesExistingReadGuard(t *testing.T) {
	project := t.TempDir()
	if err := installReadGuard(project); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(project, filepath.FromSlash(sedGuardPath))
	if err := os.WriteFile(stale, []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := cmdInit([]string{"--harness", "claude", "--yes", project}); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	got, _ := os.ReadFile(stale)
	want, _ := readGuardAssets.ReadFile("assets/prism_sed_guard.py")
	if string(got) != string(want) {
		t.Error("init did not rewrite the installed sed guard script")
	}
}

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

// sedHook runs the guard and returns (rewritten command, deny reason, the
// note to the agent); all empty means the command was let through as is.
func (h *hookEnv) sedHook(command string) (rewritten, denied, note string) {
	h.t.Helper()
	out := h.run("prism_sed_guard.py", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command, "description": "d"}})
	if out == "" {
		return "", "", ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string            `json:"permissionDecision"`
			PermissionDecisionReason string            `json:"permissionDecisionReason"`
			UpdatedInput             map[string]string `json:"updatedInput"`
			AdditionalContext        string            `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		h.t.Fatalf("sed guard output: %q", out)
	}
	o := doc.HookSpecificOutput
	switch {
	case o.PermissionDecision == "deny":
		return "", o.PermissionDecisionReason, ""
	case o.PermissionDecision == "" && o.UpdatedInput["command"] != "" && len(o.UpdatedInput) == 1:
		return o.UpdatedInput["command"], "", o.AdditionalContext
	}
	h.t.Fatalf("unexpected sed guard output (a rewrite must not decide permission): %q", out)
	return "", "", ""
}

// GNU escapes silently match nothing in BSD sed: the 2026-10-10 benchmark
// lost a task to `sed -i ” 's/\bget_command\b/.../g'` changing no file.
// Where the BSD spelling is exact, the guard runs that instead of denying,
// so the agent does not spend a call rewriting the command itself.
func TestSedGuard_RewritesToBSDSpelling(t *testing.T) {
	bsdSed(t)
	h := newHookEnv(t)
	for cmd, want := range map[string]string{
		`sed -i '' 's/\bget_command\b/lookup_command/g' src/click/core.py`:                      `sed -i '' 's/[[:<:]]get_command[[:>:]]/lookup_command/g' src/click/core.py`,
		`grep -rl get_headers src | xargs sed -i '' -E 's/\bget_headers\(/response_headers(/g'`: `grep -rl get_headers src | xargs sed -i '' -E 's/[[:<:]]get_headers\(/response_headers(/g'`,
		`find . -name '*.go' -exec sed -i '' 's/\<Old\>/New/g' {} \;`:                           `find . -name '*.go' -exec sed -i '' 's/[[:<:]]Old[[:>:]]/New/g' {} \;`,
		`sed 's/fo\+/x/' a.txt`:                                               `sed 's/fo\{1,\}/x/' a.txt`,
		`sed -i 's/a/b/' f.go`:                                                `sed -i '' 's/a/b/' f.go`,
		`sed -i -e 's/a/b/' f.go`:                                             `sed -i '' -e 's/a/b/' f.go`,
		`sed -i '' 's/\w\+_id\s*=/key =/' m.py && go test ./...`:              `sed -i '' 's/[[:alnum:]_]\{1,\}_id[[:space:]]*=/key =/' m.py && go test ./...`,
		`sed -i '' 's/\(resp\|response\)\.get_data(x)/\1.read_body(x)/' t.py`: `sed -E -i '' 's/(resp|response)\.get_data\(x\)/\1.read_body(x)/' t.py`,
		"sed -i '' \\\n  's/\\bfoo\\b/bar/g' a.py":                            "sed -i '' \\\n  's/[[:<:]]foo[[:>:]]/bar/g' a.py",
	} {
		got, denied, note := h.sedHook(cmd)
		if got != want {
			t.Errorf("%s\n  got  %q (denied %q)\n  want %q", cmd, got, denied, want)
		}
		if got != "" && !strings.Contains(note, got) {
			t.Errorf("agent not told what ran: %q", note)
		}
	}
}

// No exact BSD spelling: deny and explain, as before.
func TestSedGuard_DeniesWhatItCannotTranslate(t *testing.T) {
	bsdSed(t)
	h := newHookEnv(t)
	for _, cmd := range []string{
		`sed -i '' 's/foo\bbar/x/' a.py`,        // \b between two word characters
		`sed -i '' 's/\Bfoo/x/' a.py`,           // \B
		`sed -i '' 's/\bMatch\b\(.*\)/&/' c.go`, // \b before a group: start or end?
		"sed -i '' \"s/\\bx/y/\" `ls`",          // backticks
	} {
		if got, denied, _ := h.sedHook(cmd); !strings.Contains(denied, "BSD") {
			t.Errorf("not denied: %s (rewrote to %q)", cmd, got)
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
		if got, denied, _ := h.sedHook(cmd); got != "" || denied != "" {
			t.Errorf("touched a working command: %s (rewrote %q, denied %q)", cmd, got, denied)
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

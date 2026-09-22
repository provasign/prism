package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadGuard_InstallWritesHooksAndSettings(t *testing.T) {
	project := t.TempDir()

	if err := installReadGuard(project); err != nil {
		t.Fatalf("installReadGuard: %v", err)
	}

	for _, name := range []string{"prism_read_tracker.py", "prism_read_guard.py"} {
		p := filepath.Join(project, ".claude", "hooks", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("hook script %s not written: %v", name, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("no hooks key in settings.json: %s", raw)
	}
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected exactly 1 PostToolUse entry, got %d: %s", len(post), raw)
	}
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected exactly 1 PreToolUse entry, got %d: %s", len(pre), raw)
	}
}

// Installing twice must not duplicate entries.
func TestReadGuard_InstallIsIdempotent(t *testing.T) {
	project := t.TempDir()
	if err := installReadGuard(project); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installReadGuard(project); err != nil {
		t.Fatalf("second install: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	var doc map[string]any
	json.Unmarshal(raw, &doc) //nolint:errcheck
	hooks := doc["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Errorf("expected 1 PostToolUse entry after double install, got %d: %s", len(post), raw)
	}
}

// The core ask: install, then cleanly uninstall, leaving the user's own
// hooks and unrelated settings completely untouched.
func TestReadGuard_UninstallRemovesOnlyItsOwnEntries(t *testing.T) {
	project := t.TempDir()
	settingsPath := filepath.Join(project, ".claude", "settings.json")

	// Seed a pre-existing settings.json with the user's OWN hook on the same
	// matchers prism uses, plus an unrelated top-level setting.
	preexisting := map[string]any{
		"unrelatedSetting": "keep-me",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Read",
					"hooks": []any{
						map[string]any{"type": "command", "command": "python3 my_own_read_hook.py"},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "python3 my_bash_logger.py"},
					},
				},
			},
		},
	}
	if err := writeJSONObject(settingsPath, preexisting); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := installReadGuard(project); err != nil {
		t.Fatalf("installReadGuard: %v", err)
	}
	if err := uninstallReadGuard(project); err != nil {
		t.Fatalf("uninstallReadGuard: %v", err)
	}

	for _, name := range []string{"prism_read_tracker.py", "prism_read_guard.py"} {
		p := filepath.Join(project, ".claude", "hooks", name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("hook script %s still present after uninstall", name)
		}
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json missing after uninstall: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings.json corrupted after uninstall: %v", err)
	}
	if doc["unrelatedSetting"] != "keep-me" {
		t.Errorf("unrelated setting was lost: %s", raw)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("hooks key was dropped entirely, but the user's own hooks should survive: %s", raw)
	}
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("user's own PreToolUse hook was lost, got %d entries: %s", len(pre), raw)
	}
	preEntry := pre[0].(map[string]any)
	preHooks := preEntry["hooks"].([]any)
	if len(preHooks) != 1 || preHooks[0].(map[string]any)["command"] != "python3 my_own_read_hook.py" {
		t.Errorf("user's own Read hook command changed: %v", preHooks)
	}
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("user's own PostToolUse hook was lost, got %d entries: %s", len(post), raw)
	}
	postEntry := post[0].(map[string]any)
	postHooks := postEntry["hooks"].([]any)
	if len(postHooks) != 1 || postHooks[0].(map[string]any)["command"] != "python3 my_bash_logger.py" {
		t.Errorf("user's own Bash hook command changed: %v", postHooks)
	}
}

// Uninstalling when the hook was never installed must be a safe no-op, not
// an error (e.g. running --no-read-guard twice, or on a repo that never had
// it).
func TestReadGuard_UninstallWithoutInstallIsNoop(t *testing.T) {
	project := t.TempDir()
	if err := uninstallReadGuard(project); err != nil {
		t.Fatalf("uninstallReadGuard on a repo with no settings.json: %v", err)
	}
	if err := uninstallReadGuard(project); err != nil {
		t.Fatalf("second uninstallReadGuard: %v", err)
	}
}

func TestCmdInit_ReadGuardFlags(t *testing.T) {
	setHome(t, t.TempDir())
	project := t.TempDir()

	if rc := cmdInit([]string{"--read-guard", project}); rc != 0 {
		t.Fatalf("cmdInit --read-guard rc = %d", rc)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "hooks", "prism_read_guard.py")); err != nil {
		t.Errorf("--read-guard did not install the hook: %v", err)
	}
	// --read-guard is a standalone action: it must not also run the full
	// init flow (no prism.yaml, no harness prompt/requirement).
	if _, err := os.Stat(filepath.Join(project, "prism.yaml")); err == nil {
		t.Error("--read-guard alone should not run the full init flow (prism.yaml was written)")
	}

	if rc := cmdInit([]string{"--no-read-guard", project}); rc != 0 {
		t.Fatalf("cmdInit --no-read-guard rc = %d", rc)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "hooks", "prism_read_guard.py")); !os.IsNotExist(err) {
		t.Errorf("--no-read-guard did not remove the hook")
	}

	if rc := cmdInit([]string{"--read-guard", "--no-read-guard", project}); rc != 2 {
		t.Errorf("cmdInit --read-guard --no-read-guard rc = %d, want 2 (mutually exclusive)", rc)
	}
}

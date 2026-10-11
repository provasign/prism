package cli

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed assets/prism_read_tracker.py assets/prism_read_guard.py assets/prism_sed_guard.py
var readGuardAssets embed.FS

// readGuardTrackerCmd / readGuardGuardCmd are the exact hook command strings
// prism writes into .claude/settings.json. They double as the removal
// marker: uninstallReadGuard only ever touches hook entries whose command
// contains one of these two paths, so a user's own unrelated hooks are never
// disturbed. Relative (not absolute) so the entries survive being committed
// and cloned into a different path -- Claude Code runs hooks with cwd at the
// project root.
const (
	readGuardTrackerPath = ".claude/hooks/prism_read_tracker.py"
	readGuardGuardPath   = ".claude/hooks/prism_read_guard.py"
	sedGuardPath         = ".claude/hooks/prism_sed_guard.py"

	// readGuardInvalidateMatcher routes edits and shell commands to the
	// tracker so it forgets ranges once their file may have changed (a stale
	// range once denied a Read of code the agent had just edited).
	readGuardInvalidateMatcher = "Edit|Write|MultiEdit|NotebookEdit|Bash"

	// readGuardStateDir holds per-session tracker state; the tracker writes a
	// "*" .gitignore inside it so the state never shows up in git.
	readGuardStateDir = ".claude/prism-read-guard"

	// readGuardLegacyState is where v0.84 and earlier kept the tracker
	// state (project root, shared across sessions); install and uninstall
	// both delete it.
	readGuardLegacyState = ".prism-read-tracker.json"
)

// Hook commands name the script through $CLAUDE_PROJECT_DIR, not a path
// relative to the working directory: Claude Code runs hooks in the shell's
// current directory, so after the agent ran `cd src/...` a relative
// `python3 .claude/hooks/x.py` failed with exit 2, which Claude Code reads as
// "block". Every later Bash and Read call was refused; one 2026-10-10
// benchmark agent spent 9 calls (and wrote stub hook files into the repo)
// getting its shell back.
func hookCmd(path string) string { return `python3 "$CLAUDE_PROJECT_DIR"/` + path }

func readGuardTrackerCmd() string { return hookCmd(readGuardTrackerPath) }
func readGuardGuardCmd() string   { return hookCmd(readGuardGuardPath) }
func sedGuardCmd() string         { return hookCmd(sedGuardPath) }

// legacyHookCmds are the cwd-relative commands v0.87.4 and earlier
// registered; install replaces them and uninstall removes them.
var legacyHookCmds = map[string][]string{
	"PostToolUse": {"python3 " + readGuardTrackerPath},
	"PreToolUse":  {"python3 " + readGuardGuardPath, "python3 " + sedGuardPath},
}

func removeLegacyHookEntries(hooks map[string]any) bool {
	changed := false
	for event, cmds := range legacyHookCmds {
		for _, cmd := range cmds {
			changed = removeHookEntry(hooks, event, cmd) || changed
		}
	}
	return changed
}

// readGuardInstalled reports whether a read guard install is present, so a
// later `prism init` can upgrade its scripts and settings in place.
func readGuardInstalled(projectDir string) bool {
	return fileExists(filepath.Join(projectDir, filepath.FromSlash(readGuardGuardPath)))
}

// readGuardScripts are the hook scripts installReadGuard writes and
// uninstallReadGuard removes.
var readGuardScripts = []string{"prism_read_tracker.py", "prism_read_guard.py", "prism_sed_guard.py"}

// installReadGuard writes the two hook scripts into projectDir/.claude/hooks
// and registers them in projectDir/.claude/settings.json. Denies a native
// Read that substantially overlaps a range prism already delivered this
// session and whose file is unchanged on disk since -- measured (2026-09-22, 13-task controlled A/B, hook-on vs
// hook-off on the identical task set) to cut tokens ~11% with no change in
// resolve rate. Claude Code only: hooks are a Claude Code mechanism, no
// other supported harness exposes an equivalent.
func installReadGuard(projectDir string) error {
	hooksDir := filepath.Join(projectDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("read-guard: %w", err)
	}
	for _, name := range readGuardScripts {
		data, err := readGuardAssets.ReadFile("assets/" + name)
		if err != nil {
			return fmt.Errorf("read-guard: %w", err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, name), data, 0o644); err != nil {
			return fmt.Errorf("read-guard: %w", err)
		}
	}
	// v0.84-and-earlier state had no session scoping and no invalidation; drop it.
	_ = os.Remove(filepath.Join(projectDir, readGuardLegacyState))

	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	doc, err := readJSONObject(settingsPath)
	if err != nil {
		return fmt.Errorf("read-guard: %w", err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	// Re-running install upgrades an older install in place: the scripts are
	// rewritten above and addHookEntry only adds the entries that are missing.
	changed := removeLegacyHookEntries(hooks)
	changed = addHookEntry(hooks, "PostToolUse", "mcp__prism__prism", readGuardTrackerCmd()) || changed
	changed = addHookEntry(hooks, "PostToolUse", readGuardInvalidateMatcher, readGuardTrackerCmd()) || changed
	changed = addHookEntry(hooks, "PreToolUse", "Read", readGuardGuardCmd()) || changed
	// BSD (macOS) sed reads GNU escapes like \b as literals: the edit
	// silently changes nothing and the agent reports it done. The sed guard
	// denies those commands and says why; it does nothing where sed is GNU.
	changed = addHookEntry(hooks, "PreToolUse", "Bash", sedGuardCmd()) || changed
	doc["hooks"] = hooks
	if changed {
		if err := writeJSONObject(settingsPath, doc); err != nil {
			return fmt.Errorf("read-guard: %w", err)
		}
	}
	fmt.Println("installed read-guard hook:", filepath.Join(".claude", "hooks"))
	fmt.Println("  denies a Read that substantially overlaps content prism already delivered this session")
	fmt.Println("  runs a GNU-style sed command in its BSD (macOS) spelling, or denies it with the reason when there is none")
	fmt.Println("  uninstall any time with: prism init --no-read-guard", projectDir)
	return nil
}

// uninstallReadGuard removes exactly what installReadGuard wrote: the two
// hook scripts, the .claude/settings.json entries whose command matches
// them (a hook the user added to the same matcher survives -- only entries
// whose command string is ours are dropped), and the tracker state file. A
// settings.json entry that has other hooks alongside ours keeps those and
// only drops prism's.
func uninstallReadGuard(projectDir string) error {
	for _, name := range readGuardScripts {
		p := filepath.Join(projectDir, ".claude", "hooks", name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read-guard: %w", err)
		}
	}
	_ = os.Remove(filepath.Join(projectDir, readGuardLegacyState))
	_ = os.RemoveAll(filepath.Join(projectDir, filepath.FromSlash(readGuardStateDir)))

	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	doc, err := readJSONObject(settingsPath)
	if err != nil {
		return fmt.Errorf("read-guard: %w", err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	changed := false
	if hooks != nil {
		changed = removeHookEntry(hooks, "PostToolUse", readGuardTrackerCmd())
		changed = removeHookEntry(hooks, "PreToolUse", readGuardGuardCmd()) || changed
		changed = removeHookEntry(hooks, "PreToolUse", sedGuardCmd()) || changed
		changed = removeLegacyHookEntries(hooks) || changed
		if len(hooks) == 0 {
			delete(doc, "hooks")
			changed = true
		} else {
			doc["hooks"] = hooks
		}
	}
	if changed {
		if err := writeJSONObject(settingsPath, doc); err != nil {
			return fmt.Errorf("read-guard: %w", err)
		}
	}
	fmt.Println("removed read-guard hook and its settings.json entries")
	return nil
}

// addHookEntry appends {"matcher": matcher, "hooks": [{"type":"command",
// "command": command}]} to hooks[event] unless an entry with that exact
// matcher+command already exists.
func addHookEntry(hooks map[string]any, event, matcher, command string) bool {
	list, _ := hooks[event].([]any)
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok || entry["matcher"] != matcher {
			continue
		}
		for _, h := range asSlice(entry["hooks"]) {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				return false // already installed
			}
		}
	}
	list = append(list, map[string]any{
		"matcher": matcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
		}},
	})
	hooks[event] = list
	return true
}

// removeHookEntry drops only the inner hook whose command == command,
// leaving any other hook on the same matcher untouched; a matcher entry
// left with zero hooks is dropped, and an event left with zero matcher
// entries is removed from hooks entirely.
func removeHookEntry(hooks map[string]any, event, command string) bool {
	list, _ := hooks[event].([]any)
	if list == nil {
		return false
	}
	changed := false
	kept := make([]any, 0, len(list))
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			kept = append(kept, e)
			continue
		}
		innerKept := make([]any, 0)
		for _, h := range asSlice(entry["hooks"]) {
			hm, ok := h.(map[string]any)
			if ok && hm["command"] == command {
				changed = true
				continue
			}
			innerKept = append(innerKept, h)
		}
		if len(innerKept) == 0 {
			changed = true
			continue // this matcher entry existed only for our hook
		}
		entry["hooks"] = innerKept
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	return changed
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func readJSONObject(path string) (map[string]any, error) {
	var doc map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func writeJSONObject(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

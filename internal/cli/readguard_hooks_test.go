package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run the installed hook scripts exactly as Claude Code does:
// hook JSON on stdin, cwd and CLAUDE_PROJECT_DIR at the project root.

const hookRel = "src/main/java/tools/jackson/databind/deser/bean/BeanDeserializer.java"

type hookEnv struct {
	t       *testing.T
	python  string
	project string
	abs     string
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	project := t.TempDir()
	if err := installReadGuard(project); err != nil {
		t.Fatalf("installReadGuard: %v", err)
	}
	abs := filepath.Join(project, filepath.FromSlash(hookRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(strings.Repeat("line\n", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	return &hookEnv{t: t, python: python, project: project, abs: abs}
}

func (h *hookEnv) run(script string, payload map[string]any) string {
	h.t.Helper()
	if _, ok := payload["session_id"]; !ok {
		payload["session_id"] = "sess-1"
	}
	in, _ := json.Marshal(payload)
	cmd := exec.Command(h.python, filepath.Join(h.project, ".claude", "hooks", script))
	cmd.Dir = h.project
	cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+h.project)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("%s: %v\n%s", script, err, stderr.String())
	}
	if stderr.Len() > 0 {
		h.t.Fatalf("%s wrote to stderr: %s", script, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func (h *hookEnv) deliver(file string, from, to int) {
	h.t.Helper()
	h.run("prism_read_tracker.py", map[string]any{
		"tool_name":  "mcp__prism__prism",
		"tool_input": map[string]any{"op": "read", "args": map[string]any{"file": file, "from": from, "to": to}},
	})
}

func (h *hookEnv) post(tool string, input map[string]any) {
	h.t.Helper()
	h.run("prism_read_tracker.py", map[string]any{"tool_name": tool, "tool_input": input})
}

// readDecision returns the deny reason, or "" when the Read is allowed.
func (h *hookEnv) readDecision(offset, limit int, sessionID string) string {
	h.t.Helper()
	p := map[string]any{"tool_name": "Read", "tool_input": map[string]any{
		"file_path": h.abs, "offset": offset, "limit": limit}}
	if sessionID != "" {
		p["session_id"] = sessionID
	}
	out := h.run("prism_read_guard.py", p)
	if out == "" {
		return ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		h.t.Fatalf("guard output is not JSON: %q", out)
	}
	if doc.HookSpecificOutput.PermissionDecision != "deny" {
		return ""
	}
	return doc.HookSpecificOutput.PermissionDecisionReason
}

func TestReadGuardHook_UnchangedDeliveredRangeIsDenied(t *testing.T) {
	h := newHookEnv(t)
	h.deliver(hookRel, 1406, 1480)
	reason := h.readDecision(1406, 75, "")
	if reason == "" {
		t.Fatal("Read of an unchanged, already-delivered range was allowed")
	}
	for _, want := range []string{"1406-1480", hookRel, "op=read", "has not changed"} {
		if !strings.Contains(reason, want) {
			t.Errorf("deny reason missing %q: %s", want, reason)
		}
	}
	if r := h.readDecision(1481, 100, ""); r != "" {
		t.Errorf("Read past the delivered range was denied: %s", r)
	}
	// The state lives under .claude/prism-read-guard and ignores itself.
	ign, err := os.ReadFile(filepath.Join(h.project, ".claude", "prism-read-guard", ".gitignore"))
	if err != nil || !strings.Contains(string(ign), "*") {
		t.Errorf("state dir has no self-ignoring .gitignore: %v %q", err, ign)
	}
	if _, err := os.Stat(filepath.Join(h.project, readGuardLegacyState)); !os.IsNotExist(err) {
		t.Error("tracker wrote the legacy root-level state file")
	}
}

func TestReadGuardHook_RangesOnlyReadIsTracked(t *testing.T) {
	h := newHookEnv(t)
	h.run("prism_read_tracker.py", map[string]any{
		"tool_name": "mcp__prism__prism",
		"tool_input": map[string]any{"op": "read", "args": map[string]any{
			"ranges": []any{map[string]any{"file": hookRel, "from": 1406, "to": 1480}}}},
	})
	if h.readDecision(1406, 75, "") == "" {
		t.Error("range delivered via args.ranges was not tracked")
	}
}

func TestReadGuardHook_EditToolsInvalidateTheirFile(t *testing.T) {
	for _, tc := range []struct{ tool, key string }{
		{"Edit", "file_path"}, {"Write", "file_path"}, {"MultiEdit", "file_path"}, {"NotebookEdit", "notebook_path"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			h := newHookEnv(t)
			h.deliver(hookRel, 1406, 1480)
			h.deliver("src/main/java/Other.java", 1, 50)
			// An edit of another file keeps this file's range.
			h.post(tc.tool, map[string]any{tc.key: filepath.Join(h.project, "src/main/java/Other.java")})
			if h.readDecision(1406, 75, "") == "" {
				t.Fatal("edit of another file dropped this file's range")
			}
			h.post(tc.tool, map[string]any{tc.key: h.abs})
			if r := h.readDecision(1406, 75, ""); r != "" {
				t.Errorf("Read after %s of the file was still denied: %s", tc.tool, r)
			}
		})
	}
}

// Windows hosts pass backslash paths; prism reports '/' paths. Before the
// fix no range ever matched there, so the guard never blocked anything.
func TestReadGuardHook_BackslashEditPathInvalidates(t *testing.T) {
	h := newHookEnv(t)
	h.deliver(hookRel, 1406, 1480)
	if h.readDecision(1406, 75, "") == "" {
		t.Fatal("delivered range not enforced")
	}
	h.post("Edit", map[string]any{"file_path": `C:\work\repo\` + strings.ReplaceAll(hookRel, "/", `\`)})
	if r := h.readDecision(1406, 75, ""); r != "" {
		t.Errorf("Edit given a backslash path kept the range: %s", r)
	}
}

func TestReadGuardHook_FileRewritingBashInvalidates(t *testing.T) {
	for _, cmd := range []string{
		"git stash && git stash pop", "git checkout -- .", "git -C . reset --hard",
		"git apply fix.patch", "git restore " + hookRel, "git switch main", "git pull",
		"patch -p1 < fix.diff", "sed -i '' 's/a/b/' x.java", "perl -pi -e 's/a/b/' x.java",
		"mv /tmp/Bean.java " + hookRel, "cp /tmp/Bean.java " + hookRel,
		"cat /tmp/new > " + hookRel, "echo x >> notes.txt", "gofmt -w .", "prettier --write src",
		"black .", "go generate ./...", "python3 - <<'EOF'\nopen('x.java','w').write(s)\nEOF",
	} {
		t.Run(cmd, func(t *testing.T) {
			h := newHookEnv(t)
			h.deliver(hookRel, 1406, 1480)
			h.post("Bash", map[string]any{"command": cmd})
			if r := h.readDecision(1406, 75, ""); r != "" {
				t.Errorf("Read after %q was still denied: %s", cmd, r)
			}
		})
	}
}

func TestReadGuardHook_ReadOnlyBashKeepsRanges(t *testing.T) {
	h := newHookEnv(t)
	h.deliver(hookRel, 1406, 1480)
	for _, cmd := range []string{
		"mvn -q -o test -Dtest=X 2>&1 | tail -50", "git diff", "git status", "git log -3",
		"grep -n foo " + hookRel, "go test ./... > /tmp/log.txt", "ls >/dev/null", "ls 2>&1",
	} {
		h.post("Bash", map[string]any{"command": cmd})
		if h.readDecision(1406, 75, "") == "" {
			t.Fatalf("read-only command %q dropped the range", cmd)
		}
	}
}

func TestReadGuardHook_OtherSessionsRangesIgnored(t *testing.T) {
	h := newHookEnv(t)
	h.deliver(hookRel, 1406, 1480) // sess-1
	if r := h.readDecision(1406, 75, "sess-2"); r != "" {
		t.Errorf("a range delivered in another session blocked this session's Read: %s", r)
	}
	if h.readDecision(1406, 75, "sess-1") == "" {
		t.Error("the delivering session's own range was not enforced")
	}
}

func TestReadGuardHook_FileChangedOnDiskIsNotBlocked(t *testing.T) {
	h := newHookEnv(t)
	h.deliver(hookRel, 1406, 1480)
	// Changed without any hooked tool (e.g. an IDE save or a command the
	// Bash heuristic misses): same size, newer mtime.
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(h.abs, later, later); err != nil {
		t.Fatal(err)
	}
	if r := h.readDecision(1406, 75, ""); r != "" {
		t.Errorf("Read of a file changed on disk since delivery was denied: %s", r)
	}
}

func TestReadGuardHook_StaleSessionFilesExpire(t *testing.T) {
	h := newHookEnv(t)
	stateDir := filepath.Join(h.project, ".claude", "prism-read-guard")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(stateDir, "session-yesterday.json")
	if err := os.WriteFile(old, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	h.deliver(hookRel, 1, 10)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a day-old session state file was not expired")
	}
}

// Re-running install over a v0.84 install (tracker on mcp__prism__prism
// only, root-level state file) adds the invalidation wiring and drops the
// legacy state, without duplicating the existing entries.
func TestReadGuard_InstallUpgradesOldWiring(t *testing.T) {
	project := t.TempDir()
	settingsPath := filepath.Join(project, ".claude", "settings.json")
	old := map[string]any{"hooks": map[string]any{
		"PostToolUse": []any{map[string]any{"matcher": "mcp__prism__prism",
			"hooks": []any{map[string]any{"type": "command", "command": readGuardTrackerCmd()}}}},
		"PreToolUse": []any{map[string]any{"matcher": "Read",
			"hooks": []any{map[string]any{"type": "command", "command": readGuardGuardCmd()}}}},
	}}
	if err := writeJSONObject(settingsPath, old); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(project, readGuardLegacyState)
	if err := os.WriteFile(legacy, []byte(`[{"file":"a.go","from":1,"to":9}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installReadGuard(project); err != nil {
		t.Fatal(err)
	}
	doc, err := readJSONObject(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	var matchers []string
	for _, e := range hooks["PostToolUse"].([]any) {
		matchers = append(matchers, e.(map[string]any)["matcher"].(string))
	}
	if strings.Join(matchers, ",") != "mcp__prism__prism,"+readGuardInvalidateMatcher {
		t.Errorf("PostToolUse matchers after upgrade = %v", matchers)
	}
	if n := len(hooks["PreToolUse"].([]any)); n != 1 {
		t.Errorf("PreToolUse entries after upgrade = %d, want 1", n)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy .prism-read-tracker.json survived the upgrade")
	}

	// Uninstall removes both tracker entries and the state dir.
	if err := os.MkdirAll(filepath.Join(project, ".claude", "prism-read-guard"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := uninstallReadGuard(project); err != nil {
		t.Fatal(err)
	}
	doc, _ = readJSONObject(settingsPath)
	if _, ok := doc["hooks"]; ok {
		t.Errorf("hooks left after uninstall: %v", doc["hooks"])
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "prism-read-guard")); !os.IsNotExist(err) {
		t.Error("state dir survived uninstall")
	}
}

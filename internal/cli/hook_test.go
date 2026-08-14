package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestHookDenyReason_GrepAndRgDenied covers the shapes the PreToolUse hook
// must catch: the built-in Grep tool, and grep/rg run via Bash — with and
// without a path prefix or a leading sudo, matching the harness's own
// denied_search_attempts pattern this was ported from.
func TestHookDenyReason_GrepAndRgDenied(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		toolInput map[string]any
	}{
		{"Grep tool", "Grep", map[string]any{"pattern": "foo"}},
		{"bare grep", "Bash", map[string]any{"command": "grep -rn foo ."}},
		{"bare rg", "Bash", map[string]any{"command": "rg foo ."}},
		{"path-prefixed grep", "Bash", map[string]any{"command": "/usr/bin/grep foo bar.go"}},
		{"sudo grep", "Bash", map[string]any{"command": "sudo grep foo bar.go"}},
		{"grep mid-pipeline", "Bash", map[string]any{"command": "cat bar.go | grep foo"}},
		{"rg after separator", "Bash", map[string]any{"command": "cd /tmp && rg foo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if reason := hookDenyReason(c.toolName, c.toolInput); reason == "" {
				t.Errorf("%s: expected a deny reason, got none", c.name)
			}
		})
	}
}

// TestHookDenyReason_NeverBlocksPython: the explicit product decision is
// that python is a real general-purpose tool the agent needs (tests, repro
// scripts) and is never denied by this hook, even when it happens to read or
// search a file — only grep/rg (the narrow, unambiguous pattern) is in scope.
func TestHookDenyReason_NeverBlocksPython(t *testing.T) {
	cases := []struct {
		name    string
		command string
	}{
		{"pytest run", "python3 -m pytest tests/test_foo.py -xvs"},
		{"repro script", "python3 /tmp/test_fix.py"},
		{"plain file read", `python3 -c "print(open('tests/test_foo.py').read())"`},
		{"grep as a substring of another word", "python3 -c 'x = 1' # not agrep"},
		{"a path merely containing grep", "python3 /tmp/swebench-wt-grepwarn/run.py"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if reason := hookDenyReason("Bash", map[string]any{"command": c.command}); reason != "" {
				t.Errorf("%s: python must never be denied, got reason: %q", c.name, reason)
			}
		})
	}
}

// TestHookDenyReason_OtherToolsAndCommandsPassThrough: anything that is not
// Grep or a grep/rg Bash invocation gets no decision — this hook must not
// become a second, broader gatekeeper.
func TestHookDenyReason_OtherToolsAndCommandsPassThrough(t *testing.T) {
	cases := []struct {
		toolName  string
		toolInput map[string]any
	}{
		{"Read", map[string]any{"file_path": "foo.go"}},
		{"Edit", map[string]any{"file_path": "foo.go"}},
		{"Bash", map[string]any{"command": "git status"}},
		{"Bash", map[string]any{"command": "ls -la"}},
		{"Bash", map[string]any{"command": ""}},
	}
	for _, c := range cases {
		if reason := hookDenyReason(c.toolName, c.toolInput); reason != "" {
			t.Errorf("tool=%s input=%v: expected no decision, got %q", c.toolName, c.toolInput, reason)
		}
	}
}

// TestCmdHook_DeniesGrepWithReason: end-to-end through the stdin/stdout
// protocol Claude Code actually uses.
func TestCmdHook_DeniesGrepWithReason(t *testing.T) {
	in := `{"tool_name":"Bash","tool_input":{"command":"grep -rn foo ."}}`
	out, code := runCmdHookForTest(t, in)
	if code != 0 {
		t.Fatalf("cmdHook exit = %d, want 0 (deny is expressed via JSON, not exit code)", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	hso, ok := decoded["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput: %s", out)
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision = %v, want deny", hso["permissionDecision"])
	}
	if reason, _ := hso["permissionDecisionReason"].(string); !strings.Contains(reason, "prism_search") {
		t.Errorf("reason should point at prism_search: %q", reason)
	}
}

// TestCmdHook_PythonProducesNoOutput: for a call the hook doesn't act on,
// nothing is written to stdout — Claude Code treats silence as "no
// decision," and any stray output there risks being misparsed as one.
func TestCmdHook_PythonProducesNoOutput(t *testing.T) {
	in := `{"tool_name":"Bash","tool_input":{"command":"python3 -c \"print(open('f').read())\""}}`
	out, code := runCmdHookForTest(t, in)
	if code != 0 {
		t.Fatalf("cmdHook exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no output for a non-denied call, got: %q", out)
	}
}

// TestCmdHook_MalformedInputFailsOpen: a parse error must never block the
// tool call it exists to police.
func TestCmdHook_MalformedInputFailsOpen(t *testing.T) {
	out, code := runCmdHookForTest(t, "not json")
	if code != 0 {
		t.Fatalf("cmdHook exit = %d, want 0 (fail open)", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no output on malformed input, got: %q", out)
	}
}

// runCmdHookForTest wires stdin/stdout to cmdHook and returns what it wrote.
func runCmdHookForTest(t *testing.T, stdin string) (string, int) {
	t.Helper()
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString(stdin); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	os.Stdin = inR

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outW

	code := cmdHook([]string{"pretooluse"})

	outW.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, outR); err != nil {
		t.Fatal(err)
	}
	return buf.String(), code
}

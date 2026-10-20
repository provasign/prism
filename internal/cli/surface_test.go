package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInterfaceRecommendationCountsProjectSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", "worker.py", "notes.md", "node_modules/dependency.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := interfaceRecommendation(dir); !strings.Contains(got, "2 source files") || !strings.Contains(got, "CLI for occasional") {
		t.Fatalf("small repo recommendation: %s", got)
	}
	for i := 0; i < largeRepoSourceFiles; i++ {
		name := filepath.Join(dir, fmt.Sprintf("src%03d.go", i))
		if err := os.WriteFile(name, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := interfaceRecommendation(dir); !strings.Contains(got, "at least 500 source files") || !strings.Contains(got, "MCP for repeated") {
		t.Fatalf("large repo recommendation: %s", got)
	}
}

func TestParseInterface(t *testing.T) {
	for in, want := range map[string]string{"mcp": "mcp", "CLI": "cli", " cli ": "cli"} {
		if got, err := parseInterface(in); err != nil || got != want {
			t.Errorf("parseInterface(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := parseInterface("both"); err == nil {
		t.Error("both must be rejected")
	}
}

func TestCLISteeringInstructionsForOS(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		wantNote bool
	}{
		{"darwin", true},
		{"linux", false},
		{"windows", false},
	} {
		got := cliSteeringInstructionsForOS(tc.goos)
		if strings.Contains(got, "On macOS, use your host's Edit or patch tool") != tc.wantNote {
			t.Errorf("%s: macOS edit guidance mismatch", tc.goos)
		}
		if strings.Count(got, "<!-- prism:end -->") != 1 {
			t.Errorf("%s: steering marker count = %d", tc.goos, strings.Count(got, "<!-- prism:end -->"))
		}
	}
}

func TestSetPrismYAMLInterfaceKeepsOtherLines(t *testing.T) {
	in := "version: 2\narch_deny: \"a -> b\"\n"
	out := setPrismYAMLInterface(in, "cli")
	if !strings.Contains(out, "arch_deny: \"a -> b\"") || !strings.Contains(out, `interface: "cli"`) {
		t.Fatalf("got %q", out)
	}
	again := setPrismYAMLInterface(out, "mcp")
	if strings.Count(again, "interface:") != 1 || !strings.Contains(again, `interface: "mcp"`) {
		t.Fatalf("rewrite duplicated or missed the key: %q", again)
	}
	p := filepath.Join(t.TempDir(), "prism.yaml")
	os.WriteFile(p, []byte(again), 0o644)
	if got := readRecordedInterface(p); got != "mcp" {
		t.Fatalf("recorded = %q", got)
	}
}

func TestInitCLIInterfaceRegistersNoMCP(t *testing.T) {
	dir := t.TempDir()
	// An earlier MCP init left a server entry; CLI init must remove only Prism's.
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"prism":{"command":"prism","args":["mcp"]},"other":{"command":"x"}}}`), 0o644)
	if rc := cmdInit([]string{"--harness", "claude", "--interface", "cli", dir}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	mcpJSON, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if strings.Contains(string(mcpJSON), `"prism"`) || !strings.Contains(string(mcpJSON), `"other"`) {
		t.Errorf(".mcp.json = %s", mcpJSON)
	}
	claude, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(claude), "no MCP server is registered") || strings.Contains(string(claude), "mcp__prism__prism") {
		t.Errorf("CLAUDE.md is not CLI steering:\n%s", claude)
	}
	if strings.Contains(string(claude), "On macOS, use your host's Edit or patch tool") != (runtime.GOOS == "darwin") {
		t.Errorf("CLAUDE.md has wrong OS guidance for %s:\n%s", runtime.GOOS, claude)
	}
	settings, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if !strings.Contains(string(settings), "Bash(prism:*)") {
		t.Errorf("settings = %s", settings)
	}
	if got := readRecordedInterface(filepath.Join(dir, "prism.yaml")); got != "cli" {
		t.Errorf("recorded interface = %q", got)
	}
	// --refresh reuses the recorded choice instead of reverting to MCP.
	if rc := cmdInit([]string{"--harness", "claude", "--refresh", dir}); rc != 0 {
		t.Fatalf("refresh rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		b, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
		if strings.Contains(string(b), `"prism"`) {
			t.Errorf("refresh re-registered MCP: %s", b)
		}
	}
}

func TestInitDefaultsToMCPWhenNotInteractive(t *testing.T) {
	dir := t.TempDir()
	if rc := cmdInit([]string{"--harness", "claude", dir}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if !strings.Contains(string(b), `"prism"`) {
		t.Errorf("MCP not registered by default: %s", b)
	}
	if got := readRecordedInterface(filepath.Join(dir, "prism.yaml")); got != "mcp" {
		t.Errorf("recorded interface = %q", got)
	}
}

// prism does not rewrite the agent's sed commands (2026-10-19): init in
// either mode installs no sed guard and removes one an older prism left, and
// so do --read-guard and --no-read-guard.
func TestNoSedGuardAndOldOneRemoved(t *testing.T) {
	leftover := func(dir string) {
		t.Helper()
		hooks := filepath.Join(dir, ".claude", "hooks")
		os.MkdirAll(hooks, 0o755)
		os.WriteFile(filepath.Join(hooks, "prism_sed_guard.py"), []byte("# old\n"), 0o644)
		os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"python3 \"$CLAUDE_PROJECT_DIR\"/.claude/hooks/prism_sed_guard.py || true"},{"type":"command","command":"./mine.sh"}]}]}}`), 0o644)
	}
	gone := func(dir, how string) {
		t.Helper()
		settings, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
		if sedGuardInstalled(dir) || strings.Contains(string(settings), "prism_sed_guard") {
			t.Errorf("%s left the sed guard: %s", how, settings)
		}
		if !strings.Contains(string(settings), "./mine.sh") {
			t.Errorf("%s dropped the user's own hook: %s", how, settings)
		}
	}
	for _, args := range [][]string{
		{"--harness", "claude", "--interface", "cli"},
		{"--harness", "claude", "--interface", "mcp"},
		{"--read-guard"},
		{"--no-read-guard"},
	} {
		dir := t.TempDir()
		leftover(dir)
		if rc := cmdInit(append(args, dir)); rc != 0 {
			t.Fatalf("%v rc=%d", args, rc)
		}
		gone(dir, strings.Join(args, " "))
	}
}

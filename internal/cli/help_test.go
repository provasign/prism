package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_HelpFlagDoesNotRunTheCommand pins the fix for the bug where
// `prism init --help` performed a full init — writing prism.yaml, nine
// steering files and every project MCP registration — instead of printing
// usage. The directory must come back empty.
func TestRun_HelpFlagDoesNotRunTheCommand(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	for _, args := range [][]string{
		{"init", "--help"},
		{"init", "-h"},
		{"install", "--help"},
	} {
		out := captureStdout(func() {
			if got := Run(args); got != 0 {
				t.Fatalf("Run(%v)=%d, want 0", args, got)
			}
		})
		if !strings.Contains(out, "prism init") {
			t.Fatalf("Run(%v) printed no init usage: %q", args, out)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		if len(entries) != 0 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("Run(%v) wrote %v — --help must not run the command", args, names)
		}
		if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err == nil {
			t.Fatalf("Run(%v) wrote prism.yaml", args)
		}
	}
}

// TestCommandHelp_PerCommandBlocks checks that each command's own usage comes
// back — not the whole help text — and that aliases resolve.
func TestCommandHelp_PerCommandBlocks(t *testing.T) {
	for cmd, want := range map[string]string{
		"search":        "prism search <keyword>",
		"query":         "prism query <task>",
		"change-impact": "prism change-impact <query>",
		"refs":          "prism references <name>",
		"arch-check":    "prism arch [dir]",
	} {
		got := commandHelp(cmd)
		if !strings.Contains(got, want) {
			t.Errorf("commandHelp(%q) missing %q: %q", cmd, want, got)
		}
		if len(got) >= len(helpText) {
			t.Errorf("commandHelp(%q) returned the full help text", cmd)
		}
	}
	// Unknown commands fall back to the full help rather than printing nothing.
	if commandHelp("no-such-command") != helpText {
		t.Error("commandHelp fallback is not the full help text")
	}
}

// TestHelpRequested_LiteralHelpIsStillAQueryTerm guards the narrow reading of
// the flag: `prism search help` must keep searching for the word.
func TestHelpRequested_LiteralHelpIsStillAQueryTerm(t *testing.T) {
	if helpRequested([]string{"help"}) {
		t.Error("bare `help` argument treated as a usage request")
	}
	if !helpRequested([]string{"foo", "--help"}) {
		t.Error("--help not recognized")
	}
	if !helpRequested([]string{"-h"}) {
		t.Error("-h not recognized")
	}
}

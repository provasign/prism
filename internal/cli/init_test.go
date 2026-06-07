package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdInit(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(dir)
	if rc := cmdInit([]string{dir}); rc != 0 {
		t.Errorf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

func TestCmdInit_RejectsRemovedFlags(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{dir, "--global"},
		{dir, "--mode", "mcp"},
	} {
		if rc := cmdInit(args); rc != 2 {
			t.Errorf("cmdInit(%v) rc %d, want 2", args, rc)
		}
	}
}

func TestCmdInit_BadDir(t *testing.T) {
	parent := t.TempDir()
	fileParent := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := cmdInit([]string{filepath.Join(fileParent, "child")}); rc != 1 {
		t.Fatalf("rc %d", rc)
	}
}

func TestWriteSteeringInstructions(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir)
	// Should have written at least one instruction file
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("no files")
	}
}

func TestWriteSteeringInstructions_AllTargets(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir)
	for _, want := range []string{
		"CLAUDE.md",
		"AGENTS.md",
		"GEMINI.md",
		".cursorrules",
		".windsurfrules",
		".clinerules",
		".github/copilot-instructions.md",
		".devin/instructions.md",
		".kiro/steering/prism.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestWriteSteeringInstructions_UpgradesStaleSection(t *testing.T) {
	dir := t.TempDir()
	// Write a file containing the old stale guidance.
	stale := "# Project config\n\n## Prism — context delivery (ALWAYS use these tools)\n\n### Rules\n1. Start every task with prism_query\n"
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSteeringInstructions(dir)
	raw, _ := os.ReadFile(path)
	s := string(raw)
	// Old guidance must be gone.
	if strings.Contains(s, "Start every task with prism_query") {
		t.Error("stale instructions not replaced")
	}
	// New guidance must be present.
	if !strings.Contains(s, "shell tools always win here") {
		t.Error("new instructions not written")
	}
	// Content before the Prism section must be preserved.
	if !strings.Contains(s, "# Project config") {
		t.Error("pre-existing content was lost")
	}
}

func TestInjectPrismSection(t *testing.T) {
	block := "\n## Prism — context delivery\nnew content\n"
	tests := []struct {
		name     string
		existing string
		wantPre  string // content that must appear before the block
	}{
		{
			name:     "replaces mid-file section",
			existing: "# Header\n\n## Prism — context delivery\nold\n",
			wantPre:  "# Header",
		},
		{
			name:     "replaces section-at-start",
			existing: "## Prism — context delivery\nold\n",
			wantPre:  "",
		},
		{
			name:     "appends when absent",
			existing: "# Header\n",
			wantPre:  "# Header",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := injectPrismSection(tc.existing, block)
			if !strings.Contains(got, "new content") {
				t.Errorf("new block missing: %q", got)
			}
			if strings.Contains(got, "old") {
				t.Errorf("old content not replaced: %q", got)
			}
			if tc.wantPre != "" && !strings.Contains(got, tc.wantPre) {
				t.Errorf("pre-existing content %q lost: %q", tc.wantPre, got)
			}
		})
	}
}

func TestCmdInit_InstallAlias(t *testing.T) {
	// `prism install` must behave identically to `prism init`.
	dir := t.TempDir()
	rc := Run([]string{"install", dir})
	if rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

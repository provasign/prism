package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// initRegisterMCPTools must create project-local config dirs when they are
// absent (e.g. first-time init in a fresh repo) and write a valid prism
// mcpServers entry into each one.
func TestInitRegisterMCPToolsCreatesProjectDirs(t *testing.T) {
	projectDir := t.TempDir()
	prismBin := "/fake/prism"

	written := initRegisterMCPTools(projectDir, prismBin, false)

	// All three project-local configs must be written.
	wantPaths := []string{
		filepath.Join(projectDir, ".mcp.json"),
		filepath.Join(projectDir, ".cursor", "mcp.json"),
		filepath.Join(projectDir, ".windsurf", "mcp.json"),
	}
	writtenSet := make(map[string]bool, len(written))
	for _, p := range written {
		writtenSet[p] = true
	}
	for _, want := range wantPaths {
		if !writtenSet[want] {
			t.Errorf("expected %s to be written; got %v", want, written)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("file not on disk: %s: %v", want, err)
		}
	}
}

// The written config must contain a valid mcpServers entry pointing to the
// given prism binary with "mcp" + projectDir as args.
func TestInitRegisterMCPToolsConfigContent(t *testing.T) {
	projectDir := t.TempDir()
	prismBin := "/usr/local/bin/prism"

	initRegisterMCPTools(projectDir, prismBin, false)

	cfgPath := filepath.Join(projectDir, ".mcp.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entry, ok := cfg.MCPServers["prism"]
	if !ok {
		t.Fatal("mcpServers.prism missing")
	}
	if entry.Command != prismBin {
		t.Errorf("command: got %q want %q", entry.Command, prismBin)
	}
	if len(entry.Args) < 2 || entry.Args[0] != "mcp" {
		t.Errorf("args: got %v, want [\"mcp\", ...]", entry.Args)
	}
	if entry.Args[1] != projectDir {
		t.Errorf("args[1]: got %q want %q", entry.Args[1], projectDir)
	}
}

// An existing config must be merged rather than overwritten — pre-existing
// mcpServers entries must survive alongside the new prism entry.
func TestInitRegisterMCPToolsMergesExistingConfig(t *testing.T) {
	projectDir := t.TempDir()
	existing := `{"mcpServers":{"other-tool":{"command":"/bin/other","args":[]}}}`
	if err := os.WriteFile(filepath.Join(projectDir, ".mcp.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	initRegisterMCPTools(projectDir, "/bin/prism", false)

	raw, _ := os.ReadFile(filepath.Join(projectDir, ".mcp.json"))
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal merged config: %v", err)
	}
	if _, ok := cfg.MCPServers["other-tool"]; !ok {
		t.Error("pre-existing other-tool entry was lost after merge")
	}
	if _, ok := cfg.MCPServers["prism"]; !ok {
		t.Error("prism entry not added during merge")
	}
}

// Global user-level config dirs that don't exist on this machine must be
// skipped; only project-local dirs should be created.
func TestInitRegisterMCPToolsSkipsAbsentGlobalDirs(t *testing.T) {
	projectDir := t.TempDir()
	written := initRegisterMCPTools(projectDir, "/bin/prism", false)

	home, _ := os.UserHomeDir()
	for _, p := range written {
		// No written path should be inside the user home (Zed, global Cursor, etc.)
		// unless that global tool is actually installed on this machine.
		if rel, err := filepath.Rel(home, p); err == nil {
			// If it's under home it should only be there because the dir existed.
			dir := filepath.Dir(p)
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("wrote %s whose parent %s doesn't exist", p, dir)
			}
			_ = rel
		}
	}
}

// pruneOldLedgers must remove files older than maxAge and leave recent ones.
func TestPruneOldLedgers(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	write := func(name string, modtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, modtime, modtime); err != nil {
			t.Fatal(err)
		}
	}

	write("old.json", now.Add(-40*24*time.Hour))   // 40 days ago — should be pruned
	write("recent.json", now.Add(-5*24*time.Hour))  // 5 days ago  — must survive
	write("fresh.json", now.Add(-1*time.Hour))      // 1 hour ago  — must survive
	write("unrelated.txt", now.Add(-50*24*time.Hour)) // wrong ext — must survive

	pruneOldLedgers(dir, 30*24*time.Hour)

	if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
		t.Error("old.json should have been pruned")
	}
	for _, keep := range []string{"recent.json", "fresh.json", "unrelated.txt"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should survive pruning: %v", keep, err)
		}
	}
}

// cmdFeedback validation: missing --rating or out-of-range must return exit 2.
func TestCmdFeedbackValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"missing rating", []string{"--tool", "prism_query"}, 2},
		{"rating too high", []string{"--tool", "prism_query", "--rating", "6"}, 2},
		{"rating negative", []string{"--tool", "prism_query", "--rating", "-1"}, 2},
		{"non-numeric rating", []string{"--tool", "prism_query", "--rating", "abc"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// cmdFeedback must return 2 (usage error) without reaching the
			// network — Grove is not running in unit test context.
			got := cmdFeedback(tc.args)
			if got != tc.want {
				t.Errorf("cmdFeedback(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPEntryAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	want := mcpEntry{Command: "/usr/local/bin/prism", Args: []string{"mcp", "/proj"}}

	// Missing file → false.
	missing := filepath.Join(dir, "nope.json")
	if mcpEntryAlreadyPresent(missing, "prism", want) {
		t.Error("missing file should not be present")
	}

	// Malformed JSON → false.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if mcpEntryAlreadyPresent(bad, "prism", want) {
		t.Error("malformed json should not be present")
	}

	write := func(name string, doc map[string]any) string {
		p := filepath.Join(dir, name)
		b, _ := json.Marshal(doc)
		os.WriteFile(p, b, 0o644)
		return p
	}

	// Exact match → true.
	match := write("match.json", map[string]any{
		"mcpServers": map[string]any{"prism": want},
	})
	if !mcpEntryAlreadyPresent(match, "prism", want) {
		t.Error("exact entry should be present")
	}

	// Different command → false.
	diffCmd := write("diffcmd.json", map[string]any{
		"mcpServers": map[string]any{"prism": mcpEntry{Command: "/other/prism", Args: want.Args}},
	})
	if mcpEntryAlreadyPresent(diffCmd, "prism", want) {
		t.Error("different command should not match")
	}

	// Different args → false.
	diffArgs := write("diffargs.json", map[string]any{
		"mcpServers": map[string]any{"prism": mcpEntry{Command: want.Command, Args: []string{"mcp", "/elsewhere"}}},
	})
	if mcpEntryAlreadyPresent(diffArgs, "prism", want) {
		t.Error("different args should not match")
	}

	// Server name absent → false.
	noName := write("noname.json", map[string]any{
		"mcpServers": map[string]any{"relay": want},
	})
	if mcpEntryAlreadyPresent(noName, "prism", want) {
		t.Error("absent server name should not match")
	}
}

func TestEnsureClaudeCodeApproval(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows — set both
	// so the test redirects to the temp dir on every platform.
	setHome(t, home)
	t.Setenv("USERPROFILE", home)

	settings := filepath.Join(home, ".claude", "settings.json")

	// First call creates the file and adds the server.
	ensureClaudeCodeApproval("prism")
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings not valid json: %v", err)
	}
	servers, _ := doc["enabledMcpjsonServers"].([]any)
	if len(servers) != 1 || servers[0] != "prism" {
		t.Fatalf("expected [prism], got %v", servers)
	}

	// Second call for same server is idempotent (no duplicate).
	ensureClaudeCodeApproval("prism")
	raw, _ = os.ReadFile(settings)
	json.Unmarshal(raw, &doc)
	servers, _ = doc["enabledMcpjsonServers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("idempotent call duplicated entry: %v", servers)
	}

	// A different server is appended alongside the first.
	ensureClaudeCodeApproval("relay")
	raw, _ = os.ReadFile(settings)
	json.Unmarshal(raw, &doc)
	servers, _ = doc["enabledMcpjsonServers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %v", servers)
	}
}

func TestRunDispatchSafeSubcommands(t *testing.T) {
	// Exercise the Run dispatcher for subcommands that don't touch Grove or the
	// network, from an isolated temp cwd. We only assert they return (the exit
	// code is incidental) — the point is dispatcher + command-body coverage.
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{
		{"config"},
		{"savings"},
		{"feedback"}, // missing args → returns non-zero, exercises the early path
	} {
		_ = Run(args)
	}
}

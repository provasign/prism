package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
)

func TestLegacyProjectWarning(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{name: "no config"},
		{
			name: "current v2",
			files: map[string]string{
				"prism.yaml":       "version: 2\nmcp_surface: \"compact\"\n",
				".mcp.json":        `{"mcpServers":{"prism":{"command":"prism","args":["mcp","--compact"]}}}`,
				"AGENTS.md":        "## Prism — context delivery\nCall `mcp__prism__prism`.\n<!-- prism:end -->\n",
				"unrelated-config": "prism",
			},
		},
		{
			name:  "old prism yaml",
			files: map[string]string{"prism.yaml": "profile: fast\nagent_mode: both\n"},
			want:  true,
		},
		{
			name:  "old steering",
			files: map[string]string{"CLAUDE.md": "## Prism — context delivery (ALWAYS use these tools)\nStart with prism_query.\n"},
			want:  true,
		},
		{
			name:  "old mcp arguments",
			files: map[string]string{".codex/config.toml": "[mcp_servers.prism]\ncommand = \"prism\"\nargs = [\"mcp\"]\n"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := legacyProjectWarning(root)
			if (got != "") != tt.want {
				t.Fatalf("legacyProjectWarning() = %q, want warning=%v", got, tt.want)
			}
		})
	}
}

func TestInitializeIncludesLegacyProjectWarning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "prism.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(config.Default(), root, nil)
	res, rpcErr := NewCompactServer(h).dispatch("initialize", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	instructions := res.(map[string]any)["instructions"].(string)
	if !strings.Contains(instructions, legacyProjectMigrationWarning) || !strings.Contains(instructions, "prism init") {
		t.Fatalf("initialize instructions missing migration warning: %q", instructions)
	}
}

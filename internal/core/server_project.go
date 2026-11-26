package core

import (
	"github.com/provasign/prism/internal/config"
	"os"
	"path/filepath"
	"strings"
)

const legacyProjectMigrationWarning = "This repository was initialized by an older Prism. Run `prism init` in the repository to migrate its project-local MCP configuration and steering."

var projectMCPConfigPaths = []string{
	".mcp.json",
	".codex/config.toml",
	".cursor/mcp.json",
	".windsurf/mcp.json",
	".vscode/mcp.json",
	".gemini/settings.json",
	"opencode.json",
}

var projectSteeringPaths = []string{
	"CLAUDE.md",
	"AGENTS.md",
	"GEMINI.md",
	".github/copilot-instructions.md",
	".cursorrules",
	".windsurfrules",
}

// LegacyProjectWarning performs a small, read-only scan of project metadata so
// a harness that reopens an old repository learns how to migrate before its
// first tool call. Explicit prism init remains the only migration writer.
func LegacyProjectWarning(root string) string {
	if root == "" {
		return ""
	}
	if legacyPrismYAML(root) || legacyProjectMCP(root) || legacyProjectSteering(root) {
		return legacyProjectMigrationWarning
	}
	return ""
}

func legacyPrismYAML(root string) bool {
	raw, ok := readProjectMetadata(filepath.Join(root, "prism.yaml"))
	if !ok {
		return false
	}
	c := config.Parse(raw)
	return c.Version != "2" || c.MCPSurface != "compact"
}

func legacyProjectMCP(root string) bool {
	for _, rel := range projectMCPConfigPaths {
		raw, ok := readProjectMetadata(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok {
			continue
		}
		text := strings.ToLower(string(raw))
		if !strings.Contains(text, "prism") {
			continue
		}
		if rel == ".windsurf/mcp.json" || !strings.Contains(text, "--compact") {
			return true
		}
	}
	return false
}

func legacyProjectSteering(root string) bool {
	for _, rel := range projectSteeringPaths {
		raw, ok := readProjectMetadata(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok {
			continue
		}
		text := string(raw)
		if !strings.Contains(text, "## Prism —") {
			continue
		}
		if rel == ".cursorrules" || rel == ".windsurfrules" ||
			!strings.Contains(text, "## Prism — context delivery") ||
			!strings.Contains(text, "mcp__prism__prism") ||
			!strings.Contains(text, "<!-- prism:end -->") {
			return true
		}
	}
	return false
}

// Project metadata should be tiny. Refuse oversized files rather than making
// the time-sensitive MCP handshake read arbitrary user-controlled content.
func readProjectMetadata(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	return raw, err == nil
}

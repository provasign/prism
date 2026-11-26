package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// harness is one coding tool `prism init` configures (ENG-02 step 4). Each
// tool's facts live in one value: how it is detected, which project files
// hold its configuration, and how Prism's MCP registration is written and
// removed. Steering files are shared between tools (AGENTS.md serves four)
// and stay in writeSteeringBlock.
type harness interface {
	// Status is the tool's id, labels, command and project config files.
	Status() harnessStatus
	// Detect reports whether the tool is installed or already configured in
	// projectDir.
	Detect(projectDir string) bool
	// Plan lists the project files Apply writes; none for a tool with no
	// project-local MCP config.
	Plan(projectDir string) []string
	// Apply writes Prism's MCP registration and returns the file written.
	Apply(r *mcpRegistration) (string, bool)
	// Remove deletes Prism's MCP registration (the CLI interface).
	Remove(projectDir string)
}

// mcpRegistration is what every harness's Apply needs from one init run.
type mcpRegistration struct {
	projectDir, prismBin                    string
	permissions, refresh, denyBuiltinSearch bool
	// entry is the IDE form, with ${workspaceFolder}; claudeEntry has no
	// directory because Claude Code and Gemini start servers at the root.
	entry, claudeEntry mcpEntry
	claudeSettings     string
}

// harnessRegistry is in registration order, which is the order init prints
// "registered with" lines; harnessStatuses is in supportedHarnesses order,
// which numbers the interactive prompt.
var harnessRegistry = []harness{
	jsonHarness{
		status: harnessStatus{id: "claude", label: "Claude Code", command: "claude", configPaths: []string{".mcp.json"}, projectMCP: true},
		name:   "Claude Code", rel: ".mcp.json", maps: [][]string{{"mcpServers"}},
		build: func(r *mcpRegistration, _ string) []byte { return buildMCPConfig("prism", r.claudeEntry) },
	},
	jsonHarness{
		status: harnessStatus{id: "cursor", label: "Cursor", command: "cursor", configPaths: []string{".cursor/mcp.json", ".cursorrules"}, projectMCP: true},
		name:   "Cursor", rel: filepath.Join(".cursor", "mcp.json"), maps: [][]string{{"mcpServers"}},
		build: func(r *mcpRegistration, _ string) []byte { return buildMCPConfig("prism", r.entry) },
	},
	jsonHarness{
		status: harnessStatus{id: "vscode", label: "VS Code", command: "code", configPaths: []string{".vscode/mcp.json"}, projectMCP: true},
		name:   "VS Code", rel: filepath.Join(".vscode", "mcp.json"), maps: [][]string{{"servers"}},
		build: func(r *mcpRegistration, _ string) []byte { return buildVSCodeConfig(r.prismBin, workspaceFolderVar) },
	},
	jsonHarness{
		status: harnessStatus{id: "gemini", label: "Gemini CLI", command: "gemini", configPaths: []string{".gemini/settings.json"}, projectMCP: true},
		name:   "Gemini CLI", rel: filepath.Join(".gemini", "settings.json"), maps: [][]string{{"mcpServers"}},
		build: func(r *mcpRegistration, _ string) []byte { return buildMCPConfig("prism", r.claudeEntry) },
	},
	jsonHarness{
		status: harnessStatus{id: "opencode", label: "OpenCode", command: "opencode", configPaths: []string{"opencode.json"}, projectMCP: true},
		name:   "opencode", rel: "opencode.json", maps: [][]string{{"mcp"}, {"mcp", "servers"}},
		removeKeys: []string{"mcp"},
		build:      func(r *mcpRegistration, path string) []byte { return buildOpencodeConfigForPath(r.prismBin, path) },
	},
	codexHarness{},
	windsurfHarness{},
}

var harnessStatuses = func() []harnessStatus {
	var out []harnessStatus
	for _, id := range supportedHarnesses {
		out = append(out, harnessByID(id).Status())
	}
	return out
}()

func harnessByID(id string) harness {
	for _, h := range harnessRegistry {
		if h.Status().id == id {
			return h
		}
	}
	panic("unknown harness " + id)
}

// detectHarness is the shared Detect: the command is on PATH, or one of the
// tool's config files exists in the project.
func detectHarness(projectDir string, s harnessStatus) bool {
	if _, err := exec.LookPath(s.command); err == nil {
		return true
	}
	for _, rel := range s.configPaths {
		if fileExists(filepath.Join(projectDir, filepath.FromSlash(rel))) {
			return true
		}
	}
	return false
}

// jsonHarness registers Prism in a JSON config file under one or more map
// keys (mcpServers, servers, mcp).
type jsonHarness struct {
	status harnessStatus
	name   string // as init's "registered with" lines name it
	rel    string
	maps   [][]string
	// removeKeys is the map Remove deletes from; maps[0] when nil.
	removeKeys []string
	build      func(r *mcpRegistration, path string) []byte
}

func (h jsonHarness) Status() harnessStatus         { return h.status }
func (h jsonHarness) Detect(projectDir string) bool { return detectHarness(projectDir, h.status) }
func (h jsonHarness) Plan(projectDir string) []string {
	return []string{filepath.Join(projectDir, h.rel)}
}

func (h jsonHarness) Apply(r *mcpRegistration) (string, bool) {
	p := filepath.Join(r.projectDir, h.rel)
	// --refresh rewrites only what a previous install configured: if the
	// config file does not exist yet, this tool was never set up and must
	// not be added now.
	if r.refresh {
		if _, err := os.Stat(p); err != nil {
			return "", false
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create %s config dir: %v\n", h.name, err)
		return "", false
	}
	isClaudeCode := h.status.id == "claude"
	// Skip writing .mcp.json if the prism entry is already correct.
	// Writing the file resets Claude Code's MCP approval state, which
	// forces the user to re-approve on every `prism init` run.
	if isClaudeCode && mcpEntryAlreadyPresent(p, "prism", r.claudeEntry) {
		fmt.Printf("already registered with %s: %s\n", h.name, p)
		ensureClaudeCodeApproval(r.claudeSettings, "prism", r.permissions, r.denyBuiltinSearch)
		return p, true
	}
	if old := existingPrismCommand(p, h.maps); old != "" && old != r.prismBin && filepath.IsAbs(old) {
		fmt.Printf("replaced the machine-specific command %s with %q in %s\n", old, r.prismBin, p)
	}
	// Merge rather than overwrite existing configs.
	merged, err := mergeOrCreate(p, h.build(r, p), h.maps...)
	if err != nil {
		initWriteFailed(p, err)
		return "", false
	}
	if err := os.WriteFile(p, merged, 0o644); err != nil {
		initWriteFailed(p, err)
		return "", false
	}
	fmt.Printf("registered with %s: %s\n", h.name, p)
	if isClaudeCode {
		ensureClaudeCodeApproval(r.claudeSettings, "prism", r.permissions, r.denyBuiltinSearch)
	}
	return p, true
}

func (h jsonHarness) Remove(projectDir string) {
	keys := h.removeKeys
	if keys == nil {
		keys = h.maps[0]
	}
	p := filepath.Join(projectDir, h.rel)
	if removed, err := removeJSONMapEntry(p, keys, "prism"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove Prism from %s config (%s): %v\n", h.name, p, err)
	} else if removed {
		fmt.Printf("removed Prism MCP registration (CLI interface): %s\n", p)
	}
}

// codexHarness registers Prism in .codex/config.toml.
type codexHarness struct{}

var codexStatus = harnessStatus{id: "codex", label: "Codex CLI", command: "codex", configPaths: []string{".codex/config.toml"}, projectMCP: true}

func (codexHarness) Status() harnessStatus         { return codexStatus }
func (codexHarness) Detect(projectDir string) bool { return detectHarness(projectDir, codexStatus) }
func (codexHarness) Plan(projectDir string) []string {
	return []string{filepath.Join(projectDir, ".codex", "config.toml")}
}

func (codexHarness) Apply(r *mcpRegistration) (string, bool) {
	p := filepath.Join(r.projectDir, ".codex", "config.toml")
	if r.refresh && !fileExists(p) {
		return "", false
	}
	if err := writePrismCodexConfig(p, r.prismBin, []string{"mcp", "--compact"}); err != nil {
		initWriteFailed(p, err)
		return "", false
	}
	fmt.Printf("registered with Codex CLI: %s\n", p)
	return p, true
}

func (codexHarness) Remove(projectDir string) {
	p := filepath.Join(projectDir, ".codex", "config.toml")
	if removed, err := removePrismCodexConfig(p); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove Prism from Codex config (%s): %v\n", p, err)
	} else if removed {
		fmt.Printf("removed Prism MCP registration (CLI interface): %s\n", p)
	}
}

// windsurfHarness gets steering only: Windsurf documents no project-local
// MCP config, and Prism does not write user-global settings.
type windsurfHarness struct{}

var windsurfStatus = harnessStatus{id: "windsurf", label: "Windsurf", command: "windsurf", configPaths: []string{".windsurf/mcp.json", ".windsurfrules"}, projectMCP: false}

func (windsurfHarness) Status() harnessStatus { return windsurfStatus }
func (windsurfHarness) Detect(projectDir string) bool {
	return detectHarness(projectDir, windsurfStatus)
}
func (windsurfHarness) Plan(string) []string { return nil }

// Apply migrates a registration an older Prism wrote into .windsurf/mcp.json
// away, and says why nothing is written.
func (windsurfHarness) Apply(r *mcpRegistration) (string, bool) {
	legacyPath := filepath.Join(r.projectDir, ".windsurf", "mcp.json")
	removed, err := removeJSONMapEntry(legacyPath, []string{"mcpServers"}, "prism")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not migrate legacy Windsurf config %s: %v\n", legacyPath, err)
	} else if removed {
		fmt.Printf("removed unsupported project-local Windsurf Prism registration: %s\n", legacyPath)
	}
	fmt.Fprintln(os.Stderr, "warning: Windsurf does not document a project-local MCP config; Prism wrote project steering only and did not modify user-global Windsurf settings")
	return "", false
}

// Remove has nothing to remove: the CLI interface path never registered
// Windsurf.
func (windsurfHarness) Remove(string) {}

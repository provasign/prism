// Package cli implements the Prism command tree (flat dispatch, no cobra
// dependency — keeps Prism a true single binary with zero runtime deps).
package cli

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/httpapi"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/version"
)

const helpText = `prism - token-optimized context delivery for AI agents (requires Grove)

Usage:
  prism init [--global] [dir]     Write prism.yaml + register MCP with detected AI tools
                                  --global writes to user-level config (~/.claude, ~/.cursor, etc.)
  prism install [--global] [dir]  Alias for 'prism init'
  prism index [dir]               Index codebase via Grove (delta-aware)
  prism status [dir]              Show graph stats from Grove
  prism query <task> [dir]        Find ranked context for a task
  prism read <file> [dir]         Read file with compression
  prism search <keyword> [dir]    Search symbols by keyword
  prism lookup <name> [dir]       Show full source for a symbol
  prism compact [dir]             Compress conversation JSON from stdin
	prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
																	Submit quality feedback for a Prism result
  prism serve [--port 8888] [dir] Start MCP+HTTP server
  prism mcp [dir]                 Start MCP server on stdio
  prism savings [dir]             Show session savings dashboard
  prism config [dir]              Show resolved configuration
  prism version                   Print version

Supported AI tools (auto-detected by prism init):
  Claude Code  →  .mcp.json + CLAUDE.md
  Cursor       →  .cursor/mcp.json + .cursorrules + AGENTS.md
  Windsurf     →  .windsurf/mcp.json + .windsurfrules
  Zed          →  ~/.config/zed/settings.json (context_servers)
  VS Code      →  .vscode/mcp.json + .github/copilot-instructions.md
  Codex / generic agents → AGENTS.md
  Gemini CLI   →  GEMINI.md
`

// Run is the CLI entry point. Returns the exit code.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Print(helpText)
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(helpText)
		return 0
	case "version":
		fmt.Println("prism " + version.Version)
		return 0
	case "init", "install":
		return cmdInit(rest)
	case "index":
		return cmdIndex(rest)
	case "status":
		return cmdStatus(rest)
	case "query":
		return cmdQuery(rest)
	case "read":
		return cmdRead(rest)
	case "search":
		return cmdSearch(rest)
	case "lookup":
		return cmdLookup(rest)
	case "compact":
		return cmdCompact(rest)
	case "feedback":
		return cmdFeedback(rest)
	case "serve":
		return cmdServe(rest)
	case "mcp":
		return cmdMCP(rest)
	case "savings":
		return cmdSavings(rest)
	case "config":
		return cmdConfig(rest)
	}
	fmt.Fprintln(os.Stderr, "unknown command:", cmd)
	fmt.Print(helpText)
	return 2
}

// --- per-command implementations ---------------------------------------

func cmdInit(args []string) int {
	// Flags: --global (write to ~/.config/... instead of project dir)
	global := false
	filtered := args[:0]
	for _, a := range args {
		if a == "--global" {
			global = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	dir := dirArg(args, 0, ".")
	abs, _ := filepath.Abs(dir)
	cfg := config.Default()

	// 1. Write prism.yaml into the project. Grove is embedded in-process now,
	// so the file no longer needs grove_url / grove_binary.
	yaml := fmt.Sprintf(`version: 1
# model: auto  # Prism detects the active model from the MCP initialize handshake.
#               # Override here only if auto-detection fails, e.g.:
#               # model: "claude-sonnet-4-6"
profile: "%s"
`, cfg.Profile)
	prismYAML := filepath.Join(abs, "prism.yaml")
	if err := os.WriteFile(prismYAML, []byte(yaml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("wrote", prismYAML)

	// 2. Detect the prism binary path for use in MCP configs.
	prismBin := detectSelfPath()

	// 3. Write steering instructions so agents prefer Prism tools.
	writeSteeringInstructions(abs)

	// 4. Register with every detected AI coding tool.
	registered := initRegisterMCPTools(abs, prismBin, global)
	if len(registered) == 0 {
		fmt.Println("tip: add prism to your AI tool's MCP config (see README)")
	}
	return 0
}

// steeringInstructions is injected into per-tool instruction files so the
// agent knows to use Prism tools correctly.
const steeringInstructions = `
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle. Its value is surfacing callers, callees, and test
contracts the agent would not find by grep+read alone.

### Decision tree

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | grep — not Prism |
| Callers/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") |
| Find docs about a topic | prism_query(task=..., include=["docs"]) — filenames only |
| Blast radius of a change | prism_query(terms=[...], graph_depth=3) |

### Canonical workflow

    grep <terms>                         <- locate anchor first; grep always wins here
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph","tests"],
           graph_depth=2
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

### prism_query parameters

| Parameter | Default | Purpose |
|---|---|---|
| task | required | What you are doing |
| terms | — | Grep terms — same precision as grep, plus graph expansion |
| include | ["graph","tests"] | "graph" (callers/callees), "tests", "docs" (filenames only), "coverage_gaps" (untested symbols — use when writing/fixing code) |
| graph_depth | 2 | BFS hops: 1 = immediate callers, 3+ = blast radius |
| budget | 8000 | Token ceiling. Increase for large refactors. |

### Other tools

| Tool | When |
|---|---|
| prism_index | Once at session start — not on every step |
| prism_compact | When context window is near capacity |
| prism_search | Find a symbol by name when file is unknown (not a grep replacement) |
| prism_evidence | Sub-agent to parent: typed citations instead of prose |

### Do NOT

- Do NOT call prism_query before grep — grep finds the anchor, prism expands from it
- Do NOT use prism_search as a grep replacement — it searches symbol names only, not source text
- Do NOT use prism_read for a single function — use prism_lookup instead
- Do NOT re-run prism_index on every step — delta indexing is automatic
`

// writeSteeringInstructions writes per-tool instruction files into the project
// so agents know how to use Prism tools correctly.
// On re-init it replaces a stale Prism section rather than skipping.
func writeSteeringInstructions(projectDir string) {
	type instrFile struct {
		name    string // description for log
		relPath string // path relative to projectDir
	}
	targets := []instrFile{
		// File-based agent instruction formats
		{name: "Claude Code", relPath: "CLAUDE.md"},
		{name: "Cursor", relPath: ".cursorrules"},
		{name: "Windsurf", relPath: ".windsurfrules"},
		{name: "GitHub Copilot", relPath: ".github/copilot-instructions.md"},
		// AGENTS.md: cross-vendor spec (OpenAI Codex, etc.)
		{name: "AGENTS.md", relPath: "AGENTS.md"},
		// Gemini CLI / Gemini Code Assist
		{name: "Gemini CLI", relPath: "GEMINI.md"},
		// Cline (VS Code extension)
		{name: "Cline", relPath: ".clinerules"},
		// Devin
		{name: "Devin", relPath: ".devin/instructions.md"},
		// Kiro (Amazon): each file in .kiro/steering/ is a topic steering doc
		{name: "Kiro", relPath: ".kiro/steering/prism.md"},
	}

	for _, t := range targets {
		path := filepath.Join(projectDir, t.relPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create directory for %s instructions: %v\n", t.name, err)
			continue
		}

		var content string
		if existing, err := os.ReadFile(path); err == nil {
			// File exists — replace stale Prism section or append if absent.
			content = injectPrismSection(string(existing), steeringInstructions)
		} else {
			content = steeringInstructions
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s instructions: %v\n", t.name, err)
			continue
		}
		fmt.Printf("wrote steering instructions: %s\n", path)
	}
}

// injectPrismSection replaces the existing Prism steering section in content
// with block, or appends block if no section is present. This allows re-init
// to upgrade stale instructions rather than leaving old guidance in place.
func injectPrismSection(content, block string) string {
	const marker = "## Prism — context delivery"
	if idx := strings.Index(content, "\n"+marker); idx >= 0 {
		return content[:idx] + block
	}
	if strings.HasPrefix(content, marker) {
		return block
	}
	return strings.TrimRight(content, "\n") + block
}

// detectSelfPath returns the absolute path to the running prism binary, or
// falls back to "prism" (assumes it's on PATH).
func detectSelfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "prism"
	}
	return exe
}

// detectGrovePath returns the absolute path to the grove binary. It searches:
// 1. Same directory as the running prism binary (most common install layout).
// 2. ~/bin/grove
// 3. /usr/local/bin/grove
// 4. $PATH via exec.LookPath
// Falls back to "grove" if none found, which will work only if grove is on PATH.
func detectGrovePath() string {
	// Same directory as prism — handles ~/bin layout where all binaries live together.
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "grove")
		if isExecutable(candidate) {
			return candidate
		}
	}
	// ~/bin/grove
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "bin", "grove")
		if isExecutable(candidate) {
			return candidate
		}
	}
	// /usr/local/bin/grove
	if isExecutable("/usr/local/bin/grove") {
		return "/usr/local/bin/grove"
	}
	// $PATH
	if p, err := exec.LookPath("grove"); err == nil {
		return p
	}
	return "grove"
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// mcpEntry is the JSON structure every MCP-compatible tool expects.
type mcpEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// initRegisterMCPTools writes MCP server config for every detected tool.
// It returns the list of files written.
func initRegisterMCPTools(projectDir, prismBin string, global bool) []string {
	var written []string

	entry := mcpEntry{Command: prismBin, Args: []string{"mcp", projectDir}}

	// Wrap in the per-tool envelope format and write.
	type writer struct {
		name  string
		path  func() string // path to config file
		build func() []byte // full config content
	}

	home, _ := os.UserHomeDir()

	writers := []writer{
		{
			// Claude Code: .mcp.json at project root (project) or ~/.claude.json (global).
			// Claude Code reads project MCP servers from .mcp.json in the repo root;
			// global user-level servers live in ~/.claude.json under "mcpServers".
			name: "Claude Code",
			path: func() string {
				if global {
					return filepath.Join(home, ".claude.json")
				}
				return filepath.Join(projectDir, ".mcp.json")
			},
			build: func() []byte {
				return buildMCPConfig("prism", entry)
			},
		},
		{
			// Cursor: .cursor/mcp.json (project) or ~/.cursor/mcp.json (global)
			name: "Cursor",
			path: func() string {
				if global {
					return filepath.Join(home, ".cursor", "mcp.json")
				}
				return filepath.Join(projectDir, ".cursor", "mcp.json")
			},
			build: func() []byte {
				return buildMCPConfig("prism", entry)
			},
		},
		{
			// Windsurf: .windsurf/mcp.json (project) or ~/.windsurf/mcp.json (global)
			name: "Windsurf",
			path: func() string {
				if global {
					return filepath.Join(home, ".windsurf", "mcp.json")
				}
				return filepath.Join(projectDir, ".windsurf", "mcp.json")
			},
			build: func() []byte {
				return buildMCPConfig("prism", entry)
			},
		},
		{
			// Zed: ~/.config/zed/settings.json — patch "context_servers" key
			name: "Zed",
			path: func() string {
				return filepath.Join(home, ".config", "zed", "settings.json")
			},
			build: func() []byte {
				return buildZedConfig(prismBin, projectDir)
			},
		},
		{
			// VS Code (GitHub Copilot Chat / Continue): .vscode/mcp.json
			// VS Code natively reads workspace-scoped MCP servers from this file.
			name: "VS Code",
			path: func() string {
				return filepath.Join(projectDir, ".vscode", "mcp.json")
			},
			build: func() []byte {
				return buildVSCodeConfig(prismBin, projectDir)
			},
		},
	}

	for _, w := range writers {
		p := w.path()
		// For project-local configs (.claude, .cursor, .windsurf): create the
		// parent directory so first-time init works without a pre-existing tool
		// installation. For global user configs (Zed ~/.config/zed): only write
		// if the directory already exists (i.e. the tool is installed).
		parent := filepath.Dir(p)
		isGlobalUserDir := strings.HasPrefix(parent, home)
		if _, err := os.Stat(parent); err != nil {
			if !global && !isGlobalUserDir {
				// Project-local: create it.
				if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not create %s config dir: %v\n", w.name, mkErr)
					continue
				}
			} else {
				continue // global user tool not installed — skip
			}
		}
		// Skip writing .mcp.json if the prism entry is already correct.
		// Writing the file resets Claude Code's MCP approval state, which
		// forces the user to re-approve on every `prism init` run.
		if filepath.Base(p) == ".mcp.json" && mcpEntryAlreadyPresent(p, "prism", entry) {
			fmt.Printf("already registered with %s: %s\n", w.name, p)
			written = append(written, p)
			ensureClaudeCodeApproval("prism")
			continue
		}
		content := w.build()
		// Merge rather than overwrite existing configs.
		merged := mergeOrCreate(p, content)
		if err := os.WriteFile(p, merged, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s config (%s): %v\n", w.name, p, err)
			continue
		}
		fmt.Printf("registered with %s: %s\n", w.name, p)
		written = append(written, p)
		if filepath.Base(p) == ".mcp.json" {
			ensureClaudeCodeApproval("prism")
		}
	}

	// Codex CLI (~/.codex/config.toml) uses TOML, not JSON.
	// Only write when ~/.codex/ already exists (i.e. Codex CLI is installed).
	codexPath := filepath.Join(home, ".codex", "config.toml")
	if _, err := os.Stat(filepath.Dir(codexPath)); err == nil {
		codexArgs := []string{"mcp", projectDir}
		if err := writePrismCodexConfig(codexPath, prismBin, codexArgs); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write Codex CLI config: %v\n", err)
		} else {
			fmt.Printf("registered with Codex CLI: %s\n", codexPath)
			written = append(written, codexPath)
		}
	}

	return written
}

// buildMCPConfig returns {"mcpServers":{"<name>": entry}} JSON.
func buildMCPConfig(name string, e mcpEntry) []byte {
	type envelope struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
	}
	b, _ := json.MarshalIndent(envelope{MCPServers: map[string]mcpEntry{name: e}}, "", "  ")
	return b
}

// mcpEntryAlreadyPresent returns true if the JSON file at path already
// contains an mcpServers entry for name with the exact same command and args.
// This avoids rewriting .mcp.json on repeated `prism init` runs, which would
// reset Claude Code's MCP approval state on every run.
func mcpEntryAlreadyPresent(path string, name string, want mcpEntry) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	got, ok := doc.MCPServers[name]
	if !ok {
		return false
	}
	if got.Command != want.Command || len(got.Args) != len(want.Args) {
		return false
	}
	for i, a := range want.Args {
		if got.Args[i] != a {
			return false
		}
	}
	return true
}

// buildZedConfig returns the minimal Zed context_servers stanza.
func buildZedConfig(prismBin, projectDir string) []byte {
	type zedServer struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type zedSettings struct {
		ContextServers map[string]zedServer `json:"context_servers"`
	}
	s := zedSettings{ContextServers: map[string]zedServer{
		"prism": {Command: prismBin, Args: []string{"mcp", projectDir}},
	}}
	b, _ := json.MarshalIndent(s, "", "  ")
	return b
}

// buildVSCodeConfig returns the .vscode/mcp.json stanza VS Code's native
// MCP host expects. Schema: {"servers": {"<name>": {"type":"stdio","command":..,"args":..}}}.
func buildVSCodeConfig(prismBin, projectDir string) []byte {
	type vscodeServer struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type vscodeMCP struct {
		Servers map[string]vscodeServer `json:"servers"`
	}
	s := vscodeMCP{Servers: map[string]vscodeServer{
		"prism": {Type: "stdio", Command: prismBin, Args: []string{"mcp", projectDir}},
	}}
	b, _ := json.MarshalIndent(s, "", "  ")
	return b
}

// writePrismCodexConfig writes a prism [mcp_servers.prism] entry to Codex CLI's
// config.toml (~/.codex/config.toml). The file is created if absent.
// Existing legacy and map-style prism entries are removed idempotently.
func writePrismCodexConfig(path, prismBin string, args []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir codex config dir: %w", err)
	}
	var lines []string
	if raw, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines = stripPrismTOMLBlock(lines, "mcp_servers", "prism")
	lines = stripPrismNamedTable(lines, "mcp_servers", "prism")
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines,
		"[mcp_servers.prism]",
		`type = "stdio"`,
		fmt.Sprintf("command = %q", prismBin),
		prismTOMLStringArray("args", args),
	)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// stripPrismTOMLBlock removes all [[section]] array-of-tables blocks whose
// "name" field equals targetName, preserving everything else.
func stripPrismTOMLBlock(lines []string, section, targetName string) []string {
	header := "[[" + section + "]]"
	nameKV := `name = "` + targetName + `"`
	var out []string
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != header {
			out = append(out, lines[i])
			i++
			continue
		}
		start := i
		i++
		isMatch := false
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			if strings.TrimSpace(lines[i]) == nameKV {
				isMatch = true
			}
			i++
		}
		if !isMatch {
			out = append(out, lines[start:i]...)
		}
	}
	return out
}

// stripPrismNamedTable removes a [section.target] table and its body.
func stripPrismNamedTable(lines []string, section, targetName string) []string {
	header := "[" + section + "." + targetName + "]"
	var out []string
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != header {
			out = append(out, lines[i])
			i++
			continue
		}
		i++
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			i++
		}
	}
	return out
}

// prismTOMLStringArray formats a TOML key = ["v1", "v2"] line.
func prismTOMLStringArray(key string, vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return key + " = [" + strings.Join(quoted, ", ") + "]"
}

// mergeOrCreate reads the existing JSON at path and deep-merges content into
// it. If the file does not exist, content is returned verbatim.
// Only keys from content are upserted; existing unrelated keys are preserved.
func mergeOrCreate(path string, content []byte) []byte {
	existing, err := os.ReadFile(path)
	if err != nil {
		return content // file does not exist yet
	}
	var base, overlay map[string]json.RawMessage
	if err := json.Unmarshal(existing, &base); err != nil {
		return content // existing file is not valid JSON — overwrite
	}
	if err := json.Unmarshal(content, &overlay); err != nil {
		return content
	}
	if base == nil {
		base = make(map[string]json.RawMessage)
	}
	for k, v := range overlay {
		// For "mcpServers" / "context_servers": merge nested map rather than replace.
		if existing, ok := base[k]; ok {
			var baseNested, newNested map[string]json.RawMessage
			if json.Unmarshal(existing, &baseNested) == nil && json.Unmarshal(v, &newNested) == nil {
				for nk, nv := range newNested {
					baseNested[nk] = nv
				}
				merged, _ := json.Marshal(baseNested)
				base[k] = merged
				continue
			}
		}
		base[k] = v
	}
	out, _ := json.MarshalIndent(base, "", "  ")
	return out
}

// ensureClaudeCodeApproval adds serverName to enabledMcpjsonServers in
// ~/.claude/settings.json so Claude Code trusts the server without requiring
// interactive re-approval after every `prism init` run.
func ensureClaudeCodeApproval(serverName string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".claude", "settings.json")
	var doc map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &doc) //nolint:errcheck
	}
	if doc == nil {
		doc = map[string]any{}
	}
	var servers []any
	if s, ok := doc["enabledMcpjsonServers"].([]any); ok {
		servers = s
	}
	for _, s := range servers {
		if s == serverName {
			return // already approved
		}
	}
	servers = append(servers, serverName)
	doc["enabledMcpjsonServers"] = servers
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		return
	}
	fmt.Printf("approved %s in Claude Code MCP settings\n", serverName)
}

func cmdIndex(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, client, err := newClient(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	_ = cfg
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := client.Index(ctx, mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "index:", err)
		return 1
	}
	printJSON(res)
	return 0
}

func cmdStatus(args []string) int {
	dir := dirArg(args, 0, ".")
	_, client, err := newClient(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	res, err := client.Status(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	printJSON(res)
	return 0
}

func cmdQuery(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism query <task> [dir]")
		return 2
	}
	task := args[0]
	dir := "."
	profile := ""
	limit := 50
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--profile":
			if i+1 < len(args) {
				profile = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					limit = n
				}
				i++
			}
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	invokeArgs := map[string]any{"task": task, "limit": limit}
	if profile != "" {
		invokeArgs["profile"] = profile
	}
	out, err := invokeWithPersistentLedger(dir, "prism_query", invokeArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdRead(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism read <file> [dir]")
		return 2
	}
	file := args[0]
	dir := dirArg(args, 1, ".")
	out, err := invokeWithPersistentLedger(dir, "prism_read", map[string]any{"file": file})
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdSearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism search <keyword> [dir]")
		return 2
	}
	query := args[0]
	limit := 25
	dir := "."
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					limit = n
				}
				i++
			}
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_search", map[string]any{"query": query, "limit": limit})
	if err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdLookup(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name> [dir]")
		return 2
	}
	dir := dirArg(args, 1, ".")
	out, err := invokeWithPersistentLedger(dir, "prism_lookup", map[string]any{"name": args[0]})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdCompact(args []string) int {
	dir := dirArg(args, 0, ".")
	var turns []map[string]any
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&turns); err != nil {
		fmt.Fprintln(os.Stderr, "compact: stdin must be a JSON array of turns:", err)
		return 2
	}
	out, err := invokeWithPersistentLedger(dir, "prism_compact", map[string]any{"turns": turns})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compact:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdFeedback(args []string) int {
	tool := ""
	queryID := ""
	notes := ""
	rating := -1
	dir := "."

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--tool":
			if i+1 < len(args) {
				tool = args[i+1]
				i++
			}
		case "--query-id":
			if i+1 < len(args) {
				queryID = args[i+1]
				i++
			}
		case "--rating":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					rating = n
				}
				i++
			}
		case "--notes":
			if i+1 < len(args) {
				notes = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}

	if rating < 0 || rating > 5 {
		fmt.Fprintln(os.Stderr, "usage: prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]")
		return 2
	}
	if tool == "" {
		tool = "prism_query"
	}

	out, err := invokeWithPersistentLedger(dir, "prism_feedback", map[string]any{
		"tool":    tool,
		"queryId": queryID,
		"rating":  rating,
		"notes":   notes,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "feedback:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdSavings(args []string) int {
	dir := dirArg(args, 0, ".")
	out, err := invokeWithPersistentLedger(dir, "prism_savings", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "savings:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdConfig(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, err := config.LoadFromDir(mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	printJSON(cfg)
	return 0
}

func cmdServe(args []string) int {
	port := 8888
	rest := args
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			if p, err := strconv.Atoi(args[i+1]); err == nil {
				port = p
			}
			rest = append([]string{}, args[:i]...)
			rest = append(rest, args[i+2:]...)
			break
		}
	}
	dir := dirArg(rest, 0, ".")
	cfg, client, err := newClient(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)

	// Auto-index on startup so the first query has something to work with.
	if _, err := client.Index(context.Background(), mustAbs(dir)); err != nil {
		fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
	}

	chosen, err := pickPort(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "port:", err)
		return 1
	}
	port = chosen

	server := httpapi.New(h).Handler()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Fprintln(os.Stderr, "prism HTTP listening on", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

func cmdMCP(args []string) int {
	dir := dirArg(args, 0, ".")
	root := mustAbs(dir)

	// Validate the project root up front. Without this, a bad path would block
	// in Serve (reading stdin) instead of failing fast, and the embedded Grove
	// engine would error mid-handshake.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "mcp: project root is not a directory:", root)
		return 1
	}

	// Load config and create the Grove client without connecting yet — the MCP
	// handshake (initialize / tools/list) must be serviced immediately or
	// Claude Code will time out and never load the tools.
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	client := grove.NewClient(cfg.GroveURL, cfg.GroveBinary).WithTokenFromDir(root)

	// Open the embedded Grove engine and run the initial index in the
	// background so the MCP handshake is serviced without waiting on I/O.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan struct{}) // closed once Grove engine is open (ready for queries)
	doneCh := make(chan struct{})  // closed once the goroutine fully exits
	go func() {
		defer close(doneCh)
		if err := client.EnsureRunning(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "warning: grove not reachable:", err)
			close(readyCh)
			return
		}
		// Signal ready as soon as the engine is open so tool calls (including
		// explicit prism_index calls) are not blocked waiting for the initial
		// index to complete. Large codebases can take minutes to index.
		close(readyCh)
		if _, err := client.Index(ctx, root); err != nil {
			fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
		}
	}()

	h := mcp.NewHandlerWithReady(cfg, root, client, readyCh)
	srv := mcp.NewServer(h)
	serveErr := srv.Serve(os.Stdin, os.Stdout)

	// Stop background work and close the embedded engine before returning so no
	// SQLite handles or .grove files linger — otherwise a caller that removes
	// the project directory (e.g. a test using t.TempDir) races file creation
	// and fails with "directory not empty" on Linux or a lock error on Windows.
	cancel()
	<-doneCh
	client.Shutdown()

	if serveErr != nil {
		fmt.Fprintln(os.Stderr, "mcp:", serveErr)
		return 1
	}
	return 0
}

// --- shared helpers ------------------------------------------------------

func newClient(dir string) (*config.Config, *grove.Client, error) {
	root := mustAbs(dir)
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	client := grove.NewClient(cfg.GroveURL, cfg.GroveBinary).WithTokenFromDir(root)
	if err := client.EnsureRunning(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("grove: %w", err)
	}
	return cfg, client, nil
}

func ledgerPathForRoot(root string) string {
	sum := sha1.Sum([]byte(root))
	key := hex.EncodeToString(sum[:])
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "prism", "ledger", key+".json")
}

func invokeWithPersistentLedger(dir, tool string, args map[string]any) (any, error) {
	root := mustAbs(dir)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, err
	}
	defer client.Shutdown()

	ledgerFile := ledgerPathForRoot(root)
	var out any
	var invokeErr error
	lockFile := ledgerFile + ".lock"
	lockErr := session.WithFileLock(lockFile, 5*time.Second, func() error {
		ledger, err := session.LoadLedger(ledgerFile)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(os.Stderr, "warning: could not load savings ledger:", err)
			}
			ledger = session.NewLedger(time.Now().Format("20060102-150405"))
		}

		// The handler warm-loads the session.Tracker from disk and we flush it
		// after each CLI invocation. The lock serializes standalone CLI processes
		// that share the same repo/home cache files.
		h := mcp.NewHandlerWithLedger(cfg, root, client, ledger)
		out, invokeErr = h.Invoke(tool, args)
		h.SaveSessionCache()
		if saveErr := h.Ledger.Save(ledgerFile); saveErr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not persist savings ledger:", saveErr)
		}
		pruneOldLedgers(filepath.Dir(ledgerFile), 30*24*time.Hour)
		return nil
	})
	if lockErr != nil {
		return nil, lockErr
	}
	return out, invokeErr
}

// pruneOldLedgers removes ledger files in dir that are older than maxAge.
// Silently ignores errors — pruning is best-effort.
func pruneOldLedgers(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func dirArg(args []string, idx int, def string) string {
	if idx < len(args) {
		a := args[idx]
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return def
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

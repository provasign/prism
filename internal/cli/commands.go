// Package cli implements the Prism command tree (flat dispatch, no cobra
// dependency — keeps Prism a true single binary with zero runtime deps).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tabladrum/grove-suite/prism/internal/config"
	"github.com/tabladrum/grove-suite/prism/internal/grove"
	"github.com/tabladrum/grove-suite/prism/internal/httpapi"
	"github.com/tabladrum/grove-suite/prism/internal/mcp"
)

const helpText = `prism - token-optimized context delivery for AI agents (requires Grove)

Usage:
  prism init [--global] [dir]     Write prism.yaml + register MCP with detected AI tools
                                  --global writes to user-level config (~/.claude, ~/.cursor, etc.)
  prism index [dir]               Index codebase via Grove (delta-aware)
  prism status [dir]              Show graph stats from Grove
  prism query <task> [dir]        Find ranked context for a task
  prism read <file> [dir]         Read file with compression
  prism search <keyword> [dir]    Search symbols by keyword
  prism lookup <name> [dir]       Show full source for a symbol
  prism compact [dir]             Compress conversation JSON from stdin
  prism serve [--port 8888] [dir] Start MCP+HTTP server
  prism mcp [dir]                 Start MCP server on stdio
  prism savings [dir]             Show session savings dashboard
  prism config [dir]              Show resolved configuration
  prism version                   Print version

Supported AI tools (auto-detected by prism init):
  Claude Code  →  .claude/mcp.json
  Cursor       →  .cursor/mcp.json
  Windsurf     →  .windsurf/mcp.json
  Zed          →  ~/.config/zed/settings.json (context_servers)
  VS Code      →  install vscode-extension/prism-vscode-*.vsix
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
		fmt.Println("prism 0.1.0-dev")
		return 0
	case "init":
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

	// 1. Write prism.yaml into the project (always relative, even for --global).
	yaml := fmt.Sprintf(`version: 1
grove_url: "%s"
grove_binary: "%s"
# model: auto  # Prism detects the active model from the MCP initialize handshake.
#               # Override here only if auto-detection fails, e.g.:
#               # model: "claude-sonnet-4-6"
profile: "%s"
`, cfg.GroveURL, cfg.GroveBinary, cfg.Profile)
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
// agent knows to prefer Prism tools over built-in file/grep operations.
const steeringInstructions = `
## Prism — context delivery (ALWAYS use these tools)

This project uses [Prism](https://github.com/tabladrum/grove-suite/prism) for token-optimized context delivery.
Prism tools are registered via MCP. Follow these rules in every task:

### Rules
1. **Start every task with prism_query** — call it with the task description before
   reading any files. It returns pre-ranked, compressed context covering targets,
   dependencies, and tests within the token budget.

2. **Use prism_read instead of read_file / cat** — it applies session-aware compression:
   full text on first read, signatures on second, references on third+.
   This saves 35–92% tokens and prevents context window overflow.

3. **Use prism_search instead of grep/ripgrep** — when you need to find a symbol
   by name, use prism_search. Follow up with prism_lookup for the full source.

4. **Call prism_index once at session start** (or after significant file changes).
   Do not re-index on every step — delta indexing is automatic.

5. **Call prism_compact when the context window is near capacity** — it summarizes
   older turns while preserving recent ones.

### Tool priority order
| Instead of...          | Use...         |
|------------------------|----------------|
| read_file / open file  | prism_read     |
| grep / ripgrep / find  | prism_search   |
| manual context gather  | prism_query    |
| symbol definition      | prism_lookup   |
`

// writeSteeringInstructions writes per-tool instruction files into the project
// so agents know to prefer Prism tools over built-in alternatives.
func writeSteeringInstructions(projectDir string) {
	type instrFile struct {
		name    string // description for log
		relPath string // path relative to projectDir
		wrap    func(body string) string
	}
	// CLAUDE.md is read by Claude Code as project-level instructions.
	// .cursorrules is read by Cursor.
	// .windsurfrules is read by Windsurf.
	targets := []instrFile{
		{
			name:    "Claude Code",
			relPath: "CLAUDE.md",
			wrap:    func(body string) string { return body },
		},
		{
			name:    ".cursorrules",
			relPath: ".cursorrules",
			wrap:    func(body string) string { return body },
		},
		{
			name:    "Windsurf",
			relPath: ".windsurfrules",
			wrap:    func(body string) string { return body },
		},
	}

	for _, t := range targets {
		path := filepath.Join(projectDir, t.relPath)
		content := t.wrap(steeringInstructions)

		existing, err := os.ReadFile(path)
		if err == nil {
			// File already exists — append only if our marker is not already present.
			if strings.Contains(string(existing), "## Prism — context delivery") {
				continue // already has Prism instructions
			}
			content = string(existing) + "\n" + content
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s instructions: %v\n", t.name, err)
			continue
		}
		fmt.Printf("wrote steering instructions: %s\n", path)
	}
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
			// Claude Code: .claude/mcp.json (project) or ~/.claude/mcp.json (global)
			name: "Claude Code",
			path: func() string {
				if global {
					return filepath.Join(home, ".claude", "mcp.json")
				}
				return filepath.Join(projectDir, ".claude", "mcp.json")
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
	}

	for _, w := range writers {
		p := w.path()
		// Only write if the parent directory already exists (i.e. the tool is
		// installed). For global Zed settings we always try.
		parent := filepath.Dir(p)
		if _, err := os.Stat(parent); err != nil {
			continue // tool not installed / not detected
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

func cmdIndex(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, client := mustClient(dir)
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
	_, client := mustClient(dir)
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
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	invokeArgs := map[string]any{"task": task, "limit": limit}
	if profile != "" {
		invokeArgs["profile"] = profile
	}
	out, err := h.Invoke("prism_query", invokeArgs)
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
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	out, err := h.Invoke("prism_read", map[string]any{"file": file})
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
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	out, err := h.Invoke("prism_search", map[string]any{"query": query, "limit": limit})
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
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": args[0]})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdCompact(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	var turns []map[string]any
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&turns); err != nil {
		fmt.Fprintln(os.Stderr, "compact: stdin must be a JSON array of turns:", err)
		return 2
	}
	out, err := h.Invoke("prism_compact", map[string]any{"turns": turns})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compact:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdSavings(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	out, err := h.Invoke("prism_savings", nil)
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
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)

	// Auto-index on startup so the first query has something to work with.
	if _, err := client.Index(context.Background(), mustAbs(dir)); err != nil {
		fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
	}

	server := httpapi.New(h).Handler()
	addr := fmt.Sprintf(":%d", port)
	fmt.Fprintln(os.Stderr, "prism HTTP listening on", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

func cmdMCP(args []string) int {
	dir := dirArg(args, 0, ".")
	cfg, client := mustClient(dir)
	defer client.Shutdown()
	h := mcp.NewHandler(cfg, mustAbs(dir), client)
	// Best-effort initial index.
	if _, err := client.Index(context.Background(), mustAbs(dir)); err != nil {
		fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
	}
	srv := mcp.NewServer(h)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return 1
	}
	return 0
}

// --- shared helpers ------------------------------------------------------

func mustClient(dir string) (*config.Config, *grove.Client) {
	cfg, err := config.LoadFromDir(mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	client := grove.NewClient(cfg.GroveURL, cfg.GroveBinary)
	if err := client.EnsureRunning(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "grove:", err)
		os.Exit(1)
	}
	return cfg, client
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

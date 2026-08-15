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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/assist"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/httpapi"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/version"
)

// outputFormat controls how CLI results are printed.
type outputFormat string

const (
	formatText outputFormat = "text"
	formatLean outputFormat = "lean"
	formatJSON outputFormat = "json"
)

const helpText = `prism - semantic change intelligence for coding agents (embedded Grove)

Usage:
  prism init [--global] [dir]     Write prism.yaml + register MCP with detected AI tools
                                  --global writes to user-level config (~/.claude, ~/.cursor, etc.)
  prism install [--global] [dir]  Alias for 'prism init'
  prism index [dir]               Index codebase via Grove (delta-aware)
  prism watch [dir]               Keep the index warm: delta-reindex on file save
                                  (push model; [--debounce 2s], Ctrl+C to stop)
  prism status [dir]              Show graph stats from Grove
  prism doctor [dir]              Diagnose engine, index, and capabilities
  prism map [dir]                 Component-level architecture map: directories,
                                  induced dependency edges (weights + evidence
                                  tiers), cycles. Production shape by default
                                  ([--depth N] [--component X] [--tests]
                                  [--expand 'A->B'] [--json])
  prism cycles [dir]              Dependency cycles with per-edge evidence
                                  ([--depth N] [--tests] [--json])
  prism arch [dir]                Validate declared architecture rules
                                  (arch_deny: "<from> -> <to>" in prism.yaml)
                                  against the component view; violations cite
                                  file:line sites; exit 1 on violation — a CI
                                  gate ([--deny 'A -> B'] [--depth N] [--json])
  prism verify [dir]              Verify a diff's completeness (working tree vs
                                  --base, default HEAD): missed change-impact
                                  sites (line-precise), new
                                  cross-component deps, introduced arch
                                  violations; exit 1 if incomplete — the CI
                                  gate for agent-authored changes
                                  [--base REF] [--strict] [--format text|json]
                                  ([--base REF] [--json])
  prism query <task> --terms a,b,c [dir]  Find ranked context for a task; bug-fix/
                                  implement tasks get line-numbered source windows +
                                  per-anchor callers (edit-ready)
                                  --terms a,b,c      REQUIRED: anchor on specific symbol
                                  names (grep-precision) — guess one from the task if
                                  you don't have a name yet
                                  --include a,b      Categories: graph,docs (default: graph)
                                  --delivery source|symbols  Force delivery shape (default: phase-aware)
                                  --max-files N      source delivery: max files shown (default: 5)
                                  --format text|lean|json  Output format (default: text)
  prism read <file> [dir]         Read file with compression
                                  --format text|lean|json  Output format (default: text)
  prism search <keyword> [dir]    Search symbol names AND raw source text (a real
                                  rg/grep pass). --scope text is a pure grep
                                  ([--scope text|symbols|both] [--regex] [--limit N])
                                  --format text|lean|json  Output format (default: text)
  prism lookup <name> [dir]       Show full source for a symbol
  prism node <symbol-or-file> [dir]  One-shot orientation: a symbol's source +
                                  its neighbours, or a file's source + the
                                  symbols it defines + the files depending on it
                                  --format text|lean|json  Output format (default: text)
  prism references <name> [dir]   Find where a symbol is USED (every code occurrence,
                                  comments/strings excluded), grouped by file
                                  --format text|lean|json  Output format (default: text)
  prism resolve <name> [dir]      Resolve a name to its definition(s): file:line + kind
  prism edges <name> [dir]        Walk the graph one hop from a symbol
                                  ([--direction in|out] [--kinds calls,uses-type,...])
  prism change-impact <query> [dir]  Deterministic change-set for a method signature change:
                                  declaration(s), override/implementation family (subtype
                                  closure), super-declarations, and all resolved callers.
                                  query format: Type.method or Type.method(ParamType, ...)
                                  --format text|lean|json  Output format (default: json)
  prism rename-plan <query> <NewName> [dir]     Change-set as line edits with substitutions
                                                (--format text|lean|json; query: Type.method or Type.method(ParamType, ...))
  prism missing-implementations <query> [dir]  Types claiming the contract that do NOT
                                  implement Type.method (missing / abstract / unverifiable)
                                  — the interface-evolution companion to change-impact
                                  --format text|lean|json  Output format (default: json)
  prism dead-code [dir] [--roots a,b]  Unreachable production functions/methods
  prism assist [--model <spec>] [--apply|--apply-ambiguous] [--verify "<cmd>"] "<task>"
                                     NL task -> deterministic ops via any model (ollama:/claude:/openai:)
                                  (precision-first; relay the caveats)
                                  --format text|lean|json  Output format (default: json)
  prism compact [dir]             Compress conversation JSON from stdin
  prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
                                  Submit quality feedback for a Prism result
  prism serve [--port 8888] [dir] Start the HTTP API server (stdio MCP is 'prism mcp')
  prism mcp [dir]                 Start MCP server on stdio
  prism savings [dir]             Show session savings dashboard
  prism drift [dir]              Report files/symbols that changed since they were delivered this session
  prism config [dir]              Show resolved configuration
  prism hook pretooluse           Claude Code PreToolUse hook: reads the event JSON
                                  from stdin, denies grep/rg with a reason fed back to
                                  the model. Registered automatically by
                                  --deny-builtin-search; not meant to be run by hand.
  prism version                   Print version

prism init [dir] flags:
  --global            register in user-global configs (unlocks Zed, Codex, opencode)
  --mode <any>        accepted and IGNORED (since v0.38.0 one steering template
                      covers MCP tools and the CLI together)
  --no-permissions    skip the Claude Code tool auto-allow entry
  --deny-builtin-search
                      deny Claude Code's Grep/Bash(grep|rg) so agents actually
                      reach prism (asked interactively; Claude Code only —
                      no other agent exposes a tool-denial setting). Also
                      registers a PreToolUse hook (prism hook pretooluse) that
                      explains the denial back to the model instead of a bare
                      failure; the permissions.deny rule stays as a failsafe.
  --refresh           rewrite ONLY agents already configured (never adds new ones)
  --print-config <id> print one agent's snippet and exit, writing nothing
                      ids: claude, cursor, windsurf, vscode, zed, codex, opencode, hermes

Supported AI tools. Steering files are written unconditionally (harmless if the
tool is absent; re-running updates in place). MCP configs are written only where
the tool's config directory already exists:
  Claude Code  →  .mcp.json + CLAUDE.md
  Cursor       →  .cursor/mcp.json + .cursorrules + AGENTS.md
  Windsurf     →  .windsurf/mcp.json + .windsurfrules
  Zed          →  ~/.config/zed/settings.json (context_servers)   [--global]
  VS Code      →  .vscode/mcp.json + .github/copilot-instructions.md
  Codex CLI    →  ~/.codex/config.toml + AGENTS.md                [--global]
  opencode     →  ~/.config/opencode/opencode.json                [--global]
  Hermes       →  ~/.hermes/config.yaml   (print-config only — paste it yourself)
  Gemini CLI   →  GEMINI.md
  Cline        →  .clinerules
  Devin        →  .devin/instructions.md
  Kiro         →  .kiro/steering/prism.md
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
	case "version", "--version", "-version":
		fmt.Println("prism " + version.Version)
		return 0
	case "init", "install":
		return cmdInit(rest)
	case "watch":
		return cmdWatch(rest)
	case "index":
		return cmdIndex(rest)
	case "status":
		return cmdStatus(rest)
	case "doctor":
		return cmdDoctor(rest)
	case "map":
		return cmdMap(rest)
	case "cycles":
		return cmdCycles(rest)
	case "arch", "arch-check":
		return cmdArch(rest)
	case "verify":
		return cmdVerify(rest)
	case "query":
		return cmdQuery(rest)
	case "read":
		return cmdRead(rest)
	case "search":
		return cmdSearch(rest)
	case "node":
		return cmdNode(rest)
	case "lookup":
		return cmdLookup(rest)
	case "references", "refs":
		return cmdReferences(rest)
	case "resolve":
		return cmdResolve(rest)
	case "edges":
		return cmdEdges(rest)
	case "change-impact":
		return cmdChangeImpact(rest)
	case "missing-implementations":
		return cmdMissingImplementations(rest)
	case "rename-plan":
		return cmdRenamePlan(rest)
	case "dead-code":
		return cmdDeadCode(rest)
	case "assist":
		return cmdAssist(rest)
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
	case "drift":
		return cmdDrift(rest)
	case "config":
		return cmdConfig(rest)
	case "hook":
		return cmdHook(rest)
	}
	fmt.Fprintln(os.Stderr, "unknown command:", cmd)
	fmt.Print(helpText)
	return 2
}

// --- per-command implementations ---------------------------------------

func cmdInit(args []string) int {
	// Flags: --global (write to ~/.config/... instead of project dir)
	// --mode mcp|cli|both  (skip interactive prompt)
	// --no-permissions     (skip the Claude Code tool auto-allow entry)
	// --print-config <id>  (print one agent's snippet, write nothing, exit)
	// --refresh            (rewrite ONLY agents already configured)
	global := false
	permissions := true
	printConfig := ""
	refresh := false
	denyBuiltinSearch := false
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--global":
			global = true
		case "--no-permissions":
			permissions = false
		case "--deny-builtin-search":
			denyBuiltinSearch = true
		case "--refresh":
			refresh = true
		case "--print-config":
			if i+1 < len(args) {
				printConfig = args[i+1]
				i++
			}
		case "--mode":
			// Accepted and ignored since v0.38.0. The three modes only ever
			// chose which steering text was written: they gated no tool, and
			// "cli" registered the MCP servers anyway, so the flag described
			// what the agent was TOLD rather than what it was given. One
			// template now covers both surfaces.
			if i+1 < len(args) {
				fmt.Fprintln(os.Stderr, "note: --mode is no longer needed and is ignored; "+
					"steering now covers MCP tools and the CLI together")
				i++
			}
		default:
			filtered = append(filtered, a)
		}
	}
	args = filtered

	dir := dirArg(args, 0, ".")
	abs, _ := filepath.Abs(dir)
	cfg := config.Default()

	// --print-config is a pure query: render one agent's snippet and exit
	// without touching a single file.
	if printConfig != "" {
		return printAgentConfig(printConfig, abs, detectSelfPath(), global)
	}

	// If mode not set by flag, prompt interactively (or default to "both" if
	// stdin is not a terminal, e.g. in CI or when piped).
	// 1. Write prism.yaml into the project. Grove is embedded in-process now,
	// so the file no longer needs grove_url / grove_binary.
	yaml := fmt.Sprintf(`version: 1
# model: ""    # Optional: name the model driving this repo (e.g. "claude-sonnet-4-6")
#               # to size context budgets. There is NO auto-detection — the MCP
#               # initialize handshake does not carry the model — so unset means
#               # the default 200k-token window. Agents can also pass model= per
#               # call, which overrides this.
profile: "%s"
`, cfg.Profile)
	prismYAML := filepath.Join(abs, "prism.yaml")
	// NEVER clobber an existing prism.yaml. It holds user content init knows
	// nothing about — arch_deny rules above all, which are the CI gate for
	// declared architecture. A plain WriteFile deleted them on every re-init,
	// silently turning the arch check into a no-op. Only the three keys init
	// manages are rewritten; every other line survives byte-for-byte.
	if existing, err := os.ReadFile(prismYAML); err == nil {
		yaml = mergePrismYAML(string(existing), cfg.Profile)
	}
	if err := os.WriteFile(prismYAML, []byte(yaml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("wrote", prismYAML)

	// 2. Detect the prism binary path for use in MCP configs.
	prismBin := detectSelfPath()

	// Routing is the one thing steering cannot do. Measured at 12:1 in the
	// benchmark and observed live: an agent listed prism's connected tools,
	// said its CLAUDE.md directed it to use them, then ran Bash(grep) on the
	// next task. Denying the built-in search is the only reliable fix — but
	// it edits the user's own Claude Code settings, so ASK rather than assume.
	// Never prompt non-interactively (CI gets the safe default: no change).
	// This MUST resolve before writeSteeringInstructions: the steering text
	// itself differs when grep is actually blocked (below) — telling a user
	// who declined the deny prompt that "grep is BLOCKED" would be false
	// guidance, so the steering has to know the final answer, not the
	// pre-prompt default.
	if !denyBuiltinSearch && permissions && printConfig == "" && isInteractive() {
		denyBuiltinSearch = promptDenyBuiltinSearch()
	}

	// 3. Write steering instructions matching the chosen mode.
	writeSteeringInstructions(abs, denyBuiltinSearch)

	// 4. Register with every detected AI coding tool.
	registered := initRegisterMCPTools(abs, prismBin, global, permissions, refresh, denyBuiltinSearch)
	if len(registered) == 0 {
		fmt.Println("tip: add prism to your AI tool's MCP config (see README)")
	}
	return 0
}

// mergePrismYAML rewrites only the keys init manages (version, profile,
// agent_mode) and preserves every other line — comments, arch_deny rules,
// anything a user or a later prism version put there. Keys init manages but
// the file lacks are appended.
func mergePrismYAML(existing, profile string) string {
	managed := []struct{ key, val string }{
		{"version", "1"},
		{"profile", strconv.Quote(profile)},
	}
	seen := map[string]bool{}
	lines := strings.Split(existing, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, m := range managed {
			// Top-level key only: an indented line belongs to a nested block
			// this function must not touch.
			if line == trimmed && strings.HasPrefix(trimmed, m.key+":") {
				lines[i] = m.key + ": " + m.val
				seen[m.key] = true
			}
		}
	}
	out := strings.Join(lines, "\n")
	var missing []string
	for _, m := range managed {
		if !seen[m.key] {
			missing = append(missing, m.key+": "+m.val)
		}
	}
	if len(missing) > 0 {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += strings.Join(missing, "\n") + "\n"
	}
	return out
}

// steeringInstructions is injected when agent_mode is "both" (default).
// MCP tools are primary; the CLI section serves as fallback for subagents
// that only have Bash access.
const steeringInstructions = `
## Prism — code intelligence (ALWAYS use these tools)

prism_search/query/read/lookup/change_impact are already in your tool list --
call them directly, no lookup step. Locate a string/symbol -> prism_search(scope="text")
(a real ripgrep pass, same cost as grep). Bug/task with an anchor -> prism_query(task=...,
terms=[...]) -- terms is required, guess one keyword. Signature change, rename, or
"who breaks if I change X" -> prism_change_impact -- returns the complete site set in
one call; do not re-verify it with grep, that measurably drops real sites. A repeat
prism_read of an unchanged file returns a short cached-pointer line, not the body --
that is not an error. Three concrete substitutions, not just "prefer prism":
about to grep several candidate names ("a\|b\|c", unsure which is right)? that is
prism_query(terms=[a,b,c]) in one call, not several rounds of narrowing. About to
grep a name followed by "(" to find who constructs/calls it? that is
prism_change_impact or prism_query's caller list -- resolved call sites, not a
paren-guess that also matches definitions and comments. About to grep a def line
then guess how many lines of context to print (-A10, sed -n 'N,Mp')? that is
prism_lookup -- the whole symbol, no line-count guessing, never cut off early.
No MCP session (Bash-only subagent)? Same names as CLI verbs:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->
`

// writeSteeringInstructions writes per-tool instruction files into the project
// so agents know how to use Prism tools correctly.
// On re-init it replaces a stale Prism section rather than skipping.
func writeSteeringInstructions(projectDir string, denyBuiltinSearch bool) {
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
		// Cline agent steering
		{name: "Cline", relPath: ".clinerules"},
		// Devin
		{name: "Devin", relPath: ".devin/instructions.md"},
		// Kiro (Amazon): each file in .kiro/steering/ is a topic steering doc
		{name: "Kiro", relPath: ".kiro/steering/prism.md"},
	}

	block := steeringBlock(denyBuiltinSearch)

	for _, t := range targets {
		path := filepath.Join(projectDir, t.relPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create directory for %s instructions: %v\n", t.name, err)
			continue
		}

		var content string
		if existing, err := os.ReadFile(path); err == nil {
			// File exists — replace stale Prism section or append if absent.
			content = injectPrismSection(string(existing), block)
		} else {
			content = block
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s instructions: %v\n", t.name, err)
			continue
		}
		fmt.Printf("wrote steering instructions: %s\n", path)
	}
}

// steeringBlock returns the one steering block prism writes. There were
// three (mcp / cli / both), chosen by a prompt at init — but they gated
// nothing (every tool worked in every mode, and "cli" registered the MCP
// servers regardless), so the choice only changed which documentation the
// agent read, for a 317-token difference. Three copies of the same prose
// also drifted: a steering edit landed in one of three variants before this
// collapsed them.
// steeringBlock appends a hard-stop paragraph ONLY when grep/rg are actually
// blocked (denyBuiltinSearch). Saying "grep is BLOCKED" to a user who
// declined the deny prompt would be false — most users say no, and for them
// grep is a normal, working fallback. Text, not just structure: the deny
// mechanism stops a denied call from succeeding, but says nothing to the
// model in advance — measured live (2026-08-11), an agent tried grep anyway,
// ate a denial, then recovered via prism. Telling it up front that the
// attempt will fail should cut that wasted turn without changing anything
// for users who never opted into the deny.
func steeringBlock(denyBuiltinSearch bool) string {
	if !denyBuiltinSearch {
		return steeringInstructions
	}
	return strings.Replace(steeringInstructions,
		"## Prism — code intelligence (ALWAYS use these tools)",
		"## Prism — code intelligence (ALWAYS use these tools)\n\n"+
			"grep, rg, and the built-in Grep tool are BLOCKED in this project — "+
			"any attempt fails and wastes a turn. Do not try them, even out of "+
			"habit. prism_search(scope=\"text\") is the replacement (a real "+
			"ripgrep pass); prism_query/prism_read for context. Read/Edit still "+
			"work for files you already know you're editing.",
		1)
}

// injectPrismSection replaces the Prism steering section in content, or
// appends it when absent.
//
// The section is delimited by a start marker AND an end marker. Before the
// end marker existed this returned content[:idx]+block — silently DELETING
// everything after the Prism section on every re-init. Reproduced: a
// CLAUDE.md with "## Build / ## Prism… / ## MY IMPORTANT RULES / ## Deploy"
// lost both trailing user sections. `--refresh` makes re-running routine, so
// the section has to be bounded.
//
// A legacy section written before the end marker existed has no terminator;
// those are replaced up to the next top-level "## " heading, which preserves
// the user's following sections instead of eating them.
func injectPrismSection(content, block string) string {
	// Exact-match the headings prism has EVER generated -- not a bare
	// "## Prism — " prefix. Both failure modes are measured, same day:
	// a marker pinned to one old heading made upgrades APPEND (duplicated
	// blocks after the v0.48.0 heading rename), and an over-greedy prefix
	// marker deleted a USER-authored "## Prism — Context Delivery Layer"
	// architecture section from provasign/CLAUDE.md (restored from git).
	// Prism may only ever claim sections it wrote; when the generated
	// heading changes, ADD the old one here, never widen the match.
	// Heading STEMS, case-sensitive: early generated headings appeared both
	// with and without the "(ALWAYS use these tools)" suffix. The user's
	// clashing section was "## Prism — Context Delivery Layer" — capital C —
	// so case-sensitive stems distinguish it.
	generatedHeadings := []string{
		"## Prism — context delivery",
		"## Prism — code intelligence",
	}
	const endMarker = "<!-- prism:end -->"

	findSection := func(s string) (start, prefixLen int) {
		start = -1
		for _, h := range generatedHeadings {
			if idx := strings.Index(s, "\n"+h); idx >= 0 && (start < 0 || idx < start) {
				start, prefixLen = idx, 1
			}
			if strings.HasPrefix(s, h) {
				start, prefixLen = 0, 0
			}
		}
		return start, prefixLen
	}

	insertAt := -1
	for {
		start, prefixLen := findSection(content)
		if start < 0 {
			break
		}
		if insertAt < 0 {
			insertAt = start
		}
		head := content[:start]
		rest := content[start+prefixLen:]
		if e := strings.Index(rest, endMarker); e >= 0 {
			// Bounded section: everything through the end marker is ours.
			content = head + "\n" + strings.TrimLeft(rest[e+len(endMarker):], "\n")
		} else if n := strings.Index(rest, "\n## "); n >= 0 {
			// Legacy unbounded section: ends at the next top-level heading.
			content = head + "\n" + strings.TrimLeft(rest[n+1:], "\n")
		} else {
			content = head
		}
		content = strings.TrimRight(content, "\n") + "\n"
	}

	if insertAt < 0 || insertAt >= len(content) {
		return strings.TrimRight(content, "\n") + block
	}
	head := strings.TrimRight(content[:insertAt], "\n")
	tail := strings.TrimLeft(content[insertAt:], "\n")
	if tail == "" {
		return head + block
	}
	return head + block + "\n" + tail
}
func detectSelfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "prism"
	}
	return exe
}

// mcpEntry is the JSON structure every MCP-compatible tool expects.
// AlwaysLoad exempts the server's tool schemas from client-side deferral
// (Claude Code's tool-search): without it, cheap-tier models never make the
// ToolSearch hop and the tools go unused (measured: haiku 0 prism calls
// deferred vs 5 loaded, same task). Server-level belt to the per-tool
// anthropic/alwaysLoad _meta the MCP server also emits; clients that don't
// know the key ignore it.
type mcpEntry struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	AlwaysLoad bool     `json:"alwaysLoad,omitempty"`
}

// initRegisterMCPTools writes MCP server config for every detected tool.
// It returns the list of files written.
// initRegisterMCPTools writes prism's MCP server entry into every AI coding
// tool's config. permissions=false skips Claude Code's tool auto-allow;
// refresh=true rewrites ONLY tools already configured (never adds a new one),
// which is what an upgrade wants.
func initRegisterMCPTools(projectDir, prismBin string, global, permissions, refresh, denyBuiltinSearch bool) []string {
	var written []string

	// Scope model: project-level is the default and touches ONLY files inside
	// the repo. User-global tools (Zed, Codex CLI, opencode) and the global
	// Claude settings are written only with --global, or after the explicit
	// interactive question below — never silently.
	globalTools := global
	if !globalTools && !refresh && isInteractive() {
		globalTools = promptGlobalTools()
	}
	// Claude Code approval/permissions target: the PROJECT settings file by
	// default, so allow/deny/trust stay with the repo; machine-global only
	// under --global.
	claudeSettings := filepath.Join(projectDir, ".claude", "settings.json")
	if global {
		if home, err := os.UserHomeDir(); err == nil {
			claudeSettings = filepath.Join(home, ".claude", "settings.json")
		}
	}

	entry := mcpEntry{Command: prismBin, Args: []string{"mcp", projectDir}, AlwaysLoad: true}
	// Claude Code launches project-scope MCP servers with cwd at the project
	// root, so its entry needs no pinned absolute path — this keeps .mcp.json
	// portable and correct after the repo moves. The IDE writers below keep
	// the explicit dir because their launch cwd is not guaranteed.
	claudeEntry := mcpEntry{Command: prismBin, Args: []string{"mcp"}, AlwaysLoad: true}

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
				return buildMCPConfig("prism", claudeEntry)
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
	if globalTools {
		// opencode: ~/.config/opencode/opencode.json. USER-GLOBAL — written
		// only when global registration was requested (flag or interactive
		// consent). A project init must never touch machine-wide configs;
		// this writer used to sit in the always-on list and leaked.
		writers = append(writers, writer{
			name: "opencode",
			path: func() string {
				return filepath.Join(home, ".config", "opencode", "opencode.json")
			},
			build: func() []byte {
				return buildOpencodeConfig(prismBin)
			},
		})
	}

	for _, w := range writers {
		p := w.path()
		// --refresh rewrites only what a previous install configured: if the
		// config file does not exist yet, this tool was never set up and must
		// not be added now.
		if refresh {
			if _, err := os.Stat(p); err != nil {
				continue
			}
		}
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
		if filepath.Base(p) == ".mcp.json" && mcpEntryAlreadyPresent(p, "prism", claudeEntry) {
			fmt.Printf("already registered with %s: %s\n", w.name, p)
			written = append(written, p)
			ensureClaudeCodeApproval(claudeSettings, "prism", prismBin, permissions, denyBuiltinSearch)
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
			ensureClaudeCodeApproval(claudeSettings, "prism", prismBin, permissions, denyBuiltinSearch)
		}
	}

	// Zed and Codex CLI keep their MCP registrations in USER-GLOBAL config
	// files (~/.config/zed/settings.json, ~/.codex/config.toml). A
	// project-level init must not touch them: writing this project's path
	// there would silently re-point every other project's Zed/Codex at this
	// one. Register them only with --global, and without a pinned project
	// dir — `prism mcp` serves the editor's launch cwd, so one global entry
	// is correct in every project.
	if globalTools {
		zedPath := filepath.Join(home, ".config", "zed", "settings.json")
		if _, err := os.Stat(filepath.Dir(zedPath)); err == nil && !(refresh && !fileExists(zedPath)) {
			merged := mergeOrCreate(zedPath, buildZedConfig(prismBin))
			if err := os.WriteFile(zedPath, merged, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not write Zed config (%s): %v\n", zedPath, err)
			} else {
				fmt.Printf("registered with Zed: %s\n", zedPath)
				written = append(written, zedPath)
			}
		}

		// Codex CLI (~/.codex/config.toml) uses TOML, not JSON.
		// Only write when ~/.codex/ already exists (i.e. Codex CLI is installed).
		codexPath := filepath.Join(home, ".codex", "config.toml")
		if _, err := os.Stat(filepath.Dir(codexPath)); err == nil && !(refresh && !fileExists(codexPath)) {
			if err := writePrismCodexConfig(codexPath, prismBin, []string{"mcp"}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not write Codex CLI config: %v\n", err)
			} else {
				fmt.Printf("registered with Codex CLI: %s\n", codexPath)
				written = append(written, codexPath)
			}
		}
	} else {
		fmt.Println("note: Zed and Codex CLI use user-global configs — run `prism init --global` to register them")
	}
	// Hermes keeps its MCP servers in a nested YAML document with a separate
	// platform_toolsets list. Prism has no YAML parser, and hand-splicing that
	// structure risks corrupting a working config, so Hermes is print-only:
	// `prism init --print-config hermes` emits the snippet to paste.
	fmt.Println("note: for Hermes, run `prism init --print-config hermes` and paste the snippet")

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
	// alwaysLoad participates in "already correct": an entry written before
	// v0.44.0 lacks it, and skipping the rewrite would leave the server's
	// schemas deferrable forever. The one-time Claude Code re-approval this
	// rewrite triggers is the cost of the upgrade.
	if got.AlwaysLoad != want.AlwaysLoad {
		return false
	}
	return true
}

// fileExists reports whether path exists (used by --refresh, which must only
// rewrite configs a previous install already created).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// buildOpencodeConfig returns opencode's MCP stanza. opencode expects a
// "local" server whose command is a single argv array.
func buildOpencodeConfig(prismBin string) []byte {
	type opencodeServer struct {
		Type    string   `json:"type"`
		Command []string `json:"command"`
		Enabled bool     `json:"enabled"`
	}
	type opencodeConfig struct {
		Schema string                    `json:"$schema"`
		MCP    map[string]opencodeServer `json:"mcp"`
	}
	// No pinned project dir: this is opencode's user-global config and
	// `prism mcp` serves the launch cwd.
	c := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		MCP: map[string]opencodeServer{
			"prism": {Type: "local", Command: []string{prismBin, "mcp"}, Enabled: true},
		},
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return b
}

// buildHermesSnippet returns the YAML block a user pastes into Hermes'
// ~/.hermes/config.yaml. Hermes needs BOTH the server entry and its toolset
// registration, and prism does not write this file (see initRegisterMCPTools).
func buildHermesSnippet(prismBin string) string {
	return fmt.Sprintf(`mcp_servers:
  prism:
    command: %s
    args:
      - mcp
    timeout: 120
    connect_timeout: 60
    enabled: true

platform_toolsets:
  cli:
    - mcp-prism
`, prismBin)
}

// buildCodexSnippet returns the TOML block written to ~/.codex/config.toml,
// as text, so --print-config can show it without writing.
func buildCodexSnippet(prismBin string) string {
	return strings.Join([]string{
		"[mcp_servers.prism]",
		`type = "stdio"`,
		fmt.Sprintf("command = %q", prismBin),
		prismTOMLStringArray("args", []string{"mcp"}),
	}, "\n") + "\n"
}

// printAgentConfig implements `prism init --print-config <id>`: render the
// config snippet for one agent and exit WITHOUT writing anything. Mirrors the
// targets initRegisterMCPTools writes, plus print-only Hermes.
func printAgentConfig(id, projectDir, prismBin string, global bool) int {
	home, _ := os.UserHomeDir()
	entry := mcpEntry{Command: prismBin, Args: []string{"mcp", projectDir}}
	claudeEntry := mcpEntry{Command: prismBin, Args: []string{"mcp"}}

	pick := func(globalPath, projectPath string) string {
		if global {
			return globalPath
		}
		return projectPath
	}

	var path, body string
	switch strings.ToLower(id) {
	case "claude", "claude-code":
		path = pick(filepath.Join(home, ".claude.json"), filepath.Join(projectDir, ".mcp.json"))
		body = string(buildMCPConfig("prism", claudeEntry))
	case "cursor":
		path = pick(filepath.Join(home, ".cursor", "mcp.json"), filepath.Join(projectDir, ".cursor", "mcp.json"))
		body = string(buildMCPConfig("prism", entry))
	case "windsurf":
		path = pick(filepath.Join(home, ".windsurf", "mcp.json"), filepath.Join(projectDir, ".windsurf", "mcp.json"))
		body = string(buildMCPConfig("prism", entry))
	case "vscode", "vs-code":
		path = filepath.Join(projectDir, ".vscode", "mcp.json")
		body = string(buildVSCodeConfig(prismBin, projectDir))
	case "zed":
		path = filepath.Join(home, ".config", "zed", "settings.json")
		body = string(buildZedConfig(prismBin))
	case "codex":
		path = filepath.Join(home, ".codex", "config.toml")
		body = buildCodexSnippet(prismBin)
	case "opencode":
		path = filepath.Join(home, ".config", "opencode", "opencode.json")
		body = string(buildOpencodeConfig(prismBin))
	case "hermes":
		path = filepath.Join(home, ".hermes", "config.yaml")
		body = buildHermesSnippet(prismBin)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent %q. Known: claude, cursor, windsurf, vscode, zed, codex, opencode, hermes\n", id)
		return 2
	}
	fmt.Printf("# Add to %s\n\n%s\n", path, strings.TrimRight(body, "\n"))
	return 0
}

// buildZedConfig returns the minimal Zed context_servers stanza.
func buildZedConfig(prismBin string) []byte {
	type zedServer struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type zedSettings struct {
		ContextServers map[string]zedServer `json:"context_servers"`
	}
	// No pinned project dir: the entry lives in Zed's user-global settings,
	// and `prism mcp` serves the launch cwd (the open worktree).
	s := zedSettings{ContextServers: map[string]zedServer{
		"prism": {Command: prismBin, Args: []string{"mcp"}},
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

// ensureClaudeCodeApproval makes Claude Code both TRUST and AUTO-ALLOW the
// server in ~/.claude/settings.json:
//
//   - enabledMcpjsonServers: server trust (no re-approval prompt per run)
//   - permissions.allow: "mcp__<server>" — the server-wide grant, so the
//     agent stops prompting on every individual prism_* tool call. Whole-server
//     rather than one entry per tool so a newly added tool is covered
//     automatically and never silently re-introduces prompts.
//
// allowTools=false writes only the trust entry (`prism init --no-permissions`).
// Both edits merge into the existing document, so unrelated settings and
// unrelated permission rules survive.
// isInteractive reports whether stdin is a terminal — a pipe, file or CI run
// must never block on a prompt.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// promptGlobalTools asks — every interactive project-level init — whether to
// also register the tools that only have user-global configs. Default NO:
// a project init keeps the machine untouched.
func promptGlobalTools() bool {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Zed, Codex CLI, and opencode keep MCP registrations in USER-GLOBAL")
	fmt.Fprintln(os.Stderr, "config files (outside this repo). Register prism with them too?")
	fmt.Fprintln(os.Stderr, "  Default keeps setup project-level: nothing outside this repo is")
	fmt.Fprintln(os.Stderr, "  touched, and other projects are unaffected.")
	fmt.Fprint(os.Stderr, "Register user-global tools? [y/N]: ")
	var line string
	fmt.Scanln(&line)
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// promptDenyBuiltinSearch offers the one change that actually routes agents
// through prism, and explains the trade honestly. Default is NO: this edits
// settings the user owns.
func promptDenyBuiltinSearch() bool {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Agents usually ignore steering and use their own grep — measured 12:1,")
	fmt.Fprintln(os.Stderr, "and reproduced on a machine where prism was installed and connected.")
	fmt.Fprintln(os.Stderr, "Deny Claude Code's built-in search so prism is actually reached?")
	fmt.Fprintln(os.Stderr, "  Adds Grep, Bash(grep:*), Bash(rg:*) to permissions.deny in the")
	fmt.Fprintln(os.Stderr, "  PROJECT's .claude/settings.json (machine-global only with")
	fmt.Fprintln(os.Stderr, "  --global). Nothing becomes unfindable —")
	fmt.Fprintln(os.Stderr, "  prism_search(scope=\"text\") is a ripgrep passthrough. Reversible:")
	fmt.Fprintln(os.Stderr, "  delete those lines. Only affects Claude Code.")
	fmt.Fprint(os.Stderr, "Deny built-in search? [y/N]: ")
	var line string
	fmt.Scanln(&line)
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// ensureClaudeCodeHook registers `prism hook pretooluse` as a PreToolUse hook
// for the Bash and Grep matchers, idempotently (safe to call on every
// `prism init`/`--refresh`). It mutates doc in place and reports whether it
// changed anything. Scope is deliberately narrow: this hook only ever denies
// grep/rg — it does not touch Bash otherwise, so python and every other Bash
// use is unaffected.
func ensureClaudeCodeHook(doc map[string]any, prismBin string) bool {
	command := prismBin + " hook pretooluse"
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	changed := false
	for _, matcher := range []string{"Bash", "Grep"} {
		entries, _ := hooks["PreToolUse"].([]any)
		already := false
		for _, e := range entries {
			em, ok := e.(map[string]any)
			if !ok || em["matcher"] != matcher {
				continue
			}
			for _, hh := range asSliceAny(em["hooks"]) {
				hm, ok := hh.(map[string]any)
				if ok && hm["command"] == command {
					already = true
				}
			}
		}
		if already {
			continue
		}
		entries = append(entries, map[string]any{
			"matcher": matcher,
			"hooks":   []any{map[string]any{"type": "command", "command": command}},
		})
		hooks["PreToolUse"] = entries
		changed = true
	}
	if changed {
		doc["hooks"] = hooks
	}
	return changed
}

func ensureClaudeCodeApproval(path, serverName, prismBin string, allowTools, denyBuiltinSearch bool) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	var doc map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &doc) //nolint:errcheck
	}
	if doc == nil {
		doc = map[string]any{}
	}

	changed := false

	// 1. Server trust.
	var servers []any
	if s, ok := doc["enabledMcpjsonServers"].([]any); ok {
		servers = s
	}
	if !containsString(servers, serverName) {
		doc["enabledMcpjsonServers"] = append(servers, serverName)
		changed = true
	}

	// 2. Tool auto-allow. Note this runs even when the server was already
	// trusted — the two settings are independent, and an earlier prism
	// version wrote only the trust entry.
	rule := "mcp__" + serverName
	if allowTools {
		perms, _ := doc["permissions"].(map[string]any)
		if perms == nil {
			perms = map[string]any{}
		}
		allow, _ := perms["allow"].([]any)
		if !containsString(allow, rule) {
			perms["allow"] = append(allow, rule)
			doc["permissions"] = perms
			changed = true
		}
		// Deny the built-in text search. Steering does not route tool
		// selection: observed on a correctly installed machine, an agent
		// listed prism's tools, said its CLAUDE.md directed it to use them,
		// and then ran Bash(grep) on the next task. The benchmark said the
		// same at 12:1. The only reliable route is removing the alternative,
		// and it costs nothing: prism_search(scope="text") is a ripgrep
		// passthrough over the whole tree, so nothing becomes unfindable.
		//
		// --no-permissions skips this along with the auto-allow, and the
		// entries are plain settings.json lines a user can delete.
		if denyBuiltinSearch && !containsString(allow, "Grep") { // never deny what the user allowed
			deny, _ := perms["deny"].([]any)
			for _, d := range []string{"Grep", "Bash(grep:*)", "Bash(rg:*)"} {
				if !containsString(deny, d) {
					deny = append(deny, d)
					changed = true
				}
			}
			perms["deny"] = deny
			doc["permissions"] = perms

			// PreToolUse hook: same policy as the deny rule above, but a hook
			// fires BEFORE permission rules are evaluated and, on deny, feeds
			// permissionDecisionReason back to the model as feedback instead of
			// a bare failure — measured (haiku38, 2026-08) that agents recover
			// to prism reliably once told what to do, not just that grep failed.
			// permissions.deny stays as a failsafe: a hook that errors or is
			// unreachable must not silently reopen the gap it exists to close.
			if ensureClaudeCodeHook(doc, prismBin) {
				changed = true
			}
		}
	}

	if !changed {
		return
	}
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
	if allowTools {
		fmt.Printf("approved %s in Claude Code (trusted + %s auto-allowed)\n", serverName, rule)
	} else {
		fmt.Printf("approved %s in Claude Code MCP settings\n", serverName)
	}
}

// containsString reports whether a JSON array decoded as []any holds s.
func containsString(list []any, s string) bool {
	for _, v := range list {
		if str, ok := v.(string); ok && str == s {
			return true
		}
	}
	return false
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
	// Match the MCP path's 10-minute budget (a large monorepo cold index
	// legitimately exceeds 5); PRISM_INDEX_TIMEOUT overrides for bigger repos.
	timeout := 10 * time.Minute
	if v := os.Getenv("PRISM_INDEX_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Store-only fast path: status is three COUNT(*) queries; booting the full
	// engine (newClient -> EnsureRunning) rehydrates the whole graph first —
	// ~1.3s of work on a 500k-edge index that status never reads. Same counts,
	// same output shape, ~5ms.
	client := grove.NewClient("", "").WithTokenFromDir(root)
	res, err := client.QuickStatus(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	printJSON(res)
	return 0
}

func cmdDoctor(args []string) int {
	dir := dirArg(args, 0, ".")
	root := mustAbs(dir)
	_, client, err := newClient(root)
	if err != nil {
		printJSON(map[string]any{
			"status":  "error",
			"version": version.Version,
			"root":    root,
			"error":   err.Error(),
		})
		return 1
	}
	defer client.Shutdown()

	graph, err := client.Status(context.Background())
	if err != nil {
		printJSON(map[string]any{
			"status":  "error",
			"version": version.Version,
			"root":    root,
			"engine":  "grove",
			"error":   err.Error(),
		})
		return 1
	}

	state := "ok"
	warnings := []string{}
	if graph.FilesIndexed == 0 {
		state = "warning"
		warnings = append(warnings, "repository is not indexed; run prism index")
	}
	printJSON(map[string]any{
		"status":   state,
		"version":  version.Version,
		"root":     root,
		"engine":   "grove",
		"index":    graph,
		"warnings": warnings,
		"capabilities": map[string]any{
			"changeImpact":       true,
			"testSelection":      true,
			"sessionDelivery":    true,
			"deliveryCacheScope": "process",
			"qualityContract":    "operation-reported",
			// Which engine backs the merged full-text search (prism_query /
			// prism_search): rg > grep > the built-in scanner. "native" means
			// no external searcher was found — correct everywhere, slower on
			// large repos; install ripgrep to upgrade it.
			"textSearch": textsearch.Backend(),
		},
	})
	return 0
}

func cmdQuery(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism query <task> --terms a,b,c [dir]  (--terms is REQUIRED — guess one keyword from the task)")
		return 2
	}
	task := args[0]
	dir := "."
	profile := ""
	limit := 50
	maxFiles := 0
	delivery := ""
	format := formatText
	var terms []string
	var include []string
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
		case "--terms":
			if i+1 < len(args) {
				for _, t := range strings.Split(args[i+1], ",") {
					if t = strings.TrimSpace(t); t != "" {
						terms = append(terms, t)
					}
				}
				i++
			}
		case "--include":
			if i+1 < len(args) {
				for _, inc := range strings.Split(args[i+1], ",") {
					if inc = strings.TrimSpace(inc); inc != "" {
						include = append(include, inc)
					}
				}
				i++
			}
		case "--depth", "--graph-depth":
			// graph_depth has never been read by any handler; sending it was
			// a silent no-op. Say so instead of pretending it tunes anything.
			fmt.Fprintln(os.Stderr, "query: --depth/--graph-depth has no effect and was removed; expansion is a fixed one-hop call neighborhood")
			return 2
		case "--delivery":
			if i+1 < len(args) {
				switch args[i+1] {
				case "source", "symbols":
					delivery = args[i+1]
				}
				i++
			}
		case "--max-files":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					maxFiles = n
				}
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("query", a)
			}
			dir = a
		}
	}
	invokeArgs := map[string]any{"task": task, "limit": limit}
	if delivery != "" {
		invokeArgs["delivery"] = delivery
	}
	if maxFiles > 0 {
		invokeArgs["max_files"] = maxFiles
	}
	if profile != "" {
		invokeArgs["profile"] = profile
	}
	if len(terms) > 0 {
		invokeArgs["terms"] = terms
	}
	if len(include) > 0 {
		invokeArgs["include"] = include
	}
	out, err := invokeWithPersistentLedger(dir, "prism_query", invokeArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdRead(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism read <file> [dir]")
		return 2
	}
	file := args[0]
	dir := "."
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("read", a)
			}
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_read", map[string]any{"file": file})
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return 1
	}
	printOutput(out, format)
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
	format := formatText
	scope := ""
	regex := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--scope":
			// The steering has documented `prism search <t> --scope text` since
			// v0.37.0 while this parser knew only --limit and --format, so the
			// flag was dropped and every CLI text search silently returned the
			// merged symbol view instead. A Bash-only subagent following its own
			// instructions got the wrong answer shape and no indication why.
			if i+1 < len(args) {
				switch args[i+1] {
				case "text", "symbols", "both":
					scope = args[i+1]
				default:
					fmt.Fprintf(os.Stderr, "search: --scope wants text|symbols|both, got %q\n", args[i+1])
					return 2
				}
				i++
			}
		case "--regex":
			regex = true
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					limit = n
				}
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			// An unknown flag used to be dropped in silence — the mechanism
			// behind this whole class of bug. Fail loudly instead.
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "search: unknown flag %q\n", a)
				return 2
			}
			dir = a
		}
	}
	callArgs := map[string]any{"query": query, "limit": limit}
	if scope != "" {
		callArgs["scope"] = scope
	}
	if regex {
		callArgs["regex"] = true
	}
	out, err := invokeWithPersistentLedger(dir, "prism_search", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdLookup(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name> [dir]")
		return 2
	}
	name := args[0]
	dir := "."
	format := formatText
	fileHint := ""
	var fields []any
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--fields":
			if i+1 < len(args) {
				for _, f := range strings.Split(args[i+1], ",") {
					if f = strings.TrimSpace(f); f != "" {
						fields = append(fields, f)
					}
				}
				i++
			}
		case "--file":
			if i+1 < len(args) {
				fileHint = args[i+1]
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("lookup", a)
			}
			dir = a
		}
	}
	callArgs := map[string]any{"name": name}
	if len(fields) > 0 {
		callArgs["fields"] = fields
	}
	if fileHint != "" {
		callArgs["file"] = fileHint
	}
	out, err := invokeWithPersistentLedger(dir, "prism_lookup", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

// cmdNode is the one-shot orientation view — a symbol's source + neighbours,
// or a file's source + defined symbols + dependents.
func cmdNode(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism node <symbol-or-file> [dir] [--file <path>] [--format text|lean|json]")
		return 2
	}
	name := args[0]
	dir := "."
	format := formatText
	fileHint := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--file":
			if i+1 < len(args) {
				fileHint = args[i+1]
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("node", a)
			}
			dir = a
		}
	}
	callArgs := map[string]any{"name": name}
	if fileHint != "" {
		callArgs["file"] = fileHint
	}
	out, err := invokeWithPersistentLedger(dir, "prism_node", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "node:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdResolve(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism resolve <name> [dir]")
		return 2
	}
	name := args[0]
	dir := "."
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--format" && i+1 < len(args) {
			switch outputFormat(args[i+1]) {
			case formatText, formatLean, formatJSON:
				format = outputFormat(args[i+1])
			}
			i++
		} else if strings.HasPrefix(a, "-") {
			return rejectUnknownFlag("resolve", a)
		} else {
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_resolve", map[string]any{"name": name})
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdEdges(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism edges <name> [--dir out|in|both] [--kinds calls,uses-type,implements,...] [dir]")
		return 2
	}
	name := args[0]
	dir := "."
	direction := "both"
	var kinds []any
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir", "--direction":
			if i+1 < len(args) {
				direction = args[i+1]
				i++
			}
		case "--kinds":
			if i+1 < len(args) {
				for _, k := range strings.Split(args[i+1], ",") {
					if k = strings.TrimSpace(k); k != "" {
						kinds = append(kinds, k)
					}
				}
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("edges", a)
			}
			dir = a
		}
	}
	callArgs := map[string]any{"name": name, "direction": direction}
	if len(kinds) > 0 {
		callArgs["kinds"] = kinds
	}
	out, err := invokeWithPersistentLedger(dir, "prism_edges", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "edges:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdReferences(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism references <name> [dir]")
		return 2
	}
	name := args[0]
	dir := "."
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("references", a)
			}
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_references", map[string]any{"name": name})
	if err != nil {
		fmt.Fprintln(os.Stderr, "references:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdChangeImpact(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism change-impact <Type.method | bare-name | file:line> [dir]")
		fmt.Fprintln(os.Stderr, "  query: Type.method or Type.method(ParamType, ...)")
		return 2
	}
	query := args[0]
	dir := "."
	format := formatJSON
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("change-impact", a)
			}
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_change_impact", map[string]any{"query": query})
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdRenamePlan(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prism rename-plan <Type.method | bare-name | file:line> <NewName> [dir]")
		fmt.Fprintln(os.Stderr, "  query: Type.method or Type.method(ParamType, ...)")
		return 2
	}
	query, newName := args[0], args[1]
	dir := "."
	format := formatJSON
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("rename-plan", a)
			}
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_rename_plan",
		map[string]any{"query": query, "newName": newName})
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("rename-plan", err))
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdMissingImplementations(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism missing-implementations <Type.method | bare-name | file:line> [dir]")
		fmt.Fprintln(os.Stderr, "  query: Type.method or Type.method(ParamType, ...)")
		return 2
	}
	query := args[0]
	dir := "."
	format := formatJSON
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("missing-implementations", a)
			}
			dir = a
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_missing_implementations", map[string]any{"query": query})
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("missing-implementations", err))
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdDeadCode(args []string) int {
	dir := "."
	format := formatJSON
	var roots []any
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--roots":
			if i+1 < len(args) {
				for _, r := range strings.Split(args[i+1], ",") {
					if r = strings.TrimSpace(r); r != "" {
						roots = append(roots, r)
					}
				}
				i++
			}
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
				}
				i++
			}
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("dead-code", a)
			}
			dir = a
		}
	}
	callArgs := map[string]any{}
	if len(roots) > 0 {
		callArgs["roots"] = roots
	}
	out, err := invokeWithPersistentLedger(dir, "prism_dead_code", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dead-code:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

// grepCommandPattern matches a grep/rg invocation as a Bash command: at the
// start of the string or after a shell separator, an optional path prefix
// (/usr/bin/grep), an optional sudo, then rg or grep as a whole word. Ported
// from research/harness/swebench_ab.py's denied_search_attempts pattern,
// which was tuned twice against real false positives (a bare substring match
// hit the harness's own /tmp/...grepwarn dir name; a bare word-boundary
// match missed /usr/bin/grep).
var grepCommandPattern = regexp.MustCompile(`(?:^|[\s;&|(])(?:[^\s;&|(]*/)?(?:sudo\s+)?(?:rg|grep)\b`)

const grepHookReason = "grep/rg are blocked in this project. Use prism_search(scope=\"text\") " +
	"(MCP tool, already loaded) for the equivalent ripgrep pass, or if you are already in a " +
	"shell, `prism search <query> --format text` (CLI, same process, no extra round trip)."

// hookDenyReason returns the permissionDecisionReason for a PreToolUse call,
// or "" if it should proceed uninterrupted. Scope is deliberately narrow:
// only grep/rg (the Grep tool, or grep/rg run via Bash) is ever denied here.
// python and every other Bash use passes through untouched — python is a
// real, general-purpose tool the agent needs for tests and repro scripts,
// and pattern-matching "python reimplementing a file read" reliably enough
// to avoid false-positiving on legitimate scripts is not solved here.
func hookDenyReason(toolName string, toolInput map[string]any) string {
	switch toolName {
	case "Grep":
		return grepHookReason
	case "Bash":
		cmd, _ := toolInput["command"].(string)
		if grepCommandPattern.MatchString(cmd) {
			return grepHookReason
		}
	}
	return ""
}

// cmdHook implements the Claude Code PreToolUse hook protocol: read the
// event JSON from stdin, and on a match, write a hookSpecificOutput deny
// decision so Claude Code cancels the call and feeds the reason back to the
// model as feedback — richer than a bare permissions.deny failure, which
// remains in place as a failsafe in case this hook is unreachable (binary
// moved, PATH broken, etc.).
func cmdHook(args []string) int {
	if len(args) < 1 || args[0] != "pretooluse" {
		fmt.Fprintln(os.Stderr, "hook: usage: prism hook pretooluse (reads Claude Code's PreToolUse JSON on stdin)")
		return 2
	}
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		// Malformed/empty input must not block the tool call it exists to
		// police — silently allow rather than fail closed on a parse error.
		return 0
	}
	reason := hookDenyReason(in.ToolName, in.ToolInput)
	if reason == "" {
		return 0
	}
	printJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
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
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("feedback", a)
			}
			dir = a
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

func cmdDrift(args []string) int {
	dir := dirArg(args, 0, ".")
	out, err := invokeWithPersistentLedger(dir, "prism_drift", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
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
	port := 0 // resolved after config load: flag > prism.yaml port > 8888
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
	if port == 0 { // no --port flag: prism.yaml port, then the documented 8888
		port = cfg.Port
		if port == 0 {
			port = 8888
		}
	}
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

// requireDir rejects roots that do not exist as directories. Opening the
// engine creates <root>/.grove, so a mistyped CLI argument in the dir
// position (`prism edges --name routeService` put "routeService" there)
// used to CREATE and auto-index a stray directory instead of erroring.
func requireDir(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("directory does not exist: %s (a flag or symbol name in the dir position?)", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", root)
	}
	return nil
}

func newClient(dir string) (*config.Config, *grove.Client, error) {
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		return nil, nil, err
	}
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
	timing := os.Getenv("PRISM_TIMING") != ""
	tInv := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing] cli:%-19s %8.0fms\n", stage, float64(time.Since(tInv).Milliseconds()))
		}
	}
	root := mustAbs(dir)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, err
	}
	stamp("newClient")
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		return nil, err
	}
	stamp("autoIndex")

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

		// The lock serializes standalone CLI processes that share the savings
		// ledger. Delivery caches remain scoped to real MCP conversations.
		h := mcp.NewHandlerWithLedger(cfg, root, client, ledger)
		out, invokeErr = h.Invoke(tool, args)
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
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
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

// printOutput prints v in the requested format.
// JSON round-trips through map[string]any so both typed structs (queryResult)
// and plain maps are handled uniformly by the text/lean formatters.
// printTextMatches renders the merged full-text section of a prism_query /
// prism_search response: per-file matched lines, cached files as line
// numbers only.
func printTextMatches(m map[string]any) {
	text, _ := m["textMatches"].(string)
	if text == "" {
		text, _ = m["textHits"].(string)
	}
	if text == "" {
		return
	}
	backend, _ := m["textBackend"].(string)
	fmt.Printf("// text matches (%s):\n", backend)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Printf("//   %s\n", line)
	}
}

func printOutput(v any, format outputFormat) {
	if format == formatJSON || format == "" {
		printJSON(v)
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		printJSON(v)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		printJSON(v)
		return
	}
	switch format {
	case formatText:
		printTextOutput(m)
	case formatLean:
		printLeanOutput(m)
	default:
		printJSON(v)
	}
}

// printTextOutput renders a Prism response as plain text for agent consumption.
// Handles prism_query, prism_read, prism_search, and prism_lookup responses.
func printTextOutput(m map[string]any) {
	// prism_node: source PLUS the orientation payload. Must come first — its
	// shape overlaps prism_lookup's (symbol+content) and prism_read's
	// (file+content), so without this branch both node views fell through and
	// the edges / defines / dependents were silently discarded.
	if view, ok := m["view"].(string); ok && (view == "symbol" || view == "file") {
		printNodeText(m, view)
		return
	}
	// Unmatched lookup/node: a bare {"symbol": null} rendered as JSON told
	// the caller nothing. Print the note and the "did you mean" list.
	if matched, present := m["matched"].(bool); present && !matched {
		if _, hasContent := m["content"]; !hasContent {
			if note, _ := m["note"].(string); note != "" {
				fmt.Println("// " + note)
			} else {
				fmt.Printf("// no match for %v\n", m["name"])
			}
			for _, c := range asSliceAny(m["candidates"]) {
				fmt.Printf("  %v\n", c)
			}
			return
		}
	}
	// prism_lookup: top-level "content" + "symbol" subkey
	if sym, hasSym := m["symbol"].(map[string]any); hasSym && sym != nil {
		if content, ok := m["content"].(string); ok {
			name, _ := sym["name"].(string)
			fp, _ := sym["filePath"].(string)
			fmt.Printf("// %s — %s\n", fp, name)
			fmt.Print(content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
			return
		}
	}
	// prism_read: top-level "content" + "file" key
	if content, ok := m["content"].(string); ok {
		file, _ := m["file"].(string)
		strategy, _ := m["strategy"].(string)
		if strategy == "sha-pointer" {
			fmt.Printf("// %s [cached — use previous read]\n", file)
		} else {
			if file != "" {
				fmt.Printf("// %s\n", file)
			}
			fmt.Print(content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
		}
		return
	}
	// prism_query and prism_search: "symbols" array
	if rawSyms, ok := m["symbols"]; ok {
		syms, _ := rawSyms.([]any)
		for _, s := range syms {
			sym, ok := s.(map[string]any)
			if !ok {
				continue
			}
			fp, _ := sym["filePath"].(string)
			name, _ := sym["name"].(string)
			category, _ := sym["category"].(string)
			content, _ := sym["content"].(string)
			if content == "" {
				content, _ = sym["rawText"].(string)
			}
			if fp != "" && name != "" {
				if category != "" {
					fmt.Printf("// %s — %s [%s]\n", fp, name, category)
				} else {
					fmt.Printf("// %s — %s\n", fp, name)
				}
			}
			if content != "" {
				fmt.Print(content)
				if !strings.HasSuffix(content, "\n") {
					fmt.Println()
				}
				fmt.Println()
			}
		}
		// Merged full-text hits (prism_query: "textMatches"; prism_search:
		// "textHits") — matches outside any indexed symbol.
		printTextMatches(m)
		if note, _ := m["note"].(string); note != "" {
			fmt.Println("// " + note)
		}
		return
	}
	// prism_search scope="text" ("pure grep"): no "symbols" key at all, just
	// textHits/textBackend/truncated(/resolvedNote/timedOut) — the branch
	// above only fires when "symbols" is present, so this shape fell
	// through all the way to the JSON fallback, silently. --format text
	// documented (steering, README, the hook's own deny message) as the
	// zero-extra-round-trip path for exactly this call; it never rendered
	// as text.
	if _, hasSymbols := m["symbols"]; !hasSymbols {
		if _, hasTextHits := m["textHits"]; hasTextHits {
			printTextMatches(m)
			if w, _ := m["warning"].(string); w != "" {
				fmt.Println("// " + w)
			}
			if n, _ := m["resolvedNote"].(string); n != "" {
				fmt.Println("// " + n)
			}
			return
		}
	}
	// prism_lookup with --fields: projected columns (name/file/line + selected),
	// no "content"/"symbol". Render the requested columns compactly.
	if _, hasContent := m["content"]; !hasContent {
		if _, hasSymbols := m["symbols"]; !hasSymbols {
			if _, hasCands := m["candidates"]; !hasCands {
				if file, ok := m["file"].(string); ok {
					if _, hasName := m["name"]; hasName {
						fmt.Printf("// %v — %s:%d\n", m["name"], file, jsonInt(m["line"]))
						for _, col := range []string{"kind", "signature", "doc", "modifiers", "parent", "body"} {
							if v, ok := m[col]; ok {
								fmt.Printf("%s: %v\n", col, v)
							}
						}
						return
					}
				}
			}
		}
	}
	// prism_resolve: "candidates" list of {name, kind, file, line, testDouble}
	if rawCands, ok := m["candidates"].([]any); ok {
		name, _ := m["name"].(string)
		fmt.Printf("// %s — %d candidate(s)\n", name, len(rawCands))
		for _, c := range rawCands {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			tag := ""
			if td, _ := cm["testDouble"].(bool); td {
				tag = "  [test double]"
			}
			fmt.Printf("  %v  %v  %v:%v%s\n", cm["name"], cm["kind"], cm["file"], jsonInt(cm["line"]), tag)
		}
		return
	}
	// prism_edges: "edges" map of "<kind> <direction>" -> {shown, total, symbols}
	if rawEdges, ok := m["edges"].(map[string]any); ok {
		name, _ := m["name"].(string)
		fmt.Printf("// %s — graph edges\n", name)
		rels := make([]string, 0, len(rawEdges))
		for r := range rawEdges {
			rels = append(rels, r)
		}
		sort.Strings(rels)
		for _, r := range rels {
			g, _ := rawEdges[r].(map[string]any)
			shown, total := jsonInt(g["shown"]), jsonInt(g["total"])
			cap := ""
			if total > shown {
				cap = fmt.Sprintf(" (showing %d of %d)", shown, total)
			}
			fmt.Printf("%s%s:\n", r, cap)
			syms, _ := g["symbols"].([]any)
			for _, s := range syms {
				sm, ok := s.(map[string]any)
				if !ok {
					continue
				}
				tag := ""
				if td, _ := sm["testDouble"].(bool); td {
					tag = "  [test double]"
				}
				fmt.Printf("  %v  %v:%v%s\n", sm["name"], sm["file"], jsonInt(sm["line"]), tag)
			}
		}
		return
	}
	// The four task-shaped ops. Their responses have no "content"/"symbols"
	// key, so they used to fall straight through to printJSON — meaning
	// `--format text` was a documented no-op on exactly the commands the
	// Bash-only playbook tells agents to run that way.
	if _, ok := m["declarations"]; ok {
		printChangeImpactText(m)
		return
	}
	if _, ok := m["edits"]; ok {
		printRenamePlanText(m)
		return
	}
	if _, ok := m["missing"]; ok {
		printMissingImplText(m)
		return
	}
	if _, ok := m["dead"]; ok {
		printDeadCodeText(m)
		return
	}
	// The unified task op (prepare/verify). Its prepare shape carries the
	// whole context payload under "read", which is already markdown — text
	// mode should print it, not re-encode it as a JSON string with escaped
	// newlines.
	if mode, ok := m["mode"].(string); ok && (mode == "prepare" || mode == "verify") {
		printTaskText(m, mode)
		return
	}
	// prism_references: "byFile" map of file -> [{line, in}]
	if rawByFile, ok := m["byFile"].(map[string]any); ok {
		name, _ := m["name"].(string)
		count := jsonInt(m["count"])
		defs := jsonInt(m["definitions"])
		ambiguous, _ := m["ambiguous"].(bool)
		tier := "unambiguous"
		if ambiguous {
			tier = fmt.Sprintf("ambiguous — %d definitions share this name", defs)
		}
		fmt.Printf("// %s — %d references (%s)\n", name, count, tier)
		files := make([]string, 0, len(rawByFile))
		for f := range rawByFile {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			refs, _ := rawByFile[f].([]any)
			fmt.Printf("%s\n", f)
			for _, r := range refs {
				ref, ok := r.(map[string]any)
				if !ok {
					continue
				}
				line := jsonInt(ref["line"])
				if in, ok := ref["in"].(string); ok && in != "" {
					fmt.Printf("  %d  in %s\n", line, in)
				} else {
					fmt.Printf("  %d\n", line)
				}
			}
		}
		return
	}
	// Fallback: JSON
	printJSON(m)
}

// ─── task-shaped renderers ───────────────────────────────────────────────────
//
// These render the complete set, never a truncated one: the whole point of
// change-impact and friends is that the returned sites ARE every site, so an
// elided text view would misrepresent the one property the command sells.

// siteLine renders one change-set entry as "qualifiedName  file:line".
func siteLine(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := m["qualifiedName"].(string)
	if name == "" {
		name, _ = m["name"].(string)
	}
	line := fmt.Sprintf("  %s  %v:%d", name, m["filePath"], jsonInt(m["line"]))
	if via, _ := m["via"].(string); via != "" {
		line += "  (via " + via + ")"
	}
	return line
}

// printSiteGroup prints a labelled group, skipping empty ones.
func printSiteGroup(label string, v any) {
	items, _ := v.([]any)
	if len(items) == 0 {
		return
	}
	fmt.Printf("%s (%d):\n", label, len(items))
	for _, it := range items {
		if l := siteLine(it); l != "" {
			fmt.Println(l)
		}
	}
}

// printNotes emits the advisory keys (completeness, warnings, notes) that
// carry the caveats a caller must not silently drop.
func printNotes(m map[string]any, keys ...string) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				fmt.Printf("%s: %s\n", k, v)
			}
		case bool:
			if v {
				fmt.Printf("%s: true\n", k)
			}
		case []any:
			if len(v) > 0 {
				parts := make([]string, 0, len(v))
				for _, e := range v {
					parts = append(parts, fmt.Sprint(e))
				}
				fmt.Printf("%s: %s\n", k, strings.Join(parts, ", "))
			}
		}
	}
}

func printChangeImpactText(m map[string]any) {
	fmt.Printf("// %v — change-impact: %d site(s)\n", m["query"], jsonInt(m["totalSites"]))
	printNotes(m, "completeness")
	printSiteGroup("declarations", m["declarations"])
	printSiteGroup("supers", m["supers"])
	printSiteGroup("family", m["family"])
	printSiteGroup("declaringTypes", m["declaringTypes"])
	printSiteGroup("callers", m["callers"])
	printNotes(m, "declaringTypesNote", "externalSupers", "overridesExternal", "warning")
	if hint, ok := m["widerAnchor"].(map[string]any); ok {
		fmt.Printf("widerAnchor: %v\n", hint["message"])
	}
}

func printRenamePlanText(m map[string]any) {
	fmt.Printf("// %v → %v — rename-plan: %d site(s)\n", m["query"], m["newName"], jsonInt(m["totalSites"]))
	printNotes(m, "completeness")
	printEditGroup("edits", m["edits"])
	printEditGroup("ambiguous", m["ambiguous"])
	printSiteGroup("unresolved", m["unresolved"])
	printNotes(m, "ambiguousNote", "unresolvedNote", "externalSupers", "overridesExternal", "warning")
}

// printEditGroup renders rename edits as file:line with the before/after
// pair, which is what makes the plan reviewable without re-reading the JSON.
func printEditGroup(label string, v any) {
	items, _ := v.([]any)
	if len(items) == 0 {
		return
	}
	fmt.Printf("%s (%d):\n", label, len(items))
	for _, it := range items {
		e, ok := it.(map[string]any)
		if !ok {
			continue
		}
		fmt.Printf("  %v:%d\n", e["filePath"], jsonInt(e["line"]))
		if before, ok := e["before"].(string); ok {
			fmt.Printf("    - %s\n", before)
		}
		if after, ok := e["after"].(string); ok {
			fmt.Printf("    + %s\n", after)
		}
	}
}

func printMissingImplText(m map[string]any) {
	fmt.Printf("// %v — missing-implementations (%d type(s) already implement)\n",
		m["query"], jsonInt(m["implementedCount"]))
	printSiteGroup("contract", m["contract"])
	printSiteGroup("missing", m["missing"])
	printSiteGroup("abstractMissing", m["abstractMissing"])
	printSiteGroup("unverifiable", m["unverifiable"])
	printNotes(m, "unverifiableNote", "defaultProvided", "note")
}

func printDeadCodeText(m map[string]any) {
	fmt.Printf("// dead-code — %d considered, %d reachable from %d root(s)\n",
		jsonInt(m["considered"]), jsonInt(m["reachableCount"]), jsonInt(m["rootCount"]))
	printSiteGroup("dead", m["dead"])
	printSiteGroup("exportedUnreferenced", m["exportedUnreferenced"])
	printNotes(m, "caveats")
}

func printTaskText(m map[string]any, mode string) {
	fmt.Printf("// %v — %s\n", m["task"], mode)
	if read, ok := m["read"].(map[string]any); ok {
		if c, _ := read["content"].(string); c != "" {
			fmt.Println(c)
			if !strings.HasSuffix(c, "\n") {
				fmt.Println()
			}
		}
	}
	if obs, _ := m["obligations"].([]any); len(obs) > 0 {
		fmt.Printf("obligations (%d):\n", len(obs))
		for _, raw := range obs {
			ob, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fmt.Printf("  %v  %v:%d  — %d site(s), completeness %v\n",
				ob["qualifiedName"], ob["file"], jsonInt(ob["line"]),
				jsonInt(ob["siteCount"]), ob["completeness"])
			for _, s := range asSliceAny(ob["sites"]) {
				site, ok := s.(map[string]any)
				if !ok {
					continue
				}
				fmt.Printf("      %v  %v:%d\n", site["symbol"], site["file"], jsonInt(site["line"]))
			}
		}
	}
	// verify carries the gate's own findings; reuse the verify renderer so
	// the two commands do not drift into two descriptions of one verdict.
	if _, ok := m["verdict"]; ok {
		renderVerifyText(m)
	}
	printNotes(m, "obligationsNote", "obligationsBaseNote", "unaddressedCaveat", "changedFilesNote", "next")
}

// jsonInt coerces a JSON number (float64 after round-trip) or int to int.
func jsonInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// printLeanOutput strips metadata fields (scores, spans, IDs, timing) and
// emits compact JSON with only the fields agents actually use.
func printLeanOutput(m map[string]any) {
	// Task-shaped ops (change-impact, rename-plan, missing-implementations,
	// dead-code) return purpose-built maps with no metadata to strip; lean
	// used to reduce them to {} — pass them through.
	known := false
	for _, k := range []string{"symbols", "symbol", "file", "content"} {
		if _, ok := m[k]; ok {
			known = true
			break
		}
	}
	if !known {
		b, err := json.Marshal(m)
		if err == nil {
			fmt.Println(string(b))
			return
		}
	}
	lean := map[string]any{}
	if _, hasSyms := m["symbols"]; !hasSyms {
		// prism_read: keep content + identity fields
		for _, k := range []string{"file", "strategy", "content", "originalTokens", "deliveredTokens", "savingsPercent"} {
			if v, ok := m[k]; ok {
				lean[k] = v
			}
		}
		// prism_lookup: keep minimal symbol identity
		if sym, ok := m["symbol"].(map[string]any); ok && sym != nil {
			lean["symbol"] = map[string]any{
				"name":     sym["name"],
				"filePath": sym["filePath"],
			}
		}
		if content, ok := m["content"]; ok {
			lean["content"] = content
		}
	} else {
		// prism_query or prism_search
		if bu, ok := m["budgetUsed"]; ok {
			lean["budgetUsed"] = bu
		}
		if rawSyms, ok := m["symbols"]; ok {
			syms, _ := rawSyms.([]any)
			leanSyms := make([]any, 0, len(syms))
			for _, s := range syms {
				sym, ok := s.(map[string]any)
				if !ok {
					continue
				}
				content, _ := sym["content"].(string)
				if content == "" {
					content, _ = sym["rawText"].(string)
				}
				leanSyms = append(leanSyms, map[string]any{
					"filePath": sym["filePath"],
					"name":     sym["name"],
					"category": sym["category"],
					"content":  content,
				})
			}
			lean["symbols"] = leanSyms
		}
		if rawGaps, ok := m["coverageGaps"]; ok {
			lean["coverageGaps"] = rawGaps
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(lean)
}

// cmdAssist runs the model-agnostic harness: a natural-language task routed by
// any chat model (local Ollama / Anthropic / OpenAI) to the deterministic task
// ops. No steering files: the harness owns tool exposure, renders every result
// itself (the model never relays payloads — relay fidelity by construction),
// and optionally applies rename edits + runs a verify command. If a `shale`
// binary is present, the session emits an evidence trail (intent/note/done).
func cmdAssist(args []string) int {
	dir := "."
	model := ""
	apply := false
	applyAmbiguous := false
	verify := ""
	var taskParts []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--apply":
			apply = true
		case "--apply-ambiguous":
			apply = true
			applyAmbiguous = true
		case "--verify":
			if i+1 < len(args) {
				verify = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		default:
			taskParts = append(taskParts, a)
		}
	}
	task := strings.TrimSpace(strings.Join(taskParts, " "))
	if task == "" {
		fmt.Fprintln(os.Stderr, `usage: prism assist [--model <spec>] [--apply] [--verify "<cmd>"] [--dir <root>] "<task>"
  model specs: ollama:<tag> | claude:<model> | openai:<model>  (default: auto-detect)`)
		return 2
	}
	if model == "" {
		detected, err := assist.DetectDefaultModel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "assist:", err)
			return 1
		}
		model = detected
	}
	provider, err := assist.NewProvider(model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}

	root := mustAbs(dir)
	cfg, client, err := newClient(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}

	// One handler for the whole session: ops record into the persistent
	// ledger exactly as individual CLI invocations do.
	ledgerFile := ledgerPathForRoot(root)
	ledger, lerr := session.LoadLedger(ledgerFile)
	if lerr != nil {
		ledger = session.NewLedger(time.Now().Format("20060102-150405"))
	}
	h := mcp.NewHandlerWithLedger(cfg, root, client, ledger)
	defer func() {
		_ = h.Ledger.Save(ledgerFile)
	}()

	fmt.Printf("assist: %s @ %s\n", provider.Name(), root)
	_, err = assist.Run(task, provider, h.Invoke, assist.Options{
		Model: model, Apply: apply, ApplyAmbiguous: applyAmbiguous, Verify: verify, Root: root,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}
	return 0
}

// prefixOnce labels an error with its operation without stuttering. Grove
// already prefixes these errors ("change-impact: query must be…"), so the
// naive `Fprintln("change-impact:", err)` printed the label twice — three
// times before the MCP layer stopped re-wrapping too.
func prefixOnce(op string, err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, op+":") {
		return msg
	}
	return op + ": " + msg
}

// printNodeText renders `prism node` in text form: the source, then the
// orientation payload the JSON carries. Without this the node views matched
// prism_lookup's and prism_read's branches and their whole point — neighbours
// for a symbol, defines/dependents for a file — was dropped.
func printNodeText(m map[string]any, view string) {
	if note, _ := m["note"].(string); note != "" {
		fmt.Printf("// %s\n", note)
	}
	if cands := asSliceAny(m["candidates"]); len(cands) > 0 {
		fmt.Println("// ambiguous — candidates:")
		for _, c := range cands {
			fmt.Printf("//   %v\n", c)
		}
		return
	}

	if view == "symbol" {
		if sym, ok := m["symbol"].(map[string]any); ok && sym != nil {
			name, _ := sym["name"].(string)
			fp, _ := sym["filePath"].(string)
			fmt.Printf("// %s — %s\n", fp, name)
		}
		if content, ok := m["content"].(string); ok && content != "" {
			fmt.Print(content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
		}
		printNodeEdges(m)
		return
	}

	// File view.
	file, _ := m["file"].(string)
	if strategy, _ := m["strategy"].(string); strategy == "sha-pointer" {
		fmt.Printf("// %s [cached — use previous read]\n", file)
	} else {
		if file != "" {
			fmt.Printf("// %s\n", file)
		}
		if content, ok := m["content"].(string); ok && content != "" {
			fmt.Print(content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
		}
	}
	if defs := asSliceAny(m["defines"]); len(defs) > 0 {
		fmt.Printf("\n// defines (%d):\n", len(defs))
		for _, d := range defs {
			dm, _ := d.(map[string]any)
			if dm == nil {
				continue
			}
			fmt.Printf("//   %v  (%v:%v)\n", dm["name"], file, dm["line"])
		}
	}
	deps := asSliceAny(m["dependents"])
	fmt.Printf("\n// dependents (%d):\n", len(deps))
	if len(deps) == 0 {
		fmt.Println("//   (none — no indexed file references this one)")
	}
	for _, d := range deps {
		fmt.Printf("//   %v\n", d)
	}
}

// printNodeEdges renders the neighbour menu of a symbol node view.
func printNodeEdges(m map[string]any) {
	edges, _ := m["edges"].(map[string]any)
	if len(edges) == 0 {
		fmt.Println("\n// neighbours: (none — a leaf in the current graph)")
		return
	}
	groups := make([]string, 0, len(edges))
	for k := range edges {
		groups = append(groups, k)
	}
	sort.Strings(groups)
	fmt.Println("\n// neighbours:")
	for _, g := range groups {
		gm, _ := edges[g].(map[string]any)
		if gm == nil {
			continue
		}
		syms := asSliceAny(gm["symbols"])
		names := make([]string, 0, len(syms))
		for _, s := range syms {
			sm, _ := s.(map[string]any)
			if sm == nil {
				continue
			}
			names = append(names, fmt.Sprintf("%v", sm["name"]))
		}
		total := gm["total"]
		shown := len(names)
		const cap = 12
		if shown > cap {
			names = names[:cap]
		}
		line := strings.Join(names, ", ")
		if tn, ok := total.(float64); ok && int(tn) > len(names) {
			line += fmt.Sprintf(", … (+%d more)", int(tn)-len(names))
		}
		fmt.Printf("//   %s (%v): %s\n", g, total, line)
	}
}

// rejectUnknownFlag is the shared guard every command parser calls from its
// default case. Silently dropping an unrecognized flag is the mechanism
// behind two shipped bugs (`--format` ignored by map/cycles, `--scope`
// ignored by search): the command runs, does something other than what was
// asked, and gives no indication why. 18 of 19 parsers still did this after
// the audit; now none do.
func rejectUnknownFlag(cmd, flag string) int {
	fmt.Fprintf(os.Stderr, "%s: unknown flag %q (see prism help)\n", cmd, flag)
	return 2
}

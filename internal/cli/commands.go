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
	"sort"
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

// outputFormat controls how CLI results are printed.
type outputFormat string

const (
	formatText outputFormat = "text"
	formatLean outputFormat = "lean"
	formatJSON outputFormat = "json"
)

const helpText = `prism - token-optimized context delivery for AI agents (requires Grove)

Usage:
  prism init [--global] [dir]     Write prism.yaml + register MCP with detected AI tools
                                  --global writes to user-level config (~/.claude, ~/.cursor, etc.)
  prism install [--global] [dir]  Alias for 'prism init'
  prism index [dir]               Index codebase via Grove (delta-aware)
  prism status [dir]              Show graph stats from Grove
  prism query <task> [dir]        Find ranked context for a task
                                  --terms a,b,c      Anchor on specific symbol names (grep-precision)
                                  --include a,b      Categories: graph,tests,docs,coverage_gaps (default: graph,tests)
                                  --depth N          BFS hops for graph expansion (default: 2)
                                  --format text|lean|json  Output format (default: text)
  prism read <file> [dir]         Read file with compression
                                  --format text|lean|json  Output format (default: text)
  prism search <keyword> [dir]    Search symbols by keyword
                                  --format text|lean|json  Output format (default: text)
  prism lookup <name> [dir]       Show full source for a symbol
                                  --format text|lean|json  Output format (default: text)
  prism references <name> [dir]   Find where a symbol is USED (every code occurrence,
                                  comments/strings excluded), grouped by file
                                  --format text|lean|json  Output format (default: text)
  prism change-impact <query> [dir]  Deterministic change-set for a method signature change:
                                  declaration(s), override/implementation family (subtype
                                  closure), super-declarations, and all resolved callers.
                                  query format: Type.method or Type.method(ParamType, ...)
                                  --format text|lean|json  Output format (default: json)
  prism rename-plan <query> <NewName> [dir]     Change-set as line edits with substitutions
  prism missing-implementations <query> [dir]  Types claiming the contract that do NOT
                                  implement Type.method (missing / abstract / unverifiable)
                                  — the interface-evolution companion to change-impact
                                  --format text|lean|json  Output format (default: json)
  prism untested-surface <query> [dir]  Change-set partitioned by covering-test
                                  evidence: covered (test within 3 caller hops) vs untested
                                  --format text|lean|json  Output format (default: json)
  prism dead-code [dir] [--roots a,b]  Unreachable production functions/methods
                                  (precision-first; relay the caveats)
                                  --format text|lean|json  Output format (default: json)
  prism compact [dir]             Compress conversation JSON from stdin
  prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
                                  Submit quality feedback for a Prism result
  prism serve [--port 8888] [dir] Start MCP+HTTP server
  prism mcp [dir]                 Start MCP server on stdio
  prism savings [dir]             Show session savings dashboard
  prism drift [dir]              Report files/symbols that changed since they were delivered this session
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
	case "untested-surface":
		return cmdUntestedSurface(rest)
	case "dead-code":
		return cmdDeadCode(rest)
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
	}
	fmt.Fprintln(os.Stderr, "unknown command:", cmd)
	fmt.Print(helpText)
	return 2
}

// --- per-command implementations ---------------------------------------

func cmdInit(args []string) int {
	// Flags: --global (write to ~/.config/... instead of project dir)
	// --mode mcp|cli|both  (skip interactive prompt)
	global := false
	mode := ""
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--global":
			global = true
		case "--mode":
			if i+1 < len(args) {
				switch args[i+1] {
				case config.AgentModeMCP, config.AgentModeCLI, config.AgentModeBoth:
					mode = args[i+1]
				}
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

	// If mode not set by flag, prompt interactively (or default to "both" if
	// stdin is not a terminal, e.g. in CI or when piped).
	if mode == "" {
		mode = promptAgentMode()
	}

	// 1. Write prism.yaml into the project. Grove is embedded in-process now,
	// so the file no longer needs grove_url / grove_binary.
	yaml := fmt.Sprintf(`version: 1
# model: auto  # Prism detects the active model from the MCP initialize handshake.
#               # Override here only if auto-detection fails, e.g.:
#               # model: "claude-sonnet-4-6"
profile: "%s"
agent_mode: "%s"
`, cfg.Profile, mode)
	prismYAML := filepath.Join(abs, "prism.yaml")
	if err := os.WriteFile(prismYAML, []byte(yaml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("wrote", prismYAML)

	// 2. Detect the prism binary path for use in MCP configs.
	prismBin := detectSelfPath()

	// 3. Write steering instructions matching the chosen mode.
	writeSteeringInstructions(abs, mode)

	// 4. Register with every detected AI coding tool.
	registered := initRegisterMCPTools(abs, prismBin, global)
	if len(registered) == 0 {
		fmt.Println("tip: add prism to your AI tool's MCP config (see README)")
	}
	return 0
}

// promptAgentMode asks the user which agent interface to use.
// Returns "both" if stdin is not a terminal or the user presses Enter.
func promptAgentMode() string {
	// Detect non-interactive stdin (pipe, file, CI).
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return config.AgentModeBoth
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "How will agents access Prism?")
	fmt.Fprintln(os.Stderr, "  [1] both  — MCP tools (primary) + CLI fallback for subagents  (default)")
	fmt.Fprintln(os.Stderr, "  [2] mcp   — MCP tools only  (prism_query, prism_read, …)")
	fmt.Fprintln(os.Stderr, "  [3] cli   — CLI via Bash only  (prism query --format text, …)")
	fmt.Fprint(os.Stderr, "Choice [1]: ")
	var line string
	fmt.Scanln(&line)
	switch strings.TrimSpace(line) {
	case "2", "mcp":
		return config.AgentModeMCP
	case "3", "cli":
		return config.AgentModeCLI
	default:
		return config.AgentModeBoth
	}
}

// steeringInstructionsMCP is injected when agent_mode is "mcp" or "both".
const steeringInstructionsMCP = `
## Prism — context delivery (ALWAYS use these tools)

Prism answers whole-task questions (change impact, missing implementations,
test gaps, dead code) in ONE deterministic call, and delivers code context
cheaply. Three layers, in priority order.

### 1. Changing or auditing code? One call answers the whole task

| Situation | Tool |
|---|---|
| Renaming/changing a method signature | prism_change_impact(query="Type.method(ParamType, ...)") — ONE call: declaration + all overrides + all resolved callers |
| Adding/changing a method on an interface or base class | prism_change_impact — finds the complete override family + every caller |
| Renaming a class, struct, or type | prism_change_impact(query="Type.method") for each public method — finds all usages |
| Deprecating a symbol (need all callers to migrate) | prism_change_impact — complete call-site list in one call |
| ANY task that says "find all X" for a specific method | prism_change_impact first, before any grep |
| Renaming a method and you want the edits, not just the sites | prism_rename_plan(query="Type.method", newName="newName") — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | prism_missing_implementations(query="Type.method") — every type in the closure with no implementation |
| "What should I test before changing X?" / test-gap audit / symbols with no tests | prism_untested_surface(query="Type.method") — the change-set split covered/untested |
| Cleanups, library extraction, "is X still used / can I delete it?" at scale | prism_dead_code — unreachable production symbols, safe-to-delete list + caveats |

**Pre-task rule:** before writing any code on a task that involves changing or
renaming an existing symbol, call prism_change_impact FIRST — even if the change
looks small. Small changes can have large blast radii through inheritance and
indirect callers that grep will not find. Result groups: declarations + family
(every override/implementation) + callers = every site that must change. Param
types are optional ("Type.method" works) but improve precision on overloaded names.

Check the result's completeness field. "closed" means the set is authoritative.
"project-local" with overridesExternal means the method belongs to an external
(JDK/dependency) contract: do NOT change its signature — that breaks a contract
this project does not own — and the set covers project code only. To sweep every
project implementation of an external interface (migration/deprecation), query
the external type directly: prism_change_impact(query="Iterator.next").

Relay rule: the result is deterministic and type-resolved. Do NOT re-verify,
re-filter, dedup, or transform it through grep/sed/awk/scripts — re-processing
a solved traversal drops real sites and adds spurious ones (measured). Use the
returned sites as-is; read individual sites only to make the edits.

### 2. Reading code? Prism reads are cheaper than shell reads

| Situation | Tool |
|---|---|
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") — ~5x cheaper than prism_read |

### 3. Exploring? Shell finds the anchor, one prism_query expands it

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg, etc.) — not Prism |
| Callers/callees/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |

Canonical workflow (non-refactor tasks):

    grep/find/rg <terms>                 <- locate anchor first; shell tools always win here
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph","tests"],
           graph_depth=2
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

Housekeeping: prism_index once at session start (delta indexing is automatic —
never re-run per step); prism_drift if a stale-context warning appears.

### Do NOT

- Do NOT call prism_query before searching — use shell tools (grep, find, rg) to find the anchor first; prism expands from it
- Do NOT orchestrate multi-call traversals (references, then callers, then lookups) to enumerate a change's impact — prism_change_impact computes the complete set in one call
- Do NOT use prism_read for a single function — use prism_lookup instead
`

// steeringInstructionsCLI is injected when agent_mode is "cli".
// Agents that only have Bash access use the prism CLI with --format text.
const steeringInstructionsCLI = `
## Prism — context delivery (ALWAYS use these tools)

Prism is available as a CLI (use via Bash, --format text). It answers
whole-task questions (change impact, missing implementations, test gaps, dead
code) in ONE deterministic call, and delivers code context cheaply. Three
layers, in priority order.

### 1. Changing or auditing code? One call answers the whole task

| Situation | Command |
|---|---|
| Renaming/changing a method signature | ` + "`" + `prism change-impact 'Type.method(ParamType, ...)'` + "`" + ` — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` — override family + callers |
| Renaming a class, struct, or type | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` for each public method |
| Deprecating a symbol (need all callers to migrate) | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` — complete caller list |
| ANY task that says "find all X" for a specific method | ` + "`" + `prism change-impact` + "`" + ` first, before any grep |
| Renaming a method and you want the edits, not just the sites | ` + "`" + `prism rename-plan 'Type.method' NewName` + "`" + ` — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | ` + "`" + `prism missing-implementations 'Type.method'` + "`" + ` — every closure type with no implementation |
| "What should I test before changing X?" / test-gap audit / symbols with no tests | ` + "`" + `prism untested-surface 'Type.method'` + "`" + ` — change-set split covered/untested |
| Cleanups / "is X still used / can I delete it?" at scale | ` + "`" + `prism dead-code` + "`" + ` — unreachable production symbols + caveats |

**Pre-task rule:** before writing any code on a task that involves changing or
renaming an existing symbol, run prism change-impact first — even if the change
looks small. Small changes can have large blast radii through inheritance and
indirect callers that grep will not find. Result groups: declarations + family
(every override/implementation) + callers = every site that must change. Param
types are optional ('Type.method' works) but improve precision on overloaded names.

Check the completeness field in the output. "closed" means the set is
authoritative. "project-local" with overridesExternal means the method belongs
to an external (JDK/dependency) contract: do NOT change its signature — that
breaks a contract this project does not own — and the set covers project code
only. To sweep every project implementation of an external interface
(migration/deprecation), query the external type directly:
prism change-impact 'Iterator.next'.

Relay rule: the result is deterministic and type-resolved. Do NOT re-verify,
re-filter, dedup, or transform it through grep/sed/awk/scripts — re-processing
a solved traversal drops real sites and adds spurious ones (measured). Use the
returned sites as-is; read individual sites only to make the edits.

### 2. Reading code? Prism reads are cheaper than shell reads

| Situation | Command |
|---|---|
| Read a whole file | ` + "`" + `prism read <file> --format text` + "`" + ` — session-compressed on repeat reads |
| Read one function body | ` + "`" + `prism lookup <pkg.FuncName> --format text` + "`" + ` — ~5x cheaper than read |

### 3. Exploring? Shell finds the anchor, one prism query expands it

| Situation | Command |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/callees/tests for a symbol just found | ` + "`" + `prism query "<task>" --terms a,b --include graph,tests --format text` + "`" + ` |

Canonical workflow (non-refactor tasks):

    grep/find/rg <terms>                      <- locate anchor first; shell tools always win here
      -> prism query "<task>" \               <- expand from anchor: callers, callees, tests
           --terms <same-terms> \
           --include graph,tests \
           --format text
      then selectively:
      -> prism read <file> --format text      <- whole file, session-compressed
      -> prism lookup <pkg.FuncName> --format text  <- one function (~5x cheaper than read)

Housekeeping: prism index [dir] once at session start (delta indexing is
automatic — never re-run per step).

### Do NOT

- Do NOT call prism query before searching — use shell tools (grep, find, rg) to find the anchor first; prism expands from it
- Do NOT orchestrate multi-call traversals (references, then callers, then lookups) to enumerate a change's impact — ` + "`" + `prism change-impact` + "`" + ` computes the complete set in one call
- Do NOT use prism read for a single function — use prism lookup instead
`

// steeringInstructionsBoth is injected when agent_mode is "both" (default).
// MCP tools are primary; the CLI section serves as fallback for subagents
// that only have Bash access.
const steeringInstructionsBoth = `
## Prism — context delivery (ALWAYS use these tools)

Prism answers whole-task questions (change impact, missing implementations,
test gaps, dead code) in ONE deterministic call, and delivers code context
cheaply. Three layers, in priority order.

### When MCP tools are available

Use the registered prism_* MCP tools.

**1. Changing or auditing code? One call answers the whole task:**

| Situation | Tool |
|---|---|
| Renaming/changing a method signature | prism_change_impact(query="Type.method(ParamType, ...)") — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | prism_change_impact — override family + all callers |
| Renaming a class, struct, or type | prism_change_impact for each public method — all usages |
| Deprecating a symbol (need all callers to migrate) | prism_change_impact — complete caller list |
| ANY task that says "find all X" for a specific method | prism_change_impact first, before any grep |
| Renaming a method and you want the edits, not just the sites | prism_rename_plan(query="Type.method", newName="newName") — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | prism_missing_implementations(query="Type.method") — every closure type with no implementation |
| "What should I test before changing X?" / test-gap audit / symbols with no tests | prism_untested_surface(query="Type.method") — the change-set split covered/untested |
| Cleanups, library extraction, "can I delete this?" at scale | prism_dead_code — unreachable production symbols, safe-to-delete list + caveats |

**2. Reading code? Prism reads are cheaper than shell reads:**

| Situation | Tool |
|---|---|
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") — ~5x cheaper than prism_read |

**3. Exploring? Shell finds the anchor, one prism_query expands it:**

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg, etc.) — not Prism |
| Callers/callees/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |

**Pre-task rule:** before writing any code on a task that involves changing or
renaming an existing symbol, call prism_change_impact FIRST — even if the change
looks small. Small changes can have large blast radii through inheritance and
indirect callers that grep will not find. Result groups: declarations + family
(every override/implementation) + callers = every site that must change.

Check the result's completeness field. "closed" means the set is authoritative.
"project-local" with overridesExternal means the method belongs to an external
(JDK/dependency) contract: do NOT change its signature — that breaks a contract
this project does not own — and the set covers project code only. To sweep every
project implementation of an external interface (migration/deprecation), query
the external type directly (e.g. "Iterator.next").

Relay rule: the result is deterministic and type-resolved. Do NOT re-verify,
re-filter, dedup, or transform it through grep/sed/awk/scripts — re-processing
a solved traversal drops real sites and adds spurious ones (measured). Use the
returned sites as-is; read individual sites only to make the edits.

Canonical workflow (non-refactor tasks):

    grep/find/rg <terms>                 <- locate anchor first; shell tools always win here
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph","tests"],
           graph_depth=2
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

Housekeeping: prism_index once at session start (delta indexing is automatic —
never re-run per step); prism_drift if a stale-context warning appears.

### When only Bash is available (subagents, CI)

Use the prism CLI with --format text instead of MCP tools:

| Situation | Command |
|---|---|
| Renaming/changing a method signature | ` + "`" + `prism change-impact 'Type.method(ParamType, ...)'` + "`" + ` — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` — override family + callers |
| Renaming a class, struct, or type | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` for each public method |
| Deprecating a symbol (need all callers to migrate) | ` + "`" + `prism change-impact 'Type.method'` + "`" + ` — complete caller list |
| Renaming a method and you want the edits, not just the sites | ` + "`" + `prism rename-plan 'Type.method' NewName` + "`" + ` — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | ` + "`" + `prism missing-implementations 'Type.method'` + "`" + ` — every closure type with no implementation |
| "What should I test before changing X?" / symbols with no tests | ` + "`" + `prism untested-surface 'Type.method'` + "`" + ` — change-set split covered/untested |
| Cleanups / "can I delete this?" at scale | ` + "`" + `prism dead-code` + "`" + ` — unreachable production symbols + caveats |
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/callees/tests for a symbol just found | ` + "`" + `prism query "<task>" --terms a,b --include graph,tests --format text` + "`" + ` |
| Read a whole file | ` + "`" + `prism read <file> --format text` + "`" + ` |
| Read one function body | ` + "`" + `prism lookup <pkg.FuncName> --format text` + "`" + ` |

### Do NOT

- Do NOT call prism_query (or prism query) before searching — use shell tools first; prism expands from the anchor
- Do NOT orchestrate multi-call traversals (references, then callers, then lookups) to enumerate a change's impact — prism_change_impact / prism change-impact computes the complete set in one call
- Do NOT use prism_read / prism read for a single function — use prism_lookup / prism lookup instead
`

// writeSteeringInstructions writes per-tool instruction files into the project
// so agents know how to use Prism tools correctly.
// On re-init it replaces a stale Prism section rather than skipping.
func writeSteeringInstructions(projectDir, mode string) {
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

	block := steeringBlockForMode(mode)

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

// steeringBlockForMode returns the steering instructions for the given mode.
func steeringBlockForMode(mode string) string {
	switch mode {
	case config.AgentModeMCP:
		return steeringInstructionsMCP
	case config.AgentModeCLI:
		return steeringInstructionsCLI
	default:
		return steeringInstructionsBoth
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
	// Claude Code launches project-scope MCP servers with cwd at the project
	// root, so its entry needs no pinned absolute path — this keeps .mcp.json
	// portable and correct after the repo moves. The IDE writers below keep
	// the explicit dir because their launch cwd is not guaranteed.
	claudeEntry := mcpEntry{Command: prismBin, Args: []string{"mcp"}}

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
		if filepath.Base(p) == ".mcp.json" && mcpEntryAlreadyPresent(p, "prism", claudeEntry) {
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

	// Zed and Codex CLI keep their MCP registrations in USER-GLOBAL config
	// files (~/.config/zed/settings.json, ~/.codex/config.toml). A
	// project-level init must not touch them: writing this project's path
	// there would silently re-point every other project's Zed/Codex at this
	// one. Register them only with --global, and without a pinned project
	// dir — `prism mcp` serves the editor's launch cwd, so one global entry
	// is correct in every project.
	if global {
		zedPath := filepath.Join(home, ".config", "zed", "settings.json")
		if _, err := os.Stat(filepath.Dir(zedPath)); err == nil {
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
		if _, err := os.Stat(filepath.Dir(codexPath)); err == nil {
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
	depth := 0
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
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					depth = n
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	invokeArgs := map[string]any{"task": task, "limit": limit}
	if profile != "" {
		invokeArgs["profile"] = profile
	}
	if len(terms) > 0 {
		invokeArgs["terms"] = terms
	}
	if len(include) > 0 {
		invokeArgs["include"] = include
	}
	if depth > 0 {
		invokeArgs["graph_depth"] = depth
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
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
		case "--format":
			if i+1 < len(args) {
				switch outputFormat(args[i+1]) {
				case formatText, formatLean, formatJSON:
					format = outputFormat(args[i+1])
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
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
		} else if !strings.HasPrefix(a, "-") {
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
		fmt.Fprintln(os.Stderr, "usage: prism edges <name> [--dir out|in|both] [--kinds calls,tests,...] [dir]")
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
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
		fmt.Fprintln(os.Stderr, "usage: prism change-impact <query> [dir]")
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_change_impact", map[string]any{"query": query})
	if err != nil {
		fmt.Fprintln(os.Stderr, "change-impact:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdRenamePlan(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prism rename-plan <query> <NewName> [dir]")
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_rename_plan",
		map[string]any{"query": query, "newName": newName})
	if err != nil {
		fmt.Fprintln(os.Stderr, "rename-plan:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdMissingImplementations(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism missing-implementations <query> [dir]")
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_missing_implementations", map[string]any{"query": query})
	if err != nil {
		fmt.Fprintln(os.Stderr, "missing-implementations:", err)
		return 1
	}
	printOutput(out, format)
	return 0
}

func cmdUntestedSurface(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism untested-surface <query> [dir]")
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_untested_surface", map[string]any{"query": query})
	if err != nil {
		fmt.Fprintln(os.Stderr, "untested-surface:", err)
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
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
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

// printOutput prints v in the requested format.
// JSON round-trips through map[string]any so both typed structs (queryResult)
// and plain maps are handled uniformly by the text/lean formatters.
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
		if rawGaps, ok := m["coverageGaps"]; ok {
			if gaps, ok := rawGaps.([]any); ok && len(gaps) > 0 {
				fmt.Println("// coverage_gaps:")
				for _, g := range gaps {
					if gap, ok := g.(map[string]any); ok {
						name, _ := gap["name"].(string)
						fp, _ := gap["filePath"].(string)
						fmt.Printf("//   %s (%s)\n", name, fp)
					}
				}
			}
		}
		return
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
	// untested-surface, dead-code) return purpose-built maps with no
	// metadata to strip; lean used to reduce them to {} — pass them through.
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

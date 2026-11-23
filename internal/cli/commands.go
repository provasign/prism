// Package cli implements the Prism command tree (flat dispatch, no cobra
// dependency — keeps Prism a true single binary with zero runtime deps).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/provasign/prism/internal/assist"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/httpapi"
	"github.com/provasign/prism/internal/httpguard"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/trust"
	"github.com/provasign/prism/internal/version"
	"github.com/provasign/prism/internal/workspace"
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
  prism init [--harness <ids>] [dir]
                                  Write prism.yaml + project-local MCP config
  prism install [--harness <ids>] [dir]
                                  Alias for 'prism init'
  prism cleanup-global            Remove Prism entries written by old global setup
  prism index [dir]               Index codebase via Grove (delta-aware); text summary,
                                  [--format json] [--json] for JSON. Exits 3
                                  when a language's compiler-backed analysis
                                  could not run (dependencies not installed,
                                  toolchain missing) and prints the fix;
                                  [--allow-heuristic] accepts name-based results;
                                  [--force] re-extracts every file and re-runs
                                  every analyzer now, skipping retry waits;
                                  [--rebuild] deletes the index and builds it
                                  from scratch (repair for a broken index);
                                  [--untrusted|--trusted] for this run (see prism trust)
  prism watch [dir]               Keep the index warm: delta-reindex on file save
                                  (push model; [--debounce 2s], Ctrl+C to stop)
  prism status [dir]              Show graph stats from Grove ([--format text|json] [--json])
  prism doctor [dir]              Diagnose engine, index, and capabilities
                                  ([--format text|json] [--json]; the per-language manifest is JSON only)
  prism map [dir]                 Component-level architecture map: directories,
                                  induced dependency edges (weights + evidence
                                  tiers), cycles. Production shape by default
                                  ([--depth N] [--component X] [--tests]
                                  [--expand 'A->B'] [--json | --format text|json])
                                  [--max-sites N]  cited sites per edge (default 5)
                                  [--all-components]  include docs/examples and isolated
                                  other-language script folders (hidden by default)
                                  [--removed a,b,c]  only report what still
                                  references each removed identifier
  prism graph [dir]               Open the interactive graph explorer in a browser:
                                  directories -> files -> symbols -> one symbol's
                                  callers and callees, with search and the evidence
                                  behind each edge. Serves on localhost until Ctrl+C
                                  ([--port N] [--no-open] [--tests])
  prism cycles [dir]              Dependency cycles with per-edge evidence
                                  ([--depth N] [--tests] [--json | --format text|json])
                                  [--removed a,b,c]  only report what still
                                  references each removed identifier
  prism arch [dir]                Validate declared architecture rules
                                  (arch_deny: "<from> -> <to>" in prism.yaml)
                                  against the component view; violations cite
                                  file:line sites; exit 1 on violation — a CI
                                  gate ([--deny 'A -> B'] [--depth N] [--tests] [--strict]
                                  [--json | --format text|json])
                                  [--removed a,b,c]  only report what still
                                  references each removed identifier
  prism verify [dir]              Optionally review a diff (working tree vs
                                  --base, default HEAD) for missed sites and
                                  architecture changes. Exit 1 if incomplete;
                                  --strict also exits 1 on review.
                                  [--base REF] [--removed a,b,c] [--strict]
                                  [--format text|json] ([--json] = --format json)
  prism query --terms a,b,c [dir]  Find related implementations, callers,
                                  and tests around explicit anchors (edit-ready)
                                  [--repo <id>]  use the linked repository's graph
                                  --terms a,b,c      REQUIRED: anchor on specific symbol
                                  names (grep-precision); use prism search first when
                                  no anchor is known
                                  --include a,b      Categories: graph,docs (default: graph)
                                  --delivery source|symbols  Force delivery shape (default: source)
                                  --max-files N      source delivery: max files shown (default: 5)
                                  --limit N          max results
                                  --profile <name>   compatibility name; all profiles rank alike
                                  --format text|json  Output format (default: text)
  prism read <file> [dir]         Read file with compression
                                  [--repo <id>]  read paths inside a linked repository
                                  --from N --to M  Line range (inclusive);
                                  --offset N --limit N also work; --dir <path>
                                  [--include-sensitive]  read a .env, key or credential file
                                  --format text|json  Output format (default: text)
  prism search <term>... [dir]    Search symbol names AND raw source text (a real
                                  rg/grep pass). Pass SEVERAL terms to search them
                                  in one call (up to 10), grouped by term.
                                  [-- term...]  arguments after -- are terms, never flags
                                  Text matches ignore case by default; [--case-sensitive|-s]
                                  matches exact case, [--ignore-case|-i] states the default
                                  [--word|-w]  whole words only
                                  [--no-default-excludes]  also search .shale, .claude, .cursor, ...
                                  [--include-sensitive]  show the text of matches in .env, key and
                                  credential files (withheld by default; the match still counts)
                                  --scope text is a pure grep
                                  ([--scope text|symbols|both] [--regex] [--limit N])
                                  [--path|--paths <file-or-dir>]  scope the search (repeatable)
                                  [--glob '*.py'] [--files-only] [--exhaustive|--all] [--context N]
                                  [--include-bodies|--no-bodies]  bounded source by default
                                  [--rollup-only]  on a truncated search, skip the raw
                                  sample and return only the grouped-by-symbol rollup
                                  [--dir <path>]  where to search (default: .)
                                  [--repo <id>|all]  text matches in a linked repository
                                  --format text|json  Output format (default: text)
  prism lookup <name> [dir]       Show full source for a symbol
                                  [--repo <id>|all]  answer from a linked repository
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  Multiple names: prism lookup <name>... [--dir <path>]
                                  [--fields signature,doc,body,kind,parent,modifiers]
                                  --format text|json  Output format (default: text)
  prism node <symbol-or-file> [dir]  One-shot orientation: a symbol's source +
                                  its neighbours, or a file's source + the
                                  symbols it defines + the files depending on it
                                  [--file <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
  prism callers <symbol> [dir]    Transitive callers as a tree, resolved incoming calls
                                  and interface dispatch (marked "via"); [--depth N]
                                  (default 3, max 6; 200 callers) [--file <path>]
                                  [--format text|json]
  prism path <from> <to> [dir]    Shortest resolved call path from one symbol to another;
                                  exit 4 when there is none [--format text|json]
  prism references <name> [dir]   Find indexed syntactic uses of a name
                                  (comments/strings excluded) as path:line:col: text;
                                  Type.member or [--file <path> | --path <path>] resolves one symbol;
                                  each resolved site is tagged decl, call, read, write or type-use,
                                  and --kind call,read,write,decl,type keeps only those
                                  --format text|json  Output format (default: text)
  prism resolve <name> [dir]      Resolve a name to its definition(s): file:line + kind
                                  --format text|json  Output format (default: text)
  prism edges <name> [dir]        Walk the graph one hop from a symbol
                                  ([--direction|--dir in|out|both] [--kinds calls,uses-type,...])
                                  --format text|json  Output format (default: text)
  prism test-plan [dir]          Impacted test symbols, file targets, evidence, and gaps
                                  --base <ref>  Compare working tree with ref (default: HEAD)
                                  Two tiers: run first (resolved call paths, nearest first) and
                                  run for safety (every test in a package that depends on the change)
                                  --coverage <file>  read recorded coverage (Go profile, lcov,
                                  JaCoCo XML, coverage.py .coverage; repeatable). Without it the
                                  conventional names at the root are read when present
                                  --format text|json  Output format (default: text)
                                  Construct runner commands; judge broader validation from gaps
  prism workspace                Linked repositories: path, branch, commit, index, how linked
                                  add <path>... [--id <name>] [--no-index]
                                                            Link local checkouts: written to prism.yaml, made
                                                            visible from both, and indexed if never indexed
                                  add <git-url>[@ref]       Fetch a repository that is not checked out into a
                                                            read-only cache at that commit, index it, link it
                                  fetch                     Re-resolve cache clones' branches
                                  prune                     Delete cache clones nothing links to
                                  export [-o <file>]        Write this repository's link bundle (no source bodies)
                                  push [--server <url>]     Upload it to the link server (workspace_server)
  prism link-server --dir <store> [--port N]
                                  Serve uploaded link bundles: who references a symbol across
                                  repositories that are not checked out (PRISM_LINK_TOKEN required token)
                                  remove <id>               Unlink; the checkout is not touched
                                  discover [--link]         Related checkouts found in manifests
  prism change-impact <query> [dir]  Indexed potential impact sites for a method signature change:
                                  declaration(s), override/implementation family (subtype
                                  closure), super-declarations, and indexed callers.
                                  query format: Type.method or Type.method(ParamType, ...)
                                  Up to 10 query names may be batched; bare words are names
                                  [--diff [--base REF]]  impact of the working-tree diff: per changed
                                  symbol, the sites the diff touched and did not, direct callers of
                                  body-only changes, other components reached, and the tests
                                  [./dir | --dir <path>]  select a repository
                                  [--repo <id>]  anchor in a linked repository
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
  prism rename-plan <query> <NewName> [dir]     Change-set as line edits with substitutions
                                                (--format text|json; query: Type.method or Type.method(ParamType, ...))
                                                [--file <path> | --path <path>]  disambiguate a symbol
                                                Exits 1 with the declarations when NewName is already
                                                declared where the member would land;
                                                [--allow-collision] prints the plan anyway
  prism missing-implementations <query> [dir]  Types claiming the contract that do NOT
                                  implement Type.method (missing / abstract / unverifiable)
                                  — the interface-evolution companion to change-impact
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
  prism dead-code [dir] [--roots a,b]  Unreachable production functions/methods
                                  --format text|json  Output format (default: text)
  prism assist [--model <spec>] [--apply|--apply-ambiguous] [--verify "<cmd>"] "<task>"
                                  NL task -> deterministic ops via any model (ollama:/claude:/openai:)
                                  [--dir <path>]  repository (default: .)
                                  (precision-first; relay the caveats)
                                  --format text|json  Output format (default: text)
  prism compact [dir]             Compress conversation JSON from stdin
  prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
                                  Record quality feedback for a Prism result in
                                  .grove/feedback.jsonl; [--list] prints the records
  prism serve [--port 8888] [dir] Start the HTTP API server (stdio MCP is 'prism mcp')
                                  Loopback only, bearer token printed and written to
                                  .grove/serve.token; [--listen <addr> --token-file <f>]
  prism mcp [--compact|--legacy] [dir]
                                  Start MCP server on stdio (compact gateway is default)
  prism drift                     MCP only (prism_drift): it compares files with what the
                                  running server delivered this session; a CLI call has no session
  prism config [dir]              Show every prism.yaml key, its value and its source;
                                  --check exits 1 on an unknown key or invalid value
                                  ([--check] [--format text|json] [--json])
  prism ci [dir]                  PR/CI gate: index, verify, change-impact --diff and test-plan
                                  against --base REF; facts with site lists, no LLM.
                                  [--format text|github|sarif|json] [--json] [--untrusted|--trusted]
                                  Exits 1 on a finding in ci_fail_on (prism.yaml of the BASE
                                  revision; default missed_sites, arch), 3 when compiler-backed
                                  analysis did not run
  prism trust [dir]               Let indexing run this repository's own build tools
                                  (Maven/Gradle, its typescript, rust-analyzer, go toolchain)
  prism untrust [dir]             Never run them for this repository; also --untrusted
                                  on index or PRISM_UNTRUSTED=1. Recorded per user in
                                  ~/.config/prism/trusted.json, never read from the repo
  prism help exit-codes           What each exit code means (0 answered, 1 finding, 2 usage,
                                  3 degraded, 4 not found, 5 ambiguous, 70 internal)
  prism version                   Print version ([--verbose] also the linked Grove engine)

prism init [dir] flags:
  --harness <ids>     harnesses to configure, comma-separated or repeated
                      ids: claude, codex, cursor, windsurf, vscode, gemini, opencode
                      interactive init recommends detected/existing harnesses;
                      non-interactive init requires this flag
  --workspace <auto|none>
                      related checkouts found in manifests: auto links them,
                      none does not look (default: ask when interactive,
                      otherwise only report)
  --interface <mcp|cli>
                      how agents reach Prism: mcp registers the MCP server, cli
                      registers nothing and steers agents to the prism command
                      (asked interactively; recorded in prism.yaml; default cli
                      for a new project; a project initialized before the
                      choice was recorded keeps mcp)
  --yes, -y           reuse harnesses recorded in prism.yaml without prompting
  --global            removed: MCP configuration is project-local only
  --mode <any>        accepted and IGNORED (since v0.38.0 one steering template
                      covers MCP tools and the CLI together)
  --no-permissions    skip the Claude Code tool auto-allow entry (written to your
                      own .claude/settings.local.json, never the shared file)
  --deny-builtin-search
                      deny Claude Code's Grep/Bash(grep|rg) so agents actually
                      reach prism (asked interactively; Claude Code only —
                      no other agent exposes a tool-denial setting)
  --read-guard        install a hook that denies a native Read once prism has
                      already delivered that range this session (off by
                      default; Claude Code only). Writes
                      .claude/hooks/prism_read_*.py and registers them in
                      .claude/settings.json.
  --no-read-guard     cleanly remove the read-guard hook: its two script
                      files, its settings.json entries (only the ones it
                      owns — any other hook on the same matcher survives),
                      and .prism-read-tracker.json
  --refresh           rewrite ONLY agents already configured (never adds new ones)
  --print-config <id> print one project-local harness snippet and exit, writing nothing
                      ids: claude, codex, cursor, windsurf, vscode, gemini, opencode

The Prism executable is installed globally; every harness registration and
steering file is stored in the repository. Re-running updates in place and also
removes stale Prism-owned user-global MCP registrations from older releases:
  Claude Code  →  .mcp.json + CLAUDE.md
  Codex CLI    →  .codex/config.toml + AGENTS.md
  Cursor       →  .cursor/mcp.json + AGENTS.md
  Windsurf     →  AGENTS.md steering only (no documented project MCP config)
  VS Code      →  .vscode/mcp.json + .github/copilot-instructions.md
  Gemini CLI   →  .gemini/settings.json + GEMINI.md
  opencode     →  opencode.json + AGENTS.md
`

// Run is the CLI entry point. Returns the exit code.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Print(helpText)
		return 0
	}
	cmd, rest := args[0], args[1:]
	defer cliSpelledOutput(cmd, rest)()
	// `prism <cmd> --help` used to RUN <cmd>: no handler parsed -h/--help, so
	// `prism init --help` performed a full init (writing prism.yaml, nine
	// steering files and every project MCP registration) and `prism search
	// --help` searched for the string "--help". Answer here, once, before any
	// command body can execute. Only the flag spellings count — a bare `help`
	// argument stays a search term, so `prism search help` still searches.
	if cmd != "help" && helpRequested(rest) {
		if !knownCommand(cmd) {
			return unknownCommand(cmd)
		}
		fmt.Print(commandHelp(cmd))
		return 0
	}
	switch cmd {
	case "-h", "--help", "help":
		if len(rest) > 0 && rest[0] == "exit-codes" {
			fmt.Print(exitCodesHelp)
			return 0
		}
		fmt.Print(helpText)
		return 0
	case "version", "--version", "-version":
		fmt.Println("prism " + version.Version)
		if len(rest) > 0 && (rest[0] == "--verbose" || rest[0] == "-v") {
			// REL-02: say which Grove engine is linked, and whether a
			// replace or go.work swapped it for a local checkout.
			if g, ok := version.LinkedGrove(); ok {
				fmt.Println("grove " + g.String())
			} else {
				fmt.Println("grove unknown (no build info)")
			}
		}
		return 0
	case "init", "install":
		return cmdInit(rest)
	case "cleanup-global":
		if _, err := removeLegacyGlobalMCPRegistrations(); err != nil {
			fmt.Fprintln(os.Stderr, "cleanup-global:", err)
			return 1
		}
		return 0
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
	case "graph":
		return cmdGraph(rest)
	case "cycles":
		return cmdCycles(rest)
	case "arch", "arch-check":
		return cmdArch(rest)
	case "verify":
		return cmdVerify(rest)
	case "test-plan":
		return cmdTestPlan(rest)
	case "workspace":
		return cmdWorkspace(rest)
	case "link-server":
		return cmdLinkServer(rest)
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
	case "callers":
		return cmdCallers(rest)
	case "path":
		return cmdPath(rest)
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
	case "drift":
		return cmdDrift(rest)
	case "config":
		return cmdConfig(rest)
	case "trust", "untrust":
		return cmdTrust(cmd, rest)
	case "ci":
		return cmdCI(rest)
	}
	return unknownCommand(cmd)
}

// commandNames lists every command Run dispatches, aliases included.
var commandNames = []string{
	"help", "version", "init", "install", "cleanup-global", "watch", "index",
	"status", "doctor", "map", "graph", "cycles", "arch", "arch-check",
	"verify", "test-plan", "workspace", "link-server", "query", "read",
	"search", "node", "lookup", "references", "refs", "resolve", "edges",
	"change-impact", "missing-implementations", "rename-plan", "dead-code",
	"assist", "compact", "feedback", "serve", "mcp", "drift", "config", "trust", "untrust", "ci",
}

func knownCommand(cmd string) bool {
	for _, c := range commandNames {
		if c == cmd {
			return true
		}
	}
	return false
}

// unknownCommand prints one line naming the nearest command, never the
// full help, and returns the usage exit code.
func unknownCommand(cmd string) int {
	msg := fmt.Sprintf("prism: unknown command %q", cmd)
	if near := nearestCommand(cmd); near != "" {
		msg += fmt.Sprintf("; did you mean %q?", near)
	}
	fmt.Fprintln(os.Stderr, msg+" (prism help lists commands)")
	return 2
}

// nearestCommand returns the command within edit distance 3 of cmd (or
// one that cmd prefixes), or "".
func nearestCommand(cmd string) string {
	best, bestD := "", 4
	for _, c := range commandNames {
		d := editDistance(cmd, c)
		if len(cmd) >= 3 && strings.HasPrefix(c, cmd) && d > 1 {
			d = 1
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// helpRequested reports whether a subcommand's own arguments ask for usage.
// Deliberately narrow: the flag spellings only, so a literal `help` argument
// is still a query term for search/lookup/references.
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// helpAliases maps command aliases onto the name helpText documents them under.
var helpAliases = map[string]string{
	"install":    "init",
	"refs":       "references",
	"arch-check": "arch",
}

// commandHelp returns the usage block for one subcommand, extracted from
// helpText: its `  prism <cmd> …` line plus the indented continuation lines
// under it, and for init the flag section further down. Falls back to the full
// help when the command has no block — an unknown command should still get
// something useful rather than silence.
func commandHelp(cmd string) string {
	if c, ok := helpAliases[cmd]; ok {
		cmd = c
	}
	prefix := "  prism " + cmd
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(helpText, "\n") {
		switch {
		case strings.HasPrefix(line, prefix) && (len(line) == len(prefix) || line[len(prefix)] == ' '):
			inBlock = true
			b.WriteString(line + "\n")
		case !inBlock:
			// outside a block: nothing to collect
		case (strings.HasPrefix(line, "   ") || strings.HasPrefix(line, "\t")) && !strings.HasPrefix(line, "  prism "):
			b.WriteString(line + "\n")
		default:
			inBlock = false
		}
	}
	if b.Len() == 0 {
		return helpText
	}
	if cmd == "init" {
		if i := strings.Index(helpText, "prism init [dir] flags:"); i >= 0 {
			section := helpText[i:]
			if j := strings.Index(section, "\n\n"); j >= 0 {
				section = section[:j]
			}
			b.WriteString("\n" + section + "\n")
		}
	}
	return b.String()
}

// --- per-command implementations ---------------------------------------

func cmdInit(args []string) int {
	initWriteFailures = 0
	rc := runInit(args)
	if rc == 0 && initWriteFailures > 0 {
		fmt.Fprintf(os.Stderr, "init: %d file(s) were not written; see the warnings above\n", initWriteFailures)
		return exitFinding
	}
	return rc
}

func runInit(args []string) int {
	// --mode mcp|cli|both  (legacy no-op)
	// --no-permissions     (skip the Claude Code tool auto-allow entry)
	// --print-config <id>  (print one agent's snippet, write nothing, exit)
	// --refresh            (rewrite ONLY agents already configured)
	permissions := true
	printConfig := ""
	refresh := false
	denyBuiltinSearch := false
	readGuard := false
	noReadGuard := false
	yes := false
	workspaceMode := ""
	interfaceArg := ""
	var harnessArgs []string
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--global":
			fmt.Fprintln(os.Stderr, "init: --global was removed; Prism MCP configuration is project-local. Use --harness to select clients.")
			return 2
		case "--harness":
			if _, err := flagValue("init", args, i); err != nil {
				fmt.Fprintln(os.Stderr, "init: --harness requires a comma-separated value")
				return 2
			}
			harnessArgs = append(harnessArgs, args[i+1])
			i++
		case "--interface":
			if _, err := flagValue("init", args, i); err != nil {
				fmt.Fprintln(os.Stderr, "init: --interface requires mcp or cli")
				return 2
			}
			iface, err := parseInterface(args[i+1])
			if err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				return 2
			}
			interfaceArg = iface
			i++
		case "--no-permissions":
			permissions = false
		case "--deny-builtin-search":
			denyBuiltinSearch = true
		case "--read-guard":
			readGuard = true
		case "--no-read-guard":
			noReadGuard = true
		case "--refresh":
			refresh = true
		case "--yes", "-y":
			yes = true
		case "--workspace":
			v, err := choiceFlag("init", args, i, "auto", "none")
			if err != nil {
				return usageError(err)
			}
			workspaceMode = v
			i++
		case "--print-config":
			v, err := flagValue("init", args, i)
			if err != nil {
				return usageError(err)
			}
			printConfig = v
			i++
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
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("init", a)
			}
			filtered = append(filtered, a)
		}
	}
	args = filtered
	if len(args) > 1 {
		return usageError(fmt.Errorf("init: takes one directory, got %q and %q", args[0], args[1]))
	}

	dir := dirArg(args, 0, ".")
	abs, _ := filepath.Abs(dir)
	cfg := config.Default()
	prismYAML := filepath.Join(abs, "prism.yaml")
	recordedHarnesses := readRecordedHarnesses(prismYAML)

	// --print-config is a pure query: render one agent's snippet and exit
	// without touching a single file.
	if printConfig != "" {
		return printAgentConfig(printConfig, abs, detectSelfPath())
	}

	// --read-guard / --no-read-guard are standalone actions, like
	// --print-config: they target .claude/settings.json directly and don't
	// depend on harness selection, so requiring a full init run (and its
	// interactive harness prompt) around a one-line toggle would be
	// needless friction. Mutually exclusive; neither touches any hook the
	// user did not ask prism to install (see addHookEntry/removeHookEntry).
	if readGuard && noReadGuard {
		fmt.Fprintln(os.Stderr, "init: --read-guard and --no-read-guard are mutually exclusive")
		return 2
	}
	if readGuard {
		if err := installReadGuard(abs); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			return 1
		}
		return 0
	}
	if noReadGuard {
		if err := uninstallReadGuard(abs); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			return 1
		}
		return 0
	}

	// An existing read guard is upgraded by any init run: brew upgrades the
	// binary but not the hook scripts and settings copied into the project.
	if readGuardInstalled(abs) {
		if err := installReadGuard(abs); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
		}
	} else if sedGuardInstalled(abs) {
		// An older prism installed a sed guard; prism no longer rewrites
		// the agent's sed commands.
		if err := removeSedGuard(abs); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
		}
	}

	harnesses, err := parseHarnesses(harnessArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 2
	}
	if len(harnesses) == 0 {
		if refresh {
			if len(recordedHarnesses) == 0 {
				fmt.Fprintln(os.Stderr, "init: --refresh cannot choose harnesses because prism.yaml has no recorded selection; pass --harness <ids>")
				return 2
			}
			harnesses = recordedHarnesses
		} else if yes {
			if len(recordedHarnesses) == 0 {
				fmt.Fprintln(os.Stderr, "init: --yes cannot choose harnesses because prism.yaml has no recorded selection; pass --harness <ids>")
				return 2
			}
			harnesses = recordedHarnesses
		} else if isInteractive() {
			harnesses, err = promptHarnesses(abs, recordedHarnesses)
			if err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				return 2
			}
		} else {
			fmt.Fprintln(os.Stderr, "init: no harness selected in non-interactive mode; pass --harness <ids> (or --yes to reuse harnesses recorded in prism.yaml)")
			return 2
		}
	}

	// Interface: the flag wins; otherwise the default is what prism.yaml
	// records, which --refresh/--yes reuse and an interactive init offers.
	iface := interfaceArg
	if iface == "" {
		def := defaultInterface(prismYAML)
		if isInteractive() && !refresh && !yes {
			fmt.Fprintln(os.Stderr, interfaceRecommendation(abs))
			iface, err = promptInterface(def)
			if err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				return 2
			}
		} else {
			iface = def
		}
	}

	// 1. Write prism.yaml into the project. Grove is embedded in-process now,
	// so the file no longer needs grove_url / grove_binary.
	yaml := fmt.Sprintf(`version: 2
# model: ""    # Optional: name the model driving this repo (e.g. "claude-sonnet-4-6")
#               # to size context budgets. There is NO auto-detection — the MCP
#               # initialize handshake does not carry the model — so unset means
#               # the default 200k-token window. Agents can also pass model= per
#               # call, which overrides this.
profile: "%s"
%s
mcp_surface: "compact"
`, cfg.Profile, harnessYAMLLine(harnesses))
	// NEVER clobber an existing prism.yaml. It holds user content init knows
	// nothing about — arch_deny rules above all, which are the CI gate for
	// declared architecture. A plain WriteFile deleted them on every re-init,
	// silently turning the arch check into a no-op. Only init-owned keys
	// manages are rewritten; every other line survives byte-for-byte.
	if existing, err := os.ReadFile(prismYAML); err == nil {
		yaml = mergePrismYAML(string(existing), cfg.Profile, harnesses)
	}
	yaml = setPrismYAMLInterface(yaml, iface)
	if err := os.WriteFile(prismYAML, []byte(yaml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("wrote", prismYAML)

	// 2. Detect the prism binary path for use in MCP configs.
	prismBin := detectSelfPath()
	if warning := mcp.PrismInstallationWarning(prismBin, version.Version); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	// Related checkouts: recorded, reported, linked only on a yes.
	offerWorkspaceLinks(abs, workspaceMode)

	// 3. Write steering instructions matching the chosen mode.
	writeSteeringBlock(abs, harnesses, refresh, steeringFor(abs, iface))

	// Routing is the one thing steering cannot do. Measured at 12:1 in the
	// benchmark and observed live: an agent listed prism's connected tools,
	// said its CLAUDE.md directed it to use them, then ran Bash(grep) on the
	// next task. Denying the built-in search is the only reliable fix — but
	// it edits the user's own Claude Code settings, so ASK rather than assume.
	// Never prompt non-interactively (CI gets the safe default: no change).
	// No interactive denial prompt. The prompt's own pitch ("agents ignore
	// steering, measured 12:1") predates alwaysLoad schemas, which took
	// adoption to 90%+ WITHOUT denying anything (full38, 2026-08-17+); the
	// denial experiment itself was reverted in v0.52.0, and its leftovers
	// skewed two benchmark runs badly enough to void them. Denial remains
	// available to those who ask for it: --deny-builtin-search.

	// 4. Register with every detected AI coding tool.
	if _, err := removeLegacyGlobalMCPRegistrations(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: legacy global Prism registration cleanup was incomplete:", err)
	}
	if iface == interfaceCLI {
		unregisterMCPTools(abs, harnesses)
		if harnessSelected(harnesses, "claude") && permissions {
			allowClaudeBashPrism(filepath.Join(abs, ".claude", "settings.json"))
		}
		fmt.Println("interface: cli — no MCP server registered; agents run `prism` in their shell")
		return 0
	}
	registered := initRegisterMCPTools(abs, portablePrismCommand(prismBin), harnesses, permissions, refresh, denyBuiltinSearch)
	if len(registered) == 0 {
		fmt.Println("tip: add prism to your AI tool's MCP config (see README)")
	} else {
		if harnessSelected(harnesses, "codex") {
			fmt.Println("Codex note: project .codex/config.toml loads after the repository is trusted in Codex")
		}
		fmt.Printf("restart or reload %s so it replaces any running older Prism MCP process\n", strings.Join(harnesses, ", "))
	}
	return 0
}

// mergePrismYAML rewrites only the keys init manages (version, profile,
// harnesses, mcp_surface), removes retired agent_mode, and preserves every other line — comments, arch_deny rules,
// anything a user or a later prism version put there. Keys init manages but
// the file lacks are appended.
func mergePrismYAML(existing, profile string, harnesses []string) string {
	managed := []struct{ key, val string }{
		{"version", "2"},
		{"profile", strconv.Quote(profile)},
		{"harnesses", strconv.Quote(strings.Join(harnesses, ","))},
		{"mcp_surface", strconv.Quote("compact")},
	}
	seen := map[string]bool{}
	lines := strings.Split(existing, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line == trimmed && strings.HasPrefix(trimmed, "agent_mode:") {
			// Retired init-owned v1 setting. Keeping it makes an upgraded project
			// look as if it can still select a legacy steering mode.
			lines[i] = ""
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

func harnessYAMLLine(harnesses []string) string {
	return "harnesses: " + strconv.Quote(strings.Join(harnesses, ","))
}

func readRecordedHarnesses(path string) []string {
	value := config.LoadFile(path).Harnesses
	if value == "" {
		return nil
	}
	harnesses, err := parseHarnesses([]string{value})
	if err != nil {
		return nil
	}
	return harnesses
}

// steeringInstructions is injected into every agent instruction file.
// MCP tools are primary; the CLI section serves as fallback for subagents
// that only have Bash access.
//
// Kept SHORT on purpose (2026-08-15: 11.8k chars -> ~1.6k). This block is
// always-loaded context in nine files; the tool schemas already carry the
// per-tool detail, and repeating it here paid for the same prose twice. The
// long version also documented seven tools that the 190-cell A/B measured at
// zero calls. What survives is what changed behaviour: an explicit access
// bootstrap, one route per question, and the change_impact relay rule. Do not
// grow this back into a manual — add detail to toolDescription instead. The
// heading is also injectPrismSection's start marker — changing its text would
// orphan every section already written into a user's files.
//
// ToolSearch remains explicit because the 2026-09-01 wide-change probe got
// 15/15 first-call compliance from its imperative form. It is no longer the
// only route: the 2026-09-07 coding pilot found Codex CLI 0.153 without a
// callable ToolSearch loader and Claude with Prism tools already visible;
// both abandoned Prism under the unconditional loader instruction. The
// ordered direct -> ToolSearch -> CLI bootstrap covers all three host shapes.
const steeringInstructions = `
## Prism — context delivery

Use Prism for each repository-discovery step. Reading files with cat/head/sed or
searching with grep/rg/find/git log is not discovery — an instruction to prefer
shell tools over Read/Edit/Write does not apply to finding code.

When the task asks for ` + mcp.ImpactRequiredFor + `:
start with change_impact on the named symbol, before deciding
how to make the change. Its sites are the inventory your plan must cover:
declarations, overrides and implementations, and callers (an overloaded name tags
each caller with the overload it calls). If you don't know where the symbol is,
find it first, then run change_impact. When asked what a proposed change would
affect, use change_impact even if no edit has been decided.

First action on any code task, in this order. Stop at the first that works:
  1. The ` + "`" + `prism` + "`" + ` MCP tool (` + "`" + `mcp__prism__prism` + "`" + ` in Claude Code), if callable.
  2. If a ` + "`" + `ToolSearch` + "`" + ` tool exists: ` + "`" + `ToolSearch("select:mcp__prism__prism")` + "`" + `, once.
     Prism not being listed does not mean it is absent.
  3. The ` + "`" + `prism` + "`" + ` CLI: ` + "`" + `prism query --terms X` + "`" + `, ` + "`" + `prism lookup <pkg.Func>` + "`" + `,
     ` + "`" + `prism search <term> --scope text --format text` + "`" + `, ` + "`" + `prism change-impact Type.method` + "`" + `.

For each discovery step, pick the Prism op:
  known symbol      -> lookup          unknown location/text -> search
  known file/range  -> read            callers/related       -> query
  pre-edit sites    -> change_impact
  tests naming known symbols -> search with test path/glob, files_only
  indirect tests    -> query

Put every symbol and term you already know into ONE call (` + "`" + `name` + "`" + `/` + "`" + `terms` + "`" + ` take up
to 10; ` + "`" + `ranges` + "`" + ` reads several windows). Two lookups in a row is one lookup you did
not batch. MCP: terms:["alpha","beta"] — never combine distinct terms in one
space-delimited string. CLI: positional terms: prism search alpha beta.

Obligations:
  - Relay change_impact sites as-is; ` + mcp.ImpactOptionalFor + ` can
    proceed from the source you have read.
  - Report gaps; never narrow scope to fit what was found.
  - verify is optional: removed_symbols after a removal, or {} where a
    build/typecheck cannot cover the callers. It is never a required closing step.

<!-- prism:end -->
`

var supportedHarnesses = []string{"claude", "codex", "cursor", "windsurf", "vscode", "gemini", "opencode"}

var harnessAliases = map[string]string{
	"claude":      "claude",
	"claude-code": "claude",
	"codex":       "codex",
	"cursor":      "cursor",
	"windsurf":    "windsurf",
	"vscode":      "vscode",
	"vs-code":     "vscode",
	"gemini":      "gemini",
	"gemini-cli":  "gemini",
	"opencode":    "opencode",
}

func parseHarnesses(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	want := map[string]bool{}
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			id := strings.ToLower(strings.TrimSpace(raw))
			if id == "all" {
				for _, harness := range supportedHarnesses {
					want[harness] = true
				}
				continue
			}
			canonical, ok := harnessAliases[id]
			if !ok {
				return nil, fmt.Errorf("unknown harness %q (choose %s)", raw, strings.Join(supportedHarnesses, ", "))
			}
			want[canonical] = true
		}
	}
	var selected []string
	for _, harness := range supportedHarnesses {
		if want[harness] {
			selected = append(selected, harness)
		}
	}
	return selected, nil
}

type harnessStatus struct {
	id          string
	label       string
	command     string
	configPaths []string
	projectMCP  bool
}

var harnessStatuses = []harnessStatus{
	{id: "claude", label: "Claude Code", command: "claude", configPaths: []string{".mcp.json"}, projectMCP: true},
	{id: "codex", label: "Codex CLI", command: "codex", configPaths: []string{".codex/config.toml"}, projectMCP: true},
	{id: "cursor", label: "Cursor", command: "cursor", configPaths: []string{".cursor/mcp.json", ".cursorrules"}, projectMCP: true},
	{id: "windsurf", label: "Windsurf", command: "windsurf", configPaths: []string{".windsurf/mcp.json", ".windsurfrules"}, projectMCP: false},
	{id: "vscode", label: "VS Code", command: "code", configPaths: []string{".vscode/mcp.json"}, projectMCP: true},
	{id: "gemini", label: "Gemini CLI", command: "gemini", configPaths: []string{".gemini/settings.json"}, projectMCP: true},
	{id: "opencode", label: "OpenCode", command: "opencode", configPaths: []string{"opencode.json"}, projectMCP: true},
}

func detectedHarnesses(projectDir string, recorded []string) []string {
	want := map[string]bool{}
	for _, id := range recorded {
		want[id] = true
	}
	for _, h := range harnessStatuses {
		if _, err := exec.LookPath(h.command); err == nil {
			want[h.id] = true
		}
		for _, rel := range h.configPaths {
			if fileExists(filepath.Join(projectDir, filepath.FromSlash(rel))) {
				want[h.id] = true
			}
		}
	}
	var selected []string
	for _, id := range supportedHarnesses {
		if want[id] {
			selected = append(selected, id)
		}
	}
	return selected
}

func harnessProjectState(projectDir string, h harnessStatus) string {
	for _, rel := range h.configPaths {
		raw, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		text := string(raw)
		if strings.Contains(strings.ToLower(text), "prism") {
			if strings.Contains(text, "--compact") {
				return "current compact setup"
			}
			return "legacy Prism setup (will upgrade)"
		}
		return "existing project config"
	}
	return ""
}

func parseHarnessSelection(line string) ([]string, error) {
	parts := strings.FieldsFunc(strings.TrimSpace(line), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(parts) == 0 {
		return nil, nil
	}
	var values []string
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == "a" || lower == "all" {
			return append([]string(nil), supportedHarnesses...), nil
		}
		if lower == "n" || lower == "none" {
			return []string{}, nil
		}
		if n, err := strconv.Atoi(part); err == nil {
			if n < 1 || n > len(supportedHarnesses) {
				return nil, fmt.Errorf("harness number %d is out of range", n)
			}
			values = append(values, supportedHarnesses[n-1])
			continue
		}
		values = append(values, part)
	}
	return parseHarnesses(values)
}

func promptHarnesses(projectDir string, recorded []string) ([]string, error) {
	defaults := detectedHarnesses(projectDir, recorded)
	defaultNums := make([]string, 0, len(defaults))
	fmt.Fprintln(os.Stderr, "\nPrism found these coding harnesses:")
	for i, h := range harnessStatuses {
		var status []string
		if _, err := exec.LookPath(h.command); err == nil {
			status = append(status, "installed")
		}
		if projectState := harnessProjectState(projectDir, h); projectState != "" {
			status = append(status, projectState)
		}
		if !h.projectMCP {
			status = append(status, "steering only; no documented project-local MCP config")
		}
		if len(status) == 0 {
			status = append(status, "not detected")
		}
		if harnessSelected(defaults, h.id) {
			defaultNums = append(defaultNums, strconv.Itoa(i+1))
		}
		fmt.Fprintf(os.Stderr, "  [%d] %-12s %s\n", i+1, h.label, strings.Join(status, " · "))
	}
	fmt.Fprintf(os.Stderr, "Select harnesses [default: %s]: ", strings.Join(defaultNums, ","))
	reader := initStdin()
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	selected, err := parseHarnessSelection(line)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(line)) == 0 {
		selected = defaults
	}
	if len(selected) == 0 {
		return nil, errors.New("no harness selected")
	}
	fmt.Fprintf(os.Stderr, "Prism will configure project-local steering (and MCP, if you choose it next) for: %s\n", strings.Join(selected, ", "))
	if harnessSelected(selected, "windsurf") {
		fmt.Fprintln(os.Stderr, "  Windsurf: steering only; its documented MCP config is user-global and Prism will not write it.")
	}
	fmt.Fprint(os.Stderr, "Proceed? [Y/n] ")
	answer, readErr := reader.ReadString('\n')
	if readErr != nil && len(answer) == 0 {
		return nil, readErr
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "n" || answer == "no" {
		return nil, errors.New("cancelled")
	}
	return selected, nil
}

func harnessSelected(harnesses []string, id string) bool {
	for _, harness := range harnesses {
		if harness == id {
			return true
		}
	}
	return false
}

func selectedHarnessHasExistingSetup(projectDir string, harnesses []string, target string) bool {
	for _, h := range harnessStatuses {
		if !harnessSelected(harnesses, h.id) {
			continue
		}
		if target != "shared" && h.id != target {
			continue
		}
		for _, rel := range h.configPaths {
			if fileExists(filepath.Join(projectDir, filepath.FromSlash(rel))) {
				return true
			}
		}
	}
	return false
}

// writeSteeringInstructions writes only the instruction files used by the
// selected project harnesses. On re-init it replaces a stale Prism section.
func writeSteeringInstructions(projectDir string, harnesses []string, refresh bool) {
	writeSteeringBlock(projectDir, harnesses, refresh, steeringBlock())
}

// writeSteeringBlock is writeSteeringInstructions with the block chosen by
// the caller (MCP steering or CLI steering).
func writeSteeringBlock(projectDir string, harnesses []string, refresh bool, block string) {
	type instrFile struct {
		name    string // description for log
		relPath string // path relative to projectDir
		harness string
	}
	targets := []instrFile{
		{name: "Claude Code", relPath: "CLAUDE.md", harness: "claude"},
		{name: "Codex/Cursor/Windsurf/OpenCode", relPath: "AGENTS.md", harness: "shared"},
		{name: "GitHub Copilot", relPath: ".github/copilot-instructions.md", harness: "vscode"},
		{name: "Gemini CLI", relPath: "GEMINI.md", harness: "gemini"},
		// Deprecated locations are migration-only: Prism sections are removed,
		// never inserted. Current Cursor and Windsurf read AGENTS.md.
		{name: "legacy Cursor", relPath: ".cursorrules", harness: ""},
		{name: "legacy Windsurf", relPath: ".windsurfrules", harness: ""},
	}

	selectedFor := func(t instrFile) bool {
		if t.harness == "shared" {
			return harnessSelected(harnesses, "codex") || harnessSelected(harnesses, "cursor") ||
				harnessSelected(harnesses, "windsurf") || harnessSelected(harnesses, "opencode")
		}
		return harnessSelected(harnesses, t.harness)
	}
	// Several names can be one file: `CLAUDE.md -> AGENTS.md` is a common
	// convention (zod). Handled name by name, the CLAUDE.md pass inserted the
	// section and the AGENTS.md pass (its harness unselected) stripped it
	// again, so Claude Code got no steering at all. Each real file is written
	// once, selected if any of its names is.
	realPath := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	selectedReal := map[string]bool{}
	for _, t := range targets {
		if selectedFor(t) {
			selectedReal[realPath(filepath.Join(projectDir, t.relPath))] = true
		}
	}
	handled := map[string]bool{}

	for _, t := range targets {
		path := filepath.Join(projectDir, t.relPath)
		real := realPath(path)
		if handled[real] {
			continue
		}
		selected := selectedReal[real]
		exists := fileExists(path)
		if !selected && !exists {
			continue
		}
		if selected && refresh && !exists && !selectedHarnessHasExistingSetup(projectDir, harnesses, t.harness) {
			continue
		}
		handled[real] = true
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create directory for %s instructions: %v\n", t.name, err)
			continue
		}

		var existing string
		if raw, err := os.ReadFile(path); err == nil {
			existing = string(raw)
		}
		content := stripPrismSections(existing)
		if selected {
			content = injectPrismSection(content, block)
		}
		if content == existing {
			continue
		}
		if content == "" {
			// Preserve an existing empty instruction file; no Prism-owned content
			// remains and removing the user's file is outside init's authority.
			if exists {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not clean %s instructions: %v\n", t.name, err)
				}
			}
			continue
		}
		if existing == "" && selected {
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
func steeringBlock() string { return steeringInstructions }

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
	clean := stripPrismSections(content)
	if strings.TrimSpace(clean) == "" {
		return block
	}
	return strings.TrimRight(clean, "\n") + block
}

// stripPrismSections removes every Prism-owned steering section, including
// historical headings and duplicate blocks left by older re-init behavior.
func stripPrismSections(content string) string {
	const marker = "## Prism —"
	const endMarker = "<!-- prism:end -->"
	for {
		start := -1
		if strings.HasPrefix(content, marker) {
			start = 0
		} else if i := strings.Index(content, "\n"+marker); i >= 0 {
			start = i + 1
		}
		if start < 0 {
			break
		}
		rest := content[start:]
		nextHeading := strings.Index(rest[1:], "\n## ")
		if nextHeading >= 0 {
			nextHeading++
		}
		end := len(rest)
		if markerEnd := strings.Index(rest, endMarker); markerEnd >= 0 && (nextHeading < 0 || markerEnd < nextHeading) {
			end = markerEnd + len(endMarker)
		} else if nextHeading >= 0 {
			end = nextHeading + 1
		}
		head := strings.TrimRight(content[:start], "\n")
		tail := strings.TrimLeft(rest[end:], "\n")
		if head != "" && tail != "" {
			content = head + "\n\n" + tail
		} else {
			content = head + tail
		}
	}
	return content
}

// sharedConfigWarnings flags committed MCP configs that run prism by a
// machine-specific path, or by a path that does not exist (CON-06).
func sharedConfigWarnings(root string) []string {
	var out []string
	for _, c := range []struct {
		rel  string
		maps [][]string
	}{
		{".mcp.json", [][]string{{"mcpServers"}}},
		{".cursor/mcp.json", [][]string{{"mcpServers"}}},
		{".vscode/mcp.json", [][]string{{"servers"}}},
		{".gemini/settings.json", [][]string{{"mcpServers"}}},
		{"opencode.json", [][]string{{"mcp"}, {"mcp", "servers"}}},
	} {
		cmd := existingPrismCommand(filepath.Join(root, c.rel), c.maps)
		if cmd == "" || !filepath.IsAbs(cmd) {
			continue
		}
		msg := fmt.Sprintf("%s runs prism as %s, a path on one machine; `prism init --refresh` writes plain `prism`", c.rel, cmd)
		if _, err := os.Stat(cmd); err != nil {
			msg = fmt.Sprintf("%s runs prism as %s, which does not exist here; `prism init --refresh` writes plain `prism`", c.rel, cmd)
		}
		out = append(out, msg)
	}
	if cmd := codexPrismCommand(filepath.Join(root, ".codex", "config.toml")); filepath.IsAbs(cmd) {
		out = append(out, fmt.Sprintf(".codex/config.toml runs prism as %s, a path on one machine; `prism init --refresh` writes plain `prism`", cmd))
	}
	return out
}

// codexPrismCommand returns command = "..." from [mcp_servers.prism], or "".
func codexPrismCommand(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = t == "[mcp_servers.prism]"
			continue
		}
		if in && strings.HasPrefix(t, "command") {
			if _, v, ok := strings.Cut(t, "="); ok {
				return strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return ""
}

// initWriteFailures counts config files init meant to write and could not;
// cmdInit exits 1 when it is non-zero (CON-07: these used to return
// silently, so init reported success over a file it never wrote).
var initWriteFailures int

func initWriteFailed(path string, err error) {
	initWriteFailures++
	fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", path, err)
}

// workspaceFolderVar is the project root as Cursor and VS Code interpolate it.
const workspaceFolderVar = "${workspaceFolder}"

// portablePrismCommand is the command written into committed MCP configs:
// plain "prism", resolved on each machine's PATH (CON-06). It warns when
// that resolves to a different binary than the one running, or to none.
func portablePrismCommand(self string) string {
	onPath, err := exec.LookPath("prism")
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: prism is not on PATH; the shared config runs `prism`, so agents will not start it until %s is on PATH\n", filepath.Dir(self))
	case !sameExecutable(onPath, self):
		fmt.Fprintf(os.Stderr, "warning: prism on PATH is %s, not the running %s; the shared config uses the one on PATH\n", onPath, self)
	}
	return "prism"
}

func sameExecutable(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// existingPrismCommand returns the command of the prism entry already in a
// JSON MCP config, or "".
func existingPrismCommand(path string, maps [][]string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, keys := range maps {
		var cur any = doc
		for _, k := range keys {
			m, _ := cur.(map[string]any)
			cur = m[k]
		}
		m, _ := cur.(map[string]any)
		e, _ := m["prism"].(map[string]any)
		switch c := e["command"].(type) {
		case string:
			return c
		case []any:
			if len(c) > 0 {
				s, _ := c[0].(string)
				return s
			}
		}
	}
	return ""
}

// detectSelfPath returns the absolute path to the running prism binary, or
// falls back to "prism" (assumes it's on PATH).
func detectSelfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "prism"
	}
	return filepath.Clean(exe)
}

// mcpEntry is the JSON structure every MCP-compatible tool expects.
// AlwaysLoad is no longer written: the 2026-08-29 deferral A/B (9 paired
// bed tasks, haiku) measured ZERO routing losses and recall delta +0.004
// with schemas deferred behind the client's ToolSearch hop — steering that
// names the tools is sufficient on current models, and deferral drops ~2k
// tokens of always-resident schema from every session. The field stays in
// the struct so --refresh recognizes (and rewrites) old entries that
// carry it.
type mcpEntry struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	AlwaysLoad bool     `json:"alwaysLoad,omitempty"`
}

// initRegisterMCPTools writes Prism into the selected harnesses' project-local
// config. The executable may be global, but its registration never is.
// permissions=false skips Claude Code's tool auto-allow; refresh=true rewrites
// only selected configs that already exist.
func initRegisterMCPTools(projectDir, prismBin string, harnesses []string, permissions, refresh, denyBuiltinSearch bool) []string {
	var written []string
	claudeSettings := filepath.Join(projectDir, ".claude", "settings.json")

	// Legacy denial cleanup: v0.50-era inits wrote Grep/Bash(grep:*)/Bash(rg:*)
	// into permissions.deny, and upgrading prism never removed them — so a
	// machine kept denying grep releases after the product stopped asking for
	// it (reported live, 2026-08-20; the benchmark reset documents the same
	// leftover skewing two whole runs). When THIS init is not requesting
	// denial, surface any stale trio: offer removal interactively, warn
	// loudly otherwise. Never silent either way — the entries are in a file
	// the user owns and may have authored deliberately.
	if harnessSelected(harnesses, "claude") && !denyBuiltinSearch {
		cleanupLegacyDenyEntries(claudeSettings)
	}

	// CON-06: these files are committed, so they name no machine path. The
	// IDEs get ${workspaceFolder}, which both interpolate, instead of this
	// checkout's absolute path.
	entry := mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact", workspaceFolderVar}}
	// Claude Code launches project-scope MCP servers with cwd at the project
	// root, so its entry needs no pinned absolute path — this keeps .mcp.json
	// portable and correct after the repo moves. The IDE writers below keep
	// the explicit dir because their launch cwd is not guaranteed.
	claudeEntry := mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact"}}

	// Build each harness's compact gateway registration and write it.
	type writer struct {
		harness string
		name    string
		path    string
		build   func(string) []byte
		maps    [][]string
	}

	writers := []writer{
		{
			harness: "claude",
			name:    "Claude Code",
			path:    filepath.Join(projectDir, ".mcp.json"),
			build: func(string) []byte {
				return buildMCPConfig("prism", claudeEntry)
			},
			maps: [][]string{{"mcpServers"}},
		},
		{
			harness: "cursor",
			name:    "Cursor",
			path:    filepath.Join(projectDir, ".cursor", "mcp.json"),
			build: func(string) []byte {
				return buildMCPConfig("prism", entry)
			},
			maps: [][]string{{"mcpServers"}},
		},
		{
			harness: "vscode",
			name:    "VS Code",
			path:    filepath.Join(projectDir, ".vscode", "mcp.json"),
			build: func(string) []byte {
				return buildVSCodeConfig(prismBin, workspaceFolderVar)
			},
			maps: [][]string{{"servers"}},
		},
		{
			harness: "gemini",
			name:    "Gemini CLI",
			path:    filepath.Join(projectDir, ".gemini", "settings.json"),
			build: func(string) []byte {
				return buildMCPConfig("prism", claudeEntry)
			},
			maps: [][]string{{"mcpServers"}},
		},
		{
			harness: "opencode",
			name:    "opencode",
			path:    filepath.Join(projectDir, "opencode.json"),
			build: func(path string) []byte {
				return buildOpencodeConfigForPath(prismBin, path)
			},
			maps: [][]string{{"mcp"}, {"mcp", "servers"}},
		},
	}

	for _, w := range writers {
		if !harnessSelected(harnesses, w.harness) {
			continue
		}
		p := w.path
		// --refresh rewrites only what a previous install configured: if the
		// config file does not exist yet, this tool was never set up and must
		// not be added now.
		if refresh {
			if _, err := os.Stat(p); err != nil {
				continue
			}
		}
		parent := filepath.Dir(p)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create %s config dir: %v\n", w.name, err)
			continue
		}
		// Skip writing .mcp.json if the prism entry is already correct.
		// Writing the file resets Claude Code's MCP approval state, which
		// forces the user to re-approve on every `prism init` run.
		if filepath.Base(p) == ".mcp.json" && mcpEntryAlreadyPresent(p, "prism", claudeEntry) {
			fmt.Printf("already registered with %s: %s\n", w.name, p)
			written = append(written, p)
			ensureClaudeCodeApproval(claudeSettings, "prism", permissions, denyBuiltinSearch)
			continue
		}
		isClaudeCode := w.name == "Claude Code"
		if old := existingPrismCommand(p, w.maps); old != "" && old != prismBin && filepath.IsAbs(old) {
			fmt.Printf("replaced the machine-specific command %s with %q in %s\n", old, prismBin, p)
		}
		content := w.build(p)
		// Merge rather than overwrite existing configs.
		merged, err := mergeOrCreate(p, content, w.maps...)
		if err != nil {
			initWriteFailed(p, err)
			continue
		}
		if err := os.WriteFile(p, merged, 0o644); err != nil {
			initWriteFailed(p, err)
			continue
		}
		fmt.Printf("registered with %s: %s\n", w.name, p)
		written = append(written, p)
		if isClaudeCode {
			ensureClaudeCodeApproval(claudeSettings, "prism", permissions, denyBuiltinSearch)
		}
	}

	if harnessSelected(harnesses, "codex") {
		codexPath := filepath.Join(projectDir, ".codex", "config.toml")
		if !(refresh && !fileExists(codexPath)) {
			if err := writePrismCodexConfig(codexPath, prismBin, []string{"mcp", "--compact"}); err != nil {
				initWriteFailed(codexPath, err)
			} else {
				fmt.Printf("registered with Codex CLI: %s\n", codexPath)
				written = append(written, codexPath)
			}
		}
	}

	if harnessSelected(harnesses, "windsurf") {
		legacyPath := filepath.Join(projectDir, ".windsurf", "mcp.json")
		removed, err := removeJSONMapEntry(legacyPath, []string{"mcpServers"}, "prism")
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not migrate legacy Windsurf config %s: %v\n", legacyPath, err)
		} else if removed {
			fmt.Printf("removed unsupported project-local Windsurf Prism registration: %s\n", legacyPath)
		}
		fmt.Fprintln(os.Stderr, "warning: Windsurf does not document a project-local MCP config; Prism wrote project steering only and did not modify user-global Windsurf settings")
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
	// alwaysLoad participates in "already correct": an entry written without
	// it would keep the server's schemas deferrable forever, because
	// --refresh skips entries it considers current. The one-time client
	// re-approval this rewrite triggers is the cost of the upgrade.
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

// buildOpencodeConfig returns a project opencode MCP stanza. opencode expects
// a "local" server whose command is a single argv array.
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
	// No pinned project dir: opencode launches the project config in repo cwd.
	c := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		MCP: map[string]opencodeServer{
			"prism": {Type: "local", Command: []string{prismBin, "mcp", "--compact"}, Enabled: true},
		},
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return b
}

func buildOpencodeConfigForPath(prismBin, path string) []byte {
	useV2 := false
	raw, err := os.ReadFile(path)
	if err == nil {
		var doc map[string]any
		if json.Unmarshal(raw, &doc) == nil {
			if mcpMap, ok := doc["mcp"].(map[string]any); ok {
				if _, ok := mcpMap["servers"].(map[string]any); ok {
					useV2 = true
				}
			}
		}
	}
	if !useV2 {
		useV2 = commandMajorVersion("opencode") >= 2
	}
	if useV2 {
		entry := map[string]any{"type": "local", "command": []string{prismBin, "mcp", "--compact"}, "disabled": false}
		out, _ := json.MarshalIndent(map[string]any{"mcp": map[string]any{"servers": map[string]any{"prism": entry}}}, "", "  ")
		return out
	}
	return buildOpencodeConfig(prismBin)
}

func commandMajorVersion(name string) int {
	tempHome, err := os.MkdirTemp("", "prism-version-probe-")
	if err != nil {
		return 0
	}
	defer os.RemoveAll(tempHome)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, "--version")
	blocked := map[string]bool{"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true, "APPDATA": true}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[strings.ToUpper(key)] {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env,
		"HOME="+tempHome,
		"USERPROFILE="+tempHome,
		"XDG_CONFIG_HOME="+filepath.Join(tempHome, ".config"),
		"APPDATA="+filepath.Join(tempHome, "AppData"),
	)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	for _, field := range strings.Fields(string(output)) {
		majorText := strings.SplitN(strings.TrimPrefix(field, "v"), ".", 2)[0]
		if major, err := strconv.Atoi(majorText); err == nil {
			return major
		}
	}
	return 0
}

// buildCodexSnippet returns the project-local Codex TOML block as text.
func buildCodexSnippet(prismBin string) string {
	return strings.Join([]string{
		"[mcp_servers.prism]",
		fmt.Sprintf("command = %q", prismBin),
		prismTOMLStringArray("args", []string{"mcp", "--compact"}),
		"",
		"[mcp_servers.prism.tools.prism]",
		`approval_mode = "approve"`,
	}, "\n") + "\n"
}

// printAgentConfig implements `prism init --print-config <id>`: render the
// config snippet for one agent and exit WITHOUT writing anything. Mirrors the
// targets initRegisterMCPTools writes.
func printAgentConfig(id, projectDir, prismBin string) int {
	entry := mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact", projectDir}}
	claudeEntry := mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact"}}

	var path, body string
	switch strings.ToLower(id) {
	case "claude", "claude-code":
		path = filepath.Join(projectDir, ".mcp.json")
		body = string(buildMCPConfig("prism", claudeEntry))
	case "cursor":
		path = filepath.Join(projectDir, ".cursor", "mcp.json")
		body = string(buildMCPConfig("prism", entry))
	case "windsurf":
		fmt.Println("# Windsurf has no documented project-local MCP config. Prism writes AGENTS.md steering and does not modify user-global settings.")
		return 0
	case "vscode", "vs-code":
		path = filepath.Join(projectDir, ".vscode", "mcp.json")
		body = string(buildVSCodeConfig(prismBin, projectDir))
	case "codex":
		path = filepath.Join(projectDir, ".codex", "config.toml")
		body = buildCodexSnippet(prismBin)
	case "gemini", "gemini-cli":
		path = filepath.Join(projectDir, ".gemini", "settings.json")
		body = string(buildMCPConfig("prism", claudeEntry))
	case "opencode":
		path = filepath.Join(projectDir, "opencode.json")
		body = string(buildOpencodeConfigForPath(prismBin, filepath.Join(projectDir, "opencode.json")))
	default:
		fmt.Fprintf(os.Stderr, "unknown harness %q. Known: %s\n", id, strings.Join(supportedHarnesses, ", "))
		return 2
	}
	fmt.Printf("# Add to %s\n\n%s\n", path, strings.TrimRight(body, "\n"))
	return 0
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
		"prism": {Type: "stdio", Command: prismBin, Args: []string{"mcp", "--compact", projectDir}},
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
		fmt.Sprintf("command = %q", prismBin),
		prismTOMLStringArray("args", args),
	)
	for _, arg := range args {
		if arg == "--compact" {
			// The compact gateway is read-only. Without explicit approval,
			// Codex rejects even its lookup/search calls under policy=never.
			lines = append(lines, "", "[mcp_servers.prism.tools.prism]", `approval_mode = "approve"`)
			break
		}
	}
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

// stripPrismNamedTable removes a [section.target] table and its complete
// subtree. Older Codex configs can contain only child tables such as
// [mcp_servers.prism.tools.search]; leaving those behind makes TOML recreate
// an implicit prism server with no transport and breaks Codex startup.
func stripPrismNamedTable(lines []string, section, targetName string) []string {
	prefixes := []string{
		"[" + section + "." + targetName + "]",
		"[" + section + "." + targetName + ".",
		"[" + section + ".\"" + targetName + "\"]",
		"[" + section + ".\"" + targetName + "\".",
		"[" + section + ".'" + targetName + "']",
		"[" + section + ".'" + targetName + "'.",
	}
	isPrismHeader := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range prefixes {
			if trimmed == prefix || (strings.HasSuffix(prefix, ".") && strings.HasPrefix(trimmed, prefix)) {
				return true
			}
		}
		return false
	}
	var out []string
	i := 0
	for i < len(lines) {
		if !isPrismHeader(lines[i]) {
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

// mergeOrCreate reads the existing JSON at path, removes Prism from every
// historical server-map location, and deep-merges the current entry. Invalid
// user JSON is never overwritten: it is backed up and returned as an error.
func mergeOrCreate(path string, content []byte, prismMaps ...[]string) ([]byte, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			existing = []byte("{}")
		} else {
			return nil, err
		}
	}
	var base, overlay map[string]any
	if err := json.Unmarshal(existing, &base); err != nil {
		backup := path + ".prism-backup"
		for n := 1; fileExists(backup); n++ {
			backup = fmt.Sprintf("%s.prism-backup.%d", path, n)
		}
		if writeErr := os.WriteFile(backup, existing, 0o644); writeErr != nil {
			return nil, fmt.Errorf("invalid JSON (also could not write backup: %v): %w", writeErr, err)
		}
		return nil, fmt.Errorf("invalid JSON; original left unchanged and backup written to %s: %w", backup, err)
	}
	if err := json.Unmarshal(content, &overlay); err != nil {
		return nil, fmt.Errorf("internal generated config is invalid: %w", err)
	}
	for _, keys := range prismMaps {
		deleteNestedMapEntry(base, keys, "prism")
	}
	if base == nil {
		base = make(map[string]any)
	}
	deepMergeJSON(base, overlay)
	out, err := encodeJSONFile(base, existing)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func deepMergeJSON(base, overlay map[string]any) {
	for key, value := range overlay {
		newMap, newIsMap := value.(map[string]any)
		oldMap, oldIsMap := base[key].(map[string]any)
		if newIsMap && oldIsMap {
			deepMergeJSON(oldMap, newMap)
			continue
		}
		base[key] = value
	}
}

func deleteNestedMapEntry(doc map[string]any, keys []string, entry string) bool {
	var current any = doc
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = object[key]
		if !ok {
			return false
		}
	}
	entries, ok := current.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := entries[entry]; !ok {
		return false
	}
	delete(entries, entry)
	return true
}

// removeLegacyGlobalMCPRegistrations migrates installations from the old
// user-global model. It removes only the Prism server entry and preserves all
// unrelated servers and settings. Project init calls this before writing the
// selected repository configs so an already-installed global server cannot
// shadow the project's binary or working directory.
func removeLegacyGlobalMCPRegistrations() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, err
	}
	targets := []struct {
		path string
		keys []string
	}{
		{filepath.Join(home, ".claude.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".cursor", "mcp.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".windsurf", "mcp.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".codeium", "mcp_config.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".gemini", "settings.json"), []string{"mcpServers"}},
		{filepath.Join(home, ".config", "zed", "settings.json"), []string{"context_servers"}},
		{filepath.Join(home, ".config", "opencode", "opencode.json"), []string{"mcp"}},
		{filepath.Join(home, ".config", "opencode", "opencode.json"), []string{"mcp", "servers"}},
	}
	var changed []string
	var failures []string
	seen := map[string]bool{}
	for _, target := range targets {
		removed, removeErr := removeJSONMapEntry(target.path, target.keys, "prism")
		if removeErr != nil {
			failures = append(failures, removeErr.Error())
		}
		if removed && !seen[target.path] {
			seen[target.path] = true
			changed = append(changed, target.path)
		}
	}
	codexPath := filepath.Join(home, ".codex", "config.toml")
	removed, removeErr := removePrismCodexConfig(codexPath)
	if removeErr != nil {
		failures = append(failures, removeErr.Error())
	}
	if removed {
		changed = append(changed, codexPath)
	}
	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	removed, removeErr = removeClaudeGlobalApproval(claudeSettings)
	if removeErr != nil {
		failures = append(failures, removeErr.Error())
	}
	if removed && !seen[claudeSettings] {
		changed = append(changed, claudeSettings)
	}
	for _, path := range changed {
		fmt.Printf("removed legacy user-global Prism registration: %s\n", path)
	}
	if len(failures) > 0 {
		return changed, errors.New(strings.Join(failures, "; "))
	}
	return changed, nil
}

func removeClaudeGlobalApproval(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("invalid JSON in %s", path)
	}
	changed := false
	if servers, ok := doc["enabledMcpjsonServers"].([]any); ok {
		kept := servers[:0]
		for _, value := range servers {
			if name, _ := value.(string); name == "prism" {
				changed = true
				continue
			}
			kept = append(kept, value)
		}
		doc["enabledMcpjsonServers"] = kept
	}
	if permissions, ok := doc["permissions"].(map[string]any); ok {
		denyValues, _ := permissions["deny"].([]any)
		removeLegacyDeny := true
		for _, rule := range prismDenyEntries {
			removeLegacyDeny = removeLegacyDeny && containsString(denyValues, rule)
		}
		for _, key := range []string{"allow", "deny"} {
			values, _ := permissions[key].([]any)
			kept := values[:0]
			removedFromKey := false
			for _, value := range values {
				rule, _ := value.(string)
				prismRule := rule == "mcp__prism" || strings.HasPrefix(rule, "mcp__prism__")
				legacyDeny := key == "deny" && removeLegacyDeny && containsString2(prismDenyEntries, rule)
				if prismRule || legacyDeny {
					changed = true
					removedFromKey = true
					continue
				}
				kept = append(kept, value)
			}
			if !removedFromKey {
				continue
			}
			if len(kept) == 0 {
				delete(permissions, key)
			} else {
				permissions[key] = kept
			}
		}
		doc["permissions"] = permissions
	}
	if !changed {
		return false, nil
	}
	out, err := encodeJSONFile(doc, raw)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("remove legacy Prism approval from %s: %w", path, err)
	}
	return true, nil
}

func removeJSONMapEntry(path string, keys []string, entry string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false, fmt.Errorf("invalid JSON in %s", path)
	}
	var current any = doc
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return false, nil
		}
		current, ok = object[key]
		if !ok {
			return false, nil
		}
	}
	entries, ok := current.(map[string]any)
	if !ok {
		return false, nil
	}
	if _, ok := entries[entry]; !ok {
		return false, nil
	}
	delete(entries, entry)
	out, err := encodeJSONFile(doc, raw)
	if err != nil || os.WriteFile(path, append(out, '\n'), 0o644) != nil {
		return false, fmt.Errorf("could not remove legacy Prism registration from %s", path)
	}
	return true, nil
}

func removePrismCodexConfig(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	before := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	after := stripPrismTOMLBlock(before, "mcp_servers", "prism")
	after = stripPrismNamedTable(after, "mcp_servers", "prism")
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		return false, nil
	}
	content := strings.TrimRight(strings.Join(after, "\n"), "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, fmt.Errorf("remove legacy Prism registration from %s: %w", path, err)
	}
	return true, nil
}

// ensureClaudeCodeApproval makes Claude Code both TRUST and AUTO-ALLOW the
// server in the project's .claude/settings.json:
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

// prismDenyEntries is the exact trio historic inits wrote; cleanup matches
// nothing else, so user-authored deny rules are never touched.
var prismDenyEntries = []string{"Grep", "Bash(grep:*)", "Bash(rg:*)"}

// cleanupLegacyDenyEntries detects the prism-written search-denial trio in a
// settings file when the current init did NOT ask for denial, and offers to
// remove it (interactive) or warns about it (non-interactive).
func cleanupLegacyDenyEntries(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		return
	}
	deny, _ := perms["deny"].([]any)
	var stale []string
	for _, d := range prismDenyEntries {
		if containsString(deny, d) {
			stale = append(stale, d)
		}
	}
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s denies Claude Code's built-in search (%s) —\n", path, strings.Join(stale, ", "))
	fmt.Fprintln(os.Stderr, "written by an earlier prism init; current prism does not need it.")
	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "Remove those permissions.deny lines to restore built-in search.")
		return
	}
	fmt.Fprint(os.Stderr, "Remove them now? [y/N]: ")
	var line string
	fmt.Scanln(&line)
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
	default:
		return
	}
	kept := make([]any, 0, len(deny))
	for _, d := range deny {
		s, _ := d.(string)
		if !containsString2(prismDenyEntries, s) {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		delete(perms, "deny")
	} else {
		perms["deny"] = kept
	}
	doc["permissions"] = perms
	out, err := encodeJSONFile(doc, data)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(out, '\n'), 0o644) != nil {
		return
	}
	if os.Rename(tmp, path) == nil {
		fmt.Printf("removed legacy built-in-search denial from %s\n", path)
	}
}

func containsString2(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func ensureClaudeCodeApproval(path, serverName string, allowTools, denyBuiltinSearch bool) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		initWriteFailed(path, err)
		return
	}
	var doc map[string]any
	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		// CON-07: an unparseable file (JSONC comments, a merge conflict)
		// used to become {} and be overwritten with Prism's keys only.
		if err := json.Unmarshal(raw, &doc); err != nil {
			initWriteFailed(path, fmt.Errorf("not valid JSON, left unchanged: %w", err))
			return
		}
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
	// The rule goes to the user's own settings.local.json, not this shared
	// file: see allowLocally. One an earlier prism wrote here is moved.
	rule := "mcp__" + serverName
	if allowTools {
		if dropAllow(doc, rule) {
			changed = true
		}
		perms, _ := doc["permissions"].(map[string]any)
		if perms == nil {
			perms = map[string]any{}
		}
		allow, _ := perms["allow"].([]any)
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
		}
	}

	if allowTools {
		defer allowLocally(path, rule)
	}
	if !changed {
		if allowTools {
			fmt.Printf("approved %s in Claude Code (trusted; %s auto-allowed in your own .claude/settings.local.json)\n", serverName, rule)
		}
		return
	}
	if p, _ := doc["permissions"].(map[string]any); p != nil && len(p) == 0 {
		delete(doc, "permissions")
	}
	data, err := encodeJSONFile(doc, raw)
	if err != nil {
		initWriteFailed(path, err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		initWriteFailed(path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		initWriteFailed(path, err)
		return
	}
	if allowTools {
		fmt.Printf("approved %s in Claude Code (trusted; %s auto-allowed in your own .claude/settings.local.json)\n", serverName, rule)
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
	indexAnywhere = true // an explicit index may build anywhere (CON-11)
	defer func() { indexAnywhere = false }()
	args, format, rc := takeFormat("index", args)
	if rc != 0 {
		return rc
	}
	dir, flags, rc := dirOnlyArgs("index", args, "--allow-heuristic", "--force", "--rebuild", "--untrusted", "--trusted")
	if rc != 0 {
		return rc
	}
	switch {
	case flags["--untrusted"] && flags["--trusted"]:
		return usageError(fmt.Errorf("index: --untrusted and --trusted are exclusive"))
	case flags["--untrusted"]:
		trust.SetOverride(trust.Untrusted)
	case flags["--trusted"]:
		trust.SetOverride(trust.Trusted)
	}
	allowHeuristic := flags["--allow-heuristic"]
	// An explicit index is the user trusting this repository's files, its
	// declared links included; they are confirmed before the engine is told
	// which repositories are linked.
	if own, err := config.LoadFromDir(mustAbs(dir)); err == nil {
		workspace.ConfirmLinks(mustAbs(dir), own)
	}
	if flags["--rebuild"] {
		// Before any engine opens the store: a broken index may not open.
		if err := requireDir(mustAbs(dir)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		removed, err := grove.RemoveIndex(context.Background(), mustAbs(dir))
		if err != nil {
			fmt.Fprintln(os.Stderr, "index --rebuild:", err)
			return 1
		}
		if len(removed) > 0 {
			fmt.Fprintf(os.Stderr, "prism: removed the index (%s); building it from scratch\n", strings.Join(removed, ", "))
		}
	}
	cfg, client, err := newClientOpts(dir, true, flags["--force"])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	workspace.Register(mustAbs(dir))
	_ = cfg
	// No default deadline: a deadline cancels the index itself, and a first
	// index of a never-built project compiles its dependencies once (grafana:
	// ~29 min cold Go build cache, ~60s after). Cancelling threw that work
	// away and the next command started over. PRISM_INDEX_TIMEOUT still sets
	// one; Ctrl+C stops the run.
	ctx, cancel := context.WithCancel(context.Background())
	if v := os.Getenv("PRISM_INDEX_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), d)
		}
	}
	defer cancel()
	if st, err := client.Status(ctx); err == nil && st != nil && st.FilesIndexed == 0 {
		fmt.Fprintln(os.Stderr, "prism: first index. Compiler-backed analysis compiles the project's dependencies once\n"+
			"       (Go build cache, javac, the TypeScript checker). If the project was never built on this\n"+
			"       machine this can take several minutes; later indexes are incremental.")
	}
	done := make(chan struct{})
	defer close(done)
	go func(start time.Time) {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				fmt.Fprintf(os.Stderr, "prism: still indexing (%s elapsed)\n", time.Since(start).Round(time.Second))
			}
		}
	}(time.Now())
	res, err := client.Index(ctx, mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "index:", err)
		return 1
	}
	printOutput(res, format)
	if len(res.Readiness) == 0 {
		return 0
	}
	// The baseline index must come from a project that compiles: without
	// the compiler pass, call and reference resolution for these languages
	// is name-based. The index is written either way; say so loudly.
	fmt.Fprintln(os.Stderr, "\nprism: compiler-backed analysis is missing for:")
	for _, r := range res.Readiness {
		fmt.Fprintf(os.Stderr, "  - %s: %s\n    fix: %s\n", r.Language, r.Problem, r.Fix)
	}
	if allowHeuristic {
		fmt.Fprintln(os.Stderr, "prism: continuing with name-based resolution (--allow-heuristic); accuracy for these languages is lower.")
		return 0
	}
	fmt.Fprintln(os.Stderr, "prism: the index was written, but results for these languages are name-based and less accurate.\n"+
		"       Fix the above and rerun `prism index`, or pass --allow-heuristic to accept name-based results.")
	return 3
}

// printDoctor prints doctor's answer: JSON with --format json, else a
// short text summary; the per-language manifest stays in JSON (CON-01).
func printDoctor(format outputFormat, m map[string]any) {
	if format == formatJSON {
		printJSON(m)
		return
	}
	langs := m["languages"]
	delete(m, "languages")
	text := genericTextOf(m)
	fmt.Print(text)
	if langs != nil {
		fmt.Println("languages: per-language analysis tiers and caveats with --format json")
	}
}

// genericTextOf renders any value (struct, map) as generic text.
func genericTextOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v) + "\n"
	}
	var x any
	_ = json.Unmarshal(b, &x)
	var sb strings.Builder
	writeGeneric(&sb, "", x)
	return sb.String()
}

// cmdTrust records the user's trust decision for a repository (SEC-01).
func cmdTrust(cmd string, args []string) int {
	dir, _, rc := dirOnlyArgs(cmd, args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	state := trust.Trusted
	if cmd == "untrust" {
		state = trust.Untrusted
	}
	if err := trust.Set(root, state); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		return 1
	}
	fmt.Printf("%s: %s (recorded in %s)\n", root, trust.For(root), trust.Path())
	if state == trust.Untrusted {
		fmt.Println("Indexing will not run this repository's build tools; Java, TypeScript and Rust answers become name-based. Run `prism index --rebuild` to drop edges an earlier trusted index produced.")
	}
	return 0
}

func cmdStatus(args []string) int {
	args, format, rc := takeFormat("status", args)
	if rc != 0 {
		return rc
	}
	dir, _, rc := dirOnlyArgs("status", args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Store-only fast path: status is three COUNT(*) queries; booting the full
	// engine (newClient -> EnsureRunning) rehydrates the whole graph first —
	// ~1.3s of work on a 500k-edge index that status never reads. Same counts,
	// same output shape, ~5ms.
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root)).OneShot()
	res, err := client.QuickStatus(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	printOutput(res, format)
	return 0
}

func cmdDoctor(args []string) int {
	config.QuietProblems = true // listed under warnings instead
	args, format, rc := takeFormat("doctor", args)
	if rc != 0 {
		return rc
	}
	dir, _, rc := dirOnlyArgs("doctor", args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	_, client, err := newClient(root)
	if err != nil {
		printDoctor(format, map[string]any{
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
		printDoctor(format, map[string]any{
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
	for _, r := range graph.Readiness {
		state = "warning"
		warnings = append(warnings, r.Language+": compiler-backed analysis missing ("+r.Problem+"); results are name-based. Fix: "+r.Fix)
	}
	warnings = append(warnings, sharedConfigWarnings(root)...)
	if c, err := config.LoadFromDir(root); err == nil {
		for _, p := range c.Problems {
			warnings = append(warnings, "prism.yaml: "+p)
		}
	}
	trustState := trust.For(root).String()
	if strings.HasPrefix(trustState, "untrusted") {
		warnings = append(warnings, "repository is "+trustState+": its build tools do not run, so Java (Maven/Gradle), TypeScript and Rust (rust-analyzer) analysis is name-based; `prism trust` enables it")
	}
	groveLinked := "unknown"
	if g, ok := version.LinkedGrove(); ok {
		groveLinked = g.String()
		if g.Replaced {
			warnings = append(warnings, "grove engine is "+g.String()+", not a released version; results may differ from a release build")
		}
	}
	printDoctor(format, map[string]any{
		"status":   state,
		"version":  version.Version,
		"grove":    groveLinked,
		"trust":    trustState,
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
		// grove.Capabilities() is Grove's own release-level per-language and
		// per-operation quality manifest (indexing/resolution tier,
		// limitations, caveats) -- `grove doctor` reports it directly; a user
		// asking "does prism support X" had no answer from `prism doctor`
		// before this, only from a separate `grove doctor` run.
		"languages": grove.Capabilities(),
	})
	return 0
}

func cmdQuery(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism query --terms a,b,c [dir]  (--terms is REQUIRED; use prism search first when no anchor is known)")
		return 2
	}
	dir := "."
	dirSet := false
	profile := ""
	limit := 50
	maxFiles := 0
	delivery := ""
	format := formatText
	repo := ""
	var terms []string
	var include []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--repo":
			repo, err = flagValue("query", args, i)
			i++
		case "--profile":
			profile, err = flagValue("query", args, i)
			i++
		case "--limit":
			limit, err = positiveIntFlag("query", args, i)
			i++
		case "--terms":
			var more []string
			more, err = listFlag("query", args, i)
			terms = append(terms, more...)
			i++
		case "--include":
			var more []string
			more, err = listFlag("query", args, i)
			include = append(include, more...)
			i++
		case "--depth", "--graph-depth":
			// graph_depth has never been read by any handler; sending it was
			// a silent no-op. Say so instead of pretending it tunes anything.
			fmt.Fprintln(os.Stderr, "query: --depth/--graph-depth has no effect and was removed; expansion is a fixed one-hop call neighborhood")
			return 2
		case "--delivery":
			delivery, err = choiceFlag("query", args, i, "source", "symbols")
			i++
		case "--max-files":
			maxFiles, err = positiveIntFlag("query", args, i)
			i++
		case "--format":
			format, err = formatFlag("query", args, i)
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("query", a)
			}
			if dirSet {
				return usageError(fmt.Errorf("query: takes one directory, got %q and %q; pass terms with --terms a,b", dir, a))
			}
			dir, dirSet = a, true
		}
		if err != nil {
			return usageError(err)
		}
	}
	if len(terms) == 0 {
		fmt.Fprintln(os.Stderr, "query: --terms is required; use prism search first when no anchor is known")
		return 2
	}
	if len(terms) > maxBatchTerms {
		fmt.Fprintf(os.Stderr, "query: at most %d terms per call (the MCP gateway rejects more); ask about the terms the current question needs\n", maxBatchTerms)
		return 2
	}
	invokeArgs := map[string]any{"terms": terms, "limit": limit}
	if delivery != "" {
		invokeArgs["delivery"] = delivery
	}
	if maxFiles > 0 {
		invokeArgs["max_files"] = maxFiles
	}
	if profile != "" {
		invokeArgs["profile"] = profile
	}
	if len(include) > 0 {
		invokeArgs["include"] = include
	}
	out, text, rendered, err := invokeRepoToolText(dir, repo, "prism_query", invokeArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		return 1
	}
	printToolResult("query", out, text, rendered, format)
	return 0
}

func cmdRead(args []string) int {
	args, rc := positionalsFirst("read", "a file", "prism read <file> [file...] [--from N --to M | --offset N --limit N] [--repo <id>] [--dir <path>]", 1, args, "--from", "--to", "--repo", "--dir", "--offset", "--limit", "--format")
	if rc != 0 {
		return rc
	}
	file := args[0]
	dir := "."
	includeSensitive := false
	repo := ""
	dirSet := false
	var extra []string
	format := formatText
	offset, limit := 0, 0
	from, to := 0, 0
	for i := 1; i < len(args); i++ {
		a := args[i]
		// --flag=value spellings, as other commands accept.
		if name, value, ok := strings.Cut(a, "="); ok && strings.HasPrefix(name, "--") {
			args = append(args[:i+1], append([]string{value}, args[i+1:]...)...)
			args[i] = name
			a = name
		}
		var err error
		switch a {
		case "--from", "--to":
			// The MCP read's own field names (an agent switching from the
			// tool to the CLI writes these): an inclusive line range.
			var n int
			n, err = positiveIntFlag("read", args, i)
			if a == "--from" {
				from = n
			} else {
				to = n
			}
			i++
		case "--repo":
			repo, err = flagValue("read", args, i)
			i++
		case "--dir":
			dir, err = flagValue("read", args, i)
			dirSet = true
			i++
		case "--offset", "--limit":
			// Line-window parity with `sed -n A,Bp`, which is a quarter of
			// every file read agents make.
			var n int
			n, err = positiveIntFlag("read", args, i)
			if a == "--offset" {
				offset = n
			} else {
				limit = n
			}
			i++
		case "--format":
			format, err = formatFlag("read", args, i)
			i++
		case "--include-sensitive":
			includeSensitive = true
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("read", a)
			}
			extra = append(extra, a)
		}
		if err != nil {
			return usageError(err)
		}
	}
	if (from > 0 || to > 0) && (offset > 0 || limit > 0) {
		return usageError(fmt.Errorf("read: use --from/--to or --offset/--limit, not both"))
	}
	if from > 0 && to > 0 && to < from {
		return usageError(fmt.Errorf("read: --to %d is before --from %d; the range is inclusive, so --to must be >= --from", to, from))
	}
	// `prism read <file> <dir>` is the legacy form; any other extra argument
	// is another file. The last path used to be taken as the directory, so
	// `prism read a.go b.go c.go` failed with "not a directory" (Codex, jackson).
	if len(extra) == 1 && !dirSet {
		if fi, err := os.Stat(extra[0]); err == nil && fi.IsDir() {
			dir, extra = extra[0], nil
		}
	}
	if repo != "" {
		root, files, err := readRepoPaths(dir, repo, append([]string{file}, extra...))
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			return 1
		}
		dir, file, extra = root, files[0], files[1:]
	}
	if len(extra) > 0 {
		return cmdReadFiles(dir, append([]string{file}, extra...), from, to, offset, limit, format)
	}
	if from > 0 {
		offset = from
		if to >= from {
			limit = to - from + 1
		}
	} else if to > 0 {
		offset, limit = 1, to
	}
	readArgs := map[string]any{"file": file}
	if includeSensitive {
		readArgs["include_sensitive"] = true
	}
	if offset > 0 {
		readArgs["offset"] = offset
	}
	if limit > 0 {
		readArgs["limit"] = limit
	}
	var out any
	var text string
	var rendered bool
	var err error
	if format == formatText && !includeSensitive {
		compactArgs := map[string]any{"file": file}
		if includeSensitive {
			compactArgs["include_sensitive"] = true
		}
		start := 1
		if offset > 0 {
			start = offset
			compactArgs["from"] = start
		}
		if limit > 0 {
			compactArgs["to"] = start + limit - 1
		}
		out, text, rendered, err = invokeCompactReadText(dir, compactArgs)
	} else {
		out, text, rendered, err = invokeToolText(dir, "prism_read", readArgs)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return exitForError(err)
	}
	printToolResult("read", out, text, rendered, format)
	return exitForAnswer("read", out)
}

// maxBatchTerms is the compact MCP gateway's limit for terms and names in one
// call (internal/mcp/compactargs.go). query and multi-file read apply it, so
// a request the gateway would reject is not answered here: a 12-term CLI
// query returned 30 KB of source on jackson (Codex, 2026-10-18). search stays
// uncapped on purpose (a82bd08); its answer has its own response budget.
const maxBatchTerms = 10

// cmdReadFiles reads several files in one answer through the gateway's ranges
// form, the CLI counterpart of op=read with ranges. Each file is read from its
// first line under the same per-range window as MCP.
func cmdReadFiles(dir string, files []string, from, to, offset, limit int, format outputFormat) int {
	if from > 0 || to > 0 || offset > 0 || limit > 0 {
		fmt.Fprintln(os.Stderr, "read: --from/--to/--offset/--limit apply to one file; read the files one at a time for a line range")
		return 2
	}
	if len(files) > maxBatchTerms {
		fmt.Fprintf(os.Stderr, "read: at most %d files per call (the MCP gateway rejects more)\n", maxBatchTerms)
		return 2
	}
	ranges := make([]any, 0, len(files))
	for _, f := range files {
		ranges = append(ranges, map[string]any{"file": f, "from": 1, "to": mcp.CompactReadLimit})
	}
	out, text, rendered, err := invokeCompactReadText(dir, map[string]any{"ranges": ranges})
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return exitForError(err)
	}
	printToolResult("read", out, text, rendered, format)
	return exitForAnswer("read", out)
}

func cmdSearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism search <term> [term...] [--dir <path>]")
		return 2
	}
	limit, limitSet := 25, false
	dir := ""
	format := formatText
	scope := ""
	regex := false
	caseMode, word, noDefaultExcludes, includeSensitive := "", false, false, false
	var paths, globs []string
	filesOnly := false
	includeBodies := true
	includeBodiesSet := false
	exhaustive := false
	rollupOnly := false
	contextLines := 0
	contextSet := false
	repo := ""
	var bare []string
args:
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--":
			// End of flags: every later argument is a term, so a term that
			// starts with "-" can be searched (BUG-18).
			bare = append(bare, args[i+1:]...)
			break args
		case "--dir":
			// The unambiguous way to say where to search, now that bare
			// arguments are terms rather than "term then directory".
			dir, err = flagValue("search", args, i)
			i++
		case "--scope":
			// The steering has documented `prism search <t> --scope text` since
			// v0.37.0 while this parser knew only --limit and --format, so the
			// flag was dropped and every CLI text search silently returned the
			// merged symbol view instead. A Bash-only subagent following its own
			// instructions got the wrong answer shape and no indication why.
			scope, err = choiceFlag("search", args, i, "text", "symbols", "both")
			i++
		case "--repo":
			// A linked repository's id, or "all": text matches there.
			repo, err = flagValue("search", args, i)
			i++
		case "--regex":
			regex = true
		case "--ignore-case", "-i":
			caseMode = "insensitive"
		case "--case-sensitive", "-s":
			caseMode = "sensitive"
		case "--word", "-w":
			word = true
		case "--no-default-excludes":
			noDefaultExcludes = true
		case "--include-sensitive":
			includeSensitive = true
		case "--path", "--paths":
			// The grep operand, restored. `prism search alias --path
			// octodns/manager.py` is what an agent that already knows the
			// file wants; without it, it uses grep instead.
			var p string
			p, err = flagValue("search", args, i)
			paths = append(paths, p)
			i++
		case "--glob", "--include":
			var g string
			g, err = flagValue("search", args, i)
			globs = append(globs, g)
			i++
		case "--include-bodies":
			includeBodies = true
			includeBodiesSet = true
		case "--no-bodies":
			includeBodies = false
			includeBodiesSet = true
		case "--files-only", "-l":
			filesOnly = true
		case "--exhaustive", "--all":
			exhaustive = true
		case "--rollup-only":
			rollupOnly = true
		case "--context", "-C":
			if i+1 >= len(args) {
				return usageError(fmt.Errorf("search: --context requires a non-negative integer"))
			}
			n, convErr := strconv.Atoi(args[i+1])
			if convErr != nil || n < 0 {
				return usageError(fmt.Errorf("search: --context requires a non-negative integer, got %q", args[i+1]))
			}
			contextLines, contextSet = n, true
			i++
		case "--limit":
			limit, err = positiveIntFlag("search", args, i)
			limitSet = true
			i++
		case "--format":
			format, err = formatFlag("search", args, i)
			i++
		default:
			// An unknown flag used to be dropped in silence — the mechanism
			// behind this whole class of bug. Fail loudly instead.
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "search: unknown flag %q\n", a)
				return 2
			}
			bare = append(bare, a)
		}
		if err != nil {
			return usageError(err)
		}
	}
	if len(bare) == 0 {
		fmt.Fprintln(os.Stderr, "usage: prism search <term> [term...] [--dir <path>]")
		return 2
	}
	if len(bare) > maxBatchTerms {
		return usageError(fmt.Errorf("search takes up to %d terms; got %d. Split into two calls", maxBatchTerms, len(bare)))
	}
	if msg, ok := mcpSpelling("search", bare); ok {
		fmt.Fprintln(os.Stderr, msg)
		return 2
	}
	if !regex {
		for _, t := range bare {
			if looksLikeRegex(t) {
				fmt.Fprintf(os.Stderr, "search: %q is matched as literal text; add --regex to use it as a regular expression\n", t)
			}
		}
	}
	// `prism search <keyword> <dir>` was the documented form for many
	// releases, so the two-argument case still honours it when the second
	// argument really is a directory — but says so, because the same two
	// words are now also a legitimate two-term search.
	if dir == "" && len(bare) == 2 {
		if fi, err := os.Stat(bare[1]); err == nil && fi.IsDir() {
			fmt.Fprintf(os.Stderr,
				"search: reading %q as the directory, not a second term (legacy `prism search <term> <dir>` form); "+
					"use --dir %s to be explicit, or --dir . to search for both words\n", bare[1], bare[1])
			dir, bare = bare[1], bare[:1]
		}
	}
	if dir == "" {
		dir = "."
	}
	var query any = bare[0]
	if len(bare) > 1 {
		query = bare
	}
	// An explicit --limit is a caller-chosen shape; without it the MCP
	// default response budget applies (internal/mcp/searchbudget.go).
	searchArgs := map[string]any{"query": query}
	if limitSet {
		searchArgs["limit"] = limit
	}
	if scope != "" {
		searchArgs["scope"] = scope
	}
	if regex {
		searchArgs["regex"] = true
	}
	if caseMode != "" {
		searchArgs["case"] = caseMode
	}
	if word {
		searchArgs["word"] = true
	}
	if noDefaultExcludes {
		searchArgs["no_default_excludes"] = true
	}
	if includeSensitive {
		searchArgs["include_sensitive"] = true
	}
	if len(paths) > 0 {
		searchArgs["path"] = paths
	}
	if len(globs) > 0 {
		searchArgs["glob"] = globs
	}
	if filesOnly {
		searchArgs["files_only"] = true
	}
	if includeBodiesSet {
		searchArgs["include_bodies"] = includeBodies
	}
	if exhaustive {
		searchArgs["exhaustive"] = true
	}
	if rollupOnly {
		searchArgs["rollup_only"] = true
	}
	if contextSet {
		searchArgs["context"] = contextLines
	}
	if repo != "" {
		searchArgs["repo"] = repo
	}
	out, compactText, rendered, err := invokeSearchWithCompactRendering(dir, searchArgs, format == formatText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		return 1
	}
	printToolResult("search", out, compactText, rendered, format)
	return 0
}

func invokeSearchWithCompactRendering(dir string, args map[string]any, renderText bool) (any, string, bool, error) {
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, "", false, err
	}
	defer client.Shutdown()
	// A text search is a pure grep (searchOne): it reads no graph, so it
	// must not build one first. On a large repository the first build takes
	// minutes, and a documented-as-instant text search waited for it.
	if args["scope"] != "text" {
		if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
			return nil, "", false, err
		}
	}
	h := mcp.NewHandler(cfg, root, client)
	out, err := h.Invoke("prism_search", args)
	if err != nil || !renderText {
		return out, "", false, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return out, "", false, nil
	}
	text, rendered := h.RenderCompactSearchText(context.Background(), m, args)
	return out, text, rendered, nil
}

func cmdLookup(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name>... [--dir <path>]  (two arguments use the second as a directory if it exists)")
		return 2
	}
	explicitDir := ""
	format := formatText
	repo := ""
	fileHint := ""
	var fields []any
	var bare []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintln(os.Stderr, "lookup: --dir requires a path")
				return 2
			}
			explicitDir = args[i+1]
			i++
		case "--fields":
			items, err := listFlag("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, item := range items {
				fields = append(fields, item)
			}
			i++
		case "--repo":
			v, err := flagValue("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			repo = v
			i++
		case "--file", "--path":
			v, err := flagValue("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			fileHint = v
			i++
		case "--format":
			f, err := formatFlag("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("lookup", a)
			}
			bare = append(bare, a)
		}
	}
	if len(bare) == 0 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name>... [--dir <path>]  (two arguments use the second as a directory if it exists)")
		return 2
	}
	dir := "."
	if explicitDir != "" {
		dir = explicitDir
	} else if len(bare) == 2 {
		// Preserve the positional directory form, while allowing the natural
		// two-name batch when the second argument is not a directory. A
		// path-shaped argument is always the directory, so a mistyped path
		// still fails instead of becoming a lookup in the wrong tree; only a
		// bare word that is also a directory is ambiguous enough to note.
		if lookupArgIsPath(bare[1]) {
			dir = bare[1]
			bare = bare[:1]
		} else if info, err := os.Stat(bare[1]); err == nil && info.IsDir() {
			fmt.Fprintf(os.Stderr, "lookup: reading %q as the directory (legacy form); use --dir . to look up both names\n", bare[1])
			dir = bare[1]
			bare = bare[:1]
		}
	}
	if len(bare) > 10 {
		fmt.Fprintln(os.Stderr, "lookup: at most 10 names per call")
		return 2
	}
	var names any = bare[0]
	// Text output goes through the batch form even for one name: that is the
	// shape (and the "// lookup <name>" header) an agent gets over MCP.
	if len(bare) > 1 || format == formatText {
		names = bare
	}
	for _, n := range bare {
		warnShellExpandedName("lookup", n)
	}
	if repo == "" && fileHint != "" {
		selected, err := repoForFileHint(dir, fileHint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lookup:", err)
			return 1
		}
		repo = selected
	}
	callArgs := map[string]any{"name": names}
	if len(fields) > 0 {
		callArgs["fields"] = fields
	}
	if repo != "" {
		callArgs["repo"] = repo
	}
	if fileHint != "" {
		callArgs["file"] = fileHint
	}
	var out any
	var text string
	var rendered bool
	var err error
	if repo != "" && repo != "all" && fileHint != "" {
		delete(callArgs, "repo")
		out, text, rendered, err = invokeRepoToolText(dir, repo, "prism_lookup", callArgs)
	} else {
		out, text, rendered, err = invokeToolText(dir, "prism_lookup", callArgs)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		return exitForError(err)
	}
	// A batch answer is {results:[{name,result}]}, which the generic text
	// printer does not know; render it the way MCP does.
	printToolResult("lookup", out, text, rendered, format)
	return exitForAnswer("lookup", out)
}

// lookupArgIsPath reports whether a positional lookup argument can only be a
// directory: no symbol name is ".", "..", or starts with "/", "./", "../" or "~".
func lookupArgIsPath(a string) bool {
	if a == "." || a == ".." || filepath.IsAbs(a) {
		return true
	}
	for _, p := range []string{"./", "../", "~", ".\\", "..\\"} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

// cmdNode is the one-shot orientation view — a symbol's source + neighbours,
// or a file's source + defined symbols + dependents.
func cmdNode(args []string) int {
	args, rc := positionalsFirst("node", "a symbol or file", "prism node <symbol-or-file> [dir] [--file <path>] [--format text|json]", 1, args, "--file", "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	fileHint := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--file":
			v, err := flagValue("node", args, i)
			if err != nil {
				return usageError(err)
			}
			fileHint = v
			i++
		case "--format":
			f, err := formatFlag("node", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("node", a)
			}
			if err := positionalDir("node", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{"name": name}
	if fileHint != "" {
		callArgs["file"] = fileHint
	}
	out, err := invokeTool(dir, "prism_node", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "node:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("node", out)
}

func cmdResolve(args []string) int {
	args, rc := positionalsFirst("resolve", "a name", "prism resolve <name> [dir]", 1, args, "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--format" {
			f, err := formatFlag("resolve", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		} else if strings.HasPrefix(a, "-") {
			return rejectUnknownFlag("resolve", a)
		} else if err := positionalDir("resolve", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, err := invokeTool(dir, "prism_resolve", map[string]any{"name": name})
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("resolve", out)
}

func cmdEdges(args []string) int {
	args, rc := positionalsFirst("edges", "a symbol", "prism edges <name> [--direction out|in|both] [--kinds calls,uses-type,implements,...] [dir]", 1, args, "--dir", "--direction", "--kinds", "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	direction := "both"
	var kinds []any
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir", "--direction":
			d, err := choiceFlag("edges", args, i, "out", "in", "both")
			if err != nil {
				return usageError(err)
			}
			direction = d
			i++
		case "--kinds":
			items, err := listFlag("edges", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, item := range items {
				kinds = append(kinds, item)
			}
			i++
		case "--format":
			f, err := formatFlag("edges", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("edges", a)
			}
			if err := positionalDir("edges", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{"name": name, "direction": direction}
	if len(kinds) > 0 {
		callArgs["kinds"] = kinds
	}
	out, err := invokeTool(dir, "prism_edges", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "edges:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("edges", out)
}

// cmdCallers prints the transitive callers of one symbol (GAP-03).
func cmdCallers(args []string) int {
	args, rc := positionalsFirst("callers", "a symbol", "prism callers <symbol> [dir] [--depth N]", 1, args, "--format", "--file", "--depth")
	if rc != 0 {
		return rc
	}
	callArgs := map[string]any{"name": args[0]}
	dir, dirSet := ".", false
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--depth":
			n, err := positiveIntFlag("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			callArgs["depth"] = n
			i++
		case "--file":
			v, err := flagValue("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			callArgs["file"] = v
			i++
		case "--format":
			f, err := formatFlag("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("callers", a)
			}
			if err := positionalDir("callers", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, "prism_callers", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "callers:", err)
		return exitForError(err)
	}
	printToolResult("callers", out, text, rendered, format)
	return exitOK
}

// cmdPath prints the shortest resolved call path from one symbol to
// another (GAP-03).
func cmdPath(args []string) int {
	args, rc := positionalsFirst("path", "two symbols", "prism path <from> <to> [dir]", 2, args, "--format")
	if rc != 0 {
		return rc
	}
	callArgs := map[string]any{"from": args[0], "to": args[1]}
	dir, dirSet := ".", false
	format := formatText
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			f, err := formatFlag("path", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("path", a)
			}
			if err := positionalDir("path", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, "prism_call_path", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "path:", err)
		return exitForError(err)
	}
	printToolResult("path", out, text, rendered, format)
	if m, ok := out.(map[string]any); ok {
		if p, _ := m["path"].([]map[string]any); len(p) == 0 {
			return exitNotFound
		}
	}
	return exitOK
}

func cmdReferences(args []string) int {
	args, rc := positionalsFirst("references", "a name", "prism references <name> [dir]", 1, args, "--format", "--file", "--path", "--kind")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	file, kind := "", ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--kind":
			v, err := flagValue("references", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, k := range strings.Split(v, ",") {
				switch strings.TrimSpace(k) {
				case "call", "read", "write", "init", "decl", "type", "type-use":
				default:
					return usageError(fmt.Errorf("references: --kind %q: use call, read, write, decl or type", k))
				}
			}
			kind = v
			i++
		case "--format":
			f, err := formatFlag("references", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			v, err := flagValue("references", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("references", a)
			}
			if err := positionalDir("references", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{"name": name}
	if file != "" {
		callArgs["file"] = file
	}
	if kind != "" {
		callArgs["kind"] = kind
	}
	out, err := invokeTool(dir, "prism_references", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "references:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("references", out)
}

func cmdChangeImpact(args []string) int {
	for _, a := range args {
		if a == "--diff" {
			return cmdChangeImpactDiff(args)
		}
	}
	args, rc := positionalsFirst("change-impact", "a symbol", "prism change-impact Type.method [more names] [./dir | --dir <path>]", 1, args, "--dir", "--repo", "--format", "--file", "--path")
	if rc != 0 {
		return rc
	}
	queries := []string{args[0]}
	dir, dirSet := ".", false
	format := formatText
	file := ""
	repo := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			dir, dirSet = v, true
			i++
		case "--repo":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			repo = v
			i++
		case "--format":
			f, err := formatFlag("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages: only
			// types declared in a matching file seed the closure.
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("change-impact", a)
			}
			if lookupArgIsPath(a) {
				if err := positionalDir("change-impact", &dir, &dirSet, a); err != nil {
					return usageError(err)
				}
			} else {
				queries = append(queries, a)
			}
		}
	}
	if len(queries) > maxBatchTerms {
		return usageError(fmt.Errorf("change-impact: at most %d names per call", maxBatchTerms))
	}
	session, err := newRepoToolSession(dir, repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
		return 1
	}
	defer session.close()
	var results []map[string]any
	var texts strings.Builder
	allRendered, failed := true, false
	var firstErr error
	for _, query := range queries {
		warnShellExpandedName("change-impact", query)
		callArgs := map[string]any{"query": query}
		if file != "" {
			callArgs["file"] = file
		}
		out, text, rendered, err := session.invoke("prism_change_impact", callArgs)
		if err != nil && repo == "" {
			// The name is not defined here but a linked repository may define
			// it (lookup already answers that way). Ask them in turn instead
			// of making the agent rerun with --repo: that rerun was 2 of 10
			// change-impact calls in the 11-16 cross-repo run.
			out, text, rendered, err = changeImpactInLinked(dir, session, callArgs, err)
		}
		if len(queries) == 1 {
			if err != nil {
				fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
				return exitForError(err)
			}
			printToolResult("change-impact", out, text, rendered, format)
			return 0
		}
		entry := map[string]any{"query": query}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			entry["error"] = err.Error()
			fmt.Fprintf(&texts, "// %s: ERROR: %v\n", query, err)
			failed = true
		} else {
			entry["result"] = out
			allRendered = allRendered && rendered
			texts.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				texts.WriteByte('\n')
			}
		}
		results = append(results, entry)
	}
	printToolResult("change-impact", map[string]any{"results": results}, texts.String(), allRendered, format)
	if failed {
		return exitForError(firstErr)
	}
	return 0
}

// changeImpactInLinked retries an indexed-name miss in the linked repository
// selected by lookup. Ambiguous definitions remain errors; linked clients are
// owned by the parent session and reused for the rest of the batch.
func changeImpactInLinked(dir string, session *repoToolSession, callArgs map[string]any, miss error) (any, string, bool, error) {
	if !strings.Contains(miss.Error(), "is indexed") || session.handler.Workspace == nil || len(session.handler.Workspace.Linked()) == 0 {
		return nil, "", false, miss
	}
	name, _ := callArgs["query"].(string)
	found, err := session.handler.Invoke("prism_lookup", map[string]any{"name": name})
	m, _ := found.(map[string]any)
	if err != nil || m == nil {
		return nil, "", false, miss
	}
	if ambiguous, _ := m["ambiguous"].(bool); ambiguous {
		return nil, "", false, fmt.Errorf("%q is ambiguous across linked definitions: %v; select a repository with --repo <id> and, if needed, a declaration with --file <path>", name, m["candidates"])
	}
	note, _ := m["note"].(string)
	const marker = "shown from linked repository "
	i := strings.Index(note, marker)
	if i < 0 {
		return nil, "", false, miss
	}
	ids := strings.Fields(note[i+len(marker):])
	if len(ids) == 0 {
		return nil, "", false, miss
	}
	id := ids[0]
	linked := session.linked[id]
	if linked == nil {
		linked, err = newRepoToolSession(dir, id)
		if err != nil {
			return nil, "", false, err
		}
		if session.linked == nil {
			session.linked = make(map[string]*repoToolSession)
		}
		session.linked[id] = linked
	}
	return linked.invoke("prism_change_impact", callArgs)
}

func cmdRenamePlan(args []string) int {
	args, rc := positionalsFirst("rename-plan", "a symbol and a new name", "prism rename-plan Type.method NewName [dir]", 2, args, "--format", "--file", "--path")
	if rc != 0 {
		return rc
	}
	query, newName := args[0], args[1]
	dir, dirSet := ".", false
	format := formatText
	file := ""
	allowCollision := false
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			f, err := formatFlag("rename-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages, as
			// change-impact does: only types declared in a matching file seed.
			v, err := flagValue("rename-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		case "--allow-collision":
			allowCollision = true
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("rename-plan", a)
			}
			if err := positionalDir("rename-plan", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{"query": query, "newName": newName}
	if file != "" {
		callArgs["file"] = file
	}
	if allowCollision {
		callArgs["allow_collision"] = true
	}
	out, err := invokeTool(dir, "prism_rename_plan", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("rename-plan", err))
		return exitForError(err)
	}
	printOutput(out, format)
	// A collision without --allow-collision returns no plan: exit 1.
	if m := toMap(out); m["status"] == "collision" && !allowCollision {
		return 1
	}
	return 0
}

// cmdChangeImpactDiff runs change-impact over the working-tree diff
// (GAP-05): what the diff reaches, where verify says whether it is
// complete.
func cmdChangeImpactDiff(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	callArgs := map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--diff":
		case "--base":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			callArgs["base"] = v
			i++
		case "--format":
			f, err := formatFlag("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--json":
			format = formatJSON
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("change-impact", a)
			}
			if err := positionalDir("change-impact", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, "prism_change_impact_diff", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
		if strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "unknown revision") {
			return exitUsage
		}
		return exitForError(err)
	}
	printToolResult("change-impact", out, text, rendered, format)
	return exitOK
}

func cmdMissingImplementations(args []string) int {
	args, rc := positionalsFirst("missing-implementations", "a symbol", "prism missing-implementations Type.method [dir]", 1, args, "--format", "--file", "--path")
	if rc != 0 {
		return rc
	}
	query := args[0]
	dir, dirSet := ".", false
	format := formatText
	file := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			f, err := formatFlag("missing-implementations", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages, as
			// change-impact does: only types declared in a matching file seed.
			v, err := flagValue("missing-implementations", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("missing-implementations", a)
			}
			if err := positionalDir("missing-implementations", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{"query": query}
	if file != "" {
		callArgs["file"] = file
	}
	out, err := invokeTool(dir, "prism_missing_implementations", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("missing-implementations", err))
		return exitForError(err)
	}
	printOutput(out, format)
	return 0
}

func cmdDeadCode(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	var roots []any
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--roots":
			items, err := listFlag("dead-code", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, item := range items {
				roots = append(roots, item)
			}
			i++
		case "--format":
			f, err := formatFlag("dead-code", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("dead-code", a)
			}
			if err := positionalDir("dead-code", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	callArgs := map[string]any{}
	if len(roots) > 0 {
		callArgs["roots"] = roots
	}
	out, err := invokeTool(dir, "prism_dead_code", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dead-code:", err)
		return exitInternal
	}
	printOutput(out, format)
	return 0
}

func cmdCompact(args []string) int {
	dir, _, rc := dirOnlyArgs("compact", args)
	if rc != 0 {
		return rc
	}
	var turns []map[string]any
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&turns); err != nil {
		fmt.Fprintln(os.Stderr, "compact: stdin must be a JSON array of turns:", err)
		return 2
	}
	out, err := invokeTool(dir, "prism_compact", map[string]any{"turns": turns})
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
	list := false
	dir, dirSet := ".", false

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--tool":
			v, err := flagValue("feedback", args, i)
			if err != nil {
				return usageError(err)
			}
			tool = v
			i++
		case "--query-id":
			v, err := flagValue("feedback", args, i)
			if err != nil {
				return usageError(err)
			}
			queryID = v
			i++
		case "--rating":
			v, err := flagValue("feedback", args, i)
			if err != nil {
				return usageError(err)
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil || n < 0 || n > 5 {
				return usageError(fmt.Errorf("feedback: --rating wants an integer 0-5, got %q", v))
			}
			rating = n
			i++
		case "--notes":
			v, err := flagValue("feedback", args, i)
			if err != nil {
				return usageError(err)
			}
			notes = v
			i++
		case "--list":
			list = true
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("feedback", a)
			}
			if err := positionalDir("feedback", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}

	if list {
		root, _ := cliRoot(dir)
		data, err := os.ReadFile(mcp.FeedbackPath(root))
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "feedback:", err)
			return 1
		}
		fmt.Print(string(data))
		return 0
	}
	if rating < 0 || rating > 5 {
		fmt.Fprintln(os.Stderr, "usage: prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]")
		return 2
	}
	if tool == "" {
		tool = "prism_query"
	}

	out, err := invokeTool(dir, "prism_feedback", map[string]any{
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

// cmdDrift refuses: drift compares files with what the MCP server
// delivered in its session, and a one-shot CLI call has none, so it always
// reported checkedFiles: 0 as success (BUG-12).
func cmdDrift(args []string) int {
	fmt.Fprintln(os.Stderr, "drift: MCP only. It compares files with what a running `prism mcp` server delivered this session; a CLI call has no session. Use the prism_drift tool, or rerun the reads you depend on.")
	return 2
}

// cmdConfig prints every prism.yaml key with its resolved value and source
// (CON-08). --check exits 1 on an unknown key or an invalid value.
func cmdConfig(args []string) int {
	args, format, rc := takeFormat("config", args)
	if rc != 0 {
		return rc
	}
	check := false
	var rest []string
	for _, a := range args {
		if a == "--check" {
			check = true
			continue
		}
		rest = append(rest, a)
	}
	dir, _, rc := dirOnlyArgs("config", rest)
	if rc != 0 {
		return rc
	}
	cfg, err := config.LoadFromDir(mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	if format == formatJSON {
		keys := []map[string]string{}
		for _, k := range cfg.Keys() {
			keys = append(keys, map[string]string{"key": k[0], "value": k[1], "source": k[2]})
		}
		problems := cfg.Problems
		if problems == nil {
			problems = []string{}
		}
		printOutput(map[string]any{"keys": keys, "problems": problems}, format)
	} else {
		for _, k := range cfg.Keys() {
			v := k[1]
			if v == "" {
				v = "-"
			}
			fmt.Printf("%-17s %-30s %s\n", k[0], v, k[2])
		}
		for _, p := range cfg.Problems {
			fmt.Println("problem:", p)
		}
	}
	if check && len(cfg.Problems) > 0 {
		return exitFinding
	}
	return 0
}

func cmdServe(args []string) int {
	port := 0 // resolved after config load: flag > prism.yaml port > 8888
	dir, dirSet := ".", false
	listen, tokenFile := "127.0.0.1", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--listen":
			v, err := flagValue("serve", args, i)
			if err != nil {
				return usageError(err)
			}
			listen = v
			i++
		case a == "--token-file":
			v, err := flagValue("serve", args, i)
			if err != nil {
				return usageError(err)
			}
			tokenFile = v
			i++
		case a == "--port":
			p, err := positiveIntFlag("serve", args, i)
			if err == nil && p > 65535 {
				err = fmt.Errorf("serve: --port must be 1-65535, got %d", p)
			}
			if err != nil {
				return usageError(err)
			}
			port = p
			i++
		case strings.HasPrefix(a, "-"):
			return rejectUnknownFlag("serve", a)
		default:
			if err := positionalDir("serve", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	// SEC-02: a non-loopback listener needs a token the operator chose and
	// can hand to its clients; the loopback default gets a fresh one.
	if !httpguard.LoopbackHost(listen) && tokenFile == "" {
		return usageError(fmt.Errorf("serve: --listen %s is not a loopback address; it requires --token-file", listen))
	}
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

	token, tokenPath, err := serveToken(mustAbs(dir), tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	addr := net.JoinHostPort(listen, strconv.Itoa(port))
	guard := httpguard.Options{Token: token, Open: map[string]bool{"/health": true}}
	if !httpguard.LoopbackHost(listen) {
		guard.AllowHost = func(host string) bool {
			h, _, err := net.SplitHostPort(host)
			if err != nil {
				h = host
			}
			return strings.EqualFold(strings.Trim(h, "[]"), strings.Trim(listen, "[]"))
		}
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpguard.Wrap(httpapi.New(h).Handler(), guard),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// A shell starts a background job with SIGINT ignored; Notify installs
	// a handler, so Ctrl+C / kill -INT stop the server either way (BUG-21).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	fmt.Fprintln(os.Stderr, "prism HTTP listening on", addr)
	if tokenFile == "" {
		fmt.Fprintf(os.Stderr, "token (send as \"Authorization: Bearer <token>\"): %s\n  also in %s\n", token, tokenPath)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

// serveToken reads the token from tokenFile, or generates one and writes it
// to <root>/.grove/serve.token (0600) so a local client can read it.
func serveToken(root, tokenFile string) (token, path string, err error) {
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", "", fmt.Errorf("--token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
		if len(token) < 16 {
			return "", "", fmt.Errorf("--token-file %s: token shorter than 16 characters", tokenFile)
		}
		return token, tokenFile, nil
	}
	if token, err = httpguard.NewToken(); err != nil {
		return "", "", err
	}
	path = filepath.Join(root, ".grove", "serve.token")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return token, path, nil
}

func cmdMCP(args []string) int {
	compact := true
	positional := make([]string, 0, 1)
	for _, arg := range args {
		switch {
		case arg == "--compact":
			compact = true
		case arg == "--legacy":
			compact = false
		case strings.HasPrefix(arg, "-"):
			return rejectUnknownFlag("mcp", arg)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 1 {
		fmt.Fprintln(os.Stderr, "usage: prism mcp [--compact|--legacy] [dir]")
		return 2
	}
	dir := "."
	if len(positional) == 1 {
		dir = positional[0]
	}
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
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root))

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
		// Ready means INDEXED, not merely open. This used to close readyCh
		// before the initial index ran, so every early tool call raced the
		// background refresh and could answer from a partial graph — measured
		// 2026-08-26: five identical `prism query` invocations on an
		// unchanged, pre-indexed worktree produced FOUR different context
		// selections (28KB with the right anchors down to 3.5KB with wrong
		// ones), because seed search hit the graph mid-mutation. Correct and
		// slow beats fast and silently wrong: the first tool call on a large
		// cold repo now waits for the index, and the MCP handshake is still
		// served immediately (readyCh gates tool calls only).
		if _, err := client.Index(ctx, root); err != nil {
			fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
		}
		close(readyCh)
	}()

	// Stop background work and close the embedded engine before returning so no
	// SQLite handles or .grove files linger — otherwise a caller that removes
	// the project directory (e.g. a test using t.TempDir) races file creation
	// and fails with "directory not empty" on Linux or a lock error on Windows.
	// Also run when an upgraded binary takes over the session mid-Serve.
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			<-doneCh
			client.Shutdown()
		})
	}

	h := mcp.NewHandlerWithReady(cfg, root, client, readyCh)
	srv := mcp.NewServer(h)
	if compact {
		srv = mcp.NewCompactServer(h)
	}
	srv.OnHandoff = shutdown
	serveErr := srv.Serve(os.Stdin, os.Stdout)
	shutdown()

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

// newClient opens a one-shot client for a CLI command (see
// grove.Client.OneShot).
func newClient(dir string) (*config.Config, *grove.Client, error) {
	return newClientMode(dir, true)
}

// newClientMode opens a client; long-lived commands (watch) pass
// oneShot=false so the engine keeps resident workers between runs.
func newClientMode(dir string, oneShot bool) (*config.Config, *grove.Client, error) {
	return newClientOpts(dir, oneShot, false)
}

// newClientOpts is newClientMode with force: every Index call on the
// client re-extracts every file and re-runs every analyzer.
// indexAnywhere lets this process open an index in a directory with no
// project marker; prism index and prism watch set it.
var indexAnywhere bool

func newClientOpts(dir string, oneShot, force bool) (*config.Config, *grove.Client, error) {
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		return nil, nil, err
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	cfg.WarnProblems() // CON-08: a typo in prism.yaml used to be ignored silently
	// CON-11: a query in a directory that is no project (an empty folder,
	// ~/Downloads) used to create .grove there and index whatever it held.
	// Only prism index / watch build an index in such a directory.
	if !indexAnywhere && !workspace.LooksLikeProject(root) {
		return nil, nil, fmt.Errorf("no project here: %s has no .git, prism.yaml, .grove or build manifest; run `prism index %s` to index it anyway", root, root)
	}
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root))
	if oneShot {
		client.OneShot()
	}
	if force {
		client.Force()
	}
	if err := client.EnsureRunning(context.Background()); err != nil {
		if msg := err.Error(); strings.Contains(msg, "file is not a database") || strings.Contains(msg, "malformed") {
			return nil, nil, fmt.Errorf("grove: %w\n  fix: the index is damaged; run `prism index --rebuild`", err)
		}
		return nil, nil, fmt.Errorf("grove: %w", err)
	}
	return cfg, client, nil
}

func invokeTool(dir, tool string, args map[string]any) (any, error) {
	timing := os.Getenv("PRISM_TIMING") != ""
	tInv := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing] cli:%-19s %8.0fms\n", stage, float64(time.Since(tInv).Milliseconds()))
		}
	}
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
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

	h := mcp.NewHandler(cfg, root, client)
	return h.Invoke(tool, args)
}

// invokeToolText runs a tool and renders its answer with the same renderer
// the MCP server uses (compact gateway wording), so --format text matches
// what an agent receives over MCP. rendered=false: caller prints out itself.
func invokeToolText(dir, tool string, args map[string]any) (out any, text string, rendered bool, err error) {
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, "", false, err
	}
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		return nil, "", false, err
	}
	h := mcp.NewHandler(cfg, root, client)
	out, err = h.Invoke(tool, args)
	if err != nil {
		return nil, "", false, err
	}
	text, rendered = h.RenderToolText(true, tool, args, out)
	return out, text, rendered, nil
}

// invokeCompactReadText serves a compact read without the graph, as the MCP
// server does before its first index: a compact read is always a line window
// read from disk (safePathWithinRoot and the compact budgets still apply), so
// it neither opens the engine nor builds a missing index. A cold `prism read
// README.md` used to index the repository first, and an unusable
// .grove/grove.db failed a readable file with "file is not a database".
func invokeCompactReadText(dir string, args map[string]any) (out any, text string, rendered bool, err error) {
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	if err := requireDir(root); err != nil {
		return nil, "", false, err
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, "", false, fmt.Errorf("config: %w", err)
	}
	// Never started: the read path does not call it.
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root)).OneShot()
	h := mcp.NewHandler(cfg, root, client)
	return h.InvokeCompactReadText(args)
}

// printToolResult prints the shared text rendering for --format text. Unknown
// result shapes use the same compact JSON fallback as the MCP server, so no
// fields disappear or acquire a CLI-only rendering.
func printToolResult(op string, out any, text string, rendered bool, format outputFormat) {
	if format == formatText {
		if rendered {
			text = fitShellOutput(op, cliHintSpellingLines(text))
			fmt.Print(text)
			if !strings.HasSuffix(text, "\n") {
				fmt.Println()
			}
			return
		}
		// No compact text form: the CLI's own text renderers, ending in
		// the generic key/value fallback, never JSON (CON-02).
		printOutput(out, formatText)
		return
	}
	printOutput(out, format)
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
	groups := asSliceAny(m["textMatches"])
	if groups == nil {
		groups = asSliceAny(m["textHits"])
	}
	if len(groups) == 0 {
		return
	}
	backend, _ := m["textBackend"].(string)
	fmt.Printf("// text matches (%s):\n", backend)
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if note, _ := gm["note"].(string); note != "" && gm["file"] == nil {
			fmt.Printf("//   %s\n", note)
			// exhaustive=true inventory: the files past the render cap
			// (same as the MCP renderer in searchtext.go).
			for _, f := range asSliceAny(gm["files"]) {
				fmt.Printf("//   %v\n", f)
			}
			continue
		}
		file, _ := gm["file"].(string)
		if cached, _ := gm["cached"].(bool); cached {
			var lines []string
			for _, l := range asSliceAny(gm["lines"]) {
				lines = append(lines, fmt.Sprint(jsonInt(l)))
			}
			fmt.Printf("//   %s:%s [cached — content already delivered this session]\n",
				file, strings.Join(lines, ","))
			continue
		}
		for _, h := range asSliceAny(gm["hits"]) {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			line := jsonInt(hm["line"])
			before := asSliceAny(hm["before"])
			for i, l := range before {
				fmt.Printf("//   %s:%d-  %v\n", file, line-len(before)+i, l)
			}
			fmt.Printf("//   %s:%d: %v\n", file, line, hm["text"])
			for i, l := range asSliceAny(hm["after"]) {
				fmt.Printf("//   %s:%d-  %v\n", file, line+1+i, l)
			}
			if len(before) > 0 || hm["after"] != nil {
				fmt.Println("//   --")
			}
		}
		if more := jsonInt(gm["moreHits"]); more > 0 {
			fmt.Printf("//   %s: +%d more matches\n", file, more)
		}
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
		if format == formatJSON {
			printJSON(v)
			return
		}
		// Not an object (a bare list): still text, never JSON (CON-02).
		var any0 any
		_ = json.Unmarshal(b, &any0)
		var sb strings.Builder
		writeGeneric(&sb, "", any0)
		fmt.Print(sb.String())
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
	if root, _ := m["root"].(string); root != "" {
		if _, search := m["results"]; search || m["textHits"] != nil || m["symbols"] != nil || m["files"] != nil {
			fmt.Printf("// root: %s\n", root)
		}
	}
	// Multi-term prism_search: one group per term, each rendered by the
	// single-term path below so the two forms read identically.
	if groups, ok := m["results"]; ok {
		if note, _ := m["note"].(string); note != "" {
			fmt.Println("// " + note)
		}
		printDidYouMean(m)
		for _, g := range asSliceAny(groups) {
			gm, ok := g.(map[string]any)
			if !ok {
				continue
			}
			fmt.Printf("// ── %v ──\n", gm["query"])
			printTextOutput(gm)
		}
		for _, f := range asSliceAny(m["failedTerms"]) {
			fmt.Printf("// failed: %v\n", f)
		}
		return
	}
	// A pure text search (scope="text") has textHits and nothing else. Without
	// this branch it fell past every case below to the JSON fallback, so
	// `prism search X --scope text --format text` — the exact invocation the
	// steering gives Bash-only subagents — printed JSON, several times the
	// tokens of the line-oriented form it asked for.
	// prism_query source delivery: the assembled context IS the answer.
	// Measured 2026-08-26 (jackson worktree): the MCP surface returned the
	// full 21KB context while this CLI path printed FIVE FILE PATHS — the
	// files_only branch below fired because the query payload has "files"
	// and no "symbols" key, and the entire "content" field (5,201 delivered
	// tokens, anchors, callers) was silently discarded. Every Bash-only
	// consumer (subagents, CI — exactly who the CLAUDE.md bash table sends
	// here) got paths where the tool's whole purpose is context.
	// prism_read also carries "content" but never "files"; its own branch
	// below prints the header line — the guard keeps it out of this one.
	if content, ok := m["content"].(string); ok && content != "" {
		if _, isQuery := m["files"]; isQuery {
			fmt.Print(content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
			printTextMatches(m)
			return
		}
	}
	// files_only delivery: paths, no lines.
	if files, ok := m["files"]; ok {
		if _, hasSyms := m["symbols"]; !hasSyms {
			for _, f := range asSliceAny(files) {
				fmt.Printf("%v\n", f)
			}
			if len(asSliceAny(files)) == 0 {
				fmt.Println("// no matching files")
			}
			for _, k := range []string{"warning", "note"} {
				if s, _ := m[k].(string); s != "" {
					fmt.Println("// " + s)
				}
			}
			return
		}
	}
	if _, hasHits := m["textHits"]; hasHits {
		_, hasSyms := m["symbols"]
		_, hasContent := m["content"]
		if !hasSyms && !hasContent {
			// Headline first — the graph's reading of the term leads, the
			// grep lines follow (searchtext.go has the measurement).
			if s, _ := m["resolvedNote"].(string); s != "" {
				fmt.Println("// " + s)
			}
			printTextMatches(m)
			if len(asSliceAny(m["textHits"])) == 0 {
				// Same evidence rule as the MCP renderer (searchtext.go):
				// a bare null is indistinguishable from a broken/partial
				// search, so state completion explicitly.
				if timedOut, _ := m["timedOut"].(bool); timedOut {
					fmt.Println("// no matches — search timed out before finishing; results may be incomplete")
				} else {
					fmt.Println("// no matches — search completed (not truncated, not timed out)")
				}
			}
			for _, k := range []string{"warning", "note"} {
				if s, _ := m[k].(string); s != "" {
					fmt.Println("// " + s)
				}
			}
			printDidYouMean(m)
			// Graph rollup of a truncated search's FULL hit set (rollup.go) —
			// same rendering as the MCP text surface.
			if ru, _ := m["hitRollup"].([]any); len(ru) > 0 {
				fmt.Println("// ALL matches by enclosing symbol (graph rollup of the full set):")
				for _, e := range ru {
					em, _ := e.(map[string]any)
					if em == nil {
						continue
					}
					if note, _ := em["note"].(string); note != "" {
						fmt.Println("//   " + note)
						continue
					}
					span, _ := em["span"].(map[string]any)
					line := fmt.Sprintf("//   %v  %v", em["symbol"], em["file"])
					if span != nil {
						line += fmt.Sprintf(":%v-%v", span["start"], span["end"])
					}
					fmt.Printf("%s  (%v hits)\n", line, em["hits"])
				}
			}
			// The truncation warning is already carried in m["warning"] and
			// printed above; printing a second line here duplicated it.
			if t, _ := m["truncated"].(bool); t && m["warning"] == nil {
				fmt.Println("// truncated at the hit limit — raise --limit, or --exhaustive")
			}
			return
		}
	}
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
			printLookupFlagsText(m)
			name, _ := sym["name"].(string)
			fp, _ := sym["filePath"].(string)
			fmt.Printf("// %s — %s\n", fp, name)
			span, _ := sym["span"].(map[string]any)
			start, end := jsonInt(span["start"]), jsonInt(span["end"])
			lines := strings.SplitAfter(content, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			if start > 0 && end >= start && len(lines) <= end-start+1 {
				for i, line := range lines {
					fmt.Printf("%d"+mcp.SourceLineSep+"%s", start+i, line)
				}
			} else {
				fmt.Print(content)
			}
			if !strings.HasSuffix(content, "\n") {
				fmt.Println()
			}
			printLookupOverloadsText(m)
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
			tag := wireTestTag(cm)
			name := cm["name"]
			if p, ok := cm["pasteable"].(string); ok && p != "" {
				name = p // GAP-01: a name that resolves to this candidate alone
			}
			fmt.Printf("  %v  %v  %v:%v%s\n", name, cm["kind"], cm["file"], jsonInt(cm["line"]), tag)
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
				tag := wireTestTag(sm)
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
	if _, ok := m["edits"]; ok || (m["status"] == "collision" && m["newName"] != nil) {
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
	// prism_references: "sites" rows path:line:col: text.
	if sites, ok := m["sites"].([]any); ok && m["name"] != nil {
		name, _ := m["name"].(string)
		switch {
		case m["resolved"] == true:
			fmt.Printf("// %s — %d reference site(s), resolved to this symbol\n", name, jsonInt(m["count"]))
			printNotes(m, "completeness", "completenessScope", "callerEvidence", "degradedAnalysis")
		case m["ambiguous"] == true:
			fmt.Printf("// %s — %d use(s) of the name (ambiguous — %d definitions share it)\n", name, jsonInt(m["count"]), jsonInt(m["definitions"]))
		default:
			fmt.Printf("// %s — %d use(s) of the name (one definition)\n", name, jsonInt(m["count"]))
		}
		printReferenceRows(sites)
		for _, l := range anySliceCLI(m["otherMentions"]) {
			fmt.Printf("%v\n", l)
		}
		printNotes(m, "note", "warning")
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
	// Fallback: plain key/value text, never JSON. --format text used to
	// print raw JSON for the ambiguity choice, a bad regex, a rollup and
	// a symbols delivery (CON-02).
	fmt.Print(genericText(m))
}

// genericText renders any answer as indented "key: value" lines: scalars
// inline, lists one item per line, nested objects indented. Keys sort,
// except that query/name/status/error/note lead.
func genericText(m map[string]any) string {
	var b strings.Builder
	writeGeneric(&b, "", m)
	return b.String()
}

var genericLead = map[string]int{"query": 0, "name": 1, "status": 2, "error": 3, "warning": 4, "note": 5}

func writeGeneric(b *strings.Builder, indent string, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			li, iok := genericLead[keys[i]]
			lj, jok := genericLead[keys[j]]
			switch {
			case iok && jok:
				return li < lj
			case iok != jok:
				return iok
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			val := x[k]
			if genericScalar(val) {
				fmt.Fprintf(b, "%s%s: %s\n", indent, k, genericValue(val))
				continue
			}
			if list, ok := val.([]any); ok {
				if len(list) == 0 {
					continue
				}
				fmt.Fprintf(b, "%s%s (%d):\n", indent, k, len(list))
			} else {
				fmt.Fprintf(b, "%s%s:\n", indent, k)
			}
			writeGeneric(b, indent+"  ", val)
		}
	case []any:
		for _, item := range x {
			if genericScalar(item) {
				fmt.Fprintf(b, "%s- %s\n", indent, genericValue(item))
				continue
			}
			if im, ok := item.(map[string]any); ok && genericFlat(im) {
				fmt.Fprintf(b, "%s- %s\n", indent, genericInline(im))
				continue
			}
			fmt.Fprintf(b, "%s-\n", indent)
			writeGeneric(b, indent+"  ", item)
		}
	default:
		fmt.Fprintf(b, "%s%s\n", indent, genericValue(x))
	}
}

func genericScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// genericFlat: an object of at most six scalar fields prints on one line.
func genericFlat(m map[string]any) bool {
	if len(m) > 6 {
		return false
	}
	for _, v := range m {
		if !genericScalar(v) {
			return false
		}
	}
	return true
}

func genericInline(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+genericValue(m[k]))
	}
	return strings.Join(parts, ", ")
}

func genericValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// ─── task-shaped renderers ───────────────────────────────────────────────────
//
// These render every returned site rather than truncating the view. Coverage
// remains bounded by the indexed graph and the result's completeness notes.

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
// printDidYouMean renders an empty search's near-miss symbol candidates —
// the retry pointer attachEmptySearchGuidance's note refers to.
func printDidYouMean(m map[string]any) {
	dym := asSliceAny(m["didYouMean"])
	if len(dym) == 0 {
		return
	}
	fmt.Println("// closest indexed symbols:")
	for _, d := range dym {
		fmt.Printf("//   %v\n", d)
	}
}

func printNotes(m map[string]any, keys ...string) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				fmt.Printf("%s: %s\n", k, v)
			}
		case bool:
			if v || k == "safeToClaimComplete" {
				fmt.Printf("%s: %t\n", k, v)
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
	if text, ok := mcp.FormatMemberImpactText(m); ok {
		fmt.Print(text)
		return
	}
	fmt.Printf("// %v — change-impact: %d site(s)\n", m["query"], jsonInt(m["totalSites"]))
	fmt.Print(mcp.FormatImpactHeaderNotesText(m))
	printNotes(m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "familyCompleteness", "callerCoverage", "callerEvidence", "coverageNote", "evidenceNote", "hasHeuristicRefs")
	fmt.Print(mcp.FormatImpactRelaySitesText(m))
	fmt.Print(mcp.FormatImpactExtrasText(m))
	printSiteGroup("declarations", m["declarations"])
	printSiteGroup("supers", m["supers"])
	printSiteGroup("family", m["family"])
	printSiteGroup("declaringTypes", m["declaringTypes"])
	printSiteGroup("callers", m["callers"])
	printNotes(m, "declaringTypesNote", "externalSupers", "overridesExternal", "warning", "ambiguityNote", "scopeNote")
	if hint, ok := m["widerAnchor"].(map[string]any); ok {
		fmt.Printf("widerAnchor: %v\n", hint["note"])
	}
}

// printReferenceRows prints path:line:col: text rows with each path once.
// Past 15 rows only line:col positions are printed per file, so a wide
// answer stays no larger than the old line-number list.
func printReferenceRows(sites []any) {
	type group struct {
		path string
		rows []string
	}
	var groups []*group
	byPath := map[string]*group{}
	for _, r := range sites {
		row := fmt.Sprint(r)
		path, rest := row, ""
		if m := referenceRow.FindStringSubmatch(row); m != nil {
			path, rest = m[1], m[2]
		}
		g := byPath[path]
		if g == nil {
			g = &group{path: path}
			byPath[path] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, rest)
	}
	compact := len(sites) > 15
	for _, g := range groups {
		if !compact {
			if len(g.rows) == 1 {
				fmt.Printf("  %s:%s\n", g.path, g.rows[0])
				continue
			}
			fmt.Printf("  %s:\n", g.path)
			for _, r := range g.rows {
				fmt.Printf("    %s\n", r)
			}
			continue
		}
		pos := make([]string, 0, len(g.rows))
		for _, r := range g.rows {
			if i := strings.Index(r, ": "); i >= 0 {
				r = r[:i]
			}
			pos = append(pos, r)
		}
		fmt.Printf("  %s (%d): %s\n", g.path, len(pos), strings.Join(pos, " "))
	}
	if compact {
		fmt.Println("  (line:col positions; --format json has each line's text)")
	}
}

// referenceRow splits "path:line:col: text" or "path:line: text".
var referenceRow = regexp.MustCompile(`^(.+?):(\d+(?::\d+)?: .*|\d+(?::\d+)?)$`)

func anySliceCLI(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}

// wireTestTag renders a symbol's testLabel ("[test]" or "[test double]");
// a record with only testDouble predates testLabel.
func wireTestTag(m map[string]any) string {
	if l, _ := m["testLabel"].(string); l != "" {
		return "  [" + l + "]"
	}
	if td, _ := m["testDouble"].(bool); td {
		return "  [test double]"
	}
	return ""
}

func printRenamePlanText(m map[string]any) {
	if cols, _ := m["collisions"].([]any); len(cols) > 0 {
		fmt.Printf("// %v → %v — rename-plan: COLLISION, %v is already declared where the renamed member would land (%d):\n", m["query"], m["newName"], m["newName"], len(cols))
		for _, c := range cols {
			if cm, ok := c.(map[string]any); ok {
				fmt.Printf("  %v  %v:%d  (renaming %v)\n", cm["name"], cm["file"], jsonInt(cm["line"]), cm["renamedMember"])
			}
		}
		if _, hasEdits := m["edits"]; !hasEdits {
			printNotes(m, "note")
			return
		}
	}
	edits, _ := m["edits"].([]any)
	amb, _ := m["ambiguous"].([]any)
	unres, _ := m["unresolved"].([]any)
	fmt.Printf("// %v → %v — rename-plan: %d site(s): %d edit(s), %d ambiguous, %d unresolved\n",
		m["query"], m["newName"], len(edits)+len(amb)+len(unres), len(edits), len(amb), len(unres))
	printNotes(m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "degradedAnalysis")
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
		if reason, ok := e["reason"].(string); ok && reason != "" {
			fmt.Printf("    // %s\n", reason)
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
	printNotes(m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "unverifiableNote", "defaultProvided", "note")
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
// printLookupOverloadsText prints what lookup delivers beside the primary body:
// same-file overloads (body, or signature when the budget was spent) and
// cross-symbol ambiguity candidates. Text mode used to stop after the primary
// body, so a CLI caller never learned other overloads or candidates existed.
func printLookupOverloadsText(m map[string]any) {
	if overloads := asSliceAny(m["overloads"]); len(overloads) > 0 {
		fmt.Printf("// %d more overload(s) with the same name in this file:\n", len(overloads))
		for _, raw := range overloads {
			o, _ := raw.(map[string]any)
			if o == nil {
				continue
			}
			start, end := jsonInt(o["line"]), jsonInt(o["end"])
			fmt.Printf("// overload %v  %v:%d-%d\n", o["name"], o["file"], start, end)
			body, _ := o["content"].(string)
			if body == "" {
				body, _ = o["body"].(string)
			}
			if body == "" {
				if sig, _ := o["signature"].(string); sig != "" {
					fmt.Printf("// signature: %s\n", sig)
				}
				if omitted, _ := o["bodyOmitted"].(bool); omitted {
					fmt.Printf("// body not delivered (lookup body budget); read %v lines %d-%d\n", o["file"], start, end)
				}
				continue
			}
			lines := strings.SplitAfter(body, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			if start > 0 && end >= start+len(lines)-1 {
				for i, line := range lines {
					fmt.Printf("%d"+mcp.SourceLineSep+"%s", start+i, line)
				}
			} else {
				fmt.Print(body)
			}
			if !strings.HasSuffix(body, "\n") {
				fmt.Println()
			}
		}
	}
	for _, d := range asSliceAny(m["declarations"]) {
		fmt.Printf("// declared: %v\n", d)
	}
	if amb, _ := m["ambiguous"].(bool); amb {
		fmt.Println("// AMBIGUOUS — same score for:")
		for _, c := range asSliceAny(m["candidates"]) {
			fmt.Printf("//   %v\n", c)
		}
	}
}

// printLookupFlagsText prints a lookup's NO EXACT MATCH / AMBIGUOUS /
// inherited / case-insensitive flags BEFORE the body. This path used to
// ignore matched:false entirely, so the Bash fallback route showed a wrong
// body exactly as if it were the answer.
func printLookupFlagsText(m map[string]any) {
	note, _ := m["note"].(string)
	if matched, present := m["matched"].(bool); present && !matched {
		switch {
		case strings.HasPrefix(note, "NO EXACT MATCH"):
			fmt.Println("// " + note)
		case note != "":
			fmt.Println("// NO EXACT MATCH — " + note)
		default:
			fmt.Printf("// NO EXACT MATCH for %v — the symbol below is only the closest hit\n", m["name"])
		}
		if cands := asSliceAny(m["candidates"]); len(cands) > 0 {
			fmt.Println("// candidates:")
			for _, c := range cands {
				fmt.Printf("//   %v\n", c)
			}
		}
		fmt.Println("// closest symbol shown below; it does NOT exactly match the requested name")
		return
	}
	if amb, _ := m["ambiguous"].(bool); amb {
		fmt.Println("// AMBIGUOUS — several symbols fit equally; the first is shown, all are listed at the end")
	}
	if note != "" {
		fmt.Println("// " + note)
	}
}

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
			// Never strip the flags that say the symbol is NOT an exact
			// answer (or how it was resolved).
			for _, k := range []string{"matched", "ambiguous", "matchKind", "note", "candidates"} {
				if v, ok := m[k]; ok {
					lean[k] = v
				}
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
			v, err := flagValue("assist", args, i)
			if err != nil {
				return usageError(err)
			}
			model = v
			i++
		case "--apply":
			apply = true
		case "--apply-ambiguous":
			apply = true
			applyAmbiguous = true
		case "--verify":
			v, err := flagValue("assist", args, i)
			if err != nil {
				return usageError(err)
			}
			verify = v
			i++
		case "--dir":
			v, err := flagValue("assist", args, i)
			if err != nil {
				return usageError(err)
			}
			dir = v
			i++
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

	h := mcp.NewHandler(cfg, root, client)

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
	// Errors carry MCP-spelled hints too (the ambiguous-name answer listed
	// {"op":"change_impact",...} calls, which a CLI agent cannot run).
	msg := cliHintSpelling(err.Error())
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

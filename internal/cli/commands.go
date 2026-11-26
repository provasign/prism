// Package cli implements the Prism command tree (flat dispatch, no cobra
// dependency — keeps Prism a true single binary with zero runtime deps).
package cli

import (
	"fmt"
	"os"
	"strings"

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
  prism init [--harness <ids>] [dir]
                                  Write prism.yaml + project-local MCP config;
                                  --dry-run prints the diff of every file it would write
                                  and writes nothing; what it writes is recorded in
                                  .grove/install-manifest.json
  prism uninstall [dir]           Remove exactly what init recorded (Prism's keys,
                                  steering blocks, files it created); [--dry-run]
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
                                  ([--integrity] also checks the index file
                                  for corruption and engine-version drift)
                                  ([--format text|json] [--json]; the per-language manifest is JSON only)
  prism map [dir]                 Component-level architecture map: directories,
                                  induced dependency edges (weights + evidence
                                  tiers), cycles. Production shape by default
                                  ([--depth N] [--component X] [--tests]
                                  [--expand 'A->B'] [--json | --format text|json])
                                  [--max-sites N]  cited sites per edge (default 5)
                                  [--all-components]  include docs/examples and isolated
                                  other-language script folders (hidden by default)
  prism graph [dir]               Open the interactive graph explorer in a browser:
                                  directories -> files -> symbols -> one symbol's
                                  callers and callees, with search and the evidence
                                  behind each edge. Serves on localhost until Ctrl+C
                                  ([--port N] [--no-open] [--tests])
  prism cycles [dir]              Dependency cycles with per-edge evidence
                                  ([--depth N] [--tests] [--json | --format text|json])
  prism arch [dir]                Validate declared architecture rules
                                  (arch_deny: "<from> -> <to>" in prism.yaml)
                                  against the component view; violations cite
                                  file:line sites; exit 1 on violation — a CI
                                  gate ([--deny 'A -> B'] [--depth N] [--tests] [--strict]
                                  [--json | --format text|json])
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
                                  --format targets  one runner target per line (Go -run
                                  pattern, pytest node id, Class#method, test file); no commands
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
                                  remove <id>               Unlink; the checkout is not touched
                                  discover [--link]         Related checkouts found in manifests
                                  Guide: docs/MULTI_REPO.md
  prism link-server --dir <store> [--port N]
                                  Serve uploaded link bundles: who references a symbol across
                                  repositories that are not checked out (PRISM_LINK_TOKEN required token)
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
                                  — the interface-evolution companion to change-impact;
                                  a member not declared yet lists every implementer
                                  [--signature '(ctx context.Context) error'] marks matches
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
	case "uninstall":
		return cmdUninstall(rest)
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

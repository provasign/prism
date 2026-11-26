package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// The command table (ENG-03): every command, its aliases, its help block and
// the function that runs it, in the order `prism help` lists them. Help is
// printed from here, never sliced out of one long string, and a command is
// known exactly when it has an entry.

type command struct {
	name    string
	aliases []string
	help    string // the usage block: `  prism <name> ...` and its continuation lines
	// flags is every flag the command accepts, space-separated; a trailing
	// "=" marks one that takes a value (--base=), a trailing "!" one that
	// was removed and is recognized only so the command can say so (it is
	// not in the help). Each other one
	// must appear in help (TestCommandHelpListsEveryFlag), and every flag
	// the command's parser matches must be listed here
	// (TestCommandFlagsMatchTheirParsers).
	flags string
	run   func(args []string) int
}

// helpHeader and helpFooter frame the per-command blocks in `prism help`.
const helpHeader = `prism - semantic change intelligence for coding agents (embedded Grove)

Usage:
`

const helpFooter = `
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

var commandTable = []*command{
	{name: "init", flags: "--harness= --workspace= --interface= --yes -y --global --no-permissions --deny-builtin-search --read-guard --no-read-guard --refresh --print-config= --dry-run", run: cmdInit, help: `  prism init [--harness <ids>] [dir]
                                  Write prism.yaml + project-local MCP config;
                                  --dry-run prints the diff of every file it would write
                                  and writes nothing; what it writes is recorded in
                                  .grove/install-manifest.json
`},
	{name: "uninstall", flags: "--dry-run", run: cmdUninstall, help: `  prism uninstall [dir]           Remove exactly what init recorded (Prism's keys,
                                  steering blocks, files it created); [--dry-run]
`},
	{name: "install", flags: "--harness= --workspace= --interface= --yes -y --global --no-permissions --deny-builtin-search --read-guard --no-read-guard --refresh --print-config= --dry-run", run: cmdInit, help: `  prism install [--harness <ids>] [dir]
                                  Alias for 'prism init'
`},
	{name: "cleanup-global", run: cmdCleanupGlobal, help: `  prism cleanup-global            Remove Prism entries written by old global setup
`},
	{name: "index", flags: "--format= --json --allow-heuristic --force --rebuild --untrusted --trusted", run: cmdIndex, help: `  prism index [dir]               Index codebase via Grove (delta-aware); text summary,
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
`},
	{name: "watch", flags: "--debounce=", run: cmdWatch, help: `  prism watch [dir]               Keep the index warm: delta-reindex on file save
                                  (push model; [--debounce 2s], Ctrl+C to stop)
`},
	{name: "status", flags: "--format= --json", run: cmdStatus, help: `  prism status [dir]              Show graph stats from Grove ([--format text|json] [--json])
`},
	{name: "doctor", flags: "--integrity --format= --json", run: cmdDoctor, help: `  prism doctor [dir]              Diagnose engine, index, and capabilities
                                  ([--integrity] also checks the index file
                                  for corruption and engine-version drift)
                                  ([--format text|json] [--json]; the per-language manifest is JSON only)
`},
	{name: "map", flags: "--depth= --component= --tests --expand= --json --format= --max-sites= --all-components", run: cmdMap, help: `  prism map [dir]                 Component-level architecture map: directories,
                                  induced dependency edges (weights + evidence
                                  tiers), cycles. Production shape by default
                                  ([--depth N] [--component X] [--tests]
                                  [--expand 'A->B'] [--json | --format text|json])
                                  [--max-sites N]  cited sites per edge (default 5)
                                  [--all-components]  include docs/examples and isolated
                                  other-language script folders (hidden by default)
`},
	{name: "graph", flags: "--port= --no-open --tests", run: cmdGraph, help: `  prism graph [dir]               Open the interactive graph explorer in a browser:
                                  directories -> files -> symbols -> one symbol's
                                  callers and callees, with search and the evidence
                                  behind each edge. Serves on localhost until Ctrl+C
                                  ([--port N] [--no-open] [--tests])
`},
	{name: "cycles", flags: "--depth= --tests --json --format=", run: cmdCycles, help: `  prism cycles [dir]              Dependency cycles with per-edge evidence
                                  ([--depth N] [--tests] [--json | --format text|json])
`},
	{name: "arch", aliases: []string{"arch-check"}, flags: "--deny= --depth= --tests --strict --json --format=", run: cmdArch, help: `  prism arch [dir]                Validate declared architecture rules
                                  (arch_deny: "<from> -> <to>" in prism.yaml)
                                  against the component view; violations cite
                                  file:line sites; exit 1 on violation — a CI
                                  gate ([--deny 'A -> B'] [--depth N] [--tests] [--strict]
                                  [--json | --format text|json])
`},
	{name: "verify", flags: "--base= --removed= --strict --format= --json", run: cmdVerify, help: `  prism verify [dir]              Optionally review a diff (working tree vs
                                  --base, default HEAD) for missed sites and
                                  architecture changes. Exit 1 if incomplete;
                                  --strict also exits 1 on review.
                                  [--base REF] [--removed a,b,c] [--strict]
                                  [--format text|json] ([--json] = --format json)
`},
	{name: "query", flags: "--repo= --terms= --include= --delivery= --max-files= --limit= --profile= --format= --depth! --graph-depth!", run: cmdQuery, help: `  prism query --terms a,b,c [dir]  Find related implementations, callers,
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
`},
	{name: "read", flags: "--repo= --from= --to= --offset= --limit= --dir= --include-sensitive --format=", run: cmdRead, help: `  prism read <file> [dir]         Read file with compression
                                  [--repo <id>]  read paths inside a linked repository
                                  --from N --to M  Line range (inclusive);
                                  --offset N --limit N also work; --dir <path>
                                  [--include-sensitive]  read a .env, key or credential file
                                  --format text|json  Output format (default: text)
`},
	{name: "search", flags: "--case-sensitive -s --ignore-case -i --word -w --no-default-excludes --include-sensitive --scope= --regex --limit= --path= --paths= --glob= --include= --files-only -l --exhaustive --all --context= -C= --include-bodies --no-bodies --rollup-only --dir= --repo= --format=", run: cmdSearch, help: `  prism search <term>... [dir]    Search symbol names AND raw source text (a real
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
                                  (ripgrep spellings work too: --include = --glob,
                                  -l = --files-only, -C = --context)
                                  [--include-bodies|--no-bodies]  bounded source by default
                                  [--rollup-only]  on a truncated search, skip the raw
                                  sample and return only the grouped-by-symbol rollup
                                  [--dir <path>]  where to search (default: .)
                                  [--repo <id>|all]  text matches in a linked repository
                                  --format text|json  Output format (default: text)
`},
	{name: "lookup", flags: "--repo= --dir= --file= --path= --fields= --format=", run: cmdLookup, help: `  prism lookup <name> [dir]       Show full source for a symbol
                                  [--repo <id>|all]  answer from a linked repository
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  Multiple names: prism lookup <name>... [--dir <path>]
                                  [--fields signature,doc,body,kind,parent,modifiers]
                                  --format text|json  Output format (default: text)
`},
	{name: "node", flags: "--file= --format=", run: cmdNode, help: `  prism node <symbol-or-file> [dir]  One-shot orientation: a symbol's source +
                                  its neighbours, or a file's source + the
                                  symbols it defines + the files depending on it
                                  [--file <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
`},
	{name: "callers", flags: "--depth= --file= --format=", run: cmdCallers, help: `  prism callers <symbol> [dir]    Transitive callers as a tree, resolved incoming calls
                                  and interface dispatch (marked "via"); [--depth N]
                                  (default 3, max 6; 200 callers) [--file <path>]
                                  [--format text|json]
`},
	{name: "path", flags: "--format=", run: cmdPath, help: `  prism path <from> <to> [dir]    Shortest resolved call path from one symbol to another;
                                  exit 4 when there is none [--format text|json]
`},
	{name: "references", aliases: []string{"refs"}, flags: "--file= --path= --kind= --format=", run: cmdReferences, help: `  prism references <name> [dir]   Find indexed syntactic uses of a name
                                  (comments/strings excluded) as path:line:col: text;
                                  Type.member or [--file <path> | --path <path>] resolves one symbol;
                                  each resolved site is tagged decl, call, read, write or type-use,
                                  and --kind call,read,write,decl,type keeps only those
                                  --format text|json  Output format (default: text)
`},
	{name: "resolve", flags: "--format=", run: cmdResolve, help: `  prism resolve <name> [dir]      Resolve a name to its definition(s): file:line + kind
                                  --format text|json  Output format (default: text)
`},
	{name: "edges", flags: "--direction= --dir= --kinds= --format=", run: cmdEdges, help: `  prism edges <name> [dir]        Walk the graph one hop from a symbol
                                  ([--direction|--dir in|out|both] [--kinds calls,uses-type,...])
                                  --format text|json  Output format (default: text)
`},
	{name: "test-plan", flags: "--base= --coverage= --format=", run: cmdTestPlan, help: `  prism test-plan [dir]          Impacted test symbols, file targets, evidence, and gaps
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
`},
	{name: "workspace", flags: "--id= --no-index -o= --output= --server= --repo= --ref= --link", run: cmdWorkspace, help: `  prism workspace                Linked repositories: path, branch, commit, index, how linked
                                  add <path>... [--id <name>] [--no-index]
                                                            Link local checkouts: written to prism.yaml, made
                                                            visible from both, and indexed if never indexed
                                  add <git-url>[@ref]       Fetch a repository that is not checked out into a
                                                            read-only cache at that commit, index it, link it
                                  fetch                     Re-resolve cache clones' branches
                                  prune                     Delete cache clones nothing links to
                                  export [-o|--output <file>]  Write this repository's link bundle (no source bodies)
                                  push [--server <url>]     Upload it to the link server (workspace_server)
                                    export and push: [--repo <id>] [--ref <commit>] name the bundle
                                  remove <id>               Unlink; the checkout is not touched
                                  discover [--link]         Related checkouts found in manifests
                                  Guide: docs/MULTI_REPO.md
`},
	{name: "link-server", flags: "--dir= --port=", run: cmdLinkServer, help: `  prism link-server --dir <store> [--port N]
                                  Serve uploaded link bundles: who references a symbol across
                                  repositories that are not checked out (PRISM_LINK_TOKEN required token)
`},
	{name: "change-impact", flags: "--diff --base= --dir= --repo= --file= --path= --format= --json", run: cmdChangeImpact, help: `  prism change-impact <query> [dir]  Indexed potential impact sites for a method signature change:
                                  declaration(s), override/implementation family (subtype
                                  closure), super-declarations, and indexed callers.
                                  query format: Type.method or Type.method(ParamType, ...)
                                  Up to 10 query names may be batched; bare words are names
                                  [--diff [--base REF] [--json]]  impact of the working-tree diff: per changed
                                  symbol, the sites the diff touched and did not, direct callers of
                                  body-only changes, other components reached, and the tests
                                  [./dir | --dir <path>]  select a repository
                                  [--repo <id>]  anchor in a linked repository
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
`},
	{name: "rename-plan", flags: "--file= --path= --allow-collision --format=", run: cmdRenamePlan, help: `  prism rename-plan <query> <NewName> [dir]     Change-set as line edits with substitutions
                                                (--format text|json; query: Type.method or Type.method(ParamType, ...))
                                                [--file <path> | --path <path>]  disambiguate a symbol
                                                Exits 1 with the declarations when NewName is already
                                                declared where the member would land;
                                                [--allow-collision] prints the plan anyway
`},
	{name: "missing-implementations", flags: "--signature= --file= --path= --format=", run: cmdMissingImplementations, help: `  prism missing-implementations <query> [dir]  Types claiming the contract that do NOT
                                  implement Type.method (missing / abstract / unverifiable)
                                  — the interface-evolution companion to change-impact;
                                  a member not declared yet lists every implementer
                                  [--signature '(ctx context.Context) error'] marks matches
                                  [--file <path> | --path <path>]  disambiguate a symbol
                                  --format text|json  Output format (default: text)
`},
	{name: "dead-code", flags: "--roots= --format=", run: cmdDeadCode, help: `  prism dead-code [dir] [--roots a,b]  Unreachable production functions/methods
                                  --format text|json  Output format (default: text)
`},
	{name: "assist", flags: "--model= --apply --apply-ambiguous --verify= --dir= --format=", run: cmdAssist, help: `  prism assist [--model <spec>] [--apply|--apply-ambiguous] [--verify "<cmd>"] "<task>"
                                  NL task -> deterministic ops via any model (ollama:/claude:/openai:)
                                  [--dir <path>]  repository (default: .)
                                  (precision-first; relay the caveats)
                                  --format text|json  Output format (default: text)
`},
	{name: "compact", run: cmdCompact, help: `  prism compact [dir]             Compress conversation JSON from stdin
`},
	{name: "feedback", flags: "--tool= --rating= --notes= --query-id= --list", run: cmdFeedback, help: `  prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
                                  Record quality feedback for a Prism result in
                                  .grove/feedback.jsonl; [--list] prints the records
`},
	{name: "serve", flags: "--port= --listen= --token-file=", run: cmdServe, help: `  prism serve [--port 8888] [dir] Start the HTTP API server (stdio MCP is 'prism mcp')
                                  Loopback only, bearer token printed and written to
                                  .grove/serve.token; [--listen <addr> --token-file <f>]
`},
	{name: "mcp", flags: "--compact --legacy", run: cmdMCP, help: `  prism mcp [--compact|--legacy] [dir]
                                  Start MCP server on stdio (compact gateway is default)
`},
	{name: "drift", run: cmdDrift, help: `  prism drift                     MCP only (prism_drift): it compares files with what the
                                  running server delivered this session; a CLI call has no session
`},
	{name: "config", flags: "--check --format= --json", run: cmdConfig, help: `  prism config [dir]              Show every prism.yaml key, its value and its source;
                                  --check exits 1 on an unknown key or invalid value
                                  ([--check] [--format text|json] [--json])
`},
	{name: "ci", flags: "--base= --format= --json --untrusted --trusted", run: cmdCI, help: `  prism ci [dir]                  PR/CI gate: index, verify, change-impact --diff and test-plan
                                  against --base REF; facts with site lists, no LLM.
                                  [--format text|github|sarif|json] [--json] [--untrusted|--trusted]
                                  Exits 1 on a finding in ci_fail_on (prism.yaml of the BASE
                                  revision; default missed_sites, arch), 3 when compiler-backed
                                  analysis did not run
`},
	{name: "trust", run: cmdTrustCommand, help: `  prism trust [dir]               Let indexing run this repository's own build tools
                                  (Maven/Gradle, its typescript, rust-analyzer, go toolchain)
`},
	{name: "untrust", run: cmdUntrustCommand, help: `  prism untrust [dir]             Never run them for this repository; also --untrusted
                                  on index or PRISM_UNTRUSTED=1. Recorded per user in
                                  ~/.config/prism/trusted.json, never read from the repo
`},
	{name: "help", aliases: []string{"-h", "--help"}, run: cmdHelp, help: `  prism help exit-codes           What each exit code means (0 answered, 1 finding, 2 usage,
                                  3 degraded, 4 not found, 5 ambiguous, 70 internal)
`},
	{name: "version", aliases: []string{"--version", "-version"}, flags: "--verbose -v", run: cmdVersion, help: `  prism version                   Print version ([--verbose|-v] also the linked Grove engine)
`},
}

// flagSpec reports whether name is one of c's flags and whether it takes a
// value.
func (c *command) flagSpec(name string) (known, takesValue bool) {
	for _, f := range strings.Fields(c.flags) {
		if f == name || f == name+"!" {
			return true, false
		}
		if f == name+"=" {
			return true, true
		}
	}
	return false, false
}

// normalizeArgs is the parser every command shares, applied before its own
// loop: --flag=value becomes --flag value, everything after -- passes
// through untouched (a search term may start with a dash), and a flag the
// command does not declare is a usage error that names the nearest one.
// Values are never read as flags. The command's own loop still assigns the
// values; it no longer has to recognize the spellings.
func (c *command) normalizeArgs(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i:]...), nil
		}
		if len(a) < 2 || a[0] != '-' || isNumber(a) {
			out = append(out, a)
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		known, takesValue := c.flagSpec(name)
		if !known {
			if c.flags == "" && !strings.HasPrefix(a, "--") {
				out = append(out, a) // a command without flags owns its arguments
				continue
			}
			return nil, c.unknownFlagError(name)
		}
		switch {
		case hasValue && takesValue:
			out = append(out, name, value)
		case hasValue:
			return nil, fmt.Errorf("%s: %s takes no value", c.name, name)
		case takesValue && i+1 < len(args):
			out = append(out, a, args[i+1]) // the value, even if it starts with "-"
			i++
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *command) unknownFlagError(name string) error {
	best, bestD := "", 3
	for _, f := range strings.Fields(c.flags) {
		if strings.HasSuffix(f, "!") {
			continue
		}
		f = strings.TrimSuffix(f, "=")
		if d := editDistance(name, f); d < bestD {
			best, bestD = f, d
		}
	}
	if best != "" {
		return fmt.Errorf("%s: unknown flag %q; did you mean %q? (see prism help)", c.name, name, best)
	}
	return fmt.Errorf("%s: unknown flag %q (see prism help)", c.name, name)
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/version"
)

// init state shared with the cmdInit wrapper (MKT-05).
var (
	initDryRun   bool
	initRealRoot string            // the project root
	initRoot     string            // where init wrote: the root, or the dry-run mirror
	initBefore   map[string][]byte // snapshot before init wrote
)

func cmdInit(args []string) int {
	initWriteFailures = 0
	initDryRun, initRoot, initRealRoot, initBefore = false, "", "", nil
	var rest []string
	for _, a := range args {
		if a == "--dry-run" {
			initDryRun = true
			continue
		}
		rest = append(rest, a)
	}
	var rc int
	if initDryRun {
		// init's progress lines name the mirror; show them as the project.
		out := captureInitStdout(func() { rc = runInit(rest) })
		if initRoot != "" && initRoot != initRealRoot {
			out = strings.ReplaceAll(out, initRoot, initRealRoot)
		}
		fmt.Print(out)
	} else {
		rc = runInit(rest)
	}
	if initDryRun {
		defer func() { initDryRun = false }()
		if initRoot != "" && initRoot != initRealRoot {
			defer os.RemoveAll(initRoot)
			if rc == 0 {
				return printDryRunDiff(initRealRoot, initRoot, initBefore, snapshotInstallFiles(initRoot))
			}
		}
		return rc
	}
	if rc == 0 && initRoot != "" && initBefore != nil {
		if err := recordManifest(initRoot, manifestFromDiff(initBefore, snapshotInstallFiles(initRoot))); err != nil {
			initWriteFailed(filepath.Join(initRoot, manifestPath), err)
		}
	}
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
	if printConfig == "" {
		initRealRoot = abs
		initBefore = snapshotInstallFiles(abs)
		if initDryRun {
			// MKT-05: run against a copy and show the diff; write nothing
			// in the project and nothing user-global.
			mirror, err := makeInitMirror(abs, initBefore)
			if err != nil {
				fmt.Fprintln(os.Stderr, "init --dry-run:", err)
				return exitInternal
			}
			abs, dir = mirror, mirror
		}
		initRoot = abs
	}
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
	if warning := core.PrismInstallationWarning(prismBin, version.Version); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	// Related checkouts: recorded, reported, linked only on a yes.
	if !initDryRun {
		offerWorkspaceLinks(abs, workspaceMode)
	}

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
	if initDryRun {
		// user-global cleanup is skipped in a dry run
	} else if _, err := removeLegacyGlobalMCPRegistrations(); err != nil {
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

When the task asks for ` + core.ImpactRequiredFor + `:
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
  - Relay change_impact sites as-is; ` + core.ImpactOptionalFor + ` can
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

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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/tools"
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
  prism init [dir]                Write prism.yaml + CLI steering instructions
  prism install [dir]             Alias for 'prism init'
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
  prism compact [dir]             Compress conversation JSON from stdin
  prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]
                                  Submit quality feedback for a Prism result
  prism savings [dir]             Show session savings dashboard
  prism config [dir]              Show resolved configuration
  prism version                   Print version

Supported agent steering files written by prism init:
  Claude Code  →  CLAUDE.md
  Cursor       →  .cursorrules + AGENTS.md
  Windsurf     →  .windsurfrules
  GitHub Copilot / VS Code → .github/copilot-instructions.md
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
	case "compact":
		return cmdCompact(rest)
	case "feedback":
		return cmdFeedback(rest)
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
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			fmt.Fprintln(os.Stderr, "init: unsupported flag:", a)
			return 2
		}
		filtered = append(filtered, a)
	}
	args = filtered

	dir := dirArg(args, 0, ".")
	abs, _ := filepath.Abs(dir)
	cfg := config.Default()

	// 1. Write prism.yaml into the project. Grove is embedded in-process now,
	// so the file no longer needs grove_url / grove_binary.
	yaml := fmt.Sprintf(`version: 1
# model: auto  # Prism uses a safe default context window unless overridden.
#               # Override here for project-specific model sizing, e.g.:
#               # model: "claude-sonnet-4-6"
profile: "%s"
`, cfg.Profile)
	prismYAML := filepath.Join(abs, "prism.yaml")
	if err := os.WriteFile(prismYAML, []byte(yaml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("wrote", prismYAML)

	writeSteeringInstructions(abs)
	return 0
}

// steeringInstructionsCLI is injected into agent instruction files.
const steeringInstructionsCLI = `
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle available as a CLI. Use it via Bash to get
callers, callees, and test contracts the agent would not find by grep+read alone.

### Decision tree

| Situation | Command |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/tests for a symbol just found | ` + "`" + `prism query "<task>" --terms a,b --include graph,tests --format text` + "`" + ` |
| Read a whole file | ` + "`" + `prism read <file> --format text` + "`" + ` |
| Read one function body | ` + "`" + `prism lookup <pkg.FuncName> --format text` + "`" + ` |
| Find docs about a topic | ` + "`" + `prism query "<task>" --include docs --format text` + "`" + ` |
| Blast radius of a change | ` + "`" + `prism query "<task>" --terms a,b --depth 3 --format text` + "`" + ` |
| Symbols with no tests (before writing/fixing) | ` + "`" + `prism query "<task>" --terms a,b --include graph,coverage_gaps --format text` + "`" + ` |

### Canonical workflow

    grep/find/rg <terms>                      <- locate anchor first; shell tools always win here
      -> prism query "<task>" \               <- expand from anchor: callers, callees, tests
           --terms <same-terms> \
           --include graph,tests \
           --format text
      then selectively:
      -> prism read <file> --format text      <- whole file, session-compressed
      -> prism lookup <pkg.FuncName> --format text  <- one function (~5x cheaper than read)

### prism query flags

| Flag | Default | Purpose |
|---|---|---|
| --terms a,b | — | Anchor on specific symbol names (grep-precision + graph expansion) |
| --include a,b | graph,tests | graph (callers/callees), tests, docs (filenames only), coverage_gaps |
| --depth N | 2 | BFS hops: 1 = immediate callers, 3+ = blast radius |
| --format | text | text (default), lean (compact JSON), json (full metadata) |

### Other commands

| Command | When |
|---|---|
| prism index [dir] | Once at session start — not on every step |
| prism search <keyword> --format text | Find a symbol by name when file is unknown |

### Do NOT

- Do NOT call prism query before searching — use shell tools (grep, find, rg) to find the anchor first; prism expands from it
- Do NOT use prism search as a search replacement — it searches symbol names only, not source text
- Do NOT use prism read for a single function — use prism lookup instead
- Do NOT re-run prism index on every step — delta indexing is automatic
- Do NOT manually cross-reference coverage_gaps output — treat it as authoritative and use it as the terminal step, not the start of a manual verification chain
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
		// Cline agent steering
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
			content = injectPrismSection(string(existing), steeringInstructionsCLI)
		} else {
			content = steeringInstructionsCLI
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

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
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
	out, err := invokeWithPersistentLedger(dir, "prism_lookup", map[string]any{"name": name})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
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
		h := tools.NewHandlerWithLedger(cfg, root, client, ledger)
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
	// Fallback: JSON
	printJSON(m)
}

// printLeanOutput strips metadata fields (scores, spans, IDs, timing) and
// emits compact JSON with only the fields agents actually use.
func printLeanOutput(m map[string]any) {
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

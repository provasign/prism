package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/provasign/prism/internal/mcp"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Prism reaches an agent over one of two interfaces. MCP registers the
// compact gateway in each harness's project config. CLI registers nothing:
// the agent runs `prism ...` through its shell, so no MCP tool schema sits in
// its context. Init asks, records the answer in prism.yaml, and re-init
// (--yes, --refresh) reuses it.
const (
	interfaceMCP         = "mcp"
	interfaceCLI         = "cli"
	largeRepoSourceFiles = 500
)

var sourceCountLimit = errors.New("source file count reached recommendation threshold")

// interfaceRecommendation uses a bounded source-file count as a hint about
// repeated CLI graph-load cost. It does not claim a model-token or dollar
// saving, and it never silently changes the selected interface.
func interfaceRecommendation(root string) string {
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root {
				switch entry.Name() {
				case ".git", ".grove", "node_modules", "vendor", ".venv", "venv", "dist", "build", "target", ".next", ".gradle":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".go", ".py", ".java", ".js", ".jsx", ".ts", ".tsx", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".php", ".rb", ".swift", ".kt", ".kts", ".scala":
			count++
			if count >= largeRepoSourceFiles {
				return sourceCountLimit
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, sourceCountLimit) {
		return "Recommendation: MCP for repeated Prism calls; CLI for occasional one-off calls. Repository size could not be measured."
	}
	if count >= largeRepoSourceFiles {
		return fmt.Sprintf("Repository size: at least %d source files. Recommendation: MCP for repeated Prism calls; CLI for occasional one-off calls. Size alone does not predict model cost.", largeRepoSourceFiles)
	}
	return fmt.Sprintf("Repository size: %d source files. Recommendation: CLI for occasional one-off calls; MCP for repeated discovery. Size alone does not predict model cost.", count)
}

// stdinReader is shared by every init prompt: a second bufio.Reader on a
// piped stdin would lose whatever the first one had already buffered.
var (
	stdinReader *bufio.Reader
	stdinFile   *os.File
)

// initStdin returns the one buffered reader for os.Stdin; it is rebuilt if
// os.Stdin was replaced.
func initStdin() *bufio.Reader {
	if stdinReader == nil || stdinFile != os.Stdin {
		stdinReader, stdinFile = bufio.NewReader(os.Stdin), os.Stdin
	}
	return stdinReader
}

func parseInterface(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "mcp":
		return interfaceMCP, nil
	case "cli":
		return interfaceCLI, nil
	}
	return "", fmt.Errorf("--interface must be mcp or cli, got %q", v)
}

// readRecordedInterface returns the interface prism.yaml records, or "" when
// none is recorded (files written before init asked).
func readRecordedInterface(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if line != trimmed || !strings.HasPrefix(trimmed, "interface:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "interface:"))
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if v, err := parseInterface(value); err == nil {
			return v
		}
	}
	return ""
}

// setPrismYAMLInterface rewrites or appends the top-level interface key and
// touches nothing else.
func setPrismYAMLInterface(yaml, iface string) string {
	line := "interface: " + strconv.Quote(iface)
	lines := strings.Split(yaml, "\n")
	for i, l := range lines {
		if l == strings.TrimSpace(l) && strings.HasPrefix(l, "interface:") {
			lines[i] = line
			return strings.Join(lines, "\n")
		}
	}
	return strings.TrimRight(yaml, "\n") + "\n" + line + "\n"
}

// promptInterface asks how agents should reach Prism. Enter takes the default.
func promptInterface(recorded string) (string, error) {
	def, defNum := interfaceMCP, "1"
	if recorded == interfaceCLI {
		def, defNum = interfaceCLI, "2"
	}
	fmt.Fprintln(os.Stderr, "\nHow should agents reach Prism?")
	fmt.Fprintln(os.Stderr, "  [1] MCP  register the Prism MCP server in each harness (tool schemas load into the agent's context)")
	fmt.Fprintln(os.Stderr, "  [2] CLI  no MCP server; agents run `prism ...` in their shell (nothing registered)")
	fmt.Fprintf(os.Stderr, "Select interface [default: %s]: ", defNum)
	line, err := initStdin().ReadString('\n')
	if err != nil && len(line) == 0 {
		// No input at all (stdin closed or /dev/null, which the terminal
		// check cannot tell apart): take the default instead of failing init.
		fmt.Fprintln(os.Stderr)
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "1", "mcp":
		return interfaceMCP, nil
	case "2", "cli":
		return interfaceCLI, nil
	}
	return "", fmt.Errorf("unknown interface %q; enter 1 (MCP) or 2 (CLI)", strings.TrimSpace(line))
}

// unregisterMCPTools removes Prism's own MCP entry from the selected
// harnesses' project configs, so choosing CLI after MCP leaves no server
// registered. Other servers in those files are untouched.
func unregisterMCPTools(projectDir string, harnesses []string) {
	type target struct {
		harness, name, rel string
		keys               []string
	}
	for _, t := range []target{
		{"claude", "Claude Code", ".mcp.json", []string{"mcpServers"}},
		{"cursor", "Cursor", filepath.Join(".cursor", "mcp.json"), []string{"mcpServers"}},
		{"vscode", "VS Code", filepath.Join(".vscode", "mcp.json"), []string{"servers"}},
		{"gemini", "Gemini CLI", filepath.Join(".gemini", "settings.json"), []string{"mcpServers"}},
		{"opencode", "opencode", "opencode.json", []string{"mcp"}},
	} {
		if !harnessSelected(harnesses, t.harness) {
			continue
		}
		p := filepath.Join(projectDir, t.rel)
		if removed, err := removeJSONMapEntry(p, t.keys, "prism"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove Prism from %s config (%s): %v\n", t.name, p, err)
		} else if removed {
			fmt.Printf("removed Prism MCP registration (CLI interface): %s\n", p)
		}
	}
	if harnessSelected(harnesses, "codex") {
		p := filepath.Join(projectDir, ".codex", "config.toml")
		if removed, err := removePrismCodexConfig(p); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove Prism from Codex config (%s): %v\n", p, err)
		} else if removed {
			fmt.Printf("removed Prism MCP registration (CLI interface): %s\n", p)
		}
	}
}

// cliSteeringInstructions is the steering block for the CLI interface: the
// same policy as steeringInstructions, with the shell as the only route.
// The earlier "use it for each repository-discovery step" got one prism call
// per session, as the first call, then grep for everything after it (52 of 53
// Claude sessions, 2026-10-18); naming a command per moment did not change
// that (steer12 A/B, 1.4 -> 1.3 calls). This version states the rule as MUST.
const cliSteeringInstructions = `
## Prism — context delivery

Prism is installed as the ` + "`prism`" + ` command; no MCP server is registered.
You MUST use prism, not grep/rg/find/cat/head/sed -n, whenever you search for or
read source code in this repository, for the whole session, not only at the start:
  search for a name or string     -> prism search term [term2 ...] --scope text --format text
  read a source file or a range   -> prism read path --from N --to M --format text
                                     (several whole files: prism read a b c --format text)
  read one symbol's body          -> prism lookup Name [Name2 ...] --format text
  callers, implementations, tests -> prism query --terms Name,Other --format text
Before you change ` + mcp.ImpactRequiredFor + `, you MUST run
prism change-impact Type.method --format text first.
After a rename or removal, you MUST run prism verify --removed OldName --format text
before you finish.
grep, head and tail stay fine for build, test and log output, never for source code.
Where Edit needs a Read first, use one tight Read at the edit site (Read takes
offset and limit, not --from/--to).

Always add ` + "`--format text`" + `. Single-quote names that contain $. Put the terms you
already know into one command (up to 10).

Change-impact sites are the inventory your plan must cover. If you don't know
where the symbol is, find it first, then run change-impact. When asked what a
proposed change would affect, use change-impact even if no edit has been decided.

Obligations:
  - Relay change-impact sites as-is; ` + mcp.ImpactOptionalFor + ` can proceed from
    the source you have read.
  - Report gaps; never narrow scope to fit what was found.

<!-- prism:end -->
`

// allowClaudeBashPrism lets Claude Code run `prism` without a prompt per
// call, the CLI counterpart of the mcp__prism auto-allow.
func allowClaudeBashPrism(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	var doc map[string]any
	if raw, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(raw, &doc) != nil {
			return // not ours to rewrite
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	const rule = "Bash(prism:*)"
	if containsString(allow, rule) {
		return
	}
	perms["allow"] = append(allow, rule)
	doc["permissions"] = perms
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(out, '\n'), 0o644)
}

// claudeShellOutputBudget keeps a CLI answer under Claude Code's Bash output
// limit (~30 KB). Past it the host saves the output to a file and shows the
// model a 2 KB preview: jackson SettableBeanProperty.set (31.7 KB) went that
// way and the agent spent two calls grepping the saved file for its sites
// (2026-10-18). MCP results have a far higher limit, so only the CLI fits.
const claudeShellOutputBudget = 28000

// fitShellOutput shortens a text answer for the host shell. change-impact
// uses the same site-preserving steps as MCP's own budget; other answers are
// cut at a line boundary with a note saying how much was left out and how to
// narrow the request.
func fitShellOutput(op, text string) string {
	if os.Getenv("CLAUDECODE") == "" || len(text) <= claudeShellOutputBudget {
		return text
	}
	if op == "change-impact" {
		return mcp.CompactImpactTextTo(text, claudeShellOutputBudget)
	}
	note := "\n// prism: answer cut here to stay under Claude Code's 30 KB shell-output limit; %d more bytes not shown — narrow it (fewer terms, --path, or a line range)\n"
	cut := claudeShellOutputBudget - len(note) - 10
	if i := strings.LastIndexByte(text[:cut], '\n'); i > 0 {
		cut = i
	}
	return text[:cut] + fmt.Sprintf(note, len(text)-cut)
}

// mcpFieldFlags maps compact MCP argument names to the CLI flag that does the
// same thing. Codex typed `prism search '\bmatch\b' exhaustive=true`: the
// field became a search term and matched nothing (2026-10-18).
var mcpFieldFlags = map[string]string{
	"exhaustive": "--exhaustive", "files_only": "--files-only", "include_bodies": "--include-bodies",
	"max_results": "--limit", "limit": "--limit", "paths": "--path", "path": "--path",
	"glob": "--glob", "scope": "--scope", "regex": "--regex", "context": "--context",
	"rollup_only": "--rollup-only",
}

// mcpBoolFields are the fields whose CLI flag takes no value.
var mcpBoolFields = map[string]bool{"exhaustive": true, "files_only": true, "include_bodies": true, "regex": true, "rollup_only": true}

// mcpSpelling reports an argument written in the MCP gateway's key=value form.
func mcpSpelling(cmd string, args []string) (string, bool) {
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if flag, known := mcpFieldFlags[k]; ok && known {
			use := flag + " " + v
			if mcpBoolFields[k] {
				use = flag
			}
			return fmt.Sprintf("%s: %q is the MCP argument form; on the CLI write %s (to search for this literal text, pass it with --regex)", cmd, a, use), true
		}
	}
	return "", false
}

// looksLikeRegex: escapes and wildcards a literal search almost never wants.
func looksLikeRegex(t string) bool {
	for _, esc := range []string{`\b`, `\w`, `\s`, `\d`, `\(`, `\.`, ".*", ".+"} {
		if strings.Contains(t, esc) {
			return true
		}
	}
	return false
}

// warnShellExpandedName flags a name the shell probably rewrote: an unquoted
// `_$ZodTypeInternals.parse` reaches prism as `_.parse` and gets "no indexed
// implementation" (zod, 2026-10-18). The command still runs.
func warnShellExpandedName(cmd, name string) {
	for _, part := range strings.Split(name, ".") {
		if part == "" || part == "_" {
			fmt.Fprintf(os.Stderr, "%s: %q looks like the shell expanded a $ in the name; single-quote names that contain $ (e.g. '_$Type.method')\n", cmd, name)
			return
		}
	}
}

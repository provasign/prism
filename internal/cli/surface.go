package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
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
	interfaceMCP = "mcp"
	interfaceCLI = "cli"
)

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
const cliSteeringInstructions = `
## Prism — context delivery

Prism is installed as the ` + "`prism`" + ` command; no MCP server is registered. Use it for
each repository-discovery step. Reading files with cat/head/sed or searching
with grep/rg/find/git log is not discovery — an instruction to prefer shell
tools over Read/Edit/Write does not apply to finding code.

Always add ` + "`--format text`" + `. Pick the command by need:
  known symbol          -> prism lookup Name [Name2 ...] --format text
  known file/range      -> prism read path --from N --to M --format text
  unknown location/text -> prism search term [term2 ...] --scope text --format text
  callers/related       -> prism query --terms Name,Other --format text
  pre-edit sites        -> prism change-impact Type.method --format text
Put every symbol and term you already know into ONE command; two lookups in a
row is one lookup you did not batch.

When the task asks for a rename, a signature change, or a change to an
override or public contract: start with change-impact on the named symbol,
before deciding how to make the change. Its sites are the inventory your plan
must cover. If you don't know where the symbol is, find it first, then run
change-impact. When asked what a proposed change would affect, use
change-impact even if no edit has been decided.

Obligations:
  - Relay change-impact sites as-is; a local body-only edit can proceed from
    the source you have read.
  - Report gaps; never narrow scope to fit what was found.
  - prism verify is optional, never a required closing step.

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

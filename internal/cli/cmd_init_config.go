package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
	r := &mcpRegistration{
		projectDir: projectDir, prismBin: prismBin,
		permissions: permissions, refresh: refresh, denyBuiltinSearch: denyBuiltinSearch,
		entry: mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact", workspaceFolderVar}},
		// Claude Code launches project-scope MCP servers with cwd at the
		// project root, so its entry needs no pinned absolute path — this
		// keeps .mcp.json portable and correct after the repo moves. The IDE
		// writers keep the explicit dir because their launch cwd is not
		// guaranteed.
		claudeEntry:    mcpEntry{Command: prismBin, Args: []string{"mcp", "--compact"}},
		claudeSettings: claudeSettings,
	}
	for _, h := range harnessRegistry {
		if !harnessSelected(harnesses, h.Status().id) {
			continue
		}
		if path, ok := h.Apply(r); ok {
			written = append(written, path)
		}
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
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// The null device is a character device too: `prism init </dev/null`
	// (CI, scripts) asked a question and failed with "init: EOF".
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
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

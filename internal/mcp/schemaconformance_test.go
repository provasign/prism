package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSchemaAdvertisesEveryArgTheToolFuncReads is the guard for the bug that
// shipped 2026-08-16: prism_read's toolRead started reading "offset" and
// "limit" via intArg, the description was updated to promise them, but the
// schema's "properties" map was never edited to match — the new-string
// replace silently no-op'd against stale surrounding text. `go build`,
// `go vet`, and every existing test passed, because Invoke calls toolRead
// directly and args are a bare map[string]any: nothing type-checks a key
// against what tools/list advertises.
//
// The result: a real MCP client only ever sends keys listed in the
// inputSchema, so the parameter existed in code and was UNREACHABLE from any
// real agent. A smoke run "tested" it by invoking Invoke directly, bypassing
// the schema exactly like this bug did, and reported false adoption data
// that had to be thrown out. This test parses tools.go once (no server
// round-trip needed) and cross-checks the two views the earlier verification
// never compared.
//
// It is necessarily approximate — regexes over Go source, not real Go
// analysis — so it allows a documented false-positive list rather than
// trying to be exact. A new false positive should be understood before being
// added to the list, not silenced by widening a regex.
func TestSchemaAdvertisesEveryArgTheToolFuncReads(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "tools.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Split into per-tool toolXxx function bodies (brace-balanced), and
	// separately gather the case "prism_x": -> toolXxx() dispatch map so we
	// know which function serves which advertised tool name.
	funcs := extractFuncBodies(t, body)
	dispatch := extractDispatchMap(t, body)
	advertised := map[string]bool{}
	for _, s := range ToolSchemas() {
		advertised[s["name"].(string)] = true
	}

	argRe := regexp.MustCompile(`\b(?:stringArg|stringsArg|intArg|boolArg)\(args,\s*"([a-zA-Z_][a-zA-Z0-9_]*)"`)

	// Known cases where a func reads a key by a legacy/alias name that is
	// intentionally NOT in its own schema: a fallback for an older client
	// still sending the old key (stringArg(args, "name", stringArg(args,
	// "symbol", ...))). The primary key IS declared and checked normally;
	// only the alias is exempted. Document why before adding to this, do
	// not use it to silence a real miss.
	allow := map[string]map[string]bool{
		"toolNode":   {"symbol": true},        // pre-rename alias for "name"
		"toolLookup": {"qualifiedName": true}, // pre-rename alias for "name"
		"toolQuery":  {"intent": true},        // pre-rename alias for "task"
		"toolRead":   {"path": true},          // pre-rename alias for "file"
	}

	for name, fn := range funcs {
		toolName, ok := dispatch[name]
		if !ok {
			continue // helper, not a top-level tool handler
		}
		// Only tools actually advertised to a real client (ToolSchemas())
		// are checked. CLI-only tools fall through toolSchema()'s default
		// case (additionalProperties:true, no declared keys) and would
		// false-positive on every arg they read — that schema is honest
		// about being unchecked, not a bug.
		if !advertised[toolName] {
			continue
		}
		schema := toolSchema(toolName)
		props, _ := schema["properties"].(map[string]any)
		for _, m := range argRe.FindAllStringSubmatch(fn, -1) {
			key := m[1]
			if _, declared := props[key]; declared {
				continue
			}
			if allow[name][key] {
				continue
			}
			t.Errorf("%s (tool %q) reads arg %q via *Arg(), but toolSchema(%q) "+
				"does not declare it in properties — a real MCP client can never "+
				"send this key, so the code is dead from every real caller. "+
				"Either add it to the schema or remove the read.",
				name, toolName, key, toolName)
		}
	}
}

// extractFuncBodies returns "func (h *Handler) toolXxx(...) {...}" bodies
// keyed by function name, using brace counting from the opening "{" — good
// enough for this file's style (no braces inside string literals that this
// package's *Arg-reading functions actually contain).
func extractFuncBodies(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	sig := regexp.MustCompile(`func \(h \*Handler\) (tool[A-Za-z]+)\(`)
	for _, loc := range sig.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		open := strings.IndexByte(src[loc[1]:], '{')
		if open < 0 {
			continue
		}
		start := loc[1] + open
		depth := 0
		end := -1
		for i := start; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i + 1
				}
			}
			if end != -1 {
				break
			}
		}
		if end == -1 {
			continue
		}
		out[name] = src[start:end]
	}
	return out
}

// extractDispatchMap reads `case "prism_x": return h.toolY(...)` (and the
// bare `return h.toolY(ctx, args)` one-liner form) from Invoke, mapping
// funcName -> advertised tool name.
func extractDispatchMap(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	caseRe := regexp.MustCompile(`case "(prism_[a-z_]+)":\s*\n\s*return h\.(tool[A-Za-z]+)\(`)
	for _, m := range caseRe.FindAllStringSubmatch(src, -1) {
		out[m[2]] = m[1]
	}
	return out
}

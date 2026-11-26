package core

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

// --- helpers -------------------------------------------------------------

func categorize(s grove.SymbolRecord) ranking.Category {
	// Tests usually live in language-specific test file patterns.
	p := strings.ToLower(s.FilePath)
	if isTestFilePath(p) || strings.HasSuffix(p, "test.java") || strings.HasSuffix(p, "tests.java") ||
		strings.HasSuffix(p, "_test.rs") || strings.HasSuffix(p, "tests.rs") ||
		strings.HasSuffix(p, "_test.c") || strings.HasSuffix(p, "_test.h") ||
		strings.HasSuffix(p, "_test.cc") || strings.HasSuffix(p, "_test.cpp") ||
		strings.HasSuffix(p, "test.cs") || strings.HasSuffix(p, "tests.cs") ||
		strings.HasSuffix(p, "test.php") || strings.HasSuffix(p, "tests.php") {
		return ranking.CategoryTest
	}
	if s.Kind == "namespace" || strings.HasSuffix(p, ".md") {
		return ranking.CategoryDoc
	}
	if s.Docstring != "" && s.Signature == "" {
		return ranking.CategoryDoc
	}
	// Consts whose value is a large multi-line string containing markdown
	// markers (e.g. steeringInstructions) are documentation, not code.
	if s.Kind == "const" && isMarkdownStringConst(s.RawText) {
		return ranking.CategoryDoc
	}
	return ranking.CategoryDependency
}

// isMarkdownStringConst reports whether raw is a const declaration whose
// value is a multi-line string with 3+ markdown structural markers.
func isMarkdownStringConst(raw string) bool {
	if strings.Count(raw, "\n") < 5 {
		return false
	}
	markers := 0
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "##") || strings.HasPrefix(l, "|---") ||
			strings.HasPrefix(l, "| ---") || (strings.HasPrefix(l, "- ") && len(l) > 4) {
			markers++
		}
	}
	return markers >= 3
}

func filterDocSeeds(in []grove.SymbolRecord) []grove.SymbolRecord {
	out := in[:0]
	for _, s := range in {
		if categorize(s) != ranking.CategoryDoc {
			out = append(out, s)
		}
	}
	return out
}

func filterGeneratedPrismContext(in []grove.SymbolRecord) []grove.SymbolRecord {
	out := in[:0]
	for _, sym := range in {
		if isGeneratedPrismContext(sym) {
			continue
		}
		out = append(out, sym)
	}
	return out
}

func isGeneratedPrismContext(sym grove.SymbolRecord) bool {
	p := strings.TrimPrefix(filepath.ToSlash(sym.FilePath), "./")
	switch p {
	case ".mcp.json",
		".cursor/mcp.json",
		".windsurf/mcp.json",
		".vscode/mcp.json",
		".kiro/settings/mcp.json",
		"prism.yaml":
		return true
	case "CLAUDE.md",
		"AGENTS.md",
		"GEMINI.md",
		".cursorrules",
		".windsurfrules",
		".clinerules",
		".amp/instructions.md",
		".devin/instructions.md",
		".github/copilot-instructions.md",
		".kiro/steering/prism.md",
		".kiro/steering/provasign.md":
		text := sym.RawText
		if text == "" {
			text = sym.Docstring
		}
		return strings.Contains(text, "## Prism — context delivery")
	}
	return false
}

func stringArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

// stringsArg reads a value that may be a single string OR an array of them,
// returning the non-empty entries in order with duplicates dropped.
//
// This is what lets prism_search take several terms in one call. Measured on
// the 190-cell A/B: 490 of 900 search calls (54%) sat in back-to-back runs of
// 2-10, each a separate turn whose result is then re-read on every later turn.
// One call with N terms is one turn and one result.
func stringsArg(args map[string]any, key string) []string {
	var raw []any
	switch v := args[key].(type) {
	case string:
		raw = []any{v}
	case []string:
		for _, s := range v {
			raw = append(raw, s)
		}
	case []any:
		raw = v
	default:
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return def
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return cutUTF8(s, max) + "…"
}

func safePathWithinRoot(root, p string) (abs string, sessionPath string, err error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}
	// Resolve symlinks so /tmp and /private/tmp compare equal on macOS.
	if resolved, e := filepath.EvalSymlinks(rootAbs); e == nil {
		rootAbs = resolved
	}
	rootAbs = filepath.Clean(rootAbs)

	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
	} else {
		candidate = filepath.Clean(filepath.Join(rootAbs, p))
	}
	// Resolve symlinks on the FINAL joined path, not just absolute inputs —
	// a relative path like "leak.go" that is itself a symlink to outside
	// root joined to a containment check on the unresolved join, then
	// os.ReadFile followed the link at read time, serving the external
	// file's content under the in-repo name. EvalSymlinks fails harmlessly
	// for a not-yet-existing path; the unresolved candidate falls through
	// to the containment check below and then to a normal "not found" at
	// the read site, so this changes no behavior for missing files.
	if resolved, e := filepath.EvalSymlinks(candidate); e == nil {
		candidate = resolved
	}

	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q is outside workspace root %s; for another repository run the prism CLI there or use its own prism MCP server", p, rootAbs)
	}

	return candidate, normalizePath(rel), nil
}

// safeReadPath resolves a path a read may open: inside the primary root, or
// inside a repository linked to it in the workspace. A file in a linked
// repository keeps its path relative to the primary root ("../grove/x.go"),
// the form the agent's own file tools accept. Each root applies the same
// symlink containment as the primary; anything else is rejected.
func (h *Handler) safeReadPath(p string) (abs string, sessionPath string, err error) {
	abs, sessionPath, err = safePathWithinRoot(h.Root, p)
	if err == nil || h.Workspace == nil {
		return abs, sessionPath, err
	}
	candidate := p
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(h.Workspace.Primary, filepath.FromSlash(p))
	}
	for _, r := range h.Workspace.Linked() {
		if a, _, e := safePathWithinRoot(r.Path, candidate); e == nil {
			return a, h.Workspace.RelPath(a), nil
		}
	}
	for _, r := range h.Workspace.Roots {
		if why := r.Unusable(); why != "" && (candidate == r.Path || strings.HasPrefix(candidate, r.Path+string(filepath.Separator))) {
			return "", "", fmt.Errorf("path %q is in linked repository %s (%s), which cannot be read: %s", p, r.ID, h.Workspace.DisplayPath(r), why)
		}
	}
	return "", "", fmt.Errorf("%w; linked repositories: %s", err, h.Workspace.Describe())
}

func normalizePath(p string) string {
	p = filepath.Clean(p)
	return filepath.ToSlash(p)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SourceLineSep separates a delivered line's number from its source text.
// It was a tab, Read-style. On a tab-indented file the delimiter tab reads
// as indentation, and agents put the wrong number of tabs into an Edit
// old_string: ~20 turns of od -c archaeology in ddtb4dv8 (2026-09-03), and
// 5 failed edits on chi tree.go in both arms 2026-10-02..09 even with a note
// explaining the delimiter. A visible separator leaves every whitespace
// character after it as source, so no note is needed.
const SourceLineSep = "→"

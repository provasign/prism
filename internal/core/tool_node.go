package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

func (h *Handler) toolEdges(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	direction := stringArg(args, "direction", "both")
	var kinds []string
	if raw, ok := args["kinds"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					kinds = append(kinds, s)
				}
			}
		}
	}
	edges, err := h.Grove.Edges(ctx, name, direction, kinds)
	if err != nil {
		return nil, err
	}
	// Group by "<edgeType> <direction>" (e.g. "calls out") and cap each group,
	// reporting the true total so a hot symbol can't dump 300 callers.
	const cap = 50
	type group struct {
		shown []map[string]any
		total int
	}
	groups := map[string]*group{}
	order := []string{}
	for _, e := range edges {
		key := e.EdgeType + " " + e.Direction
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		g.total++
		if len(g.shown) < cap {
			entry := map[string]any{"name": e.Name, "file": e.File, "line": e.Line, "kind": e.Kind}
			if e.TestDouble {
				entry["testDouble"] = true
				entry["testLabel"] = e.TestLabel
			}
			g.shown = append(g.shown, entry)
		}
	}
	rel := map[string]any{}
	for _, key := range order {
		g := groups[key]
		m := map[string]any{"shown": len(g.shown), "total": g.total, "symbols": g.shown}
		rel[key] = m
	}
	out := map[string]any{"name": name, "direction": direction, "edges": rel}
	if len(rel) == 0 {
		// An unknown name and a symbol with no neighbours both had empty
		// edges; say which (CON-04: the CLI exits 4 on an unknown name).
		if cands, err := h.Grove.Resolve(ctx, name); err == nil && len(cands) == 0 {
			out["matched"] = false
			out["note"] = fmt.Sprintf("no symbol named %q in the index", name)
		}
	}
	return out, nil
}

// dedupeSymbolsByID drops duplicate symbols (same ID) while preserving order,
// used after merging two symbol searches into one candidate pool.
func dedupeSymbolsByID(syms []grove.SymbolRecord) []grove.SymbolRecord {
	seen := make(map[string]bool, len(syms))
	out := syms[:0]
	for _, s := range syms {
		if s.ID != "" && seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out
}

// toolNode is the one-shot orientation view: everything you need about ONE
// symbol (or ONE file) without a chain of round-trips. Composed entirely from
// existing primitives — lookup + edges for a symbol, read + file symbols +
// dependents for a file. No new graph machinery, no stored state.
//
// The argument is a symbol name OR a repo-relative file path; a path that the
// index knows takes the file branch.
func (h *Handler) toolNode(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", stringArg(args, "symbol", stringArg(args, "file", "")))
	if name == "" {
		return nil, errors.New("name is required (a symbol name or a repo-relative file path)")
	}
	// File branch: when the index knows this path, OR the path exists on
	// disk inside the root. Requiring indexed SYMBOLS meant every file the
	// extractor produces none for — configs, markdown, data fixtures,
	// sources in an unsupported language — fell through to the symbol
	// branch and came back "no symbol or indexed file named X" for a file
	// sitting right there. Its content and dependents are exactly what the
	// file view is for, and neither needs the file to define anything.
	if syms, err := h.Grove.FileSymbols(ctx, name); err == nil && len(syms) > 0 {
		return h.nodeFile(ctx, name, syms)
	}
	if h.fileExists(name) {
		return h.nodeFile(ctx, name, nil)
	}
	// CON-10: path:line names the symbol declared there.
	probe := map[string]any{"name": name}
	if ok, err := h.resolvePathLine(ctx, probe, "name"); ok {
		if err != nil {
			return nil, err
		}
		name = probe["name"].(string)
		args["file"] = probe["file"]
	}
	return h.nodeSymbol(ctx, name, args)
}

// fileExists reports whether name is a regular file inside the root. Used to
// decide the node file branch, so it must refuse paths that escape the root:
// prism_node is a read tool, and a read tool that will open ../../.ssh/id_rsa
// on request is an exfiltration primitive.
func (h *Handler) fileExists(name string) bool {
	if name == "" || filepath.IsAbs(name) {
		return false
	}
	root := filepath.Clean(h.Root)
	full := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(full)
	return err == nil && fi.Mode().IsRegular()
}

// nodeSymbol renders a symbol's source plus its immediate graph neighbours.
func (h *Handler) nodeSymbol(ctx context.Context, name string, args map[string]any) (any, error) {
	lookupArgs := map[string]any{"name": name}
	if f, ok := args["file"]; ok {
		lookupArgs["file"] = f
	}
	looked, err := h.toolLookup(ctx, lookupArgs)
	if err != nil {
		return nil, err
	}
	out, _ := looked.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	// Ambiguous or unmatched: relay lookup's candidate list untouched rather
	// than guessing which symbol the caller meant.
	if amb, _ := out["ambiguous"].(bool); amb {
		return out, nil
	}
	if matched, present := out["matched"].(bool); present && !matched {
		return out, nil
	}
	// Nothing resolved at all: say so instead of returning an empty shell the
	// caller has to interpret.
	if sym, present := out["symbol"]; !present || sym == nil {
		return map[string]any{
			"view":    "symbol",
			"name":    name,
			"matched": false,
			"note": fmt.Sprintf("no symbol or indexed file named %q — try prism_search for a name fragment, "+
				"or pass a repo-relative file path for the file view", name),
		}, nil
	}
	out["view"] = "symbol"

	// Neighbours in both directions — the "where do I go next" menu. Feed
	// edges the RESOLVED symbol's bare name: lookup accepts qualified names
	// ("Render.Render") but edges resolves bare ones ("Render"), so passing
	// the caller's string through would silently return nothing.
	edgeName := name
	if sym, ok := out["symbol"].(grove.SymbolRecord); ok && sym.Name != "" {
		edgeName = sym.Name
	} else if sm, ok := out["symbol"].(map[string]any); ok {
		if n, _ := sm["name"].(string); n != "" {
			edgeName = n
		}
	}
	// Orientation wants the relationship kinds, not the structural ones
	// (contains/defines/imports would bury the signal).
	kinds := []any{"calls", "implements", "extends", "overrides", "uses-type"}
	if edges, err := h.toolEdges(ctx, map[string]any{
		"name": edgeName, "direction": "both", "kinds": kinds,
	}); err == nil {
		if em, ok := edges.(map[string]any); ok {
			out["edges"] = em["edges"]
		}
	}
	h.Ledger.RecordCall("prism_node")
	return out, nil
}

// nodeFile renders a file's source, the symbols it defines, and the files that
// depend on it.
func (h *Handler) nodeFile(ctx context.Context, path string, syms []grove.SymbolRecord) (any, error) {
	out := map[string]any{"view": "file", "file": path}

	read, err := h.toolRead(ctx, map[string]any{"file": path})
	if err != nil {
		return nil, err
	}
	if rm, ok := read.(map[string]any); ok {
		for _, k := range []string{"content", "strategy", "deliveredTokens", "originalTokens", "savingsPercent"} {
			if v, ok := rm[k]; ok {
				out[k] = v
			}
		}
	}

	defined := make([]map[string]any, 0, len(syms))
	for _, s := range syms {
		defined = append(defined, map[string]any{
			"name": displayQN(s), "kind": s.Kind, "line": s.Span.Start,
		})
	}
	out["defines"] = defined

	// Dependents: edges whose TO side is a symbol in this file, grouped by the
	// depending file. Deps returns every edge touching the path in either
	// direction, so the To-side filter is what isolates "who needs me".
	if edges, err := h.Grove.Deps(ctx, path); err == nil {
		prefix := path + "::"
		seen := map[string]bool{}
		dependents := []string{}
		for _, e := range edges {
			if !strings.HasPrefix(e.To, prefix) {
				continue
			}
			from := e.From
			if i := strings.Index(from, "::"); i >= 0 {
				from = from[:i]
			}
			from = strings.TrimPrefix(from, "file:")
			if from == "" || from == path || seen[from] {
				continue
			}
			seen[from] = true
			dependents = append(dependents, from)
		}
		sort.Strings(dependents)
		out["dependents"] = dependents
		out["dependentCount"] = len(dependents)
	}
	h.Ledger.RecordCall("prism_node")
	return out, nil
}

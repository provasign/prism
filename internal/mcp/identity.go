package mcp

// Graph expansion by symbol identity (audit 2026-10-20 #3).
//
// Query expansion used to ask Grove for a selected symbol's neighbors by its
// qualified name. Qualified names are not unique across files or packages:
// left.Target and right.Target both qualify as "Target", so the anchor line
// for left.Target counted right/a.go's caller as its own, and that
// namesake's callers and tests joined the candidate set. A symbol the
// selector already resolved is expanded by its IDs; names are for display.
// Explicit name-based requests (op=edges name=..., search) stay broad.

import (
	"context"
	"sort"

	"github.com/provasign/prism/internal/grove"
)

// identityIDs returns the graph IDs that stand for sym: sym itself plus
// declarations in the SAME file with the same qualified name (overloads of
// one method share a qualified name and a caller list), never namesakes in
// other files. nil means sym carries no ID and the caller must fall back to
// its name.
func (h *Handler) identityIDs(ctx context.Context, sym grove.SymbolRecord) []string {
	if sym.ID == "" {
		return nil
	}
	ids := []string{sym.ID}
	if sym.QualifiedName == "" || sym.FilePath == "" {
		return ids
	}
	same, err := h.Grove.FileSymbols(ctx, sym.FilePath)
	if err != nil {
		return ids
	}
	for _, s := range same {
		if s.ID != sym.ID && s.ID != "" && s.QualifiedName == sym.QualifiedName && s.Kind == sym.Kind {
			ids = append(ids, s.ID)
		}
	}
	sort.Strings(ids[1:])
	return ids
}

// symbolQueries is what to pass to Grove's name-or-ID neighbor queries for
// sym: its identity IDs, or its display name when it has no ID.
func (h *Handler) symbolQueries(ctx context.Context, sym grove.SymbolRecord) []string {
	if ids := h.identityIDs(ctx, sym); len(ids) > 0 {
		return ids
	}
	q := sym.QualifiedName
	if q == "" {
		q = sym.Name
	}
	if q == "" {
		return nil
	}
	return []string{q}
}

// identityCallNeighbors is Grove.CallNeighbors for exactly sym.
func (h *Handler) identityCallNeighbors(ctx context.Context, sym grove.SymbolRecord) ([]grove.SymbolRecord, error) {
	return h.unionSymbols(ctx, sym, h.Grove.CallNeighbors)
}

// identityInboundCallers is Grove.InboundCallers for exactly sym.
func (h *Handler) identityInboundCallers(ctx context.Context, sym grove.SymbolRecord) ([]grove.SymbolRecord, error) {
	return h.unionSymbols(ctx, sym, h.Grove.InboundCallers)
}

func (h *Handler) unionSymbols(ctx context.Context, sym grove.SymbolRecord, fetch func(context.Context, string) ([]grove.SymbolRecord, error)) ([]grove.SymbolRecord, error) {
	var out []grove.SymbolRecord
	seen := map[string]bool{}
	var firstErr error
	ok := false
	for _, q := range h.symbolQueries(ctx, sym) {
		got, err := fetch(ctx, q)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok = true
		for _, s := range got {
			key := s.ID
			if key == "" {
				key = s.FilePath + "|" + s.QualifiedName
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, s)
		}
	}
	if !ok && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// identityEdges is Grove.Edges for exactly sym.
func (h *Handler) identityEdges(ctx context.Context, sym grove.SymbolRecord, direction string, kinds []string) ([]grove.EdgeRecord, error) {
	var out []grove.EdgeRecord
	seen := map[grove.EdgeRecord]bool{}
	var firstErr error
	ok := false
	for _, q := range h.symbolQueries(ctx, sym) {
		got, err := h.Grove.Edges(ctx, q, direction, kinds)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok = true
		for _, e := range got {
			k := e
			k.Confidence = 0
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, e)
		}
	}
	if !ok && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

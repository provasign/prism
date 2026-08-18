package mcp

// Hit rollup: the graph's organization of a big grep.
//
// Sized on full38 (2026-08-18, 79 replayed prism_search calls): 27% exceed
// the 25-hit cap; the tail runs to 443 hits ("hints" in magic-wormhole).
// Today those calls deliver an arbitrary 25-line sample plus a warning to
// narrow — the agent's next move is another search, another turn. The graph
// already knows the structure of the WHOLE hit set: which symbols enclose
// the matches. So when a text search truncates, re-run it uncapped purely
// for aggregation and deliver "all N hits, grouped by enclosing symbol"
// alongside the sample — 372 hits become a dozen lines, and the narrowing
// decision happens in THIS turn instead of the next one.
//
// Best-effort, additive evidence: any failure (no graph, re-search timeout,
// probe errors) silently yields no rollup — never a broken search result.

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

const (
	// rollupMaxHits bounds the uncapped aggregation pass. 2000 covers every
	// observed real search (max 443) with an order of magnitude to spare.
	rollupMaxHits = 2000
	// rollupTimeout keeps the second search pass interactive.
	rollupTimeout = 3 * time.Second
	// rollupFileCap bounds FileSymbols probes, like textMapFileCap but wider:
	// this pass exists precisely because hits span many files.
	rollupFileCap = 20
	// rollupSymbolCap bounds delivered rollup lines.
	rollupSymbolCap = 10
)

// hitRollup re-runs a truncated text search uncapped and groups every hit by
// its innermost enclosing indexed symbol. Returns nil when there is nothing
// useful to say (no graph, search failed, or everything landed outside
// indexed symbols AND in few files).
func (h *Handler) hitRollup(ctx context.Context, term string, sc searchScope, regex bool) []map[string]any {
	if h.Grove == nil {
		return nil
	}
	r := textsearch.Search(ctx, h.Root, term, textsearch.Options{
		MaxHits: rollupMaxHits, Timeout: rollupTimeout, Regex: regex,
		Paths: sc.paths, Glob: sc.glob, Exhaustive: true,
	})
	if len(r.Hits) == 0 {
		return nil
	}

	// Group hits by file, probe FileSymbols per distinct file (bounded, most
	// hits first so the cap spends its probes where the mass is).
	byFile := map[string][]int{} // file -> hit lines
	var order []string
	for _, hit := range r.Hits {
		if _, ok := byFile[hit.File]; !ok {
			order = append(order, hit.File)
		}
		byFile[hit.File] = append(byFile[hit.File], hit.Line)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return len(byFile[order[i]]) > len(byFile[order[j]])
	})

	type symEntry struct {
		sym  grove.SymbolRecord
		hits int
	}
	symCounts := map[string]*symEntry{}
	outside := 0     // hits in probed files, not inside any symbol
	unprobed := 0    // hits in files beyond the probe cap
	probed := 0
	for _, file := range order {
		lines := byFile[file]
		if probed >= rollupFileCap {
			unprobed += len(lines)
			continue
		}
		probed++
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			unprobed += len(lines)
			continue
		}
		for _, line := range lines {
			var innermost *grove.SymbolRecord
			for i, s := range syms {
				if s.Span.Start <= line && line <= s.Span.End {
					if innermost == nil ||
						(s.Span.End-s.Span.Start) < (innermost.Span.End-innermost.Span.Start) {
						innermost = &syms[i]
					}
				}
			}
			if innermost == nil {
				outside++
				continue
			}
			e, ok := symCounts[innermost.ID]
			if !ok {
				e = &symEntry{sym: *innermost}
				symCounts[innermost.ID] = e
			}
			e.hits++
		}
	}
	if len(symCounts) == 0 {
		return nil // structure adds nothing — all hits outside the graph
	}

	entries := make([]*symEntry, 0, len(symCounts))
	for _, e := range symCounts {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].hits > entries[j].hits })

	rollup := make([]map[string]any, 0, rollupSymbolCap+1)
	for i, e := range entries {
		if i == rollupSymbolCap {
			rest := 0
			for _, x := range entries[rollupSymbolCap:] {
				rest += x.hits
			}
			rollup = append(rollup, map[string]any{
				"note": pluralNote(len(entries)-rollupSymbolCap, rest)})
			break
		}
		name := e.sym.QualifiedName
		if name == "" {
			name = e.sym.Name
		}
		rollup = append(rollup, map[string]any{
			"symbol": name, "file": e.sym.FilePath,
			"span": map[string]any{"start": e.sym.Span.Start, "end": e.sym.Span.End},
			"hits": e.hits,
		})
	}
	if outside > 0 || unprobed > 0 {
		n := outside + unprobed
		rollup = append(rollup, map[string]any{
			"note": strconv.Itoa(n) + " hit(s) outside indexed symbols (comments, docs, config) or in files past the probe cap"})
	}
	return rollup
}

func pluralNote(nsyms, nhits int) string {
	return "+" + strconv.Itoa(nsyms) + " more symbols with " + strconv.Itoa(nhits) + " hit(s)"
}

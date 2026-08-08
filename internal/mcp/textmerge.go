package mcp

// Text-search merge: prism_query and prism_search combine the graph's
// symbol retrieval with a real full-text search (rg / grep / native — see
// internal/textsearch). The design goal is that an agent never needs a
// separate grep tool: anything grep would have found is in this response.
//
// Merge semantics, in evidence order:
//   - a text hit INSIDE a symbol the graph already seeded confirms that
//     seed (two independent signals) and promotes it to the front;
//   - a text hit inside an indexed symbol the graph did NOT seed adds that
//     symbol as a seed (text-promoted — it enters graph expansion like any
//     other seed);
//   - a text hit in no indexed symbol (comment, config, docs, string
//     literal) is exactly what the graph structurally cannot see — it is
//     delivered raw as a textMatches entry.
//
// Re-delivery discipline: hits in a file whose full content was already
// delivered this session (same SHA) are listed as file:line only — the
// compressor's cache-pointer contract extended to text hits.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

const (
	// textSearchTimeout bounds the inline search so prism_query latency
	// stays interactive even on the native fallback backend.
	textSearchTimeout = 3 * time.Second
	// textHitsPerTerm caps raw hits collected per search term.
	textHitsPerTerm = 40
	// textMapFileCap bounds how many distinct hit files get a FileSymbols
	// probe when mapping hits onto enclosing symbols.
	textMapFileCap = 12
	// textRenderFileCap / textRenderHitsPerFile bound the delivered
	// textMatches section so it cannot crowd out the source windows.
	textRenderFileCap     = 10
	textRenderHitsPerFile = 5
)

// textMergeResult is what mergeTextSearch feeds back into selection.
type textMergeResult struct {
	extraSeeds []grove.SymbolRecord // text-promoted symbols, not already seeded
	confirmed  map[string]bool      // seed IDs confirmed by a text hit
	rawHits    []textsearch.Hit     // hits no indexed symbol encloses
	backend    string
}

// mergeTextSearch runs the full-text search for each term and splits the
// hits into symbol promotions and raw deliverable hits. Never fails: text
// search is additive evidence, and an error here must not break retrieval.
func (h *Handler) mergeTextSearch(ctx context.Context, terms []string, seededIDs map[string]bool) textMergeResult {
	res := textMergeResult{confirmed: map[string]bool{}}
	var hits []textsearch.Hit
	seenHit := map[string]bool{}
	for _, term := range terms {
		r := textsearch.Search(ctx, h.Root, term, textsearch.Options{
			MaxHits: textHitsPerTerm,
			Timeout: textSearchTimeout,
		})
		res.backend = r.Backend
		for _, hit := range r.Hits {
			key := hit.File + ":" + strconv.Itoa(hit.Line)
			if !seenHit[key] {
				seenHit[key] = true
				hits = append(hits, hit)
			}
		}
	}
	if len(hits) == 0 {
		return res
	}

	// Map hits onto enclosing indexed symbols, one FileSymbols probe per
	// distinct file, bounded. Files beyond the cap (or with no Grove) keep
	// their hits raw — under-promotion is safe, dropping hits is not.
	fileSyms := map[string][]grove.SymbolRecord{}
	probed := 0
	symsFor := func(file string) []grove.SymbolRecord {
		if syms, ok := fileSyms[file]; ok {
			return syms
		}
		if h.Grove == nil || probed >= textMapFileCap {
			return nil
		}
		probed++
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			syms = nil
		}
		fileSyms[file] = syms
		return syms
	}

	promotedIDs := map[string]bool{}
	for _, hit := range hits {
		var enclosing *grove.SymbolRecord
		for i, s := range symsFor(hit.File) {
			if s.Span.Start <= hit.Line && hit.Line <= s.Span.End {
				sym := symsFor(hit.File)[i]
				// Prefer the innermost span: a method inside a class both
				// enclose the line; the tighter one is the real anchor.
				if enclosing == nil || (sym.Span.End-sym.Span.Start) < (enclosing.Span.End-enclosing.Span.Start) {
					enclosing = &sym
				}
			}
		}
		switch {
		case enclosing == nil:
			res.rawHits = append(res.rawHits, hit)
		case seededIDs[enclosing.ID]:
			res.confirmed[enclosing.ID] = true
		case !promotedIDs[enclosing.ID]:
			promotedIDs[enclosing.ID] = true
			res.extraSeeds = append(res.extraSeeds, *enclosing)
		}
	}
	return res
}

// renderTextMatches renders raw hits for delivery, grouped by file, with the
// session-SHA cache discipline: a file already delivered unchanged this
// session gets line numbers only (the agent has the content), never a
// re-send of its text.
func (h *Handler) renderTextMatches(rawHits []textsearch.Hit) []map[string]any {
	if len(rawHits) == 0 {
		return nil
	}
	type group struct {
		file string
		hits []textsearch.Hit
	}
	var order []string
	byFile := map[string]*group{}
	for _, hit := range rawHits {
		g, ok := byFile[hit.File]
		if !ok {
			g = &group{file: hit.File}
			byFile[hit.File] = g
			order = append(order, hit.File)
		}
		g.hits = append(g.hits, hit)
	}

	out := make([]map[string]any, 0, minInt(len(order), textRenderFileCap))
	for i, file := range order {
		if i >= textRenderFileCap {
			out = append(out, map[string]any{
				"note": strconv.Itoa(len(order)-textRenderFileCap) + " more files with matches omitted — narrow the term or grep-style search that file directly",
			})
			break
		}
		g := byFile[file]
		entry := map[string]any{"file": file}
		if h.textFileCached(file) {
			var lines []int
			for _, hit := range g.hits {
				lines = append(lines, hit.Line)
			}
			entry["lines"] = lines
			entry["cached"] = true
			entry["note"] = "content already delivered this session (unchanged) — matches listed by line only"
		} else {
			shown := make([]map[string]any, 0, minInt(len(g.hits), textRenderHitsPerFile))
			for j, hit := range g.hits {
				if j >= textRenderHitsPerFile {
					entry["moreHits"] = len(g.hits) - textRenderHitsPerFile
					break
				}
				shown = append(shown, map[string]any{"line": hit.Line, "text": hit.Text})
			}
			entry["hits"] = shown
		}
		out = append(out, entry)
	}
	return out
}

// textFileCached reports whether this file's CURRENT content was already
// delivered in full this session — the same check the compressor makes
// before emitting a SHA pointer.
func (h *Handler) textFileCached(file string) bool {
	if h.Session == nil {
		return false
	}
	content, err := os.ReadFile(filepath.Join(h.Root, filepath.FromSlash(file)))
	if err != nil || len(content) > 2<<20 {
		return false
	}
	_, seen, sameHash := h.Session.Lookup(normalizePath(file), compression.Hash(string(content)))
	return seen && sameHash
}

// resolvedRefNote tells the agent how much of a raw text search is signal.
// Measured (research/harness/grep_vs_graph_gap.py, 127 symbols / 6 repos /
// 4 languages): a whole-word grep for a symbol name returns ~30% lines that
// are NOT resolved references — 18% in jackson, 50% in gin, 98% in one
// typeorm case (372 hits, 1 real). The graph does NOT find lines grep
// misses (that set was empty, 0/127); its contribution is knowing which
// hits matter. So the honest thing to volunteer is the ratio, not a claim
// of hidden sites.
//
// Silent unless the query names exactly one indexed symbol AND the hit
// count materially exceeds its resolved references — no note when grep was
// already precise, so this cannot become wallpaper.
func (h *Handler) resolvedRefNote(ctx context.Context, query string, hits []textsearch.Hit) string {
	if h.Grove == nil || len(hits) < 10 {
		return ""
	}
	cands, err := h.Grove.Resolve(ctx, query)
	if err != nil || len(cands) == 0 {
		return "" // unknown name: no honest statement to make
	}
	// Several symbols share this name — that ambiguity IS the noise, and it
	// is the high-noise case (typeorm createQueryBuilder: 372 hits, 8
	// same-named symbols, 1 real reference to the one meant). Naming them
	// is more useful than a count.
	if len(cands) > 1 {
		names := make([]string, 0, 3)
		for _, c := range cands {
			if c.Name != "" && !c.TestDouble {
				names = append(names, c.Name)
			}
			if len(names) == 3 {
				break
			}
		}
		if len(names) < 2 {
			return ""
		}
		return fmt.Sprintf(
			"%d text hits, but %d different symbols share the name %q (%s%s) — most hits "+
				"belong to the ones you did not mean. prism_references <qualified.Name> "+
				"for just the real references to one of them.",
			len(hits), len(cands), query, strings.Join(names, ", "),
			map[bool]string{true: ", …", false: ""}[len(cands) > 3])
	}
	qn := cands[0].Name
	refs, err := h.Grove.References(ctx, qn)
	if err != nil {
		return ""
	}
	n := len(refs.Refs)
	if n == 0 || n*2 > len(hits) {
		return "" // grep was already precise enough to leave alone
	}
	return fmt.Sprintf(
		"%d text hits, but only %d are resolved references to %s — the rest match the "+
			"name without referring to it (other types, comments, strings). "+
			"prism_references %s for just the real ones.",
		len(hits), n, qn, qn)
}

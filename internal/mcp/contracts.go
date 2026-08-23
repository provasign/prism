package mcp

// Proactive contract delivery: the fan-out an agent is about to design
// around, delivered on the calls it already makes.
//
// Evidence (full38 + v0557 failure dossiers): on every graph-addressable
// failed cell the agent VISITED the contract — read the declaring file
// (a2a-414: jsonrpc_app.py; atlassian-581: config.py, the gold file itself)
// or grepped into the contract's body (Kinto: `cache.get(` hits inside
// CacheBase.get) — and then designed a fix as if the symbol were local.
// change_impact was available and called once in 76 cells: the graph
// question is never ASKED at design time, so the answer must ride along
// on reads and searches. Two prongs:
//
//   1. prism_read: a CONTRACTS trailer naming the file's fan-out symbols
//      and their obligation sets (capped, session-deduped per file).
//   2. text search: hits enclosed by a fan-out symbol carry the same
//      contract line for that symbol.
//
// Same delivery ethics as every note: silent without a graph, silent when
// there is no fan-out worth naming, hard caps so it cannot become wallpaper.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

const (
	// contractMinCallers: a symbol is contract-shaped when it has any
	// override/implementation family, or at least this many callers.
	contractMinCallers = 3
	// contractsPerFile caps the read trailer.
	contractsPerFile = 3
	// contractSiteCap caps sites shown per relation.
	contractSiteCap = 3
)

// contractLine renders one symbol's obligation line, or "" when the symbol
// has no fan-out worth naming. Cached by symbol ID on the Handler — one
// session, one cache (a package global would leak across kit sessions and
// tests).
func (h *Handler) contractLine(ctx context.Context, sym grove.SymbolRecord) string {
	h.contractMu.Lock()
	if h.contractLines == nil {
		h.contractLines = map[string]string{}
		h.contractFiles = map[string]bool{}
	}
	if l, ok := h.contractLines[sym.ID]; ok {
		h.contractMu.Unlock()
		return l
	}
	h.contractMu.Unlock()
	line := h.buildContractLine(ctx, sym)
	h.contractMu.Lock()
	h.contractLines[sym.ID] = line
	h.contractMu.Unlock()
	return line
}

func (h *Handler) buildContractLine(ctx context.Context, sym grove.SymbolRecord) string {
	if h.Grove == nil {
		return ""
	}
	name := sym.QualifiedName
	if name == "" {
		name = sym.Name
	}
	r, err := h.Grove.ChangeImpact(ctx, name)
	if err != nil || r == nil {
		return ""
	}
	if len(r.Family) == 0 && len(r.Callers) < contractMinCallers {
		return ""
	}
	site := func(s grove.SymbolRecord) string {
		parts := strings.Split(s.FilePath, "/")
		p := s.FilePath
		if len(parts) > 2 {
			p = strings.Join(parts[len(parts)-2:], "/")
		}
		return fmt.Sprintf("%s:%d", p, s.Span.Start)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s", name)
	if n := len(r.Family); n > 0 {
		fmt.Fprintf(&b, " — %d implementation(s):", n)
		for i, s := range r.Family {
			if i == contractSiteCap {
				fmt.Fprintf(&b, " +%d more", n-contractSiteCap)
				break
			}
			fmt.Fprintf(&b, " %s", site(s))
		}
	}
	if n := len(r.Callers); n > 0 {
		fmt.Fprintf(&b, "; %d caller(s)", n)
		if len(r.Family) == 0 {
			b.WriteString(":")
			for i, s := range r.Callers {
				if i == contractSiteCap {
					fmt.Fprintf(&b, " +%d more", n-contractSiteCap)
					break
				}
				fmt.Fprintf(&b, " %s", site(s))
			}
		}
	}
	b.WriteString(". Changing its signature or behavior obligates those sites (prism_change_impact for the closed set).")
	return b.String()
}

// fileContractsTrailer renders the CONTRACTS block for a whole-file read:
// the file's most contract-shaped symbols, once per file per session.
func (h *Handler) fileContractsTrailer(ctx context.Context, relPath string) string {
	if h.Grove == nil {
		return ""
	}
	h.contractMu.Lock()
	if h.contractFiles == nil {
		h.contractLines = map[string]string{}
		h.contractFiles = map[string]bool{}
	}
	seen := h.contractFiles[relPath]
	h.contractFiles[relPath] = true
	h.contractMu.Unlock()
	if seen {
		return ""
	}
	syms, err := h.Grove.FileSymbols(ctx, relPath)
	if err != nil || len(syms) == 0 {
		return ""
	}
	// Prefer wider spans first (types/interfaces before helpers) but keep
	// deterministic order.
	sort.SliceStable(syms, func(i, j int) bool {
		return (syms[i].Span.End - syms[i].Span.Start) > (syms[j].Span.End - syms[j].Span.Start)
	})
	var lines []string
	for _, s := range syms {
		if len(lines) == contractsPerFile {
			break
		}
		if l := h.contractLine(ctx, s); l != "" {
			lines = append(lines, "//   "+l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "// CONTRACTS IN THIS FILE (fan-out you are editing against):\n" +
		strings.Join(lines, "\n") + "\n"
}

// hitContractsNote renders contract lines for symbols that ENCLOSE the
// delivered text hits — the Kinto shape: the agent greps into a contract's
// body without ever naming or reading it. One line per symbol per session
// (h.contractFiles doubles as the dedup set with a "sym:" prefix).
func (h *Handler) hitContractsNote(ctx context.Context, hits []textsearch.Hit) string {
	if h.Grove == nil || len(hits) == 0 {
		return ""
	}
	fileSyms := map[string][]grove.SymbolRecord{}
	probed := 0
	var lines []string
	seenSym := map[string]bool{}
	for _, hit := range hits {
		syms, ok := fileSyms[hit.File]
		if !ok {
			if probed >= textMapFileCap {
				continue
			}
			probed++
			s, err := h.Grove.FileSymbols(ctx, hit.File)
			if err != nil {
				s = nil
			}
			fileSyms[hit.File] = s
			syms = s
		}
		var innermost *grove.SymbolRecord
		for i, s := range syms {
			if s.Span.Start <= hit.Line && hit.Line <= s.Span.End {
				if innermost == nil || (s.Span.End-s.Span.Start) < (innermost.Span.End-innermost.Span.Start) {
					innermost = &syms[i]
				}
			}
		}
		if innermost == nil || seenSym[innermost.ID] {
			continue
		}
		seenSym[innermost.ID] = true
		h.contractMu.Lock()
		if h.contractFiles == nil {
			h.contractLines = map[string]string{}
			h.contractFiles = map[string]bool{}
		}
		noted := h.contractFiles["sym:"+innermost.ID]
		h.contractMu.Unlock()
		if noted {
			continue
		}
		// Enclosing-CALLER contracts are noise (measured: a hit inside an
		// unrelated policy method produced a technically-true, useless note).
		// Only family-bearing enclosers signal "you are inside one of N
		// implementations of a contract".
		if !h.symbolHasFamily(ctx, *innermost) {
			continue
		}
		if l := h.contractLine(ctx, *innermost); l != "" {
			h.contractMu.Lock()
			h.contractFiles["sym:"+innermost.ID] = true
			h.contractMu.Unlock()
			lines = append(lines, l)
		}
		if len(lines) == contractsPerFile {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "these hits are INSIDE contract(s) with fan-out: " + strings.Join(lines, " | ")
}

// symbolHasFamily reports whether a symbol has an override/implementation
// family (cheap via the cached contract line's shape would be fragile;
// ask the graph and let contractLine's cache absorb the second call).
func (h *Handler) symbolHasFamily(ctx context.Context, sym grove.SymbolRecord) bool {
	if h.Grove == nil {
		return false
	}
	name := sym.QualifiedName
	if name == "" {
		name = sym.Name
	}
	r, err := h.Grove.ChangeImpact(ctx, name)
	return err == nil && r != nil && len(r.Family) > 0
}

// calleeContractsNote: for a call-shaped text query (".name(" / "name("),
// resolve the called member and deliver the contract lines of family-bearing
// candidates — the Kinto shape done right: grep for `cache.get(` finds call
// sites; the information that changes the design is the CALLEE's family
// (memcached/memory/postgresql), not the callers' enclosers.
func (h *Handler) calleeContractsNote(ctx context.Context, query string) string {
	if h.Grove == nil {
		return ""
	}
	m := calleePatRe.FindStringSubmatch(query)
	if m == nil {
		return ""
	}
	leaf := m[1]
	cands, err := h.Grove.Resolve(ctx, leaf)
	if err != nil {
		return ""
	}
	var lines []string
	seen := map[string]bool{}
	for _, c := range cands {
		if c.TestDouble || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		rec := grove.SymbolRecord{QualifiedName: c.Name, Name: c.Name}
		if !h.symbolHasFamily(ctx, rec) {
			continue
		}
		if l := h.contractLine(ctx, rec); l != "" {
			dup := false
			for _, x := range lines {
				if x == l {
					dup = true // Resolve can return one symbol twice
					break
				}
			}
			if !dup {
				lines = append(lines, l)
			}
		}
		if len(lines) == 2 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "the call you searched may target contract(s) with fan-out: " + strings.Join(lines, " | ")
}

// calleePatRe extracts the member leaf from a call-shaped query like
// "cache.get(" or "get(" — identifier immediately before an open paren,
// optionally preceded by a receiver segment.
var calleePatRe = regexp.MustCompile(`(?:^|[.\s])([A-Za-z_][A-Za-z0-9_]*)\($`)

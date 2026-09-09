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
	"regexp"
	"sort"
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

type textMatchGroup struct {
	file string
	hits []textsearch.Hit
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
//
// exhaustive is the caller's exhaustive=true: the agent asked for EVERY
// site, so files past the render cap are listed by path and hit count
// instead of being counted and dropped. Measured on dubbo (v070sample
// 2026-09-05): `search triple exhaustive=true` answered "176 more files
// with matches omitted" — the completeness the flag promises, withheld by
// the renderer — and the agent spent the rest of a 108-turn cell grepping
// for the inventory it had just been refused.
func (h *Handler) renderTextMatches(ctx context.Context, rawHits []textsearch.Hit, exhaustive bool) []map[string]any {
	if len(rawHits) == 0 {
		return nil
	}
	var order []string
	byFile := map[string]*textMatchGroup{}
	for _, hit := range rawHits {
		g, ok := byFile[hit.File]
		if !ok {
			g = &textMatchGroup{file: hit.File}
			byFile[hit.File] = g
			order = append(order, hit.File)
		}
		g.hits = append(g.hits, hit)
	}

	out := make([]map[string]any, 0, minInt(len(order), textRenderFileCap)+1)
	for i, file := range order {
		if i >= textRenderFileCap {
			rest := len(order) - textRenderFileCap
			if !exhaustive {
				out = append(out, map[string]any{
					"note": strconv.Itoa(rest) + " more files with matches omitted — narrow the term, or exhaustive=true to list every file",
				})
				break
			}
			break
		}
		g := byFile[file]
		entry := map[string]any{"file": file}
		if h.textFileCached(file) {
			// Cached = the FILE BODY was already delivered this session and
			// is unchanged, so surrounding context is not re-sent. The
			// matched lines themselves are never elided: they used to be
			// (line numbers only), and measured 2026-09-02 (6rqii7zt #39)
			// the agent got `grove.go: 260 [cached]` with no text, could
			// not tell what matched, and fell back to manual grep on the
			// same file — the elision saved a few hundred bytes and cost
			// the call its purpose. One line per hit is the search's whole
			// answer; the cache saving is skipping before/after context
			// and the body, not the answer itself.
			shown := make([]map[string]any, 0, minInt(len(g.hits), textRenderHitsPerFile))
			for j, hit := range g.hits {
				if j >= textRenderHitsPerFile {
					entry["moreHits"] = len(g.hits) - textRenderHitsPerFile
					break
				}
				shown = append(shown, map[string]any{"line": hit.Line, "text": hit.Text})
			}
			entry["hits"] = shown
			entry["cached"] = true
			entry["note"] = "file content already delivered this session (unchanged) — context lines omitted, matched lines shown"
		} else {
			shown := make([]map[string]any, 0, minInt(len(g.hits), textRenderHitsPerFile))
			for j, hit := range g.hits {
				if j >= textRenderHitsPerFile {
					entry["moreHits"] = len(g.hits) - textRenderHitsPerFile
					break
				}
				entry2 := map[string]any{"line": hit.Line, "text": hit.Text}
				if len(hit.Before) > 0 {
					entry2["before"] = hit.Before
				}
				if len(hit.After) > 0 {
					entry2["after"] = hit.After
				}
				shown = append(shown, entry2)
			}
			entry["hits"] = shown
		}
		out = append(out, entry)
	}
	if exhaustive {
		// Source text above is deliberately sampled, but the inventory is not:
		// every exact file and match line appears here with its enclosing symbol.
		// Directory-count rollups looked compact but forced agents to expand ten
		// paths and still lose sites (grafana QueryData/CheckHealth, 2026-09-06).
		out = append(out, map[string]any{
			"note":      "COMPLETE inventory — every exact file, match line, and indexed enclosing symbol; source excerpts above are only a sample. No path expansion or per-receiver change_impact calls are needed.",
			"inventory": h.exactHitInventory(ctx, order, byFile),
		})
	}
	return out
}

// renderCompleteTextMatches delivers every raw line in a small result set.
// Search calls this only after an independent exact count proves the global
// set is within textsearch's adaptive bound, so bypassing the normal per-file
// and file-count display caps cannot create an unbounded response.
func (h *Handler) renderCompleteTextMatches(rawHits []textsearch.Hit) []map[string]any {
	if len(rawHits) == 0 {
		return nil
	}
	var order []string
	byFile := map[string][]textsearch.Hit{}
	for _, hit := range rawHits {
		if _, ok := byFile[hit.File]; !ok {
			order = append(order, hit.File)
		}
		byFile[hit.File] = append(byFile[hit.File], hit)
	}
	out := make([]map[string]any, 0, len(order))
	for _, file := range order {
		cached := h.textFileCached(file)
		entry := map[string]any{"file": file}
		hits := make([]map[string]any, 0, len(byFile[file]))
		for _, hit := range byFile[file] {
			item := map[string]any{"line": hit.Line, "text": hit.Text}
			if !cached {
				if len(hit.Before) > 0 {
					item["before"] = hit.Before
				}
				if len(hit.After) > 0 {
					item["after"] = hit.After
				}
			}
			hits = append(hits, item)
		}
		entry["hits"] = hits
		if cached {
			entry["cached"] = true
			entry["note"] = "file content already delivered this session (unchanged) — context lines omitted, matched lines shown"
		}
		out = append(out, entry)
	}
	return out
}

// exactHitInventory is the compact, lossless half of exhaustive text search.
// One entry per file avoids repeating long paths; sites retain every line and
// the tightest indexed symbol enclosing it. A missing symbol is explicit: text
// search also covers comments, config and parser gaps.
func (h *Handler) exactHitInventory(ctx context.Context, order []string, byFile map[string]*textMatchGroup) []map[string]any {
	inventory := make([]map[string]any, 0, len(order))
	for _, file := range order {
		var syms []grove.SymbolRecord
		if h.Grove != nil {
			syms, _ = h.Grove.FileSymbols(ctx, file)
		}
		sites := make([]map[string]any, 0, len(byFile[file].hits))
		seen := map[int]bool{}
		for _, hit := range byFile[file].hits {
			if seen[hit.Line] {
				continue
			}
			seen[hit.Line] = true
			site := map[string]any{"line": hit.Line}
			enclosing := tightestEnclosingSymbol(syms, hit.Line)
			if enclosing != nil {
				name := enclosing.QualifiedName
				if name == "" {
					name = enclosing.Name
				}
				site["symbol"] = name
			}
			sites = append(sites, site)
		}
		inventory = append(inventory, map[string]any{"file": file, "sites": sites})
	}
	return inventory
}

func tightestEnclosingSymbol(syms []grove.SymbolRecord, line int) *grove.SymbolRecord {
	var enclosing *grove.SymbolRecord
	for i := range syms {
		s := &syms[i]
		if s.Span.Start <= line && line <= s.Span.End &&
			(enclosing == nil || s.Span.End-s.Span.Start < enclosing.Span.End-enclosing.Span.Start) {
			enclosing = s
		}
	}
	return enclosing
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

// structuralNote: when a search term IS a symbol with real fan-out, deliver
// the change-impact skeleton as one line — implementations and callers, with
// sites — inside the search result the agent already asked for.
//
// Why (full38, 2026-08-17): on failed fan-out cells the agents SEARCHED the
// exact symbols whose siblings they went on to miss — set_additional_properties
// (its 3rd caller parse_root_type was the missed site), the Kinto cache
// interface methods (3 sibling implementations missed by both arms) — and
// then chose a fix design as if the term were a lone string. change_impact
// was called once in 38 cells; the graph's knowledge never reached the design
// decision. This note is the reach: the agent will not ask "who else
// implements this?", so the answer rides along on the question it did ask.
//
// Fires only when the term resolves to exactly one real symbol AND that
// symbol has fan-out worth knowing (any override/implementation family, or
// 2+ callers). A leaf helper with one caller stays silent — a note printed
// on every search is wallpaper, and wallpaper trained agents to skim.
func (h *Handler) structuralNote(ctx context.Context, query string) string {
	if h.Grove == nil || !identLike.MatchString(query) {
		return ""
	}
	cands, err := h.Grove.Resolve(ctx, query)
	if err != nil {
		return ""
	}
	// Dedup: Resolve can return the same declaration twice (observed on
	// Kinto's CacheBase.get — identical name/file/line, twice). Distinct
	// SYMBOLS mean genuine ambiguity and stay silent; duplicates of one
	// symbol must not.
	var real []grove.ResolvedSymbol
	seen := map[string]bool{}
	for _, c := range cands {
		if c.TestDouble {
			continue
		}
		key := fmt.Sprintf("%s|%s", c.Name, c.File)
		if !seen[key] {
			seen[key] = true
			real = append(real, c)
		}
	}
	if len(real) != 1 {
		return "" // ambiguous names are resolvedRefNote's case, not this one
	}
	if n := h.fieldTypeNote(ctx, query, real[0]); n != "" {
		return n
	}
	r, err := h.Grove.ChangeImpact(ctx, real[0].Name)
	if err != nil || r == nil {
		return ""
	}
	if len(r.Family) == 0 && len(r.Callers) < 2 {
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
	if query == r.Query {
		b.WriteString(r.Query)
	} else {
		fmt.Fprintf(&b, "%s is %s", query, r.Query)
	}
	if len(r.Declarations) > 0 {
		fmt.Fprintf(&b, " (%s)", site(r.Declarations[0]))
	}
	if n := len(r.Family); n > 0 {
		fmt.Fprintf(&b, " — %d implementation(s):", n)
		for i, s := range r.Family {
			if i == 4 {
				fmt.Fprintf(&b, " +%d more", n-4)
				break
			}
			fmt.Fprintf(&b, " %s", site(s))
		}
	}
	if n := len(r.Callers); n > 0 {
		fmt.Fprintf(&b, "; %d caller(s):", n)
		for i, s := range r.Callers {
			if i == 3 {
				fmt.Fprintf(&b, " +%d more", n-3)
				break
			}
			fmt.Fprintf(&b, " %s %s", leafOf(s.Name), site(s))
		}
	}
	b.WriteString(". A contract change here touches that whole set — " +
		"prism_change_impact for the closed, line-precise list.")
	return b.String()
}

// fieldTypeNote is structuralNote's answer when the term names a FIELD: a
// field's own change-impact is a dead end (one declaration, no callers), but
// the type it holds is where the work is. Measured on dubbo (v070sample
// 2026-09-05, 108-turn cell): `search triple` resolved to the field
// ProtocolConfig.triple — type TripleConfig, 27 sites in 12 files covering
// 8/17 of the gold change — and said nothing, so the agent never learned the
// type existed and chased the wrong meaning of the word for 67 Bash calls.
// Silent unless the declared type is exactly one indexed type with fan-out.
func (h *Handler) fieldTypeNote(ctx context.Context, query string, field grove.ResolvedSymbol) string {
	switch field.Kind {
	case "field", "property", "variable", "constant":
	default:
		return ""
	}
	syms, err := h.Grove.SearchSymbols(ctx, field.Name, 10)
	if err != nil {
		return ""
	}
	var rec *grove.SymbolRecord
	for i := range syms {
		if syms[i].QualifiedName == field.Name && syms[i].FilePath == field.File {
			rec = &syms[i]
			break
		}
	}
	if rec == nil {
		return ""
	}
	typ := declaredTypeOf(rec.Signature, rec.Name, rec.Language)
	if typ == "" || typ == rec.Name {
		return ""
	}
	tc, err := h.Grove.Resolve(ctx, typ)
	if err != nil {
		return ""
	}
	var types []grove.ResolvedSymbol
	for _, c := range tc {
		switch c.Kind {
		case "class", "struct", "interface", "type", "enum":
			if !c.TestDouble {
				types = append(types, c)
			}
		}
	}
	if len(types) != 1 {
		return ""
	}
	r, err := h.Grove.ChangeImpact(ctx, types[0].Name)
	if err != nil || r == nil {
		return ""
	}
	sites := append(append([]grove.SymbolRecord{}, r.Family...), r.Callers...)
	if len(sites) < 2 {
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
	files := map[string]bool{}
	for _, s := range sites {
		files[s.FilePath] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is a %s %s (%s:%d) of type %s", query, field.Kind, field.Name,
		shortPath(field.File), field.Line, types[0].Name)
	if len(r.Declarations) > 0 {
		fmt.Fprintf(&b, " (%s)", site(r.Declarations[0]))
	}
	fmt.Fprintf(&b, " — %s is used at %d site(s) in %d file(s):", types[0].Name, len(sites), len(files))
	for i, s := range sites {
		if i == 4 {
			fmt.Fprintf(&b, " +%d more", len(sites)-4)
			break
		}
		fmt.Fprintf(&b, " %s %s", leafOf(s.Name), site(s))
	}
	fmt.Fprintf(&b, ". A change to what %s holds usually lands across that set — prism_change_impact %s for the complete, line-precise list.",
		field.Name, types[0].Name)
	return b.String()
}

// inventoryLineBudget bounds an exhaustive inventory. Measured (v070sample,
// 2026-09-05): the steering's own wide-refactor opener (exhaustive +
// files_only) returned a 245-path list — 26k chars at turn 3 of 74, re-read
// on every later turn for 0.93M tokens, 23% of the whole cell; a 3-term
// exhaustive search at turn 3 of 84 was 45k chars and 27% of its cell. But
// the same opener once carried the whole answer (BACKLOG item 11: the
// zookeeper-api / curator4 / curator5 module triangle), so the bound must
// keep the directory NAMES where files cluster. boundedInventory refines a
// path trie: dense directories split into their children while the budget
// allows, sparse ones list their files. Every file is either listed or
// inside exactly one named group — complete at directory granularity, and
// path=<dir> expands any group.
const inventoryLineBudget = 60

// inventoryFlatCap: at or below this many files the inventory is simply the
// list — grouping would hide more than it saves.
const inventoryFlatCap = 30

type invNode struct {
	path     string // directory path with trailing "/" ("" for the root)
	file     string // set for a leaf that is a single file
	files    []string
	hits     int
	children map[string]*invNode
	frozen   bool // refinement would not fit the budget
}

func (n *invNode) count() int {
	if n.file != "" {
		return 1
	}
	c := len(n.files)
	for _, ch := range n.children {
		c += ch.count()
	}
	return c
}

func (n *invNode) leaves() []string {
	if n.file != "" {
		return []string{n.file}
	}
	out := append([]string{}, n.files...)
	for _, ch := range n.children {
		out = append(out, ch.leaves()...)
	}
	return out
}

// boundedInventory renders files (with optional per-file hit counts) within
// budget lines. Returns the lines and a sentence describing
// their shape.
func boundedInventory(files []string, hits map[string]int, budget int) ([]string, string) {
	entry := func(f string) string {
		if n, ok := hits[f]; ok {
			return f + " (" + strconv.Itoa(n) + ")"
		}
		return f
	}
	if len(files) <= inventoryFlatCap {
		out := make([]string, 0, len(files))
		for _, f := range files {
			out = append(out, entry(f))
		}
		if hits != nil {
			return out, "every one listed as path (hit count); path= on one for its lines"
		}
		return out, "every one listed"
	}
	root := &invNode{children: map[string]*invNode{}}
	for _, f := range files {
		parts := strings.Split(f, "/")
		n := root
		for _, seg := range parts[:len(parts)-1] {
			ch := n.children[seg]
			if ch == nil {
				ch = &invNode{path: n.path + seg + "/", children: map[string]*invNode{}}
				n.children[seg] = ch
			}
			n = ch
			n.hits += hits[f]
		}
		n.files = append(n.files, f)
	}
	// Frontier: the entries currently shown (root's subdirectories and
	// root-level files). Refine the largest splittable directory while the
	// extra lines fit the budget; a directory that does not fit is frozen
	// and the next-largest is tried.
	var frontier []*invNode
	for _, ch := range root.children {
		frontier = append(frontier, ch)
	}
	for _, f := range root.files {
		frontier = append(frontier, &invNode{file: f})
	}
	lines := len(frontier)
	for {
		// Split preference: breadth-first. Splitting by raw count spent
		// the budget naming a test module's eight pom.xml files while
		// dubbo-remoting/ (15 files) stayed one line, hiding the
		// zookeeper-api/curator4/curator5 modules that were the answer
		// (BACKLOG item 11); one level of names under every top-level
		// module is what an inventory is for. A single-child chain
		// (src/ → main/ → java/) costs no lines and always splits.
		var best *invNode
		bi, bestScore := -1, -1
		for i, n := range frontier {
			if n.file != "" || n.frozen {
				continue
			}
			parts := len(n.children) + len(n.files)
			if parts < 2 && len(n.children) != 1 {
				continue
			}
			if n.count() < 3 {
				continue
			}
			// Breadth-first: shallower directories split first so every
			// top-level module gets one level of names before anything
			// goes deeper; ties by count.
			depth := strings.Count(n.path, "/")
			score := (16-depth)*100000 + n.count()
			if parts == 1 {
				score = 1 << 30 // free chain collapse
			}
			if score > bestScore {
				best, bi, bestScore = n, i, score
			}
		}
		if best == nil {
			break
		}
		extra := len(best.children) + len(best.files) - 1
		if lines+extra > budget {
			best.frozen = true
			continue
		}
		frontier = append(frontier[:bi], frontier[bi+1:]...)
		for _, ch := range best.children {
			frontier = append(frontier, ch)
		}
		for _, f := range best.files {
			frontier = append(frontier, &invNode{file: f})
		}
		lines += extra
	}
	type line struct{ key, text string }
	var out []line
	grouped := 0
	for _, n := range frontier {
		if n.file != "" {
			out = append(out, line{n.file, entry(n.file)})
			continue
		}
		c := n.count()
		if c <= 2 {
			// one or two files under a directory: name them instead
			for _, f := range n.leaves() {
				out = append(out, line{f, entry(f)})
			}
			continue
		}
		grouped += c
		t := n.path + " (" + strconv.Itoa(c) + " files"
		if hits != nil {
			t += ", " + strconv.Itoa(n.hits) + " hits"
		}
		out = append(out, line{n.path, t + ")"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	res := make([]string, 0, len(out))
	for _, l := range out {
		res = append(res, l.text)
	}
	return res, strconv.Itoa(len(files)-grouped) + " listed by path, " + strconv.Itoa(grouped) +
		" grouped by directory (every file is in exactly one group) — path=<dir> lists that group's files"
}

func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

// declaredTypeOf extracts the bare type name a field declaration holds:
// `private TripleConfig triple;` → TripleConfig, `triple: TripleConfig`
// → TripleConfig, Go's `Triple *TripleConfig` → TripleConfig. Generic
// arguments, arrays, pointers, nullability and package qualifiers are
// stripped — the head type is what the graph can resolve. Returns "" for
// shapes it does not recognise (inferred `var x = …`, tuples, lambdas).
func declaredTypeOf(sig, name, language string) string {
	sig = strings.TrimSpace(sig)
	if i := strings.Index(sig, "="); i >= 0 {
		sig = sig[:i]
	}
	sig = strings.TrimRight(strings.TrimSpace(sig), ";,")
	var typ string
	if i := strings.Index(sig, name+":"); i >= 0 {
		// name: Type (TypeScript, Python annotations, Rust, Kotlin)
		typ = strings.TrimSpace(sig[i+len(name)+1:])
	} else if i := strings.Index(sig, name+"?:"); i >= 0 {
		typ = strings.TrimSpace(sig[i+len(name)+2:]) // optional TS property
	} else {
		fields := strings.Fields(sig)
		idx := -1
		for i, f := range fields {
			if strings.TrimRight(f, "?!;,") == name || strings.TrimLeft(f, "$") == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ""
		}
		switch language {
		case "go":
			if idx+1 < len(fields) {
				typ = fields[idx+1]
			}
		default: // java, csharp, php, c, cpp: Type precedes the name
			if idx > 0 {
				typ = fields[idx-1]
			}
		}
	}
	typ = strings.TrimSpace(typ)
	if i := strings.IndexAny(typ, "<(["); i >= 0 {
		typ = typ[:i]
	}
	typ = strings.TrimLeft(typ, "*&?")
	typ = strings.TrimRight(typ, "?!")
	if i := strings.LastIndexAny(typ, ".:\\"); i >= 0 {
		typ = typ[i+1:]
	}
	switch typ {
	case "", "var", "let", "const", "final", "static", "private", "public", "protected",
		"readonly", "int", "long", "short", "byte", "char", "boolean", "bool", "float", "double",
		"string", "String", "void", "object", "Object", "any", "number", "str", "float64", "int64",
		"error", "interface{}", "dynamic":
		return ""
	}
	if !identLike.MatchString(typ) {
		return ""
	}
	return typ
}

// identLike gates structuralNote to queries that could name a symbol —
// a regex or a phrase is a text question, not a symbol question.
var identLike = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

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
				"belong to the ones you did not mean. Disambiguate by qualified name and inspect receiver/type "+
				"evidence before acting on the text hits.",
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
			"name without referring to it (other types, comments, strings). Inspect receiver/type "+
			"evidence before treating the remaining text hits as uses.",
		len(hits), n, qn)
}

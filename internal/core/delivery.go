package core

import (
	"context"
	"fmt"
	"github.com/provasign/prism/internal/lru"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

// Source delivery for prism_query: the same budgeted selection, delivered the
// way an agent about to EDIT needs it — verbatim, line-numbered source windows
// grouped by file (identical framing to a Read the agent already performed),
// headed by a per-anchor summary (callers + covering tests). Files whose full
// content was already delivered this session return a one-line sha pointer
// instead of a resend.

const (
	// windowPad is the context padding (lines) around each symbol span.
	windowPad = 2
	// windowMergeGap: windows separated by at most this many lines are merged.
	windowMergeGap = 4
	// wholeFileLines: files at or under this line count are always delivered
	// whole — windowing tiny files saves nothing and costs anchors.
	wholeFileLines = 80
	// wholeFileFraction: when windows would cover at least this fraction of a
	// file AND the file is small enough (wholeFileMaxLines), deliver the
	// whole file instead (and record it in the session tracker, making later
	// reads sha-pointer-eligible).
	wholeFileFraction = 0.8
	// wholeFileMaxLines bounds the coverage-triggered whole-file escape.
	// Unbounded, 0.8 coverage on a 951-line file delivered all 951 lines
	// verbatim (measured 2026-08-31: werkzeug routing/map.py, 5 identical
	// deliveries, ~40k chars each — the majority of prism_query's entire
	// 76%-of-all-tokens footprint came through this path). The windows the
	// ranking had ALREADY computed for those calls held every named anchor
	// and its graph-selected neighbors; collapsing them to the whole file
	// added ~700 incidental lines per call. Above this bound the windowed
	// rendering (with its omitted-ranges markers) always wins; the agent
	// reaches omitted lines via prism_read(file, offset, limit) — the same
	// follow-up shape a sed-based baseline uses, minus the guessing.
	wholeFileMaxLines = 300
	// signatureWindowLines: span cap for dependency symbols selected at
	// signature-level disclosure — enough for the signature and doc head.
	signatureWindowLines = 8
	// anchorSummaryMax is how many top anchor symbols get a summary line.
	anchorSummaryMax = 5
	// sourceDeliveryMaxFiles is the default minimum number of source files.
	// Every named-anchor file and one related file can be kept beyond it.
	sourceDeliveryMaxFiles = 2
	// searchFullBodyMaxLines/searchFullBodyMaxBytes bound when a search hit's
	// enclosing symbol is delivered in full instead of windowed. The
	// evidence-ranked path (searchevidence.go) uses them, so there is one
	// full-body threshold, not independently tuned ones.
	searchFullBodyMaxLines = 160
	searchFullBodyMaxBytes = 10000
)

type lineWindow struct{ start, end int }

// deliverSource renders a selection as the source-window delivery and returns
// the full tool response map.
// truncateSection clips a single file's rendering to a token ceiling, saying
// so in-band. Silently returning a too-large payload is worse than a short
// one: the host rejects the entire tool result, so the agent gets NOTHING and
// is told to fall back to grep.
func truncateSection(section string, maxTokens int, path string) string {
	maxBytes := maxTokens * 4
	if maxBytes <= 0 {
		return ""
	}
	if len(section) <= maxBytes {
		return section
	}
	marker := fmt.Sprintf("\n… [prism truncated %s here — Read the file directly for the remainder]\n", path)
	if len(marker) >= maxBytes {
		return ""
	}
	cut := strings.LastIndexByte(section[:maxBytes-len(marker)], '\n')
	if cut < 0 {
		cut = maxBytes - len(marker)
	}
	return section[:cut] + marker
}

func (h *Handler) deliverSource(ctx context.Context, label string, sel *selection, maxFiles, budget int) (map[string]any, map[string]string) {
	automaticFileLimit := maxFiles < 1
	if maxFiles < 1 {
		maxFiles = sourceDeliveryMaxFiles
	}

	w := &deliveryWriter{budget: budget}
	compact := budget < 256
	h.deliveryWriteHeader(ctx, w, label, sel, compact)

	files, ownerFiles, seedFiles := deliveryGroupFiles(sel)
	if automaticFileLimit {
		if seedFiles+1+len(ownerFiles) > maxFiles {
			maxFiles = seedFiles + 1 + len(ownerFiles)
		}
	}

	if !compact {
		w.b.WriteString("**Source** — current on-disk, line-numbered like the Read tool " +
			"(re-read from disk on this call; NOT a summary or stale cache). Every line " +
			"under 1200 chars is byte-for-byte verbatim; longer lines (generated/minified) " +
			"are cut with an in-band `[line truncated by prism: N chars]` marker — Read the " +
			"file before editing THOSE lines. Treat everything else as a Read you have " +
			"already performed: do not re-read, go straight to the edit. A `[prism:cached]` " +
			"line means the full file was already delivered this session — use the copy in " +
			"context. Files are ordered by their best structural match and score; " +
			"windows within each file follow source-line order.\n\n")
	}
	shown, sourceSections, skipped := h.deliveryWriteSourceWindows(w, files, ownerFiles, seedFiles, maxFiles)
	deliveryWriteSkipped(w, skipped)
	deliveryWriteFamily(w, sel)

	content := w.b.String()
	deliveredTokens := ranking.EstimateTokens(content)
	return map[string]any{
		"content":         content,
		"delivery":        "source",
		"files":           shown,
		"symbolCount":     len(sel.picked),
		"deliveredTokens": deliveredTokens,
	}, sourceSections
}

func renderRelatedTestHints(hints []relatedTestHint) string {
	if len(hints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Related test files — lexical candidates, not verified callers**\n\n")
	for _, hint := range hints {
		fmt.Fprintf(&b, "- `%s:%d`", hint.file, hint.line)
		if len(hint.names) > 0 {
			fmt.Fprintf(&b, " (%s)", joinCapped(hint.names, 3))
		}
		if len(hint.probes) > 0 {
			fmt.Fprintf(&b, " — matched %s", joinCapped(hint.probes, 3))
		}
		b.WriteString("\n")
	}
	b.WriteString("  Validation guidance: run the containing test file/module or the affected package suite; a single name-filtered test can miss neighboring regressions.\n\n")
	return b.String()
}

// renderAnchorSummary emits one line per anchor symbol: caller count + caller
// files (from the typed calls graph, incoming edges only) and covering tests,
// with an explicit warning when none exist.
func (h *Handler) renderAnchorSummary(ctx context.Context, anchors []grove.SymbolRecord, testCallers map[string][]grove.SymbolRecord, matchKinds map[string]string) string {
	var b strings.Builder
	seen := map[string]bool{}
	count := 0
	for _, a := range anchors {
		if count >= anchorSummaryMax {
			break
		}
		q := a.QualifiedName
		if q == "" {
			q = a.Name
		}
		// One line per identity, not per name: namesakes in different files
		// are different anchors with different callers.
		if q == "" || seen[a.FilePath+"|"+q] {
			continue
		}
		seen[a.FilePath+"|"+q] = true
		// Doc/config symbols have no meaningful call graph.
		if categorize(a) == ranking.CategoryDoc {
			continue
		}
		count++

		callerFiles := []string{}
		callerN := 0
		if edges, err := h.identityEdges(ctx, a, "in", []string{"calls"}); err == nil {
			seenFile := map[string]bool{}
			for _, e := range edges {
				callerN++
				if !seenFile[e.File] {
					seenFile[e.File] = true
					callerFiles = append(callerFiles, e.File)
				}
			}
		}
		fmt.Fprintf(&b, "- `%s` (%s:%d)", a.Name, a.FilePath, a.Span.Start)
		if kind := matchKinds[a.ID]; kind != "" {
			fmt.Fprintf(&b, " [match: %s]", kind)
		}
		if callerN > 0 {
			fmt.Fprintf(&b, " — %d caller%s in %s", callerN, plural(callerN), joinCapped(callerFiles, 3))
		} else if dataSymbolKind(a.Kind) {
			// A field/variable/const has readers and writers, not callers;
			// the call graph does not track accesses. "no resolved callers"
			// read as "unused" (gin Context.Errors has 27 uses).
			fmt.Fprintf(&b, " — %s: accesses are not tracked as callers; op=search terms=[%q] scope=text lists its uses", a.Kind, a.Name)
		} else {
			b.WriteString(" — no resolved callers")
		}
		b.WriteString("\n")
		// Pointer only: locations, never bodies -- prism_read/prism_lookup
		// on the file:line for the actual assertion/mocking pattern. See
		// selectContext's testCallers declaration for why this is safe
		// against the pr3493 failure (unrelated test wins a whole window)
		// that removed test delivery in the first place.
		if tc := testCallers[a.ID]; len(tc) > 0 {
			names := make([]string, 0, len(tc))
			for _, c := range tc {
				n := c.Name
				if n == "" {
					n = "test"
				}
				names = append(names, fmt.Sprintf("%s:%d (%s)", c.FilePath, c.Span.Start, n))
			}
			fmt.Fprintf(&b, "  tested by %d: %s\n", len(tc), joinCapped(names, 3))
		}
	}
	return b.String()
}

type fileGroup struct {
	path        string // normalized, root-relative
	relation    ranking.RelationTier
	best        float64
	symbols     []ranking.BudgetedSymbol
	noWholeFile bool
}

type deliveredFileRanges struct {
	hash    string
	windows []lineWindow
}

func (h *Handler) recordDeliveredRanges(path, hash string, windows []lineWindow) {
	if len(windows) == 0 {
		return
	}
	h.rangeMu.Lock()
	defer h.rangeMu.Unlock()
	if h.deliveredRanges == nil {
		h.deliveredRanges = lru.New[string, deliveredFileRanges](sessionFileCacheLimit)
	}
	entry, _ := h.deliveredRanges.Get(path)
	if entry.hash != hash {
		entry = deliveredFileRanges{hash: hash}
	}
	entry.windows = append(entry.windows, windows...)
	sort.Slice(entry.windows, func(i, j int) bool {
		return entry.windows[i].start < entry.windows[j].start
	})
	merged := entry.windows[:0]
	for _, w := range entry.windows {
		if n := len(merged); n > 0 && w.start <= merged[n-1].end+1 {
			if w.end > merged[n-1].end {
				merged[n-1].end = w.end
			}
			continue
		}
		merged = append(merged, w)
	}
	entry.windows = merged
	h.deliveredRanges.Put(path, entry)
}

func (h *Handler) deliveredRangeCovered(path, hash string, start, end int) bool {
	h.rangeMu.Lock()
	defer h.rangeMu.Unlock()
	if h.deliveredRanges == nil {
		return false
	}
	entry, ok := h.deliveredRanges.Get(path)
	if !ok || entry.hash != hash {
		return false
	}
	for _, w := range entry.windows {
		if start >= w.start && end <= w.end {
			return true
		}
	}
	return false
}

// groupPickedByFile buckets budget-selected symbols by containing file and
// orders files by their best symbol score, so the most relevant file renders
// first and file caps cut from the tail.
func groupPickedByFile(picked []ranking.BudgetedSymbol) []fileGroup {
	byPath := map[string]*fileGroup{}
	order := []string{}
	for _, p := range picked {
		rel := normalizePath(p.Symbol.FilePath)
		if rel == "" || p.Symbol.Span.Start <= 0 {
			continue
		}
		g, ok := byPath[rel]
		if !ok {
			g = &fileGroup{path: rel}
			byPath[rel] = g
			order = append(order, rel)
		}
		if p.Relation > g.relation {
			g.relation = p.Relation
			g.best = p.Score
		} else if p.Relation == g.relation && p.Score > g.best {
			g.best = p.Score
		}
		g.symbols = append(g.symbols, p)
	}
	out := make([]fileGroup, 0, len(order))
	for _, rel := range order {
		out = append(out, *byPath[rel])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].relation != out[j].relation {
			return out[i].relation > out[j].relation
		}
		if out[i].best != out[j].best {
			return out[i].best > out[j].best
		}
		return out[i].path < out[j].path // total order — ties must not follow map layout
	})
	return out
}

// renderFileSection renders one file's contribution: a sha pointer when the
// full file is already in the agent's context, the whole file when windows
// would cover most of it, or merged line-numbered windows otherwise.
// Both delivery paths clamp through the same helper: the symbols path renders
// via ranking.Render, the source path here. Fixing only one left the other
// able to emit an 85KB line and have the host reject the whole response.

func clampSourceLine(l string) string { return ranking.ClampLines(l) }

func (h *Handler) renderFileSection(fg fileGroup) (string, func(), bool) {
	abs := filepath.Join(h.Root, filepath.FromSlash(fg.path))
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", nil, false
	}
	content := string(data)
	hash := compression.Hash(content)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Already delivered in full this session, unchanged → pointer, not resend.
	if entry, seen, same := h.Session.Lookup(fg.path, hash); seen && same && entry.DisclosureLevel == "full" {
		h.Session.Record(fg.path, hash, int64(ranking.EstimateTokens(content)), "full")
		return compression.SHAPointer(fg.path, hash, entry.AccessCount) + "\n", func() {}, true
	}

	if os.Getenv("PRISM_DEBUG_PICK") != "" {
		for _, ps := range fg.symbols {
			fmt.Fprintf(os.Stderr, "[pick] %s %s span=%d-%d disc=%s score=%.2f\n",
				fg.path, ps.Symbol.Name, ps.Symbol.Span.Start, ps.Symbol.Span.End, ps.Disclosure, ps.Score)
		}
	}
	wins := symbolWindows(fg.symbols, len(lines))
	covered := 0
	for _, w := range wins {
		covered += w.end - w.start + 1
	}
	// The whole-file escape never applies to a file whose every pick is
	// signature-level: those are demoted content-only seeds (see
	// deliverSource), and collapsing to the whole file would undo the
	// demotion for exactly the small files where the license-header-dump
	// waste was measured (BACKLOG addendum #7).
	allSignature := len(fg.symbols) > 0
	for _, ps := range fg.symbols {
		if ps.Disclosure == ranking.DisclosureFull {
			allSignature = false
			break
		}
	}
	wholeFile := !fg.noWholeFile && !allSignature && (len(lines) <= wholeFileLines ||
		(len(lines) <= wholeFileMaxLines &&
			float64(covered) >= wholeFileFraction*float64(len(lines))))
	if wholeFile {
		wins = []lineWindow{{1, len(lines)}}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**`%s`**", fg.path)
	fmt.Fprintf(&b, "\n\n```%s\n", langTag(fg.path))
	prevEnd := 0
	for _, w := range wins {
		if prevEnd > 0 && w.start > prevEnd+1 {
			// Actionable omission: name the exact prism_read call that
			// fetches the gap, so reaching omitted code is one precise
			// call, not a guessed sed range (measured 2026-08-31: baseline
			// agents needed 1-5 guess-and-retry reads to land the same
			// windows this ranking picks directly).
			fmt.Fprintf(&b, "… [lines %d–%d omitted — prism_read(file, offset=%d, limit=%d) if needed] …\n",
				prevEnd+1, w.start-1, prevEnd+1, w.start-1-prevEnd)
		}
		for n := w.start; n <= w.end && n <= len(lines); n++ {
			fmt.Fprintf(&b, "%d"+SourceLineSep+"%s\n", n, clampSourceLine(lines[n-1]))
		}
		prevEnd = w.end
	}
	b.WriteString("```\n\n")

	// Record ONLY full-file deliveries — and only when the caller COMMITS the
	// section. Recording inside the render made a discarded render poison the
	// session: the over-budget shed loop re-rendered a trimmed section and
	// got back a [prism:cached] pointer to content the agent never received
	// (measured 2026-08-26 — a 64KB delivery collapsed to 6KB of pointer).
	commit := func() {
		h.recordDeliveredRanges(fg.path, hash, wins)
	}
	if wholeFile {
		commit = func() {
			h.recordDeliveredRanges(fg.path, hash, wins)
			h.Session.Record(fg.path, hash, int64(ranking.EstimateTokens(content)), "full")
		}
	}
	return b.String(), commit, true
}

// symbolWindows converts the selected symbols of one file into ordered,
// merged, padded line windows. Dependency symbols selected at signature-level
// disclosure contribute only their span head — the contract, not the body.
func symbolWindows(symbols []ranking.BudgetedSymbol, maxLine int) []lineWindow {
	raw := make([]lineWindow, 0, len(symbols))
	for _, s := range symbols {
		start, end := s.Symbol.Span.Start, s.Symbol.Span.End
		if start <= 0 {
			continue
		}
		if end < start {
			end = start
		}
		// A synthetic module-level symbol can span an entire file. Its name
		// is a locator, not a request to inline every unrelated definition.
		if (s.Disclosure != ranking.DisclosureFull || s.Symbol.Name == "<top-level>") && end-start+1 > signatureWindowLines {
			end = start + signatureWindowLines - 1
		}
		start -= windowPad
		end += windowPad
		if start < 1 {
			start = 1
		}
		if end > maxLine {
			end = maxLine
		}
		raw = append(raw, lineWindow{start, end})
	}
	// Stable + total: sort.Slice is UNSTABLE, so two windows with the same
	// start swapped at random between runs (the last nondeterminism site
	// after the ranking sorts got total orders, 2026-08-26).
	sort.SliceStable(raw, func(i, j int) bool {
		if raw[i].start != raw[j].start {
			return raw[i].start < raw[j].start
		}
		return raw[i].end < raw[j].end
	})
	merged := make([]lineWindow, 0, len(raw))
	for _, w := range raw {
		if n := len(merged); n > 0 && w.start <= merged[n-1].end+windowMergeGap+1 {
			if w.end > merged[n-1].end {
				merged[n-1].end = w.end
			}
			continue
		}
		merged = append(merged, w)
	}
	return merged
}

func langTag(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".sh":
		return "bash"
	default:
		return ""
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinCapped(items []string, cap int) string {
	if len(items) <= cap {
		return "`" + strings.Join(items, "`, `") + "`"
	}
	return "`" + strings.Join(items[:cap], "`, `") + fmt.Sprintf("` +%d more", len(items)-cap)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// dataSymbolKind reports kinds whose uses are reads/writes, not calls.
func dataSymbolKind(kind string) bool {
	switch kind {
	case "field", "variable", "const", "property", "constant", "enum_member", "attribute":
		return true
	}
	return false
}

package mcp

import (
	"context"
	"fmt"
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
// instead of a resend. prism_query picks this delivery phase-aware (debug and
// implement tasks) unless the caller passes delivery explicitly.

const (
	// windowPad is the context padding (lines) around each symbol span.
	windowPad = 2
	// windowMergeGap: windows separated by at most this many lines are merged.
	windowMergeGap = 4
	// wholeFileLines: files at or under this line count are always delivered
	// whole — windowing tiny files saves nothing and costs anchors.
	wholeFileLines = 80
	// wholeFileFraction: when windows would cover at least this fraction of a
	// file, deliver the whole file instead (and record it in the session
	// tracker, making later reads sha-pointer-eligible).
	wholeFileFraction = 0.8
	// signatureWindowLines: span cap for dependency symbols selected at
	// signature-level disclosure — enough for the signature and doc head.
	signatureWindowLines = 8
	// anchorSummaryMax is how many top anchor symbols get a summary line.
	anchorSummaryMax = 5
	// sourceDeliveryMaxFiles caps how many files get source windows;
	// the rest are listed by name.
	sourceDeliveryMaxFiles = 5
)

type lineWindow struct{ start, end int }

// deliverSource renders a selection as the source-window delivery and returns
// the full tool response map.
func (h *Handler) deliverSource(ctx context.Context, task string, sel *selection, maxFiles, budget int) map[string]any {
	if maxFiles < 1 {
		maxFiles = sourceDeliveryMaxFiles
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Context for: %s**\n\n", summarize(task, 120))

	// ── Anchor summary ────────────────────────────────────────────────────
	anchors := h.renderAnchorSummary(ctx, sel.seedSyms)
	if anchors != "" {
		b.WriteString("**Anchors — callers and covering tests (verify before editing)**\n\n")
		b.WriteString(anchors)
		b.WriteString("\n")
	}

	// ── Source windows, grouped by file, ranked ──────────────────────────
	// Keep only tests that exercise an anchor via a real tests edge; tests
	// that merely matched the task text lexically dilute an edit-ready
	// delivery with noise (measured on the pr3493 probe: unrelated help-
	// rendering tests earned whole windows).
	picked := make([]ranking.BudgetedSymbol, 0, len(sel.picked))
	for _, p := range sel.picked {
		if p.Category == ranking.CategoryTest && !sel.testEdgeIDs[p.Symbol.ID] {
			continue
		}
		picked = append(picked, p)
	}
	files := groupPickedByFile(picked)

	b.WriteString("**Source** — verbatim, current on-disk, line-numbered exactly like the " +
		"Read tool (re-read from disk on this call; NOT a summary or stale cache). " +
		"Treat each block as a Read you have already performed: do not re-read these " +
		"files, go straight to the edit. A `[prism:cached]` line means the full file " +
		"was already delivered earlier this session — use the copy in context.\n\n")

	delivered := ranking.EstimateTokens(b.String())
	shown := make([]string, 0, maxFiles)
	var skipped []fileGroup
	for i, fg := range files {
		if len(shown) >= maxFiles || (delivered > budget && i > 0) {
			skipped = append(skipped, files[i:]...)
			break
		}
		section, ok := h.renderFileSection(fg)
		if !ok {
			skipped = append(skipped, fg)
			continue
		}
		b.WriteString(section)
		delivered += ranking.EstimateTokens(section)
		shown = append(shown, fg.path)
	}
	if len(skipped) > 0 {
		b.WriteString("**Also relevant (not shown):**\n")
		for _, fg := range skipped {
			names := make([]string, 0, len(fg.symbols))
			for _, s := range fg.symbols {
				names = append(names, s.Symbol.Name)
			}
			fmt.Fprintf(&b, "- `%s` — %s\n", fg.path, strings.Join(dedupeStrings(names), ", "))
		}
	}

	content := b.String()
	deliveredTokens := ranking.EstimateTokens(content)
	return map[string]any{
		"content":         content,
		"delivery":        "source",
		"files":           shown,
		"symbolCount":     len(sel.picked),
		"deliveredTokens": deliveredTokens,
	}
}

// renderAnchorSummary emits one line per anchor symbol: caller count + caller
// files (from the typed calls graph, incoming edges only) and covering tests,
// with an explicit warning when none exist.
func (h *Handler) renderAnchorSummary(ctx context.Context, anchors []grove.SymbolRecord) string {
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
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		// Doc/config symbols have no meaningful call graph.
		if categorize(a) == ranking.CategoryDoc {
			continue
		}
		count++

		callerFiles := []string{}
		callerN := 0
		if edges, err := h.Grove.Edges(ctx, q, "in", []string{"calls"}); err == nil {
			seenFile := map[string]bool{}
			for _, e := range edges {
				callerN++
				if !seenFile[e.File] {
					seenFile[e.File] = true
					callerFiles = append(callerFiles, e.File)
				}
			}
		}
		testFiles := []string{}
		if tests, err := h.Grove.Tests(ctx, q); err == nil {
			seenFile := map[string]bool{}
			for _, t := range tests {
				if !seenFile[t.FilePath] {
					seenFile[t.FilePath] = true
					testFiles = append(testFiles, t.FilePath)
				}
			}
		}

		fmt.Fprintf(&b, "- `%s` (%s:%d)", a.Name, a.FilePath, a.Span.Start)
		if callerN > 0 {
			fmt.Fprintf(&b, " — %d caller%s in %s", callerN, plural(callerN), joinCapped(callerFiles, 3))
		} else {
			b.WriteString(" — no resolved callers")
		}
		if len(testFiles) > 0 {
			fmt.Fprintf(&b, "; tests: %s", joinCapped(testFiles, 2))
		} else {
			b.WriteString("; ⚠️ no covering tests")
		}
		b.WriteString("\n")
	}
	return b.String()
}

type fileGroup struct {
	path    string // normalized, root-relative
	best    float64
	symbols []ranking.BudgetedSymbol
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
		if p.Score > g.best {
			g.best = p.Score
		}
		g.symbols = append(g.symbols, p)
	}
	out := make([]fileGroup, 0, len(order))
	for _, rel := range order {
		out = append(out, *byPath[rel])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].best > out[j].best })
	return out
}

// renderFileSection renders one file's contribution: a sha pointer when the
// full file is already in the agent's context, the whole file when windows
// would cover most of it, or merged line-numbered windows otherwise.
func (h *Handler) renderFileSection(fg fileGroup) (string, bool) {
	abs := filepath.Join(h.Root, filepath.FromSlash(fg.path))
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false
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
		return compression.SHAPointer(fg.path, hash, entry.AccessCount) + "\n", true
	}

	wins := symbolWindows(fg.symbols, len(lines))
	covered := 0
	for _, w := range wins {
		covered += w.end - w.start + 1
	}
	wholeFile := len(lines) <= wholeFileLines ||
		float64(covered) >= wholeFileFraction*float64(len(lines))
	if wholeFile {
		wins = []lineWindow{{1, len(lines)}}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**`%s`**\n\n```%s\n", fg.path, langTag(fg.path))
	prevEnd := 0
	for _, w := range wins {
		if prevEnd > 0 && w.start > prevEnd+1 {
			fmt.Fprintf(&b, "… [lines %d–%d omitted] …\n", prevEnd+1, w.start-1)
		}
		for n := w.start; n <= w.end && n <= len(lines); n++ {
			fmt.Fprintf(&b, "%d\t%s\n", n, lines[n-1])
		}
		prevEnd = w.end
	}
	b.WriteString("```\n\n")

	// Record ONLY full-file deliveries: the sha-pointer path in prism_read
	// does not know about disclosure levels, so recording a windowed delivery
	// would make a later read return a pointer to content the agent never saw.
	if wholeFile {
		h.Session.Record(fg.path, hash, int64(ranking.EstimateTokens(content)), "full")
	}
	return b.String(), true
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
		if s.Disclosure != ranking.DisclosureFull && end-start+1 > signatureWindowLines {
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
	sort.Slice(raw, func(i, j int) bool { return raw[i].start < raw[j].start })
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

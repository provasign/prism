package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/ranking"
)

// deliveryWriter is the response under construction plus the token budget it
// must fit. Every stage of deliverSource measures against the same builder.
type deliveryWriter struct {
	b      strings.Builder
	budget int
}

// fits reports whether appending extra keeps the response within budget.
func (w *deliveryWriter) fits(extra string) bool {
	return ranking.EstimateTokens(w.b.String()+extra) <= w.budget
}

// deliveryWriteHeader writes the title, anchor summary and related-test hints
// (or the compact one-line header when the budget is tiny).
func (h *Handler) deliveryWriteHeader(ctx context.Context, w *deliveryWriter, label string, sel *selection, compact bool) {
	b := &w.b
	if compact {
		b.WriteString("**Context** — budget-limited; use prism_read for source.\n")
		return
	}
	fmt.Fprintf(b, "**Context for: %s**\n\n", summarize(label, 120))

	// ── Anchor summary ────────────────────────────────────────────────────
	anchors := h.renderAnchorSummary(ctx, sel.seedSyms, sel.testCallers, sel.anchorMatchKinds)
	if anchors != "" {
		b.WriteString("**Anchors — callers (verify before editing)**\n\n")
		b.WriteString(anchors)
		b.WriteString("\n")
	}
	if hints := renderRelatedTestHints(sel.relatedTests); hints != "" {
		b.WriteString(hints)
	}
	if ranking.EstimateTokens(b.String()) > w.budget {
		b.Reset()
		fmt.Fprintf(b, "budget=%d is too small for anchor summaries; use prism_lookup.\n", w.budget)
	}
}

// deliveryGroupFiles turns the picked symbols (plus lexical owners) into
// ranked file groups. It returns the groups, the files that hold lexical
// owners, and how many groups hold a seed.
func deliveryGroupFiles(sel *selection) ([]fileGroup, map[string]bool, int) {
	// ── Source windows, grouped by file, ranked ──────────────────────────
	// Drop test symbols from the source windows: without an edge tying a test
	// to an anchor, a lexically task-matched test dilutes an edit-ready
	// delivery with noise (measured on the pr3493 probe: unrelated help-
	// rendering tests earned whole windows).
	picked := make([]ranking.BudgetedSymbol, 0, len(sel.picked))
	pickedIDs := make(map[string]bool, len(sel.picked)+len(sel.lexicalOwners))
	for _, p := range sel.picked {
		if p.Category == ranking.CategoryTest {
			continue
		}
		// Content-only seeds (term matched the BODY, never the name — a
		// license header, a doc comment) get signature disclosure, not a
		// full window: measured (BACKLOG addendum #7) a full-body dump of
		// a content-matched file spent most of a 22.5kB delivery on code
		// that was never used. The signature line keeps the pointer; the
		// body is one prism_lookup away if the mention actually matters.
		if p.Disclosure == ranking.DisclosureFull && sel.contentOnlySeeds[p.Symbol.ID] {
			p.Disclosure = ranking.DisclosureSignature
		}
		picked = append(picked, p)
		pickedIDs[p.Symbol.ID] = true
	}
	ownerFiles := map[string]bool{}
	for _, owner := range sel.lexicalOwners {
		if pickedIDs[owner.ID] {
			continue
		}
		picked = append(picked, ranking.BudgetedSymbol{
			Symbol: owner, Relation: ranking.RelationDirectCall, Score: 1,
			Category: ranking.CategoryDependency, Disclosure: ranking.DisclosureFull,
		})
		pickedIDs[owner.ID] = true
		ownerFiles[normalizePath(owner.FilePath)] = true
	}
	files := groupPickedByFile(picked)
	seedFiles := 0
	for _, fg := range files {
		if fg.relation == ranking.RelationSeed {
			seedFiles++
		}
	}
	return files, ownerFiles, seedFiles
}

// deliveryWriteSourceWindows renders up to maxFiles file sections, each fitted
// to its share of the budget. It returns the paths shown, their rendered
// sections, and the file groups that were not shown.
func (h *Handler) deliveryWriteSourceWindows(w *deliveryWriter, files []fileGroup, ownerFiles map[string]bool, seedFiles, maxFiles int) ([]string, map[string]string, []fileGroup) {
	budget := w.budget
	shown := make([]string, 0, maxFiles)
	sourceSections := make(map[string]string, maxFiles)
	var skipped []fileGroup
	for i, fg := range files {
		if len(shown) >= maxFiles {
			skipped = append(skipped, files[i:]...)
			break
		}
		section, commit, ok := h.renderFileSection(fg)
		if !ok {
			skipped = append(skipped, fg)
			continue
		}
		// Selector costs do not include expanded source windows. Apply the
		// file share to the rendered section; related files get half the
		// share so they cannot displace a named target with a broad class.
		sectionCap := int(float64(budget) * ranking.FileBudgetFraction)
		// When every named anchor is in this one file, the global budget
		// already bounds it. A fractional file cap would collapse several
		// explicitly named functions to signatures while leaving most of
		// the global budget unused.
		if fg.relation == ranking.RelationSeed && seedFiles == 1 {
			sectionCap = budget - ranking.EstimateTokens(w.b.String())
		}
		if fg.relation != ranking.RelationSeed && !ownerFiles[fg.path] {
			sectionCap /= 2
		}
		if sectionCap < 1 {
			sectionCap = 1
		}
		section, commit, truncated, ok := h.deliveryFitSection(w, &fg, section, commit, sectionCap)
		if !ok {
			skipped = append(skipped, fg)
			continue
		}
		w.b.WriteString(section)
		sourceSections[fg.path] = section
		// A whole-file render may have been byte-clamped above. Its commit
		// closure would otherwise mark the unseen tail as fully delivered,
		// turning the next query into a false cached pointer.
		if !truncated {
			commit()
		}
		shown = append(shown, fg.path)
	}
	return shown, sourceSections, skipped
}

// deliveryFitSection shrinks one file's rendered section until it fits both
// the global budget and sectionCap: windows instead of the whole file, then
// shedding the lowest-scored symbols, then signatures, then a byte cut. It
// returns ok=false when nothing useful fits. fg is the caller's loop copy and
// is updated in place exactly as the inline loop body did.
func (h *Handler) deliveryFitSection(w *deliveryWriter, fg *fileGroup, section string, commit func(), sectionCap int) (string, func(), bool, bool) {
	fitsSection := func(s string) bool {
		return w.fits(s) && ranking.EstimateTokens(s) <= sectionCap
	}
	// A section is measured BEFORE it is committed. The old code checked
	// the running total on entry only, so one oversized file could
	// overshoot the budget by its entire length — and the first file was
	// exempt outright, which is how a single 89KB section got emitted and
	// the host rejected the whole response.
	truncated := false
	if !fitsSection(section) {
		// A whole-file shortcut can exceed the section cap even when the
		// selected symbol windows would fit. Try those exact windows
		// before degrading named anchors to signatures.
		fg.noWholeFile = true
		if sec2, commit2, ok2 := h.renderFileSection(*fg); ok2 {
			section, commit = sec2, commit2
		}
	}
	if !fitsSection(section) {
		// Over budget: shed the LOWEST-scored symbols and re-render,
		// never byte-truncate first. Byte truncation cuts the tail, and
		// windows render in line order — so the windows that died were
		// whichever sat at high line numbers, including the caller's own
		// SEED (measured 2026-08-26: a 2,012-line file's section ended
		// at line 1369 while the anchor the terms named sat at 1482,
		// picked, full-disclosure, score 1.0 — sacrificed for thirty
		// low-score text-hit windows above it). Seeds are named by the
		// caller; they are the last thing to shed, not the first.
		trimmed := *fg
		for len(trimmed.symbols) > 1 && !fitsSection(section) {
			worst, wi := trimmed.symbols[0], 0
			for j, ps := range trimmed.symbols {
				if ps.Relation < worst.Relation ||
					(ps.Relation == worst.Relation && ps.Score < worst.Score) {
					worst, wi = ps, j
				}
			}
			if worst.Relation == ranking.RelationSeed {
				break // only seeds left — take the truncation below
			}
			trimmed.symbols = append(append([]ranking.BudgetedSymbol{},
				trimmed.symbols[:wi]...), trimmed.symbols[wi+1:]...)
			if sec2, commit2, ok2 := h.renderFileSection(trimmed); ok2 {
				section, commit = sec2, commit2
			} else {
				break
			}
		}
		if !fitsSection(section) {
			for j := range trimmed.symbols {
				trimmed.symbols[j].Disclosure = ranking.DisclosureSignature
			}
			if sec2, commit2, ok2 := h.renderFileSection(trimmed); ok2 {
				section, commit = sec2, commit2
			}
		}
		if !fitsSection(section) {
			remaining := w.budget - ranking.EstimateTokens(w.b.String())
			if remaining > sectionCap {
				remaining = sectionCap
			}
			cut := truncateSection(section, remaining, fg.path)
			truncated = cut != section
			section = cut
			if section == "" || !fitsSection(section) {
				return section, commit, truncated, false
			}
		}
	}
	return section, commit, truncated, true
}

// deliveryWriteSkipped lists the file groups that were not shown, as far as
// the budget allows.
func deliveryWriteSkipped(w *deliveryWriter, skipped []fileGroup) {
	if len(skipped) == 0 {
		return
	}
	var omitted strings.Builder
	omitted.WriteString("**Also relevant (not shown):**\n")
	for _, fg := range skipped {
		names := make([]string, 0, len(fg.symbols))
		for _, s := range fg.symbols {
			names = append(names, s.Symbol.Name)
		}
		line := fmt.Sprintf("- `%s` — %s\n", fg.path, strings.Join(dedupeStrings(names), ", "))
		if !w.fits(omitted.String() + line) {
			break
		}
		omitted.WriteString(line)
	}
	if omitted.Len() > len("**Also relevant (not shown):**\n") {
		w.b.WriteString(omitted.String())
	}
}

// deliveryWriteFamily appends the family section.
//
// ── Family section ───────────────────────────────────────────────────
// Overrides/overloads of the anchors, APPENDED after the main windows so
// they can only add coverage, never displace it. When family competed in
// the candidate pool it evicted gold-covering files from the maxFiles
// slots and mean oracle-bed recall fell 0.65 -> 0.55 (2026-08-26); as an
// appendix the same symbols are pure upside. Tight bodies only, few of
// them, deduped against files already shown in full.
func deliveryWriteFamily(w *deliveryWriter, sel *selection) {
	fam := sel.familySyms
	if len(fam) == 0 {
		return
	}
	wrote := 0
	var fb strings.Builder
	delivered := make(map[string]bool)
	for _, pk := range sel.picked {
		delivered[pk.Symbol.ID] = true
	}
	for _, fs := range fam {
		if wrote >= 6 {
			break
		}
		// ID-level dedupe only. Skipping by FILE over-blocked: a file
		// shown for OTHER symbols' windows still lacks this member's
		// body, and the autopsied gold misses (sibling-class overrides)
		// live in exactly such files — measured 2026-08-26: the appendix
		// never fired and family-vs-none scored identical on all 12
		// oracle tasks.
		if delivered[fs.ID] || fs.RawText == "" {
			continue
		}
		body := fs.RawText
		if lines := strings.Count(body, "\n"); lines > 60 {
			cut := body
			for i, n := 0, 0; i < len(cut); i++ {
				if cut[i] == '\n' {
					n++
					if n == 60 {
						body = cut[:i] + "\n… [family member truncated — prism_lookup for the rest]"
						break
					}
				}
			}
		}
		name := fs.QualifiedName
		if name == "" {
			name = fs.Name
		}
		entry := fmt.Sprintf("\n`%s` — %s:%d\n```\n%s\n```\n", name, fs.FilePath, fs.Span.Start, strings.TrimRight(body, "\n"))
		if !w.fits("\n**Family — overrides/overloads of the anchors (fixes often touch these too):**\n" + fb.String() + entry) {
			continue
		}
		fb.WriteString(entry)
		wrote++
	}
	if wrote > 0 {
		w.b.WriteString("\n**Family — overrides/overloads of the anchors (fixes often touch these too):**\n")
		w.b.WriteString(fb.String())
	}
}

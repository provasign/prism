package mcp

// prism_verify — the diff-completeness verifier. Given the diff between a
// git base and the working tree (an agent's output, a PR), compute what the
// change set SHOULD have been and report what is missing:
//
//   - signature-changed / renamed symbols -> change_impact -> required
//     sites; required sites the diff did not touch are MISSED SITES,
//     line-precise where the graph has AST call sites.
//   - affected tests for the changed files (what must run).
//   - component dependencies whose entire evidence originates in changed
//     files (NEW cross-component dependencies introduced by this diff),
//     plus declared arch rules with the same tier-aware gating as
//     prism_arch_check.
//
// A model's "I updated all the callers" is a probabilistic assertion; this
// is the deterministic check of it. Verdict: complete | incomplete | clean.

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/view"
)

type lineRange struct{ start, end int }

// gitPrefix returns the work-root's path relative to the git repository root
// ("" when they coincide). Diff and show paths are repo-root-relative; every
// symbol path Prism holds is work-root-relative — a corpus rooted in a
// subdirectory (guava/guava) silently mismatches every path without this
// (measured: verify passed "complete" on guava with 38 forgotten files
// because gitShow returned nil for every changed file).
func gitPrefix(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitChangedRanges parses `git diff --unified=0 <base>` into after-side
// changed line ranges per file (work-root-relative paths). A pure deletion
// is recorded as a one-line touch marker at its after-side position.
func gitChangedRanges(root, base string) (map[string][]lineRange, error) {
	cmd := exec.Command("git", "-C", root, "diff", "--unified=0", "--no-color", base, "--", ".")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", base, err)
	}
	prefix := gitPrefix(root)
	changed := map[string][]lineRange{}
	var cur string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			cur = strings.TrimPrefix(strings.TrimPrefix(line, "+++ b/"), prefix)
		case strings.HasPrefix(line, "+++ /dev/null"):
			cur = "" // deleted file: no after side to touch-check
		case strings.HasPrefix(line, "@@ ") && cur != "":
			// @@ -l[,s] +c[,d] @@
			fields := strings.Fields(line)
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "+") {
					continue
				}
				nums := strings.SplitN(strings.TrimPrefix(f, "+"), ",", 2)
				c, _ := strconv.Atoi(nums[0])
				d := 1
				if len(nums) == 2 {
					d, _ = strconv.Atoi(nums[1])
				}
				if d == 0 { // pure deletion: mark the position
					if c < 1 {
						c = 1
					}
					changed[cur] = append(changed[cur], lineRange{c, c})
				} else {
					changed[cur] = append(changed[cur], lineRange{c, c + d - 1})
				}
				break
			}
		}
	}
	return changed, nil
}

func gitShow(root, base, relPath string) []byte {
	out, err := exec.Command("git", "-C", root, "show", base+":"+gitPrefix(root)+relPath).Output()
	if err != nil {
		return nil // new file at base, or not tracked
	}
	return out
}

func touched(changed map[string][]lineRange, file string, span grove.SpanInfo) bool {
	for _, r := range changed[file] {
		if r.start <= span.End && r.end >= span.Start {
			return true
		}
	}
	return false
}

func lineTouched(changed map[string][]lineRange, file string, line int) bool {
	for _, r := range changed[file] {
		if r.start <= line && line <= r.end {
			return true
		}
	}
	return false
}

// missedSite is one required-but-untouched change site.
type missedSite struct {
	Symbol        string `json:"symbol"`
	QualifiedName string `json:"qualifiedName"`
	File          string `json:"file"`
	Line          int    `json:"line"`
	Kind          string `json:"kind"`
	BecauseOf     string `json:"becauseOf"` // the signature change that requires this site
	Detail        string `json:"detail"`
}

func (h *Handler) toolVerify(ctx context.Context, args map[string]any) (any, error) {
	base := stringArg(args, "base", "HEAD")
	changed, err := gitChangedRanges(h.Root, base)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return map[string]any{"verdict": "clean", "base": base,
			"note": "no changes vs " + base}, nil
	}
	changedFiles := make([]string, 0, len(changed))
	for f := range changed {
		changedFiles = append(changedFiles, f)
	}
	sort.Strings(changedFiles)

	// 1) Which changed symbols altered their contract? Diff each changed
	// file's base version (parsed in memory) against the current index.
	type seed struct {
		sym    grove.SymbolRecord
		reason string
	}
	var seeds []seed
	var unverifiedSeeds []string
	addSeed := func(sym grove.SymbolRecord, reason string) {
		switch sym.Kind {
		case "function", "method", "constructor":
			seeds = append(seeds, seed{sym, reason})
		case "const", "variable", "field", "document", "file",
			"decorator", "annotation":
			// No call-shaped blast radius; renames are rename_plan's job.
		default:
			// interface/struct/class/type contract changes: real blast
			// radius, no automated per-member verification yet — surfaced
			// as unverified, never silently passed.
			unverifiedSeeds = append(unverifiedSeeds,
				fmt.Sprintf("%s %s (%s) — automated verification not implemented for this kind; review its references", sym.Kind, displayQN(sym), reason))
		}
	}
	for _, f := range changedFiles {
		baseContent := gitShow(h.Root, base, f)
		if baseContent == nil {
			continue // new file: nothing had callers under the old contract
		}
		before, err := h.Grove.PreviewFileSymbols(f, baseContent)
		if err != nil {
			continue // unsupported file type
		}
		fd, err := h.Grove.DiffFile(ctx, before, f)
		if err != nil {
			continue
		}
		for _, c := range fd.Changed {
			if c.SignatureChanged && c.After != nil {
				addSeed(*c.After, "signature of "+displayQN(*c.After)+" changed")
				continue
			}
			// A pure declaration block (Go/TS interface, type alias) holds
			// member SIGNATURES as its body — its members are not separate
			// symbols, so a member's signature change surfaces only as a
			// body change on the block. That IS a contract change; routing
			// it through addSeed lands it in unverifiedSeeds -> "review"
			// (measured: a mutated Driver.ts interface member passed
			// verify as "complete" without this — every implementation of
			// the member was a required site, none flagged).
			if c.BodyChanged && c.After != nil {
				switch c.After.Kind {
				case "interface", "type", "trait", "protocol":
					addSeed(*c.After, "declaration block "+displayQN(*c.After)+" changed (member contracts live in its body)")
				}
			}
		}
		for _, c := range fd.Renamed {
			if c.After != nil {
				addSeed(*c.After, displayQN(*c.After)+" renamed")
			}
		}
	}

	// 2) For each contract change, the engine computes the required set;
	// anything the diff did not touch is a missed site. Line-precise where
	// the caller's AST call sites name the seed; span-level otherwise.
	var missed []missedSite
	var sigChanges []map[string]any
	var notes []string
	seen := map[string]bool{}
	for _, sd := range seeds {
		sigChanges = append(sigChanges, map[string]any{
			"symbol": displayQN(sd.sym), "file": sd.sym.FilePath, "line": sd.sym.Span.Start,
			"reason": sd.reason,
		})
		impact, err := h.changeImpactFor(ctx, sd.sym)
		if err != nil {
			// FAIL CLOSED: a contract change whose blast radius could not be
			// computed must not read as "complete".
			unverifiedSeeds = append(unverifiedSeeds,
				displayQN(sd.sym)+" — impact could not be computed: "+err.Error())
			continue
		}
		if len(impact.Family)+len(impact.Callers)+len(impact.DeclaringTypes) == 0 {
			// FAIL CLOSED: an EMPTY blast radius for a signature change is
			// almost always the edit itself severing resolution — under
			// signature-sensitive binding (Java/TS), overrides and callers of
			// the OLD contract no longer match the NEW signature, so the
			// post-edit graph honestly reports nobody depending on it
			// (measured: a mutated SettableBeanProperty.set returned
			// family=0 callers=0 while 22 real sites depended on the old
			// contract). A genuinely uncalled method lands here too; review
			// is the honest verdict for both.
			unverifiedSeeds = append(unverifiedSeeds,
				displayQN(sd.sym)+" — the post-edit graph shows no overrides, callers, or declaring "+
					"types for the NEW signature; dependents of the OLD signature cannot be enumerated "+
					"from this graph (or the method is uncalled) — review its old-contract dependents manually")
			continue
		}
		if impact.Completeness != "" && impact.Completeness != "closed" {
			notes = append(notes, displayQN(sd.sym)+": impact completeness is "+impact.Completeness)
		}
		required := make([]grove.SymbolRecord, 0,
			len(impact.Family)+len(impact.Callers)+len(impact.DeclaringTypes))
		required = append(required, impact.Family...)
		required = append(required, impact.Callers...)
		required = append(required, impact.DeclaringTypes...)
		for _, site := range required {
			if site.FilePath == sd.sym.FilePath && site.Span.Start == sd.sym.Span.Start {
				continue // the seed itself
			}
			// Line-precise: the caller's recorded call lines naming the seed.
			var callLines []int
			for _, cs := range site.CallSites {
				if cs.Callee == sd.sym.Name || strings.HasSuffix(cs.Callee, "."+sd.sym.Name) {
					callLines = append(callLines, cs.Line)
				}
			}
			if len(callLines) > 0 {
				for _, ln := range callLines {
					if !lineTouched(changed, site.FilePath, ln) {
						key := fmt.Sprintf("%s:%d:%s", site.FilePath, ln, sd.sym.Name)
						if seen[key] {
							continue
						}
						seen[key] = true
						missed = append(missed, missedSite{
							Symbol: site.Name, QualifiedName: displayQN(site),
							File: site.FilePath, Line: ln, Kind: site.Kind,
							BecauseOf: sd.reason,
							Detail:    fmt.Sprintf("calls %s at line %d — line not touched by the diff", sd.sym.Name, ln),
						})
					}
				}
				continue
			}
			// Span-level fallback (no AST call sites recorded).
			if !touched(changed, site.FilePath, site.Span) {
				key := fmt.Sprintf("%s:%d:%s", site.FilePath, site.Span.Start, sd.sym.Name)
				if seen[key] {
					continue
				}
				seen[key] = true
				missed = append(missed, missedSite{
					Symbol: site.Name, QualifiedName: displayQN(site),
					File: site.FilePath, Line: site.Span.Start, Kind: site.Kind,
					BecauseOf: sd.reason,
					Detail:    "in the required change set — file region not touched by the diff",
				})
			}
		}
	}
	sort.Slice(missed, func(i, j int) bool {
		if missed[i].File != missed[j].File {
			return missed[i].File < missed[j].File
		}
		return missed[i].Line < missed[j].Line
	})

	// 3) Tests that must run for this diff.
	var affectedTests []map[string]any
	if tests, err := h.Grove.AffectedTests(ctx, changedFiles); err == nil {
		for _, t := range tests {
			affectedTests = append(affectedTests, map[string]any{
				"name": displayQN(t), "file": t.FilePath, "line": t.Span.Start})
		}
	}

	// 4) Cross-component dependency CANDIDATES: induced edges whose every
	// evidence site sits inside code this diff touched (line-level, not
	// file-level — a pre-existing edge elsewhere in a touched file must not
	// fire). No base-graph comparison is performed, so these are candidates
	// for review, never asserted as "introduced".
	symbols, edges, err := h.Grove.SnapshotGraph(ctx)
	var newDeps []map[string]any
	archStatus := "no-rules"
	var archIntroduced []view.Violation
	if err == nil {
		v := view.Build(symbols, edges, view.Options{MaxSites: 1 << 30})
		for _, e := range v.Edges {
			all := len(e.Sites) > 0
			for _, s := range e.Sites {
				if !touched(changed, s.FromFile, grove.SpanInfo{Start: s.FromLine, End: s.FromLine}) {
					all = false
					break
				}
			}
			if all {
				sites := e.Sites
				if len(sites) > 3 {
					sites = sites[:3]
				}
				newDeps = append(newDeps, map[string]any{
					"from": e.From, "to": e.To, "weight": e.Weight,
					"minTier": view.MinTier(e.Tiers), "sites": sites,
					"caveat": "candidate — all evidence is in changed code; no base-graph comparison performed",
				})
			}
		}
		if len(h.Cfg.ArchDeny) > 0 {
			if rules, rerr := view.ParseRules(h.Cfg.ArchDeny); rerr == nil {
				archStatus = "pass"
				for _, viol := range v.CheckRules(rules) {
					// Only violations this diff touches; pre-existing debt is
					// prism arch's job, not the diff verdict's.
					introduced := false
					for _, s := range viol.Edge.Sites {
						if lineTouched(changed, s.FromFile, s.FromLine) || touched(changed, s.FromFile, grove.SpanInfo{Start: s.FromLine, End: s.FromLine}) {
							introduced = true
							break
						}
					}
					if introduced {
						archIntroduced = append(archIntroduced, viol)
						if viol.MinTier != "heuristic" {
							archStatus = "fail"
						} else if archStatus == "pass" {
							archStatus = "review"
						}
					}
				}
			}
		}
	}

	// Verdict, fail-closed: incomplete beats review beats complete. A
	// contract change we could not verify can never yield "complete".
	verdict := "complete"
	switch {
	case len(missed) > 0 || archStatus == "fail":
		verdict = "incomplete"
	case len(unverifiedSeeds) > 0:
		verdict = "review"
	}
	h.Ledger.RecordCall("prism_verify")
	return map[string]any{
		"verdict":          verdict,
		"base":             base,
		"changedFiles":     changedFiles,
		"signatureChanges": sigChanges,
		"missedSites":      missed,
		"unverifiedSeeds":  unverifiedSeeds,
		"affectedTests":    affectedTests,
		"newDependencies":  newDeps,
		"archStatus":       archStatus,
		"archIntroduced":   archIntroduced,
		"notes":            notes,
	}, nil
}

// changeImpactFor resolves the required change set for a symbol. Methods go
// through change_impact (override family + callers, completeness-reported);
// bare functions fall back to the resolved caller set, reported as
// completeness "callers-only".
func (h *Handler) changeImpactFor(ctx context.Context, sym grove.SymbolRecord) (*grove.ChangeImpactResult, error) {
	var candidates []string
	if sym.QualifiedName != "" && strings.Contains(sym.QualifiedName, ".") {
		candidates = append(candidates, sym.QualifiedName)
	}
	if sym.ParentSymbol != "" {
		candidates = append(candidates, sym.ParentSymbol+"."+sym.Name)
	}
	candidates = append(candidates, sym.Name)
	var lastErr error
	for _, q := range candidates {
		r, err := h.Grove.ChangeImpact(ctx, q)
		if err == nil && len(r.Declarations) > 0 {
			return r, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	// Bare function (change_impact wants Type.method): the required set is
	// its resolved callers. Zero callers with a clean resolution is a
	// trivially complete set, not a failure.
	callers, err := h.Grove.Callers(ctx, sym.Name)
	if err == nil {
		return &grove.ChangeImpactResult{
			Query:        sym.Name,
			Declarations: []grove.SymbolRecord{sym},
			Callers:      callers,
			Completeness: "callers-only",
		}, nil
	}
	if lastErr == nil {
		lastErr = err
	}
	return nil, lastErr
}

func displayQN(s grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

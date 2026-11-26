package core

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// verifySignatureChanges is the signatureChanges report for the seeds.
// Whole-file deletions collapse to ONE rendered line per file. The
// per-symbol seeds still run through impact analysis unchanged — only the
// sigChanges REPORT is grouped: measured 2026-09-02 (6rqii7zt #88), a
// deleted 34-symbol directory produced ~30 near-identical "X removed with
// file Y" lines in a 13.9k-char verify response, and the agent dismissed the
// whole block in one sentence. A report shape the reader skips is noise even
// when every line is true.
func verifySignatureChanges(seeds []verifySeed) []map[string]any {
	var sigChanges []map[string]any
	deletedFileSyms := map[string]int{}
	for _, sd := range seeds {
		if strings.Contains(sd.reason, " removed with file ") {
			deletedFileSyms[sd.sym.FilePath]++
			continue
		}
		sigChanges = append(sigChanges, map[string]any{
			"symbol": displayQN(sd.sym), "file": sd.sym.FilePath, "line": sd.sym.Span.Start,
			"reason": sd.reason,
		})
	}
	delFiles := make([]string, 0, len(deletedFileSyms))
	for f := range deletedFileSyms {
		delFiles = append(delFiles, f)
	}
	sort.Strings(delFiles)
	for _, f := range delFiles {
		sigChanges = append(sigChanges, map[string]any{
			"file":   f,
			"reason": fmt.Sprintf("file deleted — %d symbol(s) removed with it", deletedFileSyms[f]),
		})
	}
	return sigChanges
}

// verifyCheckSeeds runs the per-kind checks: for each contract change, the
// engine computes the required set; anything the diff did not touch is a
// missed site. Line-precise where the caller's AST call sites name the seed;
// span-level otherwise. Members added to interfaces are checked against
// their implementations. The missed sites come out sorted by file and line.
func (s *verifyState) verifyCheckSeeds(ctx context.Context, changedFiles, deletedFiles []string) {
	baseImpacts, baseFailures := s.verifyBaseImpacts(ctx, changedFiles, deletedFiles)
	baseSites := newBaseSiteProjector(s.h, s.changed, deletedFiles)
	for seedIndex, sd := range s.seeds {
		var bc *grove.ChangeImpactResult
		if seedIndex < len(baseImpacts) {
			bc = baseImpacts[seedIndex]
		}
		if seedIndex < len(baseFailures) && baseFailures[seedIndex] != "" {
			s.unverifiedSeeds = append(s.unverifiedSeeds, displayQN(sd.sym)+": base impact unresolved: "+baseFailures[seedIndex])
		}
		s.verifyCheckContractSeed(ctx, sd, bc, baseSites)
	}
	for _, am := range s.addedMembers {
		s.verifyCheckAddedMember(ctx, am)
	}
	sort.Slice(s.missed, func(i, j int) bool {
		if s.missed[i].File != s.missed[j].File {
			return s.missed[i].File < s.missed[j].File
		}
		return s.missed[i].Line < s.missed[j].Line
	})
}

// verifyBaseImpacts reconstructs the base graph once, restoring every
// changed/deleted file, and computes each seed's base-contract impact.
// Name/signature matches cannot establish a type family.
func (s *verifyState) verifyBaseImpacts(ctx context.Context, changedFiles, deletedFiles []string) ([]*grove.ChangeImpactResult, []string) {
	if len(s.seeds) == 0 {
		return nil, nil
	}
	files := make(map[string][]byte, len(changedFiles)+len(deletedFiles))
	for _, f := range append(append([]string{}, changedFiles...), deletedFiles...) {
		files[f] = gitShow(s.h.Root, s.base, f)
	}
	queries := make([][2]string, len(s.seeds))
	for i, sd := range s.seeds {
		sym := sd.sym
		if sd.before != nil {
			sym = *sd.before
		}
		queries[i] = [2]string{displayQN(sym), sym.FilePath}
	}
	baseImpacts, baseFailures, previewErr := s.h.Grove.PreviewChangeImpacts(ctx, queries, files)
	if previewErr != nil {
		s.unverifiedSeeds = append(s.unverifiedSeeds, "base-contract coverage unavailable: "+previewErr.Error())
	}
	return baseImpacts, baseFailures
}

// verifyCheckContractSeed checks one function/method/constructor seed: stale
// references to an old name, then every site of its required set the diff
// did not touch. bc is the seed's base-contract impact, when known.
func (s *verifyState) verifyCheckContractSeed(ctx context.Context, sd verifySeed, bc *grove.ChangeImpactResult, baseSites *baseSiteProjector) {
	impact, err := s.h.changeImpactFor(ctx, sd.sym)
	s.verifyCheckStaleRefs(ctx, sd)
	if err != nil {
		if bc == nil {
			// FAIL CLOSED: a contract change whose blast radius could not
			// be computed must not read as "complete".
			s.unverifiedSeeds = append(s.unverifiedSeeds,
				displayQN(sd.sym)+" — impact could not be computed: "+err.Error())
			return
		}
		impact = bc
		s.notes = append(s.notes, displayQN(sd.sym)+": required set enumerated from the BASE contract (old-signature family + callers)")
	} else if obligationSiteCount(impact) == 0 {
		if bc == nil {
			// FAIL CLOSED: empty blast radius, and the base contract was
			// not recoverable either (no before-symbol, unparsable
			// signature, or genuinely uncalled) — review, never a pass.
			s.unverifiedSeeds = append(s.unverifiedSeeds,
				displayQN(sd.sym)+" — the post-edit graph shows no overrides, callers, or declaring "+
					"types for the NEW signature; dependents of the OLD signature cannot be enumerated "+
					"from this graph (or the method is uncalled) — review its old-contract dependents manually")
			return
		}
		impact = bc
		s.notes = append(s.notes, displayQN(sd.sym)+": required set enumerated from the BASE contract (old-signature family + callers)")
	}
	if impact.Completeness != "" && impact.Completeness != "closed" {
		s.notes = append(s.notes, displayQN(sd.sym)+": impact completeness is "+impact.Completeness)
	}
	if coverage, note := impactCoverage(impact); coverage == "partial" || impactCallerCoverage(impact) == "partial" {
		s.unverifiedSeeds = append(s.unverifiedSeeds, displayQN(sd.sym)+": "+note)
	}
	if impact.HasHeuristicRefs {
		s.notes = append(s.notes, displayQN(sd.sym)+": required set includes name-derived "+
			"(framework template/query) references — probably right, not certain")
	}
	if sd.callsStayValid {
		for _, ref := range s.h.goFuncValueRefs(ctx, sd.sym) {
			if lineTouched(s.changed, ref.File, ref.Line) {
				continue
			}
			key := fmt.Sprintf("%s:%d:%s", ref.File, ref.Line, sd.sym.Name)
			if !s.seen[key] {
				s.seen[key] = true
				s.missed = append(s.missed, ref)
			}
		}
	}
	var required []grove.SymbolRecord
	if impact != bc {
		required = impactSites(impact, false)
	}
	// Augment a non-empty post-edit set with base-contract survivors the
	// severed graph no longer links to the seed (Java overload families:
	// measured 3/30 file catch on jackson-serialize without this).
	if bc != nil {
		projected, gaps := baseSites.project(ctx, bc, sd.sym.Name)
		required = append(required, projected...)
		s.unverifiedSeeds = append(s.unverifiedSeeds, gaps...)
	}
	for _, site := range required {
		s.verifyCheckRequiredSite(sd, impact, site)
	}
}

// verifyCheckStaleRefs reports stale references to a renamed seed's old name.
//
// RENAMES: the post-edit graph cannot see stale callers of a name that no
// longer resolves. Measured 2026-08-25 (electrum seed_type->calc_seed_type,
// 4 call sites reverted): verify printed "calc_seed_type renamed" and then
// "no missed sites". The gate was structurally blind to the shape
// rename_plan exists for. Stale references to the OLD name ARE recoverable
// from the reference layer, which is name-based rather than
// resolution-based.
func (s *verifyState) verifyCheckStaleRefs(ctx context.Context, sd verifySeed) {
	for _, st := range s.h.staleOldNameRefs(ctx, sd.sym, sd.before) {
		// One physical line, one finding. Every member of a renamed
		// TYPE seeds separately (networkx: __call__, _convert_and_call,
		// …), so without this the same decorator line was reported 32
		// times for 3 real sites — a gate that inflates its own output
		// teaches people to ignore it.
		key := st.file + ":" + strconv.Itoa(st.line) + ":" + st.oldName
		if s.staleSeen[key] {
			continue
		}
		s.staleSeen[key] = true
		name := st.enclosing
		if name == "" {
			name = st.oldName
		}
		s.missed = append(s.missed, missedSite{
			Symbol: st.oldName, QualifiedName: name,
			File: st.file, Line: st.line, Kind: "stale-reference",
			BecauseOf: displayQN(sd.sym),
			Detail: "still references the old name " + st.oldName +
				" — renamed to " + st.newName,
		})
	}
}

// verifyCheckRequiredSite reports one required-set site of a seed when the
// diff did not touch it.
func (s *verifyState) verifyCheckRequiredSite(sd verifySeed, impact *grove.ChangeImpactResult, site grove.SymbolRecord) {
	if site.FilePath == sd.sym.FilePath && site.Span.Start == sd.sym.Span.Start {
		return // the seed itself
	}
	// Line-precise: the caller's recorded call lines naming the seed.
	var callLines []int
	for _, cs := range site.CallSites {
		if cs.Callee == sd.sym.Name || strings.HasSuffix(cs.Callee, "."+sd.sym.Name) {
			callLines = append(callLines, cs.Line)
		}
	}
	if len(callLines) > 0 {
		if sd.callsStayValid {
			return
		}
		for _, ln := range callLines {
			if !lineTouched(s.changed, site.FilePath, ln) {
				key := fmt.Sprintf("%s:%d:%s", site.FilePath, ln, sd.sym.Name)
				if s.seen[key] {
					continue
				}
				s.seen[key] = true
				s.missed = append(s.missed, missedSite{
					Symbol: site.Name, QualifiedName: displayQN(site),
					File: site.FilePath, Line: ln, Kind: site.Kind,
					BecauseOf: sd.reason,
					Detail:    fmt.Sprintf("calls %s at line %d — line not touched by the diff", sd.sym.Name, ln),
				})
			}
		}
		return
	}
	if sd.callsStayValid && valueOnlyCaller(impact, site) {
		// A caller where no call names the seed holds it as a value,
		// and goFuncValueRefs above decided those (a typed slot
		// breaks, `f := Do` does not). Contract members still fall
		// through: an interface's Do no longer matches.
		return
	}
	// Span-level fallback (no AST call sites recorded).
	if !touched(s.changed, site.FilePath, site.Span) {
		key := fmt.Sprintf("%s:%d:%s", site.FilePath, site.Span.Start, sd.sym.Name)
		if s.seen[key] {
			return
		}
		s.seen[key] = true
		s.missed = append(s.missed, missedSite{
			Symbol: site.Name, QualifiedName: displayQN(site),
			File: site.FilePath, Line: site.Span.Start, Kind: site.Kind,
			BecauseOf: sd.reason,
			Detail:    "in the required change set — file region not touched by the diff",
		})
	}
}

// verifyCheckAddedMember reports every type that does not implement a member
// added to an interface.
func (s *verifyState) verifyCheckAddedMember(ctx context.Context, am verifyAddedMember) {
	query := displayQN(am.iface) + "." + am.name
	because := "member " + query + " added to interface " + displayQN(am.iface)
	mi, err := s.h.Grove.MissingImplementations(ctx, query)
	if err != nil || mi == nil {
		reason := "no answer"
		if err != nil {
			reason = err.Error()
		}
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			fmt.Sprintf("interface %s (%s) — missing-implementations could not be computed (%s); review its implementations", displayQN(am.iface), because, strings.SplitN(reason, "\n", 2)[0]))
		return
	}
	for _, t := range mi.Missing {
		s.missed = append(s.missed, missedSite{
			Symbol: t.Name, QualifiedName: displayQN(t),
			File: t.FilePath, Line: t.Span.Start, Kind: t.Kind,
			BecauseOf: because,
			Detail:    "does not implement " + query + "; the compiler rejects every use of it as " + displayQN(am.iface),
		})
	}
	for _, t := range mi.Unverifiable {
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			fmt.Sprintf("%s %s (%s) — its implementation may come from outside the index; check it implements %s", t.Kind, displayQN(t), because, query))
	}
}

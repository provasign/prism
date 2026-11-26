package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// verifySeed is one changed symbol whose contract changed.
type verifySeed struct {
	sym    grove.SymbolRecord
	before *grove.SymbolRecord // base-side symbol (old contract), when known
	reason string
	// callsStayValid: the edit only added a trailing variadic parameter
	// to a Go function, so direct calls compile unchanged. Interface
	// declarations, other implementations and value uses still count.
	callsStayValid bool
}

// verifyAddedMember is a member added to an interface's body. Every type
// meant to implement the interface must add them; missing-implementations
// names the ones that do not (2026-11-22, BUG-03: these used to stay
// "verification not implemented for this kind").
type verifyAddedMember struct {
	iface grove.SymbolRecord
	name  string
}

// verifyState carries the state one prism_verify run shares between its
// seed stage, its per-kind checks and its report.
type verifyState struct {
	h       *Handler
	base    string
	changed map[string][]lineRange

	seeds             []verifySeed
	unverifiedSeeds   []string
	addedMembers      []verifyAddedMember
	contentAdvisories []string
	// testCoverage answers a DIFFERENT question than the seeds above: not
	// "did every caller of a changed CONTRACT get updated" but "does a
	// verified test exercise the function/method whose BODY changed" — a
	// pure body edit (the common shape of a bug fix) never becomes a seed
	// at all (addSeed only fires on signature change/rename/removal/member-
	// contract extraction), so it was previously invisible to verify.
	// Measured (2026-09-23, 12-task hard-core-failure review): every
	// unsolved task was a body-only change, and the agent's own test run
	// was the thing that gave a false pass — this is informational, not
	// gating (never affects verdict), so a coverage miss on a change this
	// can't resolve (e.g. a new file, a symbol change_impact can't scope)
	// degrades to a silent skip, never a wrong verdict.
	testCoverage []testCoverageEntry

	missed    []missedSite
	notes     []string
	seen      map[string]bool
	staleSeen map[string]bool
}

// verifyCollectSeeds is the seed stage: which changed symbols altered their
// contract? Diff each changed file's base version (parsed in memory) against
// the current index, then treat every symbol of a deleted file as removed.
func (s *verifyState) verifyCollectSeeds(ctx context.Context, changedFiles, deletedFiles []string) {
	for _, f := range changedFiles {
		s.verifySeedChangedFile(ctx, f)
	}

	// Deleted files: every symbol they declared is a removed contract. These
	// files never appear in `changed` (their after-side is /dev/null), so
	// without this a deletion — the most destructive contract change there
	// is — was invisible to the gate. Also covers the old path of a move.
	for _, f := range deletedFiles {
		s.verifySeedDeletedFile(f)
	}
}

// verifyAddSeed records a contract change. Symbols in test files are NOT
// contracts: a deleted test function has no callers that must migrate, yet
// each one used to surface as "review its old-contract dependents manually" —
// measured on a real diff, 3 deleted test funcs produced 3 of 7 flags with
// zero actionable content. A gate that mostly cries wolf gets clicked past.
func (s *verifyState) verifyAddSeed(sym grove.SymbolRecord, before *grove.SymbolRecord, reason string) {
	if isTestFilePath(sym.FilePath) {
		return
	}
	// A Java anonymous class is a local implementation, not a named
	// contract. Its synthetic <anonymous@line:col> identity changes when
	// code moves, which looks like a removal/rename and falsely marks
	// unrelated interface callers as missed sites. Changes to the
	// interface itself still seed their own impact check.
	if isSyntheticAnonymousJavaSymbol(sym) {
		return
	}
	switch sym.Kind {
	case "function", "method", "constructor":
		s.seeds = append(s.seeds, verifySeed{sym: sym, before: before, reason: reason})
	case "document", "file":
		// Not code; no contract to break.
	case "const", "variable", "field", "decorator", "annotation":
		// No call-shaped blast radius, so change_impact cannot compute a
		// required set — but a renamed/removed exported const or field
		// DOES break its references. Silently dropping these produced a
		// false "complete" (measured: renaming an exported const with a
		// stale reference left behind verified clean, exit 0). Surface
		// them as unverified so the verdict degrades to "review".
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			fmt.Sprintf("%s %s (%s) — no call-shaped blast radius to verify; check references with prism_search scope=text exhaustive=true for %s",
				sym.Kind, displayQN(sym), reason, sym.Name))
	default:
		// interface/struct/class/type contract changes: real blast
		// radius, no automated per-member verification yet — surfaced
		// as unverified, never silently passed.
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			fmt.Sprintf("%s %s (%s) — automated verification not implemented for this kind; review its references", sym.Kind, displayQN(sym), reason))
	}
}

// verifySeedChangedFile diffs one changed file's base version against the
// index and seeds its contract changes, renames and removals.
func (s *verifyState) verifySeedChangedFile(ctx context.Context, f string) {
	h := s.h
	baseContent := gitShow(h.Root, s.base, f)
	if baseContent == nil {
		return // new file: nothing had callers under the old contract
	}
	// Unsupported file types return (nil, nil) from the parser; an error
	// here is a real analysis failure. A changed file whose contracts
	// could not be diffed must degrade the verdict, not silently pass.
	before, err := h.Grove.PreviewFileSymbols(f, baseContent)
	if err != nil {
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			f+" — base version could not be parsed ("+err.Error()+"); its contract changes were NOT checked")
		return
	}
	fd, err := h.Grove.DiffFile(ctx, before, f)
	if err != nil {
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			f+" — contract diff failed ("+err.Error()+"); its contract changes were NOT checked")
		return
	}
	for _, c := range fd.Changed {
		s.verifySeedChange(f, c)
	}
	s.verifyTestCoverageFor(ctx, f, fd.Changed)
	for _, c := range fd.Renamed {
		if c.After != nil {
			s.verifyAddSeed(*c.After, c.Before, displayQN(*c.After)+" renamed")
		}
	}
	// REMOVED symbols are contract changes too — the strongest kind. A
	// deleted method breaks every caller, yet fd.Removed was never read,
	// so deleting a called method verified "complete" with exit 0
	// (measured). The removed symbol carries the OLD contract, so it is
	// its own "before": change_impact resolves the callers that must
	// change.
	for _, rem := range fd.Removed {
		removed := rem
		s.verifyAddSeed(removed, &removed, displayQN(removed)+" removed")
	}
}

// verifySeedChange seeds one changed symbol of a changed file.
func (s *verifyState) verifySeedChange(f string, c grove.SymbolChange) {
	if goUnexportedStringConstValueOnly(c.Before, c.After) {
		s.contentAdvisories = append(s.contentAdvisories,
			fmt.Sprintf("%s:%d %s — string content changed; verify does not assess its behavior; run relevant tests",
				c.After.FilePath, c.After.Span.Start, displayQN(*c.After)))
		return
	}
	if c.SignatureChanged && c.After != nil {
		s.verifySeedSignatureChange(f, c)
		return
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
			s.verifySeedDeclarationBlock(f, c)
		}
	}
}

// verifySeedSignatureChange seeds a symbol whose signature line changed.
func (s *verifyState) verifySeedSignatureChange(f string, c grove.SymbolChange) {
	// Go callers are still valid when the only signature edit adds
	// an optional trailing variadic parameter, so requiring every
	// old call to change produced false missed sites. The change is
	// still a contract change: a method must still match its
	// interface, and a function used as a value has a new type.
	// Keep the seed and exempt only direct call lines.
	if c.Before != nil && strings.HasSuffix(f, ".go") &&
		goAddedTrailingVariadic(c.Before.Signature, c.After.Signature) {
		n := len(s.seeds)
		s.verifyAddSeed(*c.After, c.Before, "optional trailing variadic parameter added to "+
			displayQN(*c.After)+"; direct calls stay valid")
		if len(s.seeds) > n {
			s.seeds[n].callsStayValid = true
		}
		return
	}
	// GAP-04: a one-line interface (`type I interface{ A() }`)
	// that gains a member changes its signature line, not its
	// body, so it skipped the added-member check below and
	// stayed UNVERIFIED. Diff its members here too.
	if c.After.Kind == "interface" && c.Before != nil && !isTestFilePath(c.After.FilePath) {
		beforeBlock := splitInlineBody(blockText(s.h.Root, s.base, f, c.Before, true))
		afterBlock := splitInlineBody(blockText(s.h.Root, "", f, c.After, false))
		added, removed := memberAdditionsRemovals(beforeBlock, afterBlock)
		if len(added) > 0 && len(removed) == 0 && len(memberContractChanges(beforeBlock, afterBlock)) == 0 {
			for _, name := range added {
				s.addedMembers = append(s.addedMembers, verifyAddedMember{iface: *c.After, name: name})
			}
			return
		}
	}
	// "signature of X changed" is wrong for a const whose VALUE
	// changed; say what actually happened per kind.
	word := "signature"
	switch c.After.Kind {
	case "const", "variable", "field":
		word = "declaration"
	}
	s.verifyAddSeed(*c.After, c.Before, word+" of "+displayQN(*c.After)+" changed")
}

// verifySeedDeclarationBlock seeds a declaration block (interface, type,
// trait, protocol) whose body changed.
//
// Member-level extraction: line-diff the block's before/after text and turn
// each member whose parameter list changed into a synthesized method seed —
// the normal pipeline (base-contract enumeration) then finds every
// implementation and caller of the OLD member contract. Falls back to the
// unverified-block seed when nothing extractable changed (fail-closed,
// measured: typeorm held at review/0% without this).
func (s *verifyState) verifySeedDeclarationBlock(f string, c grove.SymbolChange) {
	beforeBlock := blockText(s.h.Root, s.base, f, c.Before, true)
	afterBlock := blockText(s.h.Root, "", f, c.After, false)
	members := memberContractChanges(beforeBlock, afterBlock)
	added, removed := memberAdditionsRemovals(beforeBlock, afterBlock)
	if !isTestFilePath(c.After.FilePath) && c.After.Kind == "interface" {
		for _, name := range added {
			s.addedMembers = append(s.addedMembers, verifyAddedMember{iface: *c.After, name: name})
		}
	} else {
		added = nil
	}
	if len(removed) > 0 {
		s.verifyAddSeed(*c.After, c.Before, "member(s) "+strings.Join(removed, ", ")+" removed from declaration block "+displayQN(*c.After))
	}
	if len(members) == 0 {
		if len(added) == 0 && len(removed) == 0 {
			s.verifyAddSeed(*c.After, c.Before, "declaration block "+displayQN(*c.After)+" changed (member contracts live in its body)")
		}
		return
	}
	for _, m := range members {
		after := *c.After
		after.Name = m.name
		after.QualifiedName = displayQN(*c.After) + "." + m.name
		after.Kind = "method"
		after.Signature = m.afterSig
		beforeSym := after
		beforeSym.Signature = m.beforeSig
		s.verifyAddSeed(after, &beforeSym,
			"member "+after.QualifiedName+" of declaration block changed")
	}
}

// verifyTestCoverageFor is the test-coverage check: every changed
// function/method/constructor, contract-changed or not (this is the whole
// point — body-only changes are excluded from `seeds`). Non-fatal by design:
// a lookup failure (ambiguous name, unscoped symbol) just skips that entry
// rather than degrading the verdict, since this signal has never been
// validated as gating-safe the way the seed/impact logic has.
func (s *verifyState) verifyTestCoverageFor(ctx context.Context, f string, changes []grove.SymbolChange) {
	for _, c := range changes {
		if c.After == nil || isTestFilePath(c.After.FilePath) {
			continue
		}
		switch c.After.Kind {
		case "function", "method", "constructor":
		default:
			continue
		}
		// Module-level pseudo-symbols ("<top-level>", "<module>") are how
		// some parsers attribute import/constant edits; they are not a
		// function a test could call, and a warning on one sends the agent
		// hunting for coverage that cannot exist (seen on the first probe).
		if strings.HasPrefix(c.After.Name, "<") {
			continue
		}
		name := c.After.QualifiedName
		if name == "" {
			name = c.After.Name
		}
		entry := testCoverageEntry{symbol: displayQN(*c.After), file: f, line: c.After.Span.Start}
		if r, err := s.h.Grove.ChangeImpactScoped(ctx, name, f); err == nil {
			entry.checked = true
			for _, caller := range r.Callers {
				if isVerifiedTestCaller("/" + filepath.ToSlash(caller.FilePath)) {
					entry.coveredBy = append(entry.coveredBy,
						fmt.Sprintf("%s:%d", caller.FilePath, caller.Span.Start))
				}
			}
		}
		if entry.checked {
			s.testCoverage = append(s.testCoverage, entry)
		}
	}
}

// verifySeedDeletedFile seeds every symbol a deleted file declared.
func (s *verifyState) verifySeedDeletedFile(f string) {
	baseContent := gitShow(s.h.Root, s.base, f)
	if baseContent == nil {
		return
	}
	gone, err := s.h.Grove.PreviewFileSymbols(f, baseContent)
	if err != nil {
		// Unsupported file type: nothing to parse, but a deletion we
		// could not analyze must not read as verified.
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			f+" was deleted and could not be parsed — review its references manually")
		return
	}
	for _, sym := range gone {
		sym := sym
		s.verifyAddSeed(sym, &sym, displayQN(sym)+" removed with file "+f)
	}
}

package mcp

// Hypothesis ledger: session-scoped evidence that an agent's READING of a
// task term is wrong — surfaced as an additive scope note, never a stop.
//
// Measured (BACKLOG addendum 2, item 12, 2026-09-03): on the unanswerable
// "Remove triple parameter" cell, 59% of a $3.09 session was spent AFTER
// search ended — the agent confabulated a reading ("triple parameter" =
// the 3-arg constructor) and paid to make the fabricated change build.
// The preventing signal was already in prism's own session by the halfway
// mark: 5 same-stem negative searches plus 5 change_impact results all
// "completeness: closed" with <=6 sites, contradicting the task's "the
// change is deliberately wide" head-on. Nothing said so.
//
// The note fires only when BOTH accumulate — >=3 same-stem empties AND
// >=3 closed-small impacts — verified against the benign sibling cell
// (one change_impact total: never triggers) and the pathological one
// (trips at call 16 of 25, before the confabulation pivot). It rides ON
// TOP of the empty-result retry guidance, which stays verbatim: this is
// the counterweight to "keep retrying", not its replacement — it attacks
// the guess, not the persistence.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// scopeNoteEmptyThreshold: same-stem empty searches before the note arms.
	scopeNoteEmptyThreshold = 3
	// scopeNoteClosedThreshold: closed-small change_impact results before it arms.
	scopeNoteClosedThreshold = 3
	// scopeNoteSmallSites: a change_impact at or under this many sites counts
	// as "small" — no wide blast radius.
	scopeNoteSmallSites = 6
)

type hypothesisLedger struct {
	mu          sync.Mutex
	emptyStems  map[string]int      // stem -> count of empty-result searches
	stemTerms   map[string][]string // stem -> example terms searched (capped)
	closedSmall int                 // change_impact results: closed, <=scopeNoteSmallSites sites
	noted       bool                // fire at most once per session
}

var identStemRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]{2,}`)

// stemOf reduces a search term to a comparable lowercase stem: the longest
// identifier-ish token, lowercased. "Triple<", "ImmutableTriple", "Tuple3"
// and "class Triple" all carry the "triple"-family evidence; exact equality
// of stems is deliberately loose (substring containment either way) so the
// family accumulates without a linguistics engine.
func stemOf(term string) string {
	var longest string
	for _, tok := range identStemRe.FindAllString(term, -1) {
		if len(tok) > len(longest) {
			longest = tok
		}
	}
	return strings.ToLower(longest)
}

// recordEmptySearch notes an all-empty search's terms.
func (l *hypothesisLedger) recordEmptySearch(terms []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.emptyStems == nil {
		l.emptyStems = map[string]int{}
		l.stemTerms = map[string][]string{}
	}
	seen := map[string]bool{}
	for _, t := range terms {
		s := stemOf(t)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		// Fold into an existing family when one contains the other
		// (triple / immutabletriple / tuple counts as one family only for
		// containment, not similarity).
		key := s
		for existing := range l.emptyStems {
			if strings.Contains(existing, s) || strings.Contains(s, existing) {
				key = existing
				break
			}
		}
		l.emptyStems[key]++
		if len(l.stemTerms[key]) < 6 {
			l.stemTerms[key] = append(l.stemTerms[key], t)
		}
	}
}

// recordClosedImpact notes a change_impact result's completeness + size.
func (l *hypothesisLedger) recordClosedImpact(completeness string, totalSites int) {
	if completeness != "closed" || totalSites > scopeNoteSmallSites {
		return
	}
	l.mu.Lock()
	l.closedSmall++
	l.mu.Unlock()
}

// scopeNote returns the one-shot contradiction warning when both evidence
// thresholds are met, else "".
func (l *hypothesisLedger) scopeNote() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.noted || l.closedSmall < scopeNoteClosedThreshold {
		return ""
	}
	var stem string
	var n int
	for s, c := range l.emptyStems {
		if c > n {
			stem, n = s, c
		}
	}
	if n < scopeNoteEmptyThreshold {
		return ""
	}
	l.noted = true
	terms := append([]string(nil), l.stemTerms[stem]...)
	sort.Strings(terms)
	return fmt.Sprintf(
		"scope note: %d negative searches on stem %q (%s) and %d change_impact results all "+
			"closed with <=%d sites — nothing in this repo has a wide blast radius matching "+
			"your terms. If the task describes a WIDE change, your reading of the task term "+
			"is probably wrong: restate the term (what else could it name?), don't re-search it.",
		n, stem, strings.Join(terms, ", "), l.closedSmall, scopeNoteSmallSites)
}

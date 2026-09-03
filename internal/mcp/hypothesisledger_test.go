package mcp

import (
	"strings"
	"testing"
)

// The two measured session shapes from BACKLOG addendum 2 item 12, replayed
// against the ledger directly (deterministic, no engine):
//
//   A (pathological, ms-vfs18): 5 same-stem empty searches (Tuple3, Triple<,
//     class Triple, ImmutableTriple, commons.lang3.tuple) + 5 change_impact
//     results all closed with 1-6 sites -> the note MUST fire, once.
//   B (benign, ddtb4dv8): 4 empties on assorted stems + ONE closed impact ->
//     must never fire.
func TestHypothesisLedger_PathologicalSessionTrips(t *testing.T) {
	var l hypothesisLedger
	empties := [][]string{
		{"Tuple3"}, {"Triple<"}, {"class Triple"}, {"ImmutableTriple"}, {"commons.lang3.tuple"},
	}
	impacts := []int{3, 1, 6, 1, 1}
	// scopeNote is one-shot; capture it whenever it first fires (the call
	// sites check after every record, so the test does too).
	note := ""
	for i := 0; i < 5; i++ {
		l.recordEmptySearch(empties[i])
		if n := l.scopeNote(); n != "" {
			if note != "" {
				t.Fatalf("note fired twice (step %d)", i)
			}
			note = n
		}
		l.recordClosedImpact("closed", impacts[i])
	}
	if n := l.scopeNote(); n != "" {
		if note != "" {
			t.Fatal("note fired twice")
		}
		note = n
	}
	if note == "" {
		t.Fatal("pathological session (5 same-stem empties + 5 closed-small impacts) must trip the note")
	}
	for _, want := range []string{"triple", "restate the term", "wide blast radius"} {
		if !strings.Contains(strings.ToLower(note), strings.ToLower(want)) {
			t.Errorf("note missing %q: %s", want, note)
		}
	}
	if again := l.scopeNote(); again != "" {
		t.Error("note must fire at most once per session")
	}
}

func TestHypothesisLedger_BenignSessionNeverTrips(t *testing.T) {
	var l hypothesisLedger
	// B's four empties were on ASSORTED questions; even granting the same
	// stem, only one closed impact ever accumulated.
	for _, terms := range [][]string{
		{`func (g \*CodeGraph) Search`}, {`") Search("`}, {"semDirtyZZ"}, {"grove queryZZ"},
	} {
		l.recordEmptySearch(terms)
	}
	l.recordClosedImpact("closed", 1)
	if n := l.scopeNote(); n != "" {
		t.Fatalf("benign session must not trip the note, got: %q", n)
	}
}

func TestHypothesisLedger_WideImpactDoesNotCount(t *testing.T) {
	var l hypothesisLedger
	for i := 0; i < 5; i++ {
		l.recordEmptySearch([]string{"Triple<"})
		// A WIDE closed result (57 sites) is evidence the term IS right —
		// it must not count toward the contradiction.
		l.recordClosedImpact("closed", 57)
		// Nor does a project-local (not closed) result.
		l.recordClosedImpact("project-local", 2)
	}
	if n := l.scopeNote(); n != "" {
		t.Fatalf("wide/unclosed impacts must not arm the note, got: %q", n)
	}
}

func TestStemOf(t *testing.T) {
	cases := map[string]string{
		"Triple<":                 "triple",
		"ImmutableTriple":         "immutabletriple",
		"class Triple":            "triple", // longest token wins ("Triple" > "class")
		"commons.lang3.tuple":     "commons",
		`") Search("`:             "search",
		"e.Query(":                "query",
		"..":                      "",
	}
	for in, want := range cases {
		if got := stemOf(in); got != want {
			t.Errorf("stemOf(%q) = %q, want %q", in, got, want)
		}
	}
}

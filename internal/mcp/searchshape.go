package mcp

import "fmt"

// searchShape is the one decision about how a search answers. It is computed
// once from the arguments, before any retrieval, and every later step reads
// it instead of re-deriving its own rule from the flags. The table in
// TestSearchShapeTable pins each row.
//
//	request                      context  bodies         fit        notes
//	default                      2        one            budget
//	include_bodies=false         2        none           budget
//	include_bodies=true          2        wideNoPick     budget4k   wide answer, 4k-token fit
//	context c, c <= 15           c        none           bounded*   windows are the answer
//	context c, c > 15            15       wide           bounded*   caller wants the functions; context de-duplicated
//	max_results=n (no context)   2        wide           bounded*   *bounded only when n >= 25, else no fit
//	exhaustive                   0        none           bounded*
//	files_only / rollup_only     0        none           bounded*
//
// include_bodies always wins over the context rule. "one" is a single
// bounded body (<= searchBudgetBodyLines) for the top-ranked non-test match:
// the small-result pick when it fits, else the evidence-ranked choice.
// "wide" is up to two regions per term, the small-result pick first.
type searchShape struct {
	// context is the number of lines around each text hit sent to the
	// search; contextAsked says the caller chose it.
	context      int
	contextAsked bool
	// contextClamped is the value the caller asked for when it was above
	// searchContextCap, else 0.
	contextClamped int
	bodies         bodyMode
	fit            fitMode
	// budget: the default response budget applies (searchbudget.go): test
	// hits collapse, best lines per file, short sampling notes.
	budget bool
	// explicitBodies: include_bodies=true was passed (the wider 4k fit).
	explicitBodies bool
	// dedupeContext: drop context lines a delivered body already prints.
	dedupeContext bool
	// adaptive: the default or a raised limit keeps the exact-count pass.
	adaptive bool
}

type bodyMode int

const (
	bodiesNone bodyMode = iota
	bodiesOne
	bodiesWide
	bodiesWideNoPick
)

func (m bodyMode) String() string {
	switch m {
	case bodiesNone:
		return "none"
	case bodiesOne:
		return "one"
	case bodiesWide:
		return "wide"
	case bodiesWideNoPick:
		return "wideNoPick"
	}
	return fmt.Sprintf("bodyMode(%d)", int(m))
}

type fitMode int

const (
	fitNone fitMode = iota
	// fitBudget: applySearchBudget at searchBudgetTokens (1.2k-2k tokens).
	fitBudget
	// fitBudgetWide: applySearchBudget at 4k tokens (include_bodies=true).
	fitBudgetWide
	// fitBounded: boundSearchPresentation / boundSymbolPresentation at 4k
	// tokens with the long notes.
	fitBounded
)

func (m fitMode) String() string {
	switch m {
	case fitNone:
		return "none"
	case fitBudget:
		return "budget"
	case fitBudgetWide:
		return "budget4k"
	case fitBounded:
		return "bounded"
	}
	return fmt.Sprintf("fitMode(%d)", int(m))
}

// searchShapeFor reads the normalized legacy prism_search fields (query,
// path, glob, limit, context, include_bodies, files_only, exhaustive,
// rollup_only). limit is the already-clamped effective limit.
func searchShapeFor(args map[string]any, limit int) searchShape {
	_, explicitContext := args["context"]
	_, explicitLimit := args["limit"]
	includeBodiesValue, explicitIncludeBodies := args["include_bodies"]
	filesOnly := boolArg(args, "files_only")
	rollupOnly := boolArg(args, "rollup_only")
	exhaustive := boolArg(args, "exhaustive")
	locatorsOnly := filesOnly || rollupOnly || exhaustive

	s := searchShape{
		contextAsked:   explicitContext,
		explicitBodies: explicitIncludeBodies && includeBodiesValue == true,
		adaptive:       limit >= defaultSearchLimit,
		budget:         !explicitContext && !explicitLimit && !locatorsOnly,
	}

	// Context: the caller's value, clamped; else a small default except
	// where locations are the point (files_only, rollup_only) or the
	// bounded inventory already carries the shape (exhaustive).
	switch {
	case explicitContext:
		s.context = intArg(args, "context", 0)
		if s.context > searchContextCap {
			s.contextClamped = s.context
			s.context = searchContextCap
		}
		if s.context < 0 {
			s.context = 0
		}
	case locatorsOnly:
		s.context = 0
	default:
		s.context = defaultSearchContext
	}

	// Bodies.
	switch {
	case locatorsOnly:
		s.bodies = bodiesNone
	case explicitIncludeBodies && includeBodiesValue != true:
		s.bodies = bodiesNone
	case explicitIncludeBodies:
		s.bodies = bodiesWideNoPick
	case explicitContext && s.contextClamped == 0:
		// A window request: the schema says explicit context disables
		// enclosing-body delivery (jackson pr6042, 2026-10-11: both were
		// sent, 13.7k chars, the windows twice).
		s.bodies = bodiesNone
	case explicitContext:
		// Past the cap the caller wants the function, not a window.
		s.bodies = bodiesWide
	case s.budget:
		s.bodies = bodiesOne
	default:
		s.bodies = bodiesWide
	}

	// Windows and bodies in one answer never print a source line twice:
	// context lines a body repeats are dropped, the match line stays.
	s.dedupeContext = s.bodies != bodiesNone

	// Fit.
	switch {
	case s.budget && s.explicitBodies:
		s.fit = fitBudgetWide
	case s.budget:
		s.fit = fitBudget
	case s.adaptive && !locatorsOnly:
		s.fit = fitBounded
	default:
		s.fit = fitNone
	}
	return s
}

// clampNote is the note for a context request above the cap, or "".
func (s searchShape) clampNote() string {
	if s.contextClamped == 0 {
		return ""
	}
	if s.bodies == bodiesNone {
		return fmt.Sprintf("context clamped to %d (asked for %d) — for more than a function's worth, use prism_read on the file",
			searchContextCap, s.contextClamped)
	}
	return fmt.Sprintf("context clamped to %d (asked for %d); enclosing functions are delivered below — for a whole file, use prism_read",
		searchContextCap, s.contextClamped)
}

// scope is the searchScope this shape implies for one term's retrieval.
func (s searchShape) scope(args map[string]any) searchScope {
	return searchScope{
		paths:        stringsArg(args, "path"),
		glob:         stringsArg(args, "glob"),
		filesOnly:    boolArg(args, "files_only"),
		exhaustive:   boolArg(args, "exhaustive"),
		context:      s.context,
		rollupOnly:   boolArg(args, "rollup_only"),
		adaptive:     s.adaptive,
		contextAsked: s.contextAsked,
		budget:       s.budget,
	}
}

// budgetFor is the searchBudget carrier the fitting code reads.
func (s searchShape) budgetFor(sc searchScope, queries []string, regex bool) searchBudget {
	return searchBudget{
		on:             s.budget,
		testsTargeted:  searchTargetsTests(sc, queries),
		regex:          regex,
		sc:             sc,
		explicitBodies: s.explicitBodies,
	}
}

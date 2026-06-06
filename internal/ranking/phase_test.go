package ranking

import "testing"

func TestDetectPhase_Explore(t *testing.T) {
	cases := []string{
		"Understand how the session tracker works",
		"Give me an overview of the ranking pipeline",
		"Explain what prism_query does",
		"Walk me through the compression module",
	}
	for _, task := range cases {
		if got := DetectPhase(task); got != PhaseExplore {
			t.Errorf("task %q: got %q, want %q", task, got, PhaseExplore)
		}
	}
}

func TestDetectPhase_Implement(t *testing.T) {
	cases := []string{
		"Implement a new caching layer for Grove responses",
		"Add a retry mechanism to the HTTP client",
		"Build the phase-aware budget shaping feature",
	}
	for _, task := range cases {
		if got := DetectPhase(task); got != PhaseImplement {
			t.Errorf("task %q: got %q, want %q", task, got, PhaseImplement)
		}
	}
}

func TestDetectPhase_Review(t *testing.T) {
	cases := []string{
		"Review the changes in the ranking module",
		"Audit the new cache implementation",
		"Check the pull request diff for security issues",
	}
	for _, task := range cases {
		if got := DetectPhase(task); got != PhaseReview {
			t.Errorf("task %q: got %q, want %q", task, got, PhaseReview)
		}
	}
}

func TestDetectPhase_Debug(t *testing.T) {
	cases := []string{
		"Fix the bug in the LRU eviction logic",
		"The test is failing — investigate why",
		"Debug the unexpected panic in toolQuery",
		"Root cause the regression in compression",
	}
	for _, task := range cases {
		if got := DetectPhase(task); got != PhaseDebug {
			t.Errorf("task %q: got %q, want %q", task, got, PhaseDebug)
		}
	}
}

func TestDetectPhase_Unknown_NoKeywords(t *testing.T) {
	task := "Perform the operation on the module"
	if got := DetectPhase(task); got != PhaseUnknown {
		t.Errorf("task %q: got %q, want %q", task, got, PhaseUnknown)
	}
}

func TestDetectPhase_TieUsesActionPrecedence(t *testing.T) {
	cases := map[string]Phase{
		"Review and fix the release branch":                  PhaseDebug,
		"Implement and review the release branch":            PhaseReview,
		"Explain and implement the cache behavior":           PhaseImplement,
		"Understand and review the ranking pipeline changes": PhaseReview,
	}
	for task, want := range cases {
		if got := DetectPhase(task); got != want {
			t.Errorf("task %q: got %q, want %q", task, got, want)
		}
	}
}

func TestShapeForPhase_MultipliersInRange(t *testing.T) {
	for _, phase := range []Phase{PhaseExplore, PhaseImplement, PhaseReview, PhaseDebug, PhaseUnknown} {
		hint, mult := ShapeForPhase(phase)
		if mult <= 0 || mult > 1.0 {
			t.Errorf("phase %q: budget multiplier %.2f out of (0,1]", phase, mult)
		}
		// hint must be empty (unknown) or a known profile name.
		if hint != "" {
			if _, ok := Profiles[hint]; !ok {
				t.Errorf("phase %q: profile hint %q not in Profiles", phase, hint)
			}
		}
	}
}

func TestShapeForPhase_ReviewShrinksbudget(t *testing.T) {
	_, mult := ShapeForPhase(PhaseReview)
	if mult >= 1.0 {
		t.Errorf("review phase should shrink budget (mult < 1.0), got %.2f", mult)
	}
}

func TestShapeForPhase_ImplementFullBudget(t *testing.T) {
	_, mult := ShapeForPhase(PhaseImplement)
	if mult != 1.0 {
		t.Errorf("implement phase should use full budget (1.0), got %.2f", mult)
	}
}

package ranking

import (
	"strings"
	"unicode"
)

// Phase represents the inferred agent work phase, used to shape the token
// budget and ranking profile automatically when the caller does not specify one.
type Phase string

const (
	PhaseExplore   Phase = "explore"
	PhaseImplement Phase = "implement"
	PhaseReview    Phase = "review"
	PhaseDebug     Phase = "debug"
	PhaseUnknown   Phase = "unknown"
)

// phaseShape captures the per-phase profile preference and budget multiplier.
// BudgetMultiplier > 1.0 expands the token budget; < 1.0 shrinks it.
type phaseShape struct {
	Profile          string
	BudgetMultiplier float64
}

var phaseShapes = map[Phase]phaseShape{
	// Explore: broad context, moderate budget — agent is orienting itself.
	PhaseExplore: {Profile: "default", BudgetMultiplier: 0.75},

	// Implement: graph-distance-heavy profile, full budget — need all dependencies.
	PhaseImplement: {Profile: "implement_feature", BudgetMultiplier: 1.0},

	// Review: recency + edit-frequency emphasis, tighter budget — agent only needs diffs.
	PhaseReview: {Profile: "code_review", BudgetMultiplier: 0.60},

	// Debug: recency + test coverage emphasis, full budget — need recent changes + tests.
	PhaseDebug: {Profile: "fix_bug", BudgetMultiplier: 1.0},

	// Unknown: use whatever the caller specified.
	PhaseUnknown: {Profile: "", BudgetMultiplier: 1.0},
}

// exploreKeywords are task-description words strongly associated with an
// orientation/exploration phase.
var exploreKeywords = []string{
	"understand", "explain", "overview", "explore", "summarize", "summary",
	"describe", "what is", "how does", "tell me", "walk me through",
	"familiarize", "find out", "discover", "map", "diagram",
}

// implementKeywords map to the implement phase.
var implementKeywords = []string{
	"implement", "add", "create", "build", "write", "develop", "introduce",
	"new feature", "feature", "scaffold", "generate",
}

// reviewKeywords map to the review phase.
var reviewKeywords = []string{
	"review", "audit", "check", "assess", "evaluate", "inspect", "pr",
	"pull request", "diff", "changes", "refactor",
}

// debugKeywords map to the debug phase.
var debugKeywords = []string{
	"fix", "bug", "error", "broken", "fails", "failing", "failure", "crash",
	"exception", "panic", "debug", "diagnose", "investigate", "root cause",
	"regression", "unexpected", "wrong output",
}

// DetectPhase infers the agent work phase from a free-text task description.
// It uses a keyword-voting approach: each list casts votes, the winner wins;
// ties use action-oriented precedence so mixed tasks still get a useful shape.
func DetectPhase(task string) Phase {
	lower := strings.ToLower(task)

	scores := map[Phase]int{
		PhaseExplore:   countMatches(lower, exploreKeywords),
		PhaseImplement: countMatches(lower, implementKeywords),
		PhaseReview:    countMatches(lower, reviewKeywords),
		PhaseDebug:     countMatches(lower, debugKeywords),
	}

	bestScore := 0
	for _, phase := range []Phase{PhaseExplore, PhaseImplement, PhaseReview, PhaseDebug} {
		if scores[phase] > bestScore {
			bestScore = scores[phase]
		}
	}
	if bestScore == 0 {
		return PhaseUnknown
	}

	// If multiple phases tie, prefer the phase that most changes what context
	// Prism should deliver. Debug needs tests and recent changes, then review
	// needs diffs, then implement needs dependencies. Explore is the fallback.
	for _, phase := range []Phase{PhaseDebug, PhaseReview, PhaseImplement, PhaseExplore} {
		if scores[phase] == bestScore {
			return phase
		}
	}
	return PhaseUnknown
}

// ShapeForPhase returns the budget multiplier and suggested profile for a phase.
// If the phase is unknown (or the caller already supplied an explicit profile),
// this is a no-op (multiplier 1.0, empty profile).
func ShapeForPhase(phase Phase) (profileHint string, budgetMultiplier float64) {
	s, ok := phaseShapes[phase]
	if !ok {
		return "", 1.0
	}
	return s.Profile, s.BudgetMultiplier
}

func countMatches(text string, keywords []string) int {
	n := 0
	for _, kw := range keywords {
		if containsWord(text, kw) {
			n++
		}
	}
	return n
}

// containsWord reports whether text contains kw as a whole-word match.
// A match is valid only when the characters immediately before and after
// kw (if any) are not letters or digits — this prevents "implementation"
// from matching the keyword "implement".
func containsWord(text, kw string) bool {
	for i := 0; i <= len(text)-len(kw); i++ {
		if !strings.EqualFold(text[i:i+len(kw)], kw) {
			continue
		}
		// Check left boundary.
		if i > 0 && isWordChar(rune(text[i-1])) {
			continue
		}
		// Check right boundary.
		end := i + len(kw)
		if end < len(text) && isWordChar(rune(text[end])) {
			continue
		}
		return true
	}
	return false
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// Package ranking implements Prism's 4-signal composite scoring and the
// budget-aware greedy selector that decides which symbols to deliver and at
// what fidelity.
//
// A 5th signal, SemanticSimilarity (TF-IDF/Model2Vec cosine similarity to
// the task string), was removed 2026-08-01. Measured on 15 hand-verified
// concept queries across 5 real corpora: an agent guessing ONE keyword and
// using lexical substring search already wins or ties embedding-based
// discovery in 12/15 cases, often by a wide margin (rank 1 vs rank 19+).
// Embeddings earned their keep in exactly one case (a query with no
// single-word guess at all), and even there landed at rank 3, not rank 1.
// Given that, a heuristic signal contributing up to 25% of the delivery
// score — with its own persisted reinforcement loop nudging its weight from
// real usage — was outweighing evidence that it barely helps. The remaining
// 4 signals are unchanged in relative meaning; weights below are the
// original per-profile weights renormalized to sum to 1.0 after dropping
// SemanticSimilarity, so each profile's RELATIVE emphasis is preserved.
package ranking

// SignalValues holds the 4 ranking signals for a single symbol.
// All values are in [0.0, 1.0].
type SignalValues struct {
	GraphDistance float64
	Recency       float64
	TestRelevance float64
	EditFrequency float64
}

// Profile defines per-signal weights for a task type. Weights should sum to
// 1.0 but the implementation tolerates any non-negative weights.
type Profile struct {
	Name          string
	GraphDistance float64
	Recency       float64
	TestRelevance float64
	EditFrequency float64
}

// Score returns the weighted composite score for the given signals + profile.
func Score(s SignalValues, p Profile) float64 {
	return s.GraphDistance*p.GraphDistance +
		s.Recency*p.Recency +
		s.TestRelevance*p.TestRelevance +
		s.EditFrequency*p.EditFrequency
}

// Profiles is the predefined set of ranking profiles. Looked up by name in
// SelectProfile; falls back to "default" on unknown names.
var Profiles = map[string]Profile{
	// was GraphDistance 0.30, SemanticSimilarity 0.25, Recency 0.15, TestRelevance 0.15, EditFrequency 0.15 (sum 0.75 w/o semantic)
	"implement_feature": {
		Name: "implement_feature", GraphDistance: 0.40,
		Recency: 0.20, TestRelevance: 0.20, EditFrequency: 0.20,
	},
	// was GraphDistance 0.20, SemanticSimilarity 0.10, Recency 0.25, TestRelevance 0.25, EditFrequency 0.20 (sum 0.90 w/o semantic)
	"fix_bug": {
		Name: "fix_bug", GraphDistance: 0.2222,
		Recency: 0.2778, TestRelevance: 0.2778, EditFrequency: 0.2222,
	},
	// was GraphDistance 0.20, SemanticSimilarity 0.20, Recency 0.15, TestRelevance 0.20, EditFrequency 0.25 (sum 0.80 w/o semantic)
	"code_review": {
		Name: "code_review", GraphDistance: 0.25,
		Recency: 0.1875, TestRelevance: 0.25, EditFrequency: 0.3125,
	},
	// was GraphDistance 0.25, SemanticSimilarity 0.25, Recency 0.20, TestRelevance 0.15, EditFrequency 0.15 (sum 0.75 w/o semantic)
	"default": {
		Name: "default", GraphDistance: 0.3333,
		Recency: 0.2667, TestRelevance: 0.20, EditFrequency: 0.20,
	},
}

// SelectProfile returns the profile by name; falls back to "default" if
// the name is unknown or empty.
func SelectProfile(name string) Profile {
	if p, ok := Profiles[name]; ok {
		return p
	}
	return Profiles["default"]
}

// RelevanceThreshold is the minimum composite score below which symbols are
// downgraded to DisclosureSignature instead of DisclosureFull.
const RelevanceThreshold = 0.15

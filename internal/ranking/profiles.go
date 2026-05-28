// Package ranking implements Prism's 5-signal composite scoring and the
// budget-aware greedy selector that decides which symbols to deliver and at
// what fidelity.
package ranking

// SignalValues holds the 5 ranking signals for a single symbol.
// All values are in [0.0, 1.0].
type SignalValues struct {
	GraphDistance      float64
	SemanticSimilarity float64
	Recency            float64
	TestRelevance      float64
	EditFrequency      float64
}

// Profile defines per-signal weights for a task type. Weights should sum to
// 1.0 but the implementation tolerates any non-negative weights.
type Profile struct {
	Name               string
	GraphDistance      float64
	SemanticSimilarity float64
	Recency            float64
	TestRelevance      float64
	EditFrequency      float64
}

// Score returns the weighted composite score for the given signals + profile.
func Score(s SignalValues, p Profile) float64 {
	return s.GraphDistance*p.GraphDistance +
		s.SemanticSimilarity*p.SemanticSimilarity +
		s.Recency*p.Recency +
		s.TestRelevance*p.TestRelevance +
		s.EditFrequency*p.EditFrequency
}

// Profiles is the predefined set of ranking profiles. Looked up by name in
// SelectProfile; falls back to "default" on unknown names.
var Profiles = map[string]Profile{
	"implement_feature": {
		Name: "implement_feature", GraphDistance: 0.30, SemanticSimilarity: 0.25,
		Recency: 0.15, TestRelevance: 0.15, EditFrequency: 0.15,
	},
	"fix_bug": {
		Name: "fix_bug", GraphDistance: 0.20, SemanticSimilarity: 0.10,
		Recency: 0.25, TestRelevance: 0.25, EditFrequency: 0.20,
	},
	"code_review": {
		Name: "code_review", GraphDistance: 0.20, SemanticSimilarity: 0.20,
		Recency: 0.15, TestRelevance: 0.20, EditFrequency: 0.25,
	},
	"default": {
		Name: "default", GraphDistance: 0.25, SemanticSimilarity: 0.25,
		Recency: 0.20, TestRelevance: 0.15, EditFrequency: 0.15,
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

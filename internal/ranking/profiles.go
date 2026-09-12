// Package ranking orders symbols by verified relation to task anchors and then
// by stable retrieval order, before applying a shared delivery budget.
package ranking

// SignalValues contains the one measured within-tier signal. Structural
// relation is a separate tier, so no score can outrank a verified call edge.
type SignalValues struct {
	RetrievalOrder float64
}

// Profile is retained for compatibility with stored configuration and query
// arguments. Named profiles currently share one rule until an oracle bed shows
// that a distinct rule improves delivery.
type Profile struct {
	Name           string
	RetrievalOrder float64
}

// Score ranks candidates within their structural relation tier.
func Score(s SignalValues, p Profile) float64 {
	return s.RetrievalOrder * p.RetrievalOrder
}

// Profiles accepts the existing profile names as aliases for the same
// deterministic rule. The names no longer change candidate order.
var Profiles = map[string]Profile{
	"default":           {Name: "default", RetrievalOrder: 1},
	"implement_feature": {Name: "implement_feature", RetrievalOrder: 1},
	"fix_bug":           {Name: "fix_bug", RetrievalOrder: 1},
	"code_review":       {Name: "code_review", RetrievalOrder: 1},
}

// SelectProfile returns a recognized profile or the default for unknown names.
func SelectProfile(name string) Profile {
	if p, ok := Profiles[name]; ok {
		return p
	}
	return Profiles["default"]
}

// RelevanceThreshold demotes low-scoring retrieval-only symbols to signatures.
const RelevanceThreshold = 0.15

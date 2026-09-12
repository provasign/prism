package ranking

import (
	"testing"
)

// ── Select (budget-aware greedy) ─────────────────────────────────────────────

func makeCandidates(n int) []Candidate {
	out := make([]Candidate, n)
	for i := range out {
		out[i] = Candidate{
			Score:    float64(n-i) / float64(n),
			Category: CategoryDependency,
		}
	}
	return out
}

func BenchmarkSelect_100Candidates(b *testing.B) {
	candidates := makeCandidates(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Select(nil, candidates, 32000)
	}
}

func BenchmarkSelect_500Candidates(b *testing.B) {
	candidates := makeCandidates(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Select(nil, candidates, 32000)
	}
}

package ranking

import (
	"testing"
)

// ── DetectPhase ──────────────────────────────────────────────────────────────

var sinkPhase Phase

func BenchmarkDetectPhase_Explore(b *testing.B) {
	task := "Understand how the session tracker works and give me an overview"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkPhase = DetectPhase(task)
	}
}

func BenchmarkDetectPhase_Implement(b *testing.B) {
	task := "Implement a new caching layer for Grove responses and add unit tests"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkPhase = DetectPhase(task)
	}
}

func BenchmarkDetectPhase_LongTask(b *testing.B) {
	// Simulate a verbose task description an agent might pass.
	task := "Please implement the phase-aware budget shaping feature described in the " +
		"proposal. It should detect the agent work phase (explore, implement, review, " +
		"debug) from the task description and automatically adjust both the ranking " +
		"profile and the token budget. Make sure to add comprehensive tests."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkPhase = DetectPhase(task)
	}
}

// ── LearnedWeights.Apply ─────────────────────────────────────────────────────

var sinkProfile Profile

func BenchmarkLearnedWeights_Apply_NoWeights(b *testing.B) {
	lw := &LearnedWeights{weights: make(map[string]SignalValues)}
	base := SelectProfile("fix_bug")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkProfile = lw.Apply(base)
	}
}

func BenchmarkLearnedWeights_Apply_WithWeights(b *testing.B) {
	lw := &LearnedWeights{weights: map[string]SignalValues{
		"fix_bug": {SemanticSimilarity: 0.08, TestRelevance: 0.12, GraphDistance: -0.05},
	}}
	base := SelectProfile("fix_bug")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkProfile = lw.Apply(base)
	}
}

func BenchmarkLearnedWeights_RecordOutcome(b *testing.B) {
	root := b.TempDir()
	lw := LoadLearnedWeights(root)
	lw.path = "" // disable disk write so we measure only the in-memory update
	cited := []string{"pkg/foo.go", "pkg/bar.go", "internal/x.go"}
	delivered := []string{"pkg/foo.go", "pkg/bar.go", "internal/x.go", "internal/y.go", "cmd/main.go"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lw.RecordOutcome("fix_bug", cited, delivered, false)
	}
}

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

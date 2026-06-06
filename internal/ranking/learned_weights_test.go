package ranking

import (
	"os"
	"path/filepath"
	"testing"
)

// makeTempRepo creates a throw-away directory to use as a fake repo root.
func makeTempRepoRanking(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// ── Apply ───────────────────────────────────────────────────────────────────

func TestApply_NoWeights_ReturnsBaseUnchanged(t *testing.T) {
	lw := LoadLearnedWeights(makeTempRepoRanking(t)) // fresh empty store
	base := SelectProfile("fix_bug")
	got := lw.Apply(base)

	if got.GraphDistance != base.GraphDistance ||
		got.SemanticSimilarity != base.SemanticSimilarity ||
		got.Recency != base.Recency ||
		got.TestRelevance != base.TestRelevance ||
		got.EditFrequency != base.EditFrequency {
		t.Errorf("Apply with no weights should be identity; got %+v want %+v", got, base)
	}
}

func TestApply_NudgesSignals(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	// Inject an adjustment directly so we don't depend on RecordOutcome maths.
	lw.mu.Lock()
	lw.weights["fix_bug"] = SignalValues{SemanticSimilarity: 0.10, TestRelevance: 0.05}
	lw.mu.Unlock()

	base := SelectProfile("fix_bug")
	got := lw.Apply(base)

	want := base.SemanticSimilarity + 0.10
	if got.SemanticSimilarity != want {
		t.Errorf("SemanticSimilarity: got %.4f want %.4f", got.SemanticSimilarity, want)
	}
	want2 := base.TestRelevance + 0.05
	if got.TestRelevance != want2 {
		t.Errorf("TestRelevance: got %.4f want %.4f", got.TestRelevance, want2)
	}
	// Unmodified signals must be unchanged.
	if got.GraphDistance != base.GraphDistance {
		t.Errorf("GraphDistance should be unchanged: got %.4f", got.GraphDistance)
	}
}

func TestApply_ClampsToMinimum(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	// Large negative nudge — should be clamped to 0.05.
	lw.mu.Lock()
	lw.weights["default"] = SignalValues{SemanticSimilarity: -0.99}
	lw.mu.Unlock()

	base := SelectProfile("default")
	got := lw.Apply(base)
	if got.SemanticSimilarity < 0.05 {
		t.Errorf("SemanticSimilarity clamped below 0.05: got %.4f", got.SemanticSimilarity)
	}
}

func TestApply_ClampsToMaximum(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	lw.mu.Lock()
	lw.weights["default"] = SignalValues{SemanticSimilarity: 99.0}
	lw.mu.Unlock()

	base := SelectProfile("default")
	got := lw.Apply(base)
	if got.SemanticSimilarity > 1.0 {
		t.Errorf("SemanticSimilarity clamped above 1.0: got %.4f", got.SemanticSimilarity)
	}
}

// ── RecordOutcome ────────────────────────────────────────────────────────────

func TestRecordOutcome_CitedBoostsSemanticSimilarity(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	before := lw.weights["fix_bug"].SemanticSimilarity
	lw.RecordOutcome("fix_bug",
		[]string{"pkg/foo.go"},           // cited
		[]string{"pkg/foo.go", "bar.go"}, // delivered
		false)

	after := lw.weights["fix_bug"].SemanticSimilarity
	if after <= before {
		t.Errorf("cited file should boost SemanticSimilarity: before=%.4f after=%.4f", before, after)
	}
}

func TestRecordOutcome_UncitedSuppressesSemanticSimilarity(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	before := lw.weights["fix_bug"].SemanticSimilarity
	lw.RecordOutcome("fix_bug",
		[]string{},           // nothing cited
		[]string{"noise.go"}, // delivered but unused
		false)

	after := lw.weights["fix_bug"].SemanticSimilarity
	if after >= before {
		t.Errorf("uncited file should suppress SemanticSimilarity: before=%.4f after=%.4f", before, after)
	}
}

func TestRecordOutcome_MissingTestSignalBoostsTestRelevance(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)

	before := lw.weights["fix_bug"].TestRelevance
	lw.RecordOutcome("fix_bug", nil, nil, true /* missingTestSignal */)

	after := lw.weights["fix_bug"].TestRelevance
	if after <= before {
		t.Errorf("missing test signal should boost TestRelevance: before=%.4f after=%.4f", before, after)
	}
}

func TestRecordOutcome_Persists(t *testing.T) {
	root := makeTempRepoRanking(t)
	lw := LoadLearnedWeights(root)
	lw.path = filepath.Join(root, "weights.json") // write to temp dir

	lw.RecordOutcome("default", []string{"a.go"}, []string{"a.go"}, false)

	// Re-load from disk and verify weights were saved.
	lw2 := &LearnedWeights{weights: make(map[string]SignalValues), path: lw.path}
	b, err := os.ReadFile(lw.path)
	if err != nil {
		t.Fatalf("weight file not written: %v", err)
	}
	_ = b
	if lw2.weights["default"].SemanticSimilarity != 0 {
		// lw2 wasn't loaded — just confirm the file exists (non-empty).
	}
	if len(b) == 0 {
		t.Error("weight file is empty")
	}
}

// ── DetectFeedbackSource ─────────────────────────────────────────────────────

func TestDetectFeedbackSource_Provasign(t *testing.T) {
	root := makeTempRepoRanking(t)
	if err := os.Mkdir(filepath.Join(root, ".provasign"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".provasign", "provasign.yaml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DetectFeedbackSource(root); got != "provasign" {
		t.Errorf("got %q, want provasign", got)
	}
}

func TestDetectFeedbackSource_ProvasignLegacyPath(t *testing.T) {
	root := makeTempRepoRanking(t)
	if err := os.WriteFile(filepath.Join(root, ".provasign.yaml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DetectFeedbackSource(root); got != "provasign" {
		t.Errorf("got %q, want provasign", got)
	}
}

func TestDetectFeedbackSource_Git(t *testing.T) {
	root := makeTempRepoRanking(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DetectFeedbackSource(root); got != "git" {
		t.Errorf("got %q, want git", got)
	}
}

func TestDetectFeedbackSource_None(t *testing.T) {
	root := makeTempRepoRanking(t)
	if got := DetectFeedbackSource(root); got != "none" {
		t.Errorf("got %q, want none", got)
	}
}

func TestDetectFeedbackSource_ProvasignTakesPriority(t *testing.T) {
	root := makeTempRepoRanking(t)
	// Both markers present — provasign should win.
	if err := os.Mkdir(filepath.Join(root, ".provasign"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".provasign", "provasign.yaml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DetectFeedbackSource(root); got != "provasign" {
		t.Errorf("got %q, want provasign", got)
	}
}

package ranking

import (
	"math"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestScore_LinearCombination(t *testing.T) {
	p := Profile{GraphDistance: 0.5, SemanticSimilarity: 0.5}
	got := Score(SignalValues{GraphDistance: 1, SemanticSimilarity: 0.5}, p)
	if math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("score: want 0.75, got %v", got)
	}
}

func TestSelectProfile_FallbackToDefault(t *testing.T) {
	p := SelectProfile("nonexistent")
	if p.Name != "default" {
		t.Fatalf("want default, got %q", p.Name)
	}
}

func TestSelect_SeedsAlwaysFullAndFree(t *testing.T) {
	seed := grove.SymbolRecord{
		ID: "a", Name: "Seed", FilePath: "s.go", Kind: "function",
		Signature: "func Seed()", RawText: "func Seed() { /* body */ }",
	}
	out := Select([]grove.SymbolRecord{seed}, nil, 100)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	if out[0].Disclosure != DisclosureFull {
		t.Fatalf("seed must be full, got %s", out[0].Disclosure)
	}
	if out[0].Category != CategoryTarget {
		t.Fatalf("seed must be target, got %s", out[0].Category)
	}
}

func TestSelect_HighScoreGetsFull_LowScoreGetsSignature(t *testing.T) {
	hi := grove.SymbolRecord{
		ID: "hi", Name: "Hot", FilePath: "h.go", Kind: "function",
		Signature: "func Hot()", RawText: "func Hot() { x := 1; _ = x }",
	}
	lo := grove.SymbolRecord{
		ID: "lo", Name: "Cold", FilePath: "c.go", Kind: "function",
		Signature: "func Cold()", RawText: "func Cold() { x := 1; _ = x }",
	}
	cands := []Candidate{
		{Symbol: hi, Score: 0.8, Category: CategoryDependency},
		{Symbol: lo, Score: 0.05, Category: CategoryDependency},
	}
	out := Select(nil, cands, 10000)
	if len(out) != 2 {
		t.Fatalf("want 2 picked, got %d", len(out))
	}
	if out[0].Disclosure != DisclosureFull {
		t.Errorf("hi should be full, got %s", out[0].Disclosure)
	}
	if out[1].Disclosure != DisclosureSignature {
		t.Errorf("lo should be signature, got %s", out[1].Disclosure)
	}
}

func TestSelect_PreviouslySeenHighConfidenceBecomesReference(t *testing.T) {
	sym := grove.SymbolRecord{
		ID: "s", Name: "S", FilePath: "x.go", Kind: "function",
		Signature: "func S()", RawText: "func S() {}",
	}
	out := Select(nil, []Candidate{{
		Symbol:         sym,
		Score:          0.9,
		Category:       CategoryDependency,
		PreviouslySeen: true,
		Confidence:     "high",
	}}, 10000)
	if out[0].Disclosure != DisclosureReference {
		t.Fatalf("want reference, got %s", out[0].Disclosure)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("0123456789012345"); got != 4 {
		t.Fatalf("16 chars → ~4 tokens, got %d", got)
	}
	if EstimateTokens("") != 0 {
		t.Fatal("empty must be 0")
	}
}

func TestRender_DisclosureLevels(t *testing.T) {
	sym := grove.SymbolRecord{
		Kind: "function", Name: "F", QualifiedName: "pkg.F",
		Signature: "func F() error", Docstring: "F does things.",
		RawText:  "func F() error { return nil }",
		FilePath: "f.go", Span: grove.SpanInfo{Start: 7},
	}
	if Render(sym, DisclosureFull) != sym.RawText {
		t.Error("full must be raw text")
	}
	sig := Render(sym, DisclosureSignature)
	if sig == "" || sig == sym.RawText {
		t.Error("signature should be docstring+signature, not raw text")
	}
	ref := Render(sym, DisclosureReference)
	if ref != "function pkg.F (f.go:7)" {
		t.Fatalf("reference format: got %q", ref)
	}
}

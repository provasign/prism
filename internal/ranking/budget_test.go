package ranking

import (
	"math"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestScore_LinearCombination(t *testing.T) {
	p := Profile{GraphDistance: 0.5, Recency: 0.5}
	got := Score(SignalValues{GraphDistance: 1, Recency: 0.5}, p)
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
		{Symbol: lo, Score: 0.05, Category: CategoryDependency}, // below cliff — only hi selected
	}
	// lo scores below ScoreCliffFactor*0.8=0.48, so it is dropped by the cliff cutoff.
	// Test only validates that hi gets full disclosure.
	out := Select(nil, cands, 10000)
	if len(out) != 1 {
		t.Fatalf("want 1 picked (lo below score cliff), got %d", len(out))
	}
	if out[0].Disclosure != DisclosureFull {
		t.Errorf("hi should be full, got %s", out[0].Disclosure)
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

// --- E: Trivial-body elision ---------------------------------------------

func TestIsTrivialBody_ShortNoCallsFunction(t *testing.T) {
	sym := grove.SymbolRecord{
		Kind: "function",
		Span: grove.SpanInfo{Start: 10, End: 14}, // 4 lines
		// No CallSites
	}
	if !IsTrivialBody(sym) {
		t.Error("short function with no calls should be trivial")
	}
}

func TestIsTrivialBody_LongFunction(t *testing.T) {
	sym := grove.SymbolRecord{
		Kind: "function",
		Span: grove.SpanInfo{Start: 0, End: 20}, // 20 lines
	}
	if IsTrivialBody(sym) {
		t.Error("20-line function should not be trivial")
	}
}

func TestIsTrivialBody_HasCallSites(t *testing.T) {
	sym := grove.SymbolRecord{
		Kind:      "function",
		Span:      grove.SpanInfo{Start: 0, End: 3},
		CallSites: []grove.CallSite{{Callee: "otherFunc", Line: 2}},
	}
	if IsTrivialBody(sym) {
		t.Error("function with outgoing calls should not be trivial")
	}
}

func TestIsTrivialBody_ZeroSpan(t *testing.T) {
	// Span not populated by Grove — should NOT assume trivial.
	sym := grove.SymbolRecord{
		Kind: "function",
		Span: grove.SpanInfo{Start: 0, End: 0},
	}
	if IsTrivialBody(sym) {
		t.Error("zero span (not populated) should not be treated as trivial")
	}
}

func TestIsTrivialBody_NonFunctionKind(t *testing.T) {
	// Struct types and interfaces should not be elided.
	for _, kind := range []string{"struct", "interface", "enum", "class"} {
		sym := grove.SymbolRecord{
			Kind: kind,
			Span: grove.SpanInfo{Start: 0, End: 4},
		}
		if IsTrivialBody(sym) {
			t.Errorf("kind=%q should not be trivial", kind)
		}
	}
}

func TestChooseDisclosure_DocCategoryForcesReference(t *testing.T) {
	// Docs have no graph — regardless of score, they must come back as
	// DisclosureReference (ranked name only, no content).
	c := Candidate{
		Symbol: grove.SymbolRecord{
			Kind:          "document",
			Name:          "docs/design.md",
			QualifiedName: "docs/design.md",
			FilePath:      "docs/design.md",
			RawText:       "# Design\n" + "very long content...",
		},
		Score:    0.99,
		Category: CategoryDoc,
	}
	if got := chooseDisclosure(c); got != DisclosureReference {
		t.Errorf("doc with high score: want DisclosureReference, got %s", got)
	}
}

func TestChooseDisclosure_TrivialBodyForcesSignature(t *testing.T) {
	// High-score function that would normally get DisclosureFull, but has a
	// trivial body — should be demoted to DisclosureSignature.
	c := Candidate{
		Symbol: grove.SymbolRecord{
			Kind: "function",
			Span: grove.SpanInfo{Start: 5, End: 9}, // 4 lines, trivial
		},
		Score: 0.99, // well above RelevanceThreshold
	}
	if got := chooseDisclosure(c); got != DisclosureSignature {
		t.Errorf("trivial body with high score: want DisclosureSignature, got %s", got)
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

func TestRender_DisclosureSignaturePlaintextDoesNotDuplicateContent(t *testing.T) {
	sym := grove.SymbolRecord{
		Kind:          "document",
		Language:      "plaintext",
		Name:          "config.json",
		QualifiedName: "config.json",
		Docstring:     "{\n  \"billing\": true\n}",
		Signature:     "{\n  \"billing\": true\n}",
		FilePath:      "config.json",
	}
	got := Render(sym, DisclosureSignature)
	if got != sym.Signature {
		t.Fatalf("plaintext signature should avoid docstring+signature duplication, got %q", got)
	}
}

func TestSelect_FileCapDemotesAggregateTiling(t *testing.T) {
	// The measured failure (2026-08-31, werkzeug routing/map.py): a seed
	// plus many same-file graph candidates, each individually affordable,
	// collectively tiling the whole file. Seeds are never demoted but DO
	// count toward the file's spend, so a seed-heavy file hits
	// FileBudgetFraction sooner — further full-disclosure candidates from
	// it must degrade to signature, while another file's candidate (own
	// category budget, own file) still gets its body.
	line := "\tx := 1\n\t_ = x\n"
	bigBody := "func X() {\n"
	for i := 0; i < 150; i++ {
		bigBody += line
	}
	bigBody += "}"
	smallBody := "func Y() {\n" + line + line + "}"

	seed := grove.SymbolRecord{
		ID: "seed", Name: "Anchor", FilePath: "big.go", Kind: "method",
		Signature: "func (m *M) Anchor()", RawText: bigBody,
	}
	var cands []Candidate
	for i := 0; i < 8; i++ {
		cands = append(cands, Candidate{
			Symbol: grove.SymbolRecord{
				ID: string(rune('a' + i)), Name: "M", FilePath: "big.go",
				Kind: "method", Signature: "func (m *M) X()", RawText: smallBody,
			},
			Score: 0.9, Category: CategoryDependency,
		})
	}
	other := grove.SymbolRecord{
		ID: "other", Name: "O", FilePath: "other.go", Kind: "function",
		Signature: "func O()", RawText: smallBody,
	}
	cands = append(cands, Candidate{Symbol: other, Score: 0.9, Category: CategoryTest})

	// budget sized so the SEED alone crosses fileCap: fileCap =
	// 0.4*budget < seed tokens, while category budgets stay roomy for
	// the small candidate bodies.
	budget := 2 * EstimateTokens(Render(seed, DisclosureFull))
	out := Select([]grove.SymbolRecord{seed}, cands, budget)

	fullFromBig, sigFromBig, fullFromOther := 0, 0, 0
	for _, b := range out {
		if b.Category == CategoryTarget {
			continue
		}
		switch {
		case b.Symbol.FilePath == "big.go" && b.Disclosure == DisclosureFull:
			fullFromBig++
		case b.Symbol.FilePath == "big.go" && b.Disclosure == DisclosureSignature:
			sigFromBig++
		case b.Symbol.FilePath == "other.go" && b.Disclosure == DisclosureFull:
			fullFromOther++
		}
	}
	if sigFromBig == 0 {
		t.Errorf("seed spent big.go past its file cap; candidates must demote to signature (full=%d sig=%d)", fullFromBig, sigFromBig)
	}
	if fullFromBig != 0 {
		t.Errorf("file already past cap at seed time — no candidate body from it should land (full=%d)", fullFromBig)
	}
	if fullFromOther == 0 {
		t.Error("other.go must be unaffected by big.go's cap")
	}
}

package ranking

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestDirectCallerSurvivesHotUnrelatedFile(t *testing.T) {
	caller := grove.SymbolRecord{ID: "caller", Name: "Caller", FilePath: "caller.go", Kind: "function", Signature: "func Caller()", RawText: "func Caller() {}"}
	unrelated := grove.SymbolRecord{ID: "unrelated", Name: "Unrelated", FilePath: "unrelated.go", Kind: "function", Signature: "func Unrelated()", RawText: "func Unrelated() {}"}
	// The old live-Git score put the unrelated file at 0.55 and the direct
	// caller at 0.19, then the cliff dropped the caller altogether.
	picked := Select(nil, []Candidate{
		{Symbol: unrelated, Relation: RelationRetrieval, Score: 0.55, Category: CategoryDependency},
		{Symbol: caller, Relation: RelationDirectCall, Score: 0.19, Category: CategoryDependency},
	}, 10000)
	if len(picked) == 0 || picked[0].Symbol.ID != caller.ID {
		t.Fatalf("direct caller must outrank unrelated hot file: picked=%+v", picked)
	}
}

func TestSelectChargesSeedsToBudget(t *testing.T) {
	seed := grove.SymbolRecord{
		ID: "large-seed", Name: "LargeSeed", FilePath: "large.go", Kind: "function",
		Signature: "func LargeSeed()", RawText: "func LargeSeed() {" + strings.Repeat("body\n", 1200) + "}",
	}
	const budget = 300
	picked := Select([]grove.SymbolRecord{seed}, nil, budget)
	spent := 0
	for _, item := range picked {
		spent += item.TokenCost
	}
	if spent > budget {
		t.Fatalf("budget=%d delivered %d estimated tokens", budget, spent)
	}
}

func TestSelectKeepsFirstSeedWhenAllReferencesCannotFit(t *testing.T) {
	seeds := []grove.SymbolRecord{
		{ID: "first", Name: "First", FilePath: "a.go", Kind: "function", Signature: "func First()"},
		{ID: "second", Name: "Second", FilePath: "b.go", Kind: "function", Signature: "func Second()"},
	}
	budget := EstimateTokens(Render(seeds[0], DisclosureReference))
	picked := Select(seeds, nil, budget)
	if len(picked) != 1 || picked[0].Symbol.ID != "first" || picked[0].TokenCost > budget {
		t.Fatalf("expected the first named anchor within budget=%d, got %+v", budget, picked)
	}
}

func TestNonAnchorTypeCannotConsumeBudgetWithWholeBody(t *testing.T) {
	typ := grove.SymbolRecord{
		ID: "container", Name: "Container", FilePath: "container.py", Kind: "class",
		Signature: "class Container:", RawText: "class Container:\n" + strings.Repeat("    def unrelated(self): pass\n", 400),
	}
	picked := Select(nil, []Candidate{{Symbol: typ, Relation: RelationDirectCall, Score: 0.9, Category: CategoryDependency}}, 8000)
	if len(picked) != 1 || picked[0].Disclosure != DisclosureSignature {
		t.Fatalf("non-anchor type should remain a compact pointer: %+v", picked)
	}
}

func TestModuleAnchorCannotCrowdOutNamedFunction(t *testing.T) {
	module := grove.SymbolRecord{
		ID: "module", Name: "<top-level>", FilePath: "module.py", Kind: "module",
		QualifiedName: "module", Signature: "module", RawText: strings.Repeat("module statement\n", 2200),
	}
	fn := grove.SymbolRecord{
		ID: "function", Name: "generate", FilePath: "module.py", Kind: "function",
		Signature: "def generate():", RawText: "def generate():\n" + strings.Repeat("    useful work\n", 500),
	}
	picked := Select([]grove.SymbolRecord{module, fn}, nil, 8000)
	if len(picked) != 2 || picked[0].Disclosure != DisclosureSignature || picked[1].Disclosure != DisclosureFull {
		t.Fatalf("module pointer should leave room for named function body: %+v", picked)
	}
}

func TestProfilesShareOneDeterministicRule(t *testing.T) {
	s := SignalValues{RetrievalOrder: 0.7}
	want := Score(s, SelectProfile("default"))
	for _, name := range []string{"implement_feature", "fix_bug", "code_review"} {
		if got := Score(s, SelectProfile(name)); got != want {
			t.Fatalf("profile %s changed order: got %g want %g", name, got, want)
		}
	}
}

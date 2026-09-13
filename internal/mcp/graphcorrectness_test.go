package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func commitGraphFixture(t *testing.T, h *Handler) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@test", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
}

func TestVerifyBaseGraphDoesNotInventSameNameFamily(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"base.py":  "class Base:\n    def savings(self, x): return x\n",
		"child.py": "from base import Base\nclass Child(Base):\n    def savings(self, x): return x\n",
		"decoy.py": "class ScenarioResult:\n    def savings(self, x): return x / 100\ndef unrelated(s: ScenarioResult):\n    return s.savings(5)\n",
	})
	commitGraphFixture(t, h)
	if err := os.WriteFile(filepath.Join(h.Root, "child.py"), []byte("from base import Base\nclass Child(Base):\n    def savings(self, x, y): return x + y\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	missed := fmt.Sprint(out["missedSites"])
	if strings.Contains(missed, "decoy.py") {
		t.Fatalf("invented unrelated obligation: %v", out)
	}
	if _, _, err := h.Grove.PreviewChangeImpacts(t.Context(), nil, nil); err == grove.ErrPreviewUnavailable {
		if out["verdict"] != "review" {
			t.Fatalf("old engine must report missing base coverage: %v", out)
		}
		return
	}
	if !strings.Contains(missed, "base.py") || out["verdict"] != "incomplete" {
		t.Fatalf("missed superclass contract: %v", out)
	}
}

func TestVerifyBaseSitesUseCurrentCoordinates(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	if _, _, err := h.Grove.PreviewChangeImpacts(t.Context(), nil, nil); err == grove.ErrPreviewUnavailable {
		t.Skip("requires coordinated Grove preview API; old engine coverage tested separately")
	}
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true})
	// All surviving calls migrate, but none retain their base line number.
	p := filepath.Join(dir, "use1/a.go")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte("// moved source\n\n\n\n\n\n\n\n\n\n"), content...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "use2/b.go")); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if out["verdict"] != "complete" {
		t.Fatalf("moved/removed callers are satisfied: %v", out)
	}
}

func TestVerifyBaseSitesDoNotTreatMovementAsCallMigration(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	applyAgentChange(t, h, dir, map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true})
	p := filepath.Join(dir, "use1/a.go")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte("// moved source\n\n\n\n\n\n\n\n\n\n"), content...), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	missed := mustJSON(t, out["missedSites"])
	if out["verdict"] != "incomplete" || len(missed) != 1 || missed[0]["file"] != "use1/a.go" || fmt.Sprint(missed[0]["line"]) != "15" {
		t.Fatalf("untouched call must be reported at current line 15: %v", out)
	}
}

func TestImpactSupersCountAndCoverageSurviveDelivery(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"base.py": "class Base:\n    def run(self): pass\nclass Child(Base):\n    def run(self): pass\n",
	})
	raw, err := h.Invoke("prism_change_impact", map[string]any{"query": "Child.run"})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if out["totalSites"] != 2 || out["callerCoverage"] != "partial" || out["familyCompleteness"] != "closed" || out["completeness"] != "partial" {
		t.Fatalf("incorrect inventory or certainty: %v", out)
	}
	rendered, ok := renderChangeImpactAsText(out)
	if !ok || !strings.Contains(rendered, "callerCoverage: partial") || !strings.Contains(rendered, "Base.run") {
		t.Fatalf("lost contract or coverage: %s", rendered)
	}
	cached := graphPointerResponse("prism_change_impact", "hash", 2, out)
	if cached["callerCoverage"] != "partial" || cached["familyCompleteness"] != "closed" {
		t.Fatalf("cached result lost coverage: %v", cached)
	}
}

func TestImpactSiteInventoryDeduplicatesSupers(t *testing.T) {
	s := grove.SymbolRecord{FilePath: "base.py", QualifiedName: "Base.run", Kind: "method", Span: grove.SpanInfo{Start: 2}}
	r := &grove.ChangeImpactResult{Supers: []grove.SymbolRecord{s}, Family: []grove.SymbolRecord{s}}
	if obligationSiteCount(r) != 1 || len(impactSites(r, true)) != 1 {
		t.Fatal("duplicate contract counted twice")
	}
}

func TestVerifyPythonCoverageRequiresReview(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"api.py": "class API:\n    def run(self, x): return x\ndef use(a: API):\n    return a.run(1)\n",
	})
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@test", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(h.Root, "api.py"), []byte("class API:\n    def run(self, x, y): return x + y\ndef use(a: API):\n    return a.run(1, 2)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if out["verdict"] != "review" {
		t.Fatalf("dynamic caller gaps cannot pass: %v", out)
	}
	if out["gateFailure"] != false {
		t.Fatalf("review should not fail the default gate: %v", out)
	}
	raw, err = h.Invoke("prism_verify", map[string]any{"strict": true})
	if err != nil {
		t.Fatal(err)
	}
	strict := raw.(map[string]any)
	if strict["verdict"] != "review" || strict["gateFailure"] != true {
		t.Fatalf("strict must fail review without changing its verdict: %v", strict)
	}
	text, rendered := renderVerifyAsText(strict)
	if !rendered || !strings.Contains(text, "gate: failed") {
		t.Fatalf("MCP text must show the strict gate decision: %q", text)
	}
}

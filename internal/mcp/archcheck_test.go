package mcp

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestToolArchCheck_E2E(t *testing.T) {
	h := viewFixture(t) // alpha -> beta -> gamma production chain

	// No rules declared: honest note, not a hollow pass.
	out, err := h.Invoke("prism_arch_check", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.(map[string]any)["status"]; s != "no-rules" {
		t.Fatalf("status = %v, want no-rules", s)
	}

	// A rule the production chain satisfies: pass.
	out, err = h.Invoke("prism_arch_check", map[string]any{"deny": []any{"alpha -> gamma"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.(map[string]any)["status"]; s != "pass" {
		t.Fatalf("status = %v, want pass (alpha does not reach gamma in production)", s)
	}

	// A rule the chain breaks: fail, citing the concrete crossing site.
	out, err = h.Invoke("prism_arch_check", map[string]any{"deny": []any{"alpha -> beta"}})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != "fail" {
		t.Fatalf("status = %v, want fail", m["status"])
	}
	viols := mustJSON(t, m["violations"])
	if len(viols) != 1 {
		t.Fatalf("violations = %+v", viols)
	}
	edge := viols[0]["edge"].(map[string]any)
	sites, _ := edge["sites"].([]any)
	if len(sites) == 0 {
		t.Fatal("violation carries no sites")
	}
	if f := sites[0].(map[string]any)["fromFile"]; f != "alpha/alpha.go" {
		t.Fatalf("site fromFile = %v", f)
	}

	// A malformed rule fails loudly.
	if _, err := h.Invoke("prism_arch_check", map[string]any{"deny": []any{"no arrow"}}); err == nil {
		t.Fatal("malformed rule must error, not weaken the gate")
	}
}

// Heuristic-only evidence is a review item, not a build break — unless
// strict. Uses the test-only alpha->gamma edge (heuristic attribution is
// not guaranteed for it, so this test constructs the split synthetically
// at the unit layer; here we assert the strict flag plumbing end-to-end).
func TestToolArchCheck_StrictFlag(t *testing.T) {
	h := viewFixture(t)
	// alpha->beta carries precise/measured evidence: strict must not change
	// a hard failure.
	out, err := h.Invoke("prism_arch_check", map[string]any{
		"deny": []any{"alpha -> beta"}, "strict": true})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.(map[string]any)["status"]; s != "fail" {
		t.Fatalf("status = %v, want fail", s)
	}
}

// TestArchCheck_InjectionBenchmark is the engine-ceiling measurement for the
// referee: generate a layered Go module (4 layers x 3 packages, dependencies
// only downward), declare the upward direction forbidden, INJECT seeded
// upward violations, and measure detection. The claim being tested is exact:
// on native-resolved Go imports, arch_check detects every injected violation
// (recall 1.0) and reports nothing that was not injected (precision 1.0).
// Grep has no equivalent operation: the rule is about the component graph,
// not about any string the diff contains.
func TestArchCheck_InjectionBenchmark(t *testing.T) {
	const layers, perLayer, injections = 4, 3, 10
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/arch\n\ngo 1.26\n")

	// Base architecture: l{i}/p{j} imports l{i+1}/p{j} — downward only.
	pkg := func(layer, j int) string { return fmt.Sprintf("l%d/p%d", layer, j) }
	for i := 0; i < layers; i++ {
		for j := 0; j < perLayer; j++ {
			name := fmt.Sprintf("p%d", j)
			fn := fmt.Sprintf("F%d%d", i, j)
			body := fmt.Sprintf("package %s\n\nfunc %s() string { return %q }\n", name, fn, pkg(i, j))
			if i < layers-1 {
				body = fmt.Sprintf(
					"package %s\n\nimport lower %q\n\nfunc %s() string { return lower.F%d%d() }\n",
					name, "example.com/arch/"+pkg(i+1, j), fn, i+1, j)
			}
			write(pkg(i, j)+"/f.go", body)
		}
	}

	// Inject seeded upward violations: a file in a lower layer importing a
	// strictly higher layer. Seeded RNG -> reproducible; dedupe pairs.
	rng := rand.New(rand.NewSource(42))
	injected := map[[2]string]bool{}
	n := 0
	for n < injections {
		fromLayer := 1 + rng.Intn(layers-1) // 1..3 (has a layer above)
		toLayer := rng.Intn(fromLayer)      // strictly higher (smaller index)
		fj, tj := rng.Intn(perLayer), rng.Intn(perLayer)
		pair := [2]string{pkg(fromLayer, fj), pkg(toLayer, tj)}
		if injected[pair] {
			continue
		}
		injected[pair] = true
		write(pair[0]+fmt.Sprintf("/violation%d.go", n), fmt.Sprintf(
			"package p%d\n\nimport upper %q\n\nfunc Bad%d() string { return upper.F%d%d() }\n",
			fj, "example.com/arch/"+pair[1], n, toLayer, tj))
		n++
	}

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Forbid every upward layer direction.
	var deny []any
	for lo := 1; lo < layers; lo++ {
		for hi := 0; hi < lo; hi++ {
			deny = append(deny, fmt.Sprintf("l%d -> l%d", lo, hi))
		}
	}
	start := time.Now()
	out, err := h.Invoke("prism_arch_check", map[string]any{"deny": deny})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	m := out.(map[string]any)
	if m["status"] != "fail" {
		t.Fatalf("status = %v with %d injected violations", m["status"], injections)
	}
	detected := map[[2]string]bool{}
	for _, viol := range mustJSON(t, m["violations"]) {
		edge := viol["edge"].(map[string]any)
		pair := [2]string{edge["from"].(string), edge["to"].(string)}
		if !injected[pair] {
			t.Errorf("FALSE POSITIVE: reported %s -> %s, never injected", pair[0], pair[1])
		}
		detected[pair] = true
		if sites, _ := edge["sites"].([]any); len(sites) == 0 {
			t.Errorf("violation %v without sites", pair)
		}
	}
	for pair := range injected {
		if !detected[pair] {
			t.Errorf("MISSED: injected violation %s -> %s not reported", pair[0], pair[1])
		}
	}
	t.Logf("injection benchmark: %d/%d detected, %d false positives, check wall time %v",
		len(detected), len(injected), len(detected)-len(injected)+countFP(injected, detected), elapsed)
}

func countFP(injected, detected map[[2]string]bool) int {
	fp := 0
	for pair := range detected {
		if !injected[pair] {
			fp++
		}
	}
	return fp
}

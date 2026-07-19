package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// viewFixture builds a real Go module with a three-package dependency CHAIN
// alpha -> beta -> gamma. The module's import structure is the oracle: the
// induced view must contain alpha->beta and beta->gamma, must NOT invent
// alpha->gamma (no primitive edge crosses it), and must report no cycles.
func viewFixture(t *testing.T) *Handler {
	t.Helper()
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
	write("go.mod", "module example.com/m\n\ngo 1.26\n")
	write("alpha/alpha.go", `package alpha

import "example.com/m/beta"

func Entry() string { return beta.Hello() }
`)
	write("beta/beta.go", `package beta

import "example.com/m/gamma"

func Hello() string { return gamma.Base() + "!" }
`)
	write("gamma/gamma.go", `package gamma

func Base() string { return "hi" }
`)
	// Test file whose helper reaches PAST beta straight to gamma: with test
	// files included this manufactures an alpha->gamma edge that does not
	// exist in the production import structure. The default view must
	// exclude it so the map matches the oracle.
	write("alpha/alpha_test.go", `package alpha

import "example.com/m/gamma"

func helperUsesGamma() string { return gamma.Base() }
`)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

func TestToolMap_ImportChainOracle(t *testing.T) {
	h := viewFixture(t)
	out, err := h.Invoke("prism_map", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)

	// Components: the three packages must be present.
	names := map[string]bool{}
	for _, cm := range mustJSON(t, m["components"]) {
		names[cm["name"].(string)] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !names[want] {
			t.Fatalf("component %q missing; got %v", want, names)
		}
	}

	// Induced edges vs the module's real import structure.
	edges := map[string]map[string]any{}
	for _, em := range mustJSON(t, m["edges"]) {
		edges[em["from"].(string)+"->"+em["to"].(string)] = em
	}
	if edges["alpha->beta"] == nil {
		t.Fatalf("missing induced edge alpha->beta; got %v", keys(edges))
	}
	if edges["beta->gamma"] == nil {
		t.Fatalf("missing induced edge beta->gamma; got %v", keys(edges))
	}
	if edges["alpha->gamma"] != nil {
		t.Fatal("induced edge alpha->gamma invented — no primitive edge crosses it")
	}
	// Provenance: the alpha->beta edge must carry at least one concrete site.
	ab := edges["alpha->beta"]
	sites, _ := ab["sites"].([]any)
	if len(sites) == 0 {
		t.Fatalf("alpha->beta has no constituent sites: %v", ab)
	}
	if w, _ := ab["weight"].(float64); int(w) < len(sites) {
		t.Fatalf("weight %v < len(sites) %d", ab["weight"], len(sites))
	}

	// A chain has no cycles.
	if cycles, _ := m["cycles"].([][]string); len(cycles) != 0 {
		t.Fatalf("chain reported cycles: %v", cycles)
	}

	// Tier honesty: the result must state its completeness claim, and
	// test-file exclusion must be reported, not silent.
	if c, _ := m["completeness"].(string); c == "" {
		t.Fatal("map result missing completeness claim")
	}
	if n, _ := m["testFilesExcluded"].(int); n != 1 {
		t.Fatalf("testFilesExcluded = %v, want 1 (alpha_test.go)", m["testFilesExcluded"])
	}
	if s, _ := m["scope"].(string); s == "" {
		t.Fatal("map result missing scope statement")
	}
}

func TestToolMap_IncludeTestsRevealsTestOnlyEdge(t *testing.T) {
	h := viewFixture(t)
	out, err := h.Invoke("prism_map", map[string]any{"include_tests": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	edges := map[string]bool{}
	for _, em := range mustJSON(t, m["edges"]) {
		edges[em["from"].(string)+"->"+em["to"].(string)] = true
	}
	if !edges["alpha->gamma"] {
		t.Fatalf("include_tests=true must reveal the test-only alpha->gamma edge; got %v", edges)
	}
	if n, _ := m["testFilesExcluded"].(int); n != 0 {
		t.Fatalf("include_tests view reports %v excluded", m["testFilesExcluded"])
	}
}

func TestToolMap_ExpandEdge(t *testing.T) {
	h := viewFixture(t)
	out, err := h.Invoke("prism_map", map[string]any{"from": "alpha", "to": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	em := mustJSONObj(t, m["edge"])
	sites, _ := em["sites"].([]any)
	if len(sites) == 0 {
		t.Fatal("expansion returned no sites")
	}
	s0, _ := sites[0].(map[string]any)
	if s0["fromFile"] != "alpha/alpha.go" {
		t.Fatalf("expansion site fromFile = %v", s0["fromFile"])
	}
	if _, err := h.Invoke("prism_map", map[string]any{"from": "alpha", "to": "nosuch"}); err == nil {
		t.Fatal("expanding a nonexistent edge must error")
	}
	if _, err := h.Invoke("prism_map", map[string]any{"from": "alpha"}); err == nil {
		t.Fatal("from without to must error")
	}
}

func TestToolCycles_ChainHasNone(t *testing.T) {
	h := viewFixture(t)
	out, err := h.Invoke("prism_cycles", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n, _ := m["count"].(int); n != 0 {
		t.Fatalf("cycles count = %v, want 0 for an import chain", m["count"])
	}
}

// mustJSON coerces a typed slice result through JSON into []map[string]any —
// the same shape the MCP transport serves clients.
func mustJSON(t *testing.T, v any) []map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustJSONObj(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

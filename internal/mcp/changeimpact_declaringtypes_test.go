package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestToolChangeImpact_RelaysDeclaringTypes drives a Go interface fixture
// end-to-end through the MCP handler: interface member specs are not indexed
// symbols, so the engine surfaces the declaring TYPE as a change site and the
// tool must relay it (group + note + totalSites). This is the grafana
// DataKeyCache/RouteService regression: every G* run at every tier missed the
// interface declaration because the relay payload never carried it.
func TestToolChangeImpact_RelaysDeclaringTypes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "cache.go", `package p

type DataKeyCache interface {
	GetById(id string) (string, bool)
}
`)
	mustWrite(t, dir, "impl.go", `package p

type ossCache struct{}

func (c *ossCache) GetById(id string) (string, bool) { return "", false }

func use(c DataKeyCache) {
	c.GetById("x")
}
`)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "DataKeyCache.GetById"})
	if err != nil {
		t.Fatalf("change_impact: %v", err)
	}
	m := out.(map[string]any)

	dts, ok := m["declaringTypes"].([]map[string]any)
	if !ok || len(dts) != 1 {
		t.Fatalf("declaringTypes = %v, want exactly the DataKeyCache interface", m["declaringTypes"])
	}
	if dts[0]["name"] != "DataKeyCache" || dts[0]["kind"] != "interface" {
		t.Errorf("declaringTypes[0] = %v, want DataKeyCache interface", dts[0])
	}
	if _, ok := m["declaringTypesNote"]; !ok {
		t.Error("declaringTypesNote missing — the relay instruction is part of the payload")
	}
	// totalSites counts declaringTypes: decl(1) + family(1) + caller(1) + type(1).
	if got := m["totalSites"].(int); got != 4 {
		t.Errorf("totalSites = %d, want 4 (declaringTypes counted)", got)
	}
	if safe, ok := m["safeToClaimComplete"].(bool); !ok || safe {
		t.Fatalf("change-impact must not authorize a global completeness claim: %v", m)
	}
	if scope := m["completenessScope"]; scope != "indexed-project-only" {
		t.Fatalf("completenessScope = %v", scope)
	}
	if relay := anySlice(m["relaySites"]); len(relay) != 4 {
		t.Fatalf("relaySites = %v, want the four de-duplicated affected sites", relay)
	}
}

func TestImpactRelaySitesPreservesSameNamedSites(t *testing.T) {
	r := &grove.ChangeImpactResult{Callers: []grove.SymbolRecord{
		{FilePath: "src/Overloads.java", Name: "run", Kind: "method", Span: grove.SpanInfo{Start: 10}},
		{FilePath: "src/Overloads.java", Name: "run", Kind: "method", Span: grove.SpanInfo{Start: 20}},
		{FilePath: "src/Overloads.java", Name: "run", Kind: "field", Span: grove.SpanInfo{Start: 20}},
	}}
	got := impactRelaySites(r, 40)
	if len(got) != 3 {
		t.Fatalf("same-named sites collapsed: %v", got)
	}
	for _, want := range []string{
		"src/Overloads.java:10:run [method]",
		"src/Overloads.java:20:run [method]",
		"src/Overloads.java:20:run [field]",
	} {
		found := false
		for _, site := range got {
			found = found || site == want
		}
		if !found {
			t.Errorf("relaySites missing %q: %v", want, got)
		}
	}
}

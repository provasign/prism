package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestToolChangeImpact_RelaysGoInterfaceMember drives a Go interface fixture
// end-to-end through the MCP handler. This is the grafana
// DataKeyCache/RouteService regression: every G* run at every tier missed the
// interface declaration because the relay payload never carried it. Go
// interface member specs were then unindexed, so the engine surfaced the
// declaring TYPE (declaringTypes) as the change site. Since astkit indexes
// interface methods as symbols (2026-09-26), the member itself is the
// declaration and is relayed at its own line; declaringTypes stays empty.
func TestToolChangeImpact_RelaysGoInterfaceMember(t *testing.T) {
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

	if dts, ok := m["declaringTypes"].([]map[string]any); ok && len(dts) > 0 {
		t.Errorf("declaringTypes = %v, want none: the interface member is indexed", dts)
	}
	// decl(1) + family(1) + caller(1).
	if got := m["totalSites"].(int); got != 3 {
		t.Errorf("totalSites = %d, want 3", got)
	}
	if safe, ok := m["safeToClaimComplete"].(bool); !ok || safe {
		t.Fatalf("change-impact must not authorize a global completeness claim: %v", m)
	}
	if scope := m["completenessScope"]; scope != "indexed-project-only" {
		t.Fatalf("completenessScope = %v", scope)
	}
	relay := anySlice(m["relaySites"])
	found := false
	for _, site := range relay {
		found = found || site == "cache.go:4:GetById [method]"
	}
	if len(relay) != 3 || !found {
		t.Fatalf("relaySites = %v, want the interface member at cache.go:4 among three sites", relay)
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

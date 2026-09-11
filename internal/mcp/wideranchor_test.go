package mcp

// The anchor-selection guard on prism_change_impact: querying a concrete
// implementation whose interface sibling holds a strictly larger closed
// family must surface a widerAnchor hint (the grafana-querydata failure
// shape: concrete QueryData = 4 callers, Service.QueryData = 50-site
// family). Querying the interface itself — already the widest — must not.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func widerAnchorFixture(t *testing.T) *Handler {
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
	write("go.mod", "module example.com/wa\n\ngo 1.26\n")
	// The interface and two implementations.
	write("core/svc.go", "package core\n\n"+
		"type Service interface {\n\tHandle(x string) string\n}\n\n"+
		"type Fast struct{}\n\nfunc (Fast) Handle(x string) string { return x }\n\n"+
		"type Slow struct{}\n\nfunc (Slow) Handle(x string) string { return x + x }\n")
	// Interface-typed callers: dispatch through Service, so they belong to the
	// interface method's family/callers, not to either concrete type's.
	write("app/route.go", "package app\n\nimport \"example.com/wa/core\"\n\n"+
		"func Route(s core.Service, x string) string { return s.Handle(x) }\n"+
		"func Retry(s core.Service, x string) string { return s.Handle(x) + s.Handle(x) }\n"+
		"func Fan(s core.Service, xs []string) []string {\n"+
		"\tout := make([]string, 0, len(xs))\n"+
		"\tfor _, x := range xs {\n\t\tout = append(out, s.Handle(x))\n\t}\n\treturn out\n}\n")
	// One direct caller of the concrete type — the narrow anchor's whole world.
	write("app/direct.go", "package app\n\nimport \"example.com/wa/core\"\n\n"+
		"func Direct(x string) string { var f core.Fast; return f.Handle(x) }\n")
	// An unrelated type with a same-named method is not a wider contract,
	// regardless of the larger family belonging to Service.Handle.
	write("gadget/widget.go", "package gadget\n\n"+
		"type Widget struct{}\n\nfunc (Widget) Handle(x string, n int) string { return x }\n\n"+
		"func Spin(x string) string { var w Widget; return w.Handle(x, 1) }\n")

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

// A synthetic narrow baseline isolates the hint guard; candidate probes still
// use the real graph. Same-name disconnected anchors must not redirect queries.
func TestChangeImpact_WiderAnchorHintOnDisconnectedAnchor(t *testing.T) {
	h := widerAnchorFixture(t)
	narrow := &grove.ChangeImpactResult{
		Query: "Widget.Handle",
		Declarations: []grove.SymbolRecord{{
			Name: "Handle", QualifiedName: "Widget.Handle",
			FilePath: "gadget/widget.go", Kind: "method",
		}},
		// One caller, no family — the mis-anchored shape.
		Callers:      []grove.SymbolRecord{{Name: "Spin", FilePath: "gadget/widget.go", Kind: "function"}},
		Completeness: "closed",
	}
	hint := h.widerAnchorHint(t.Context(), narrow)
	if hint != nil {
		t.Fatalf("unrelated same-name method must not be suggested as a base contract: %v", hint)
	}
}

func TestChangeImpact_RelatedWiderAnchor(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"base.py": "class Base:\n    def run(self): pass\nclass First(Base):\n    def run(self): pass\nclass Second(Base):\n    def run(self): pass\n",
	})
	r, err := h.Grove.ChangeImpact(t.Context(), "First.run")
	if err != nil {
		t.Fatal(err)
	}
	hint := h.widerAnchorHint(t.Context(), r)
	if hint == nil || hint["qualifiedName"] != "Base.run" {
		t.Fatalf("expected related Base.run hint, got %v", hint)
	}
	if strings.Contains(hint["note"].(string), "CLOSED") {
		t.Fatalf("dynamic caller coverage must not be called closed: %v", hint)
	}
}

func TestChangeImpact_NoHintOnWidestAnchor(t *testing.T) {
	h := widerAnchorFixture(t)
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "Service.Handle"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if hint, ok := m["widerAnchor"]; ok {
		t.Fatalf("interface query must not carry a widerAnchor hint (it IS the widest); got %v", hint)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

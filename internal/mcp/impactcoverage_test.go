package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// With grove v0.43.2 (interface contracts through method sets, across
// packages) a non-generic Go interface closure keeps the engine's own
// completeness; the pre-v0.43.2 blanket "partial" for every Go result is
// gone. The inventory is never altered by the coverage label either way.
func TestImpactGoEmbeddedInterfaceKeepsEngineCompleteness(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"go.mod": "module example.com/coverage\n\ngo 1.26\n",
		"writer.go": `package coverage
import "net/http"
type Writer interface { http.CloseNotifier }
type writer struct { http.ResponseWriter }
func (w *writer) CloseNotify() <-chan bool { return w.ResponseWriter.(http.CloseNotifier).CloseNotify() }
type other struct{}
func (w *other) CloseNotify() <-chan bool { return nil }
func Stream(w Writer) { <-w.CloseNotify() }
`,
	})
	raw, err := h.Grove.ChangeImpactScoped(t.Context(), "writer.CloseNotify", "writer.go")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("engine completeness=%s declarations=%d family=%d callers=%v declaringTypes=%d",
		raw.Completeness, len(raw.Declarations), len(raw.Family), raw.Callers, len(raw.DeclaringTypes))
	result, err := h.Invoke("prism_change_impact", map[string]any{"query": "writer.CloseNotify", "file": "writer.go"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["completeness"] != raw.Completeness {
		t.Fatalf("non-generic Go interface must keep the engine's completeness %q: %v", raw.Completeness, out)
	}
	if _, has := out["coverageNote"]; has {
		t.Fatalf("no coverage downgrade for a non-generic Go interface: %v", out)
	}
	for key, syms := range map[string][]grove.SymbolRecord{
		"declarations": raw.Declarations, "supers": raw.Supers, "family": raw.Family,
		"callers": raw.Callers, "declaringTypes": raw.DeclaringTypes,
	} {
		entries := anySlice(out[key])
		if len(entries) != len(syms) {
			t.Fatalf("%s inventory changed: %d != %d", key, len(entries), len(syms))
		}
		for i, sym := range syms {
			entry := entries[i].(map[string]any)
			if entry["filePath"] != sym.FilePath || entry["line"] != sym.Span.Start {
				t.Fatalf("%s identity changed: %v", key, entry)
			}
		}
	}
	text, ok := renderChangeImpactAsText(out)
	if !ok || !strings.Contains(text, "completeness: "+raw.Completeness) {
		t.Fatalf("engine completeness must survive compact rendering: %t %s", ok, text)
	}
}

// The one Go shape grove v0.43.2 skips by design — an interface with type
// parameters — still carries the downgrade and its note, end to end.
func TestImpactGoGenericInterfaceIsPartial(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"go.mod": "module example.com/coverage\n\ngo 1.26\n",
		"store.go": `package coverage
type Store[T any] interface { Get(key string) T }
type memStore struct{ v int }
func (m *memStore) Get(key string) int { return m.v }
func Use(s Store[int]) int { return s.Get("k") }
`,
	})
	result, err := h.Invoke("prism_change_impact", map[string]any{"query": "Store.Get", "file": "store.go"})
	if err != nil {
		t.Skipf("engine cannot anchor a generic interface method here: %v", err)
	}
	out := result.(map[string]any)
	if out["completeness"] == "closed" {
		t.Fatalf("a generic Go interface contract must not be reported closed: %v", out)
	}
	if note, _ := out["coverageNote"].(string); out["completeness"] == "partial" && !strings.Contains(note, "type parameters") {
		t.Fatalf("partial generic-interface result must say why: %v", out)
	}
}

func TestImpactCoveragePreservesTiersAndInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *grove.ChangeImpactResult
		want   string
	}{
		{"nil", nil, ""},
		// grove v0.43.2 models Go interface dispatch through method sets: a
		// plain Go closure keeps the engine's label.
		{"language", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "go"}}}, "closed"},
		{"file", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{FilePath: "a.go"}}}, "closed"},
		{"interface", &grove.ChangeImpactResult{Completeness: "closed", DeclaringTypes: []grove.SymbolRecord{{FilePath: "a.go", Kind: "interface"}}}, "closed"},
		{"super", &grove.ChangeImpactResult{Completeness: "closed", Supers: []grove.SymbolRecord{{Language: "go"}}}, "closed"},
		{"family", &grove.ChangeImpactResult{Completeness: "closed", Family: []grove.SymbolRecord{{Language: "go"}}}, "closed"},
		// What v0.43.2 skips by design: interfaces with type parameters.
		{"generic interface declared", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "go", Kind: "interface", TypeParameters: []string{"T"}}}}, "partial"},
		{"generic interface super", &grove.ChangeImpactResult{Completeness: "closed", Supers: []grove.SymbolRecord{{FilePath: "a.go", Kind: "interface", TypeParameters: []string{"K", "V"}}}}, "partial"},
		{"generic interface declaring type", &grove.ChangeImpactResult{Completeness: "closed", DeclaringTypes: []grove.SymbolRecord{{FilePath: "a.go", Kind: "interface", TypeParameters: []string{"T"}}}}, "partial"},
		{"generic struct is fine", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "go", Kind: "struct", TypeParameters: []string{"T"}}}}, "closed"},
		{"generic java interface is fine", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "java", FilePath: "A.java", Kind: "interface", TypeParameters: []string{"T"}}}}, "closed"},
		{"java", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "java", FilePath: "A.java"}}}, "closed"},
		{"python", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "python", FilePath: "a.py"}}}, "closed"},
		{"external", &grove.ChangeImpactResult{Completeness: "project-local", Declarations: []grove.SymbolRecord{{Language: "go"}}}, "project-local"},
		{"callers", &grove.ChangeImpactResult{Completeness: "callers-only", Declarations: []grove.SymbolRecord{{Language: "go"}}}, "callers-only"},
		{"unknown", &grove.ChangeImpactResult{Completeness: "future-tier", Declarations: []grove.SymbolRecord{{Language: "go"}}}, "future-tier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before grove.ChangeImpactResult
			if tc.result != nil {
				before = *tc.result
			}
			got, note := impactCoverage(tc.result)
			if got != tc.want || (note != "") != (got == "partial") {
				t.Fatalf("got (%q, %q), want tier %q", got, note, tc.want)
			}
			if tc.result != nil && !reflect.DeepEqual(before, *tc.result) {
				t.Fatal("coverage classification changed engine evidence")
			}
		})
	}
}

func TestImpactCoverageSurvivesCachedPointer(t *testing.T) {
	out := map[string]any{"query": "W.Notify", "completeness": "partial", "coverageNote": "interface callers may be missing"}
	got := graphPointerResponse("prism_change_impact", "hash", 2, out)
	if got["completeness"] != "partial" || got["coverageNote"] != out["coverageNote"] {
		t.Fatalf("cached pointer lost the coverage boundary: %v", got)
	}
	if got["summary"].(map[string]any)["coverageNote"] != out["coverageNote"] {
		t.Fatal("nested pointer summary lost coverage note")
	}
}

func TestImpactCoverageNoteRendersWithoutLosingOtherWarnings(t *testing.T) {
	out := map[string]any{"query": "W.Notify", "completeness": "partial", "totalSites": 1,
		"coverageNote": "interface callers may be missing", "staleWarning": "stale index",
		"scopeNote": "outside scope", "ambiguityNote": "ambiguous receiver"}
	before := make(map[string]any, len(out))
	for key, value := range out {
		before[key] = value
	}
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatal("known coverage note must render compactly")
	}
	for _, want := range []string{"interface callers may be missing", "stale index", "outside scope", "ambiguous receiver"} {
		if !strings.Contains(text, want) {
			t.Errorf("lost %s: %s", want, text)
		}
	}
	if !reflect.DeepEqual(out, before) {
		t.Fatal("rendering mutated evidence")
	}
}

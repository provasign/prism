package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestImpactGoEmbeddedInterfaceIsNotClosed(t *testing.T) {
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
	if out["completeness"] != "partial" {
		t.Fatalf("Go interface coverage is not proved closed: %v", out)
	}
	if h.hypLedger.closedSmall != 0 {
		t.Fatal("partial impact must not enter the closed-impact ledger")
	}
	if note, _ := out["coverageNote"].(string); !strings.Contains(note, "interface") || !strings.Contains(note, "missing") {
		t.Fatalf("missing actionable coverage warning: %v", out)
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
	if !ok || !strings.Contains(text, "completeness: partial") || !strings.Contains(text, out["coverageNote"].(string)) {
		t.Fatalf("coverage must survive compact rendering: %t %s", ok, text)
	}
}

func TestImpactCoveragePreservesTiersAndInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *grove.ChangeImpactResult
		want   string
	}{
		{"nil", nil, ""},
		{"language", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{Language: "go"}}}, "partial"},
		{"file", &grove.ChangeImpactResult{Completeness: "closed", Declarations: []grove.SymbolRecord{{FilePath: "a.go"}}}, "partial"},
		{"interface", &grove.ChangeImpactResult{Completeness: "closed", DeclaringTypes: []grove.SymbolRecord{{FilePath: "a.go"}}}, "partial"},
		{"super", &grove.ChangeImpactResult{Completeness: "closed", Supers: []grove.SymbolRecord{{Language: "go"}}}, "partial"},
		{"family", &grove.ChangeImpactResult{Completeness: "closed", Family: []grove.SymbolRecord{{Language: "go"}}}, "partial"},
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

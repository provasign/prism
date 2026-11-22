package view

import (
	"reflect"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func langSym(id, file, lang string) grove.SymbolRecord {
	s := sym(id, file, "function", id, 1, true)
	s.Language = lang
	return s
}

// BUG-21: map hides docs folders and isolated other-language script
// folders, names them, and keeps everything when not asked to exclude.
func TestExcludeAuxiliaryComponents(t *testing.T) {
	symbols := []grove.SymbolRecord{
		langSym("a", "cmd/app/main.go", "go"),
		langSym("b", "internal/core/core.go", "go"),
		langSym("p", "internal/cli/assets/hook.py", "python"),
		langSym("d1", "docs/research/x/run.py", "python"),
		langSym("d2", "docs/research/y/run.py", "python"),
		langSym("w", "web/page.ts", "typescript"),
		langSym("g", "internal/lonely/lonely.go", "go"),
	}
	edges := []grove.Edge{
		edge("a", "b", "calls", "native"),
		edge("d1", "d2", "calls", "astkit"),  // docs folders linked to each other
		edge("w", "b", "calls", "heuristic"), // typescript linked to Go code: kept
	}
	v := Build(symbols, edges, Options{ExcludeAuxiliary: true})
	want := []string{"docs/research/x", "docs/research/y", "internal/cli/assets"}
	if !reflect.DeepEqual(v.AuxiliaryExcluded, want) {
		t.Fatalf("auxiliary = %v, want %v", v.AuxiliaryExcluded, want)
	}
	var names []string
	for _, c := range v.Components {
		names = append(names, c.Name)
	}
	// An isolated Go folder in a Go project stays.
	if !reflect.DeepEqual(names, []string{"cmd/app", "internal/core", "internal/lonely", "web"}) {
		t.Fatalf("components = %v", names)
	}
	if all := Build(symbols, edges, Options{}); len(all.Components) != 7 || all.AuxiliaryExcluded != nil {
		t.Fatalf("default build dropped components: %d %v", len(all.Components), all.AuxiliaryExcluded)
	}
}

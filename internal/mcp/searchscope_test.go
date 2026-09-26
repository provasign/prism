package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// path=/glob= must narrow the SYMBOL pass exactly as they narrow the text
// pass. Measured on jackson (2026-08-25): `anySetter` scoped to
// src/main/java returned 25 symbols, 8 from src/test/java — the agent's own
// narrowing was discarded, it re-grepped to recover, and prism's calls
// became additive instead of substitutive.
func TestFilterSymbolsByScope(t *testing.T) {
	syms := []grove.SymbolRecord{
		{Name: "A", FilePath: "src/main/java/com/x/A.java"},
		{Name: "B", FilePath: "src/test/java/com/x/BTest.java"},
		{Name: "C", FilePath: "src/main/java/com/x/deep/C.java"},
		{Name: "D", FilePath: "other/D.java"},
	}
	got := filterSymbolsByScope(syms, searchScope{paths: []string{"src/main/java"}})
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "C" {
		t.Fatalf("path filter wrong: %v", names(got))
	}
	// trailing slash must behave identically
	if len(filterSymbolsByScope(syms, searchScope{paths: []string{"src/main/java/"}})) != 2 {
		t.Error("trailing slash changed the result")
	}
	// a path must match a DIRECTORY boundary, never a prefix of a sibling
	sib := []grove.SymbolRecord{{Name: "X", FilePath: "src/maintenance/X.java"}}
	if len(filterSymbolsByScope(sib, searchScope{paths: []string{"src/main"}})) != 0 {
		t.Error("src/main must not match src/maintenance")
	}
	// glob narrows by basename or full path
	if len(filterSymbolsByScope(syms, searchScope{glob: []string{"*Test.java"}})) != 1 {
		t.Error("glob by basename failed")
	}
	// "**" crosses directories, as in text scope (click pr3471: **/types.py
	// returned zero symbols).
	if got := filterSymbolsByScope(syms, searchScope{glob: []string{"**/deep/*.java"}}); len(got) != 1 || got[0].Name != "C" {
		t.Errorf("** glob failed: %v", names(got))
	}
	if got := filterSymbolsByScope(syms, searchScope{glob: []string{"**/D.java"}}); len(got) != 1 || got[0].Name != "D" {
		t.Errorf("**/ prefix glob failed: %v", names(got))
	}
	// empty scope passes everything through untouched
	if len(filterSymbolsByScope(syms, searchScope{})) != 4 {
		t.Error("empty scope must not filter")
	}
}

func names(s []grove.SymbolRecord) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

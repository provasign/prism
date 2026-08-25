package mcp

import (
	"context"
	"regexp"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// The gate must degrade to SILENCE, never to a false accusation.
func TestStaleOldNameRefs_SilentWithoutGraphOrRename(t *testing.T) {
	h := newTestHandler(t) // no Grove
	sym := grove.SymbolRecord{Name: "newName", Kind: "function"}
	before := grove.SymbolRecord{Name: "oldName", Kind: "function"}
	if r := h.staleOldNameRefs(context.Background(), sym, &before); r != nil {
		t.Errorf("no graph must yield no findings, got %v", r)
	}
	if r := h.staleOldNameRefs(context.Background(), sym, nil); r != nil {
		t.Error("no before-symbol (not a rename) must yield nothing")
	}
	same := grove.SymbolRecord{Name: "newName", Kind: "function"}
	if r := h.staleOldNameRefs(context.Background(), sym, &same); r != nil {
		t.Error("same name and no parent change is not a rename")
	}
}

// Only CALL-position kinds break on rename. Measured on electrum: the bare
// reference layer flagged 25 sites on a COMPLETE diff because the old name
// survived as variables and kwargs; 24 of those were noise.
func TestIsCallableCoversDecoratorClasses(t *testing.T) {
	for _, k := range []string{"function", "method", "constructor", "class", "struct"} {
		if !isCallable(k) {
			t.Errorf("%s should count as callable (networkx renames a decorator CLASS)", k)
		}
	}
	for _, k := range []string{"variable", "field", "constant", "module", ""} {
		if isCallable(k) {
			t.Errorf("%s must not count as callable", k)
		}
	}
}

// A quoted or commented occurrence is not a call site.
func TestStripCommentsAndStringsLine(t *testing.T) {
	cases := map[string]bool{
		`t = seed_type(seed)`:            true,  // real call
		`# t = seed_type(seed)`:          false, // comment
		`msg = "call seed_type(x) here"`: false, // string
		`d = {"seed_type": 1}`:           false, // key, no call
	}
	re := `seed_type\s*\(`
	for line, wantCall := range cases {
		got := regexpMatch(re, stripCommentsAndStringsLine(line))
		if got != wantCall {
			t.Errorf("%q: call-position=%v want %v (stripped: %q)",
				line, got, wantCall, stripCommentsAndStringsLine(line))
		}
	}
}

func regexpMatch(pat, s string) bool { return regexp.MustCompile(pat).MatchString(s) }

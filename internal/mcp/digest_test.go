package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func dbig(lines int) string { return strings.Repeat("x\n", lines) }

func dsym(name string, start, end int, sig, body string) grove.SymbolRecord {
	return grove.SymbolRecord{
		ID: name, Name: name, QualifiedName: "T." + name, Kind: "method",
		Signature: sig, RawText: body, Span: grove.SpanInfo{Start: start, End: end},
	}
}

// The threshold is the design: below it a whole file beats any round trip.
func TestDigestOnlyForLargeFiles(t *testing.T) {
	h := newTestHandler(t)
	s := []grove.SymbolRecord{dsym("a", 1, 20, "void a()", "void a(){}")}
	if _, ok := h.fileDigest("f.java", dbig(799), s, nil); ok {
		t.Error("799 lines must be delivered whole")
	}
	if _, ok := h.fileDigest("f.java", dbig(1000), s, nil); !ok {
		t.Error("1000 lines must digest")
	}
	if _, ok := h.fileDigest("f.java", dbig(1000), nil, nil); ok {
		t.Error("no indexed symbols means no digest")
	}
}

// Bodies are the scarce resource: they must go to symbols that HAVE a body,
// ranked by match quality. A first cut spent half its slots on one-line
// field declarations that the outline already lists.
func TestDigestBodySelection(t *testing.T) {
	h := newTestHandler(t)
	syms := []grove.SymbolRecord{
		dsym("_delegate", 10, 10, "Deser _delegate;", "Deser _delegate;"),          // declaration
		dsym("resolveDelegate", 100, 140, "void resolveDelegate()", "body-A"),      // real method, exact-ish
		dsym("unrelated", 200, 240, "void unrelated()", "body-B"),                  // no match
		dsym("helper", 300, 340, "void helper(Deser _delegate)", "body-C"),         // incidental mention
	}
	out, ok := h.fileDigest("f.java", dbig(1200), syms, []string{"resolveDelegate"})
	if !ok {
		t.Fatal("expected digest")
	}
	c := out["content"].(string)
	if !strings.Contains(c, "resolveDelegate  (lines 100-140)") {
		t.Error("the name-matching method must get a body")
	}
	if strings.Contains(c, "_delegate  (lines 10-10)") {
		t.Error("a one-line declaration must not consume a body slot")
	}
	if strings.Contains(c, "unrelated  (lines 200-240)") {
		t.Error("non-matching symbol must not get a body")
	}
	// every symbol still appears in the MAP, body or not
	for _, want := range []string{"100-140", "200-240", "300-340"} {
		if !strings.Contains(c, want) {
			t.Errorf("outline must list every symbol; missing %s", want)
		}
	}
}

// With nothing to go on, a digest degrades to a pure map rather than
// guessing which bodies matter.
func TestDigestWithoutHintsIsOutlineOnly(t *testing.T) {
	h := newTestHandler(t)
	syms := []grove.SymbolRecord{dsym("a", 10, 50, "void a()", "body")}
	out, _ := h.fileDigest("f.java", dbig(1200), syms, nil)
	if out["bodiesIncluded"].(int) != 0 {
		t.Errorf("no hints must mean no bodies, got %v", out["bodiesIncluded"])
	}
}

// Generic words in a task string must not match everything.
func TestHintTokensDropsNoise(t *testing.T) {
	got := hintTokens("fix the null value returned from this public method")
	for _, bad := range []string{"null", "value", "this", "public", "method", "fix"} {
		for _, g := range got {
			if strings.EqualFold(g, bad) {
				t.Errorf("generic word %q survived tokenization: %v", bad, got)
			}
		}
	}
	if len(hintTokens("BeanDeserializerBase.deserializeFromObject")) != 2 {
		t.Errorf("qualified name should split into two tokens: %v",
			hintTokens("BeanDeserializerBase.deserializeFromObject"))
	}
}

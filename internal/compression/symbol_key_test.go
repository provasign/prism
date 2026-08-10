package compression

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestSymbolKey_PrefersQualifiedName(t *testing.T) {
	if k := SymbolKey(grove.SymbolRecord{Name: "Close", QualifiedName: "Conn.Close"}); k != "Conn.Close" {
		t.Errorf("got %q", k)
	}
	if k := SymbolKey(grove.SymbolRecord{Name: "Close"}); k != "Close" {
		t.Errorf("got %q", k)
	}
}

// Two same-named members whose keys collide must be dropped from the SHA map
// entirely: an ambiguous identity could pointer a changed body inside a
// "lossless" delta.
func TestComputeSymbolSHAs_DropsCollidingKeys(t *testing.T) {
	content := "func (a A) Close() { a.x() }\n" +
		"func (b B) Close() { b.y() }\n" +
		"func Open() {}\n" +
		"func Open2() {}\n"
	syms := []grove.SymbolRecord{
		{Name: "Close", QualifiedName: "A.Close", Span: grove.SpanInfo{Start: 1, End: 1}},
		{Name: "Close", QualifiedName: "B.Close", Span: grove.SpanInfo{Start: 2, End: 2}},
		{Name: "Open", Span: grove.SpanInfo{Start: 3, End: 3}},  // no qualified name → bare key
		{Name: "Open", Span: grove.SpanInfo{Start: 4, End: 4}}, // collides on bare key
	}
	m := computeSymbolSHAs(syms, content)
	if _, ok := m["A.Close"]; !ok {
		t.Error("qualified A.Close missing")
	}
	if _, ok := m["B.Close"]; !ok {
		t.Error("qualified B.Close missing")
	}
	if _, ok := m["Open"]; ok {
		t.Error("colliding bare key must be dropped")
	}
	if _, ok := m["Close"]; ok {
		t.Error("bare Close must not appear when qualified names exist")
	}
}

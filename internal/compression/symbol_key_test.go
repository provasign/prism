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
	syms := []grove.SymbolRecord{
		{Name: "Close", QualifiedName: "A.Close", RawText: "func (a A) Close() { a.x() }"},
		{Name: "Close", QualifiedName: "B.Close", RawText: "func (b B) Close() { b.y() }"},
		{Name: "Open", RawText: "func Open() {}"},  // no qualified name → bare key
		{Name: "Open", RawText: "func Open2() {}"}, // collides on bare key
	}
	m := computeSymbolSHAs(syms)
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

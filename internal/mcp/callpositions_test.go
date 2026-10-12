package mcp

import (
	"strings"
	"testing"
)

// A rename's callers list where each one calls the target: the compiler's
// positions (go/types) where it ran, the parser's name-matched positions,
// marked, where it did not (Python).
func TestChangeImpactListsEachCallersCallPositions(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"go.mod":  "module ex\n\ngo 1.22\n",
		"w.go":    "package ex\n\ntype W struct{}\n\nfunc (W) Value(n int) int { return n }\n",
		"use.go":  "package ex\n\nfunc use(w W) int {\n\ta := w.Value(1)\n\treturn a + w.Value(2)\n}\n",
		"lib.py":  "def value(x):\n    return x\n",
		"main.py": "from lib import value\n\ndef run():\n    a = value(1); b = value(2)\n    return a + b\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.Value"})
	if !strings.Contains(got, "[at 4:9, 5:15]") || strings.Contains(got, "by name") {
		t.Fatalf("go/types positions missing or marked name-matched:\n%s", got)
	}
	got = callCompact(t, srv, "change_impact", map[string]any{"name": "value", "symbol_file": "lib.py"})
	if !strings.Contains(got, "[by name at 4:9, 4:23]") || !strings.Contains(got, "matched by name only") {
		t.Fatalf("python name-matched positions missing or unmarked:\n%s", got)
	}
}

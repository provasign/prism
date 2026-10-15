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

// A name-matched caller lists every call whose arguments the signature can
// take, keyword arguments included, names the calls it cannot, and drops the
// "omitted" count: werkzeug's get_data calls were cut by a plain argument
// count and only counted, and the agent grepped for them. get_data(cache=False)
// cannot call Response.get_data, which has no cache parameter.
func TestChangeImpactNameMatchedPositionsListEveryCall(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"resp.py": "class Response:\n    def get_data(self, as_text=False):\n        return b''\n",
		"test_resp.py": "from resp import Response\n\ndef test_it():\n    r = Response()\n" +
			"    assert r.get_data() == b''\n    assert r.get_data() == b''\n" +
			"    assert r.get_data(as_text=True) == ''\n    assert r.get_data(cache=False) == b''\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Response.get_data"})
	if !strings.Contains(got, "[by name at 5:14, 6:14, 7:14; not fitting the signature: 8:14]") {
		t.Fatalf("name-matched positions should list the three fitting calls and name the misfit:\n%s", got)
	}
	if strings.Contains(got, "omitted by evidence limit") {
		t.Fatalf("positions list every line; the omitted count should be gone:\n%s", got)
	}
}

// With compiler positions the caller's call lines are the compiler's: a
// same-named call on another type is neither shown nor counted as omitted.
func TestChangeImpactCompilerPositionsDropOtherSameNamedCalls(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"go.mod": "module ex\n\ngo 1.22\n",
		"w.go":   "package ex\n\ntype W struct{}\n\nfunc (W) Value(n int) int { return n }\n\ntype V struct{}\n\nfunc (V) Value(n int) int { return n }\n",
		"use.go": "package ex\n\nfunc use(w W, v V) int {\n\ta := v.Value(1)\n\tb := v.Value(2)\n" +
			"\tc := w.Value(3)\n\td := w.Value(4)\n\te := w.Value(5)\n\treturn a + b + c + d + e\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.Value"})
	if !strings.Contains(got, "[at 6:9, 7:9, 8:9]") {
		t.Fatalf("compiler positions missing:\n%s", got)
	}
	if strings.Contains(got, "v.Value") || strings.Contains(got, "omitted by evidence limit") {
		t.Fatalf("V.Value calls must not appear as W.Value evidence or omitted lines:\n%s", got)
	}
	if !strings.Contains(got, "6: c := w.Value(3)") {
		t.Fatalf("evidence should show the compiler's call lines:\n%s", got)
	}
}

// A same-named call whose argument count the signature cannot take is named
// as not fitting, not listed as a call: hono's path.match(/re/) is
// String.match, not Router.match(method, path). The regex literal's brackets
// must not upset the argument reading.
func TestChangeImpactNameMatchedPositionsNameMisfits(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"router.ts": "export class Router {\n  match(method: string, path: string): string[] {\n    return [method, path]\n  }\n}\n",
		"use.ts": "import { Router } from './router'\nexport function add(router: Router, path: string) {\n  const parts = path.match(/[^}]+(?:{x})?/g)\n" +
			"  const hit = router.match('GET', path)\n  return [parts, hit]\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Router.match"})
	if !strings.Contains(got, "[by name at 4:22; not fitting the signature: 3:22]") {
		t.Fatalf("String.match call should be named as not fitting:\n%s", got)
	}
	if strings.Contains(got, "3: const parts") {
		t.Fatalf("a call that does not fit must not be shown as evidence:\n%s", got)
	}
}

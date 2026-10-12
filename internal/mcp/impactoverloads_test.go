package mcp

import (
	"strings"
	"testing"
)

// An overload migration (gson JsonWriter.value -> stringValue/booleanValue/
// numberValue) needs each call's overload. The answer tags each caller with
// the overload(s) it calls, as the compiler resolved them.
func TestChangeImpactTagsCallersWithTheirOverload(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"p/W.java": "package p;\n\npublic class W {\n    public W value(String s) { return this; }\n    public W value(boolean b) { return this; }\n    public W value(long n) { return this; }\n}\n",
		"p/U.java": "package p;\n\npublic class U {\n    void str(W w) { w.value(\"x\"); }\n    void flag(W w) { w.value(true); }\n    void num(W w) { w.value(1); }\n    void both(W w) {\n        w.value(\"y\");\n        w.value(2L);\n    }\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.value"})
	for _, want := range []string{
		"U.str", "[calls value(String)]",
		"U.flag", "[calls value(boolean)]",
		"U.num", "[calls value(long)]",
		"U.both", "[calls value(String), value(long)]",
		"callSitesByOverload",
		"value(String) — 1 call(s)", "p/U.java: 4",
		"value(boolean) — 1 call(s)", "p/U.java: 5",
		"value(long) — 1 call(s)", "p/U.java: 6",
		"mixed", "p/U.java: 8 (value(String) | value(long)), 9 (value(String) | value(long))",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// A rejected field comes back with the request it should have been: the
// gson agent's search with an invented "terms_note" was its last prism call.
func TestUnsupportedFieldErrorCarriesTheRetry(t *testing.T) {
	srv := compactFixture(t, map[string]string{"a.go": "package a\n\nfunc F() {}\n"})
	got := callCompactErr(t, srv, "search", map[string]any{"terms": []string{"F"}, "regex": true, "terms_note": ""})
	if !strings.Contains(got, `Retry: prism(op="search", args={"regex":true,"terms":["F"]})`) {
		t.Fatalf("no ready retry:\n%s", got)
	}
}

// A same-named call to another type's method with a different arity
// (an annotation-style value()) is not listed under the overloads.
func TestCallSitesByOverloadSkipOtherArity(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"p/W.java": "package p;\n\npublic class W {\n    public W value(String s) { return this; }\n    public W value(long n) { return this; }\n}\n",
		"p/V.java": "package p;\n\npublic class V {\n    public int value() { return 0; }\n}\n",
		"p/U.java": "package p;\n\npublic class U {\n    void mix(W w, V v) {\n        w.value(\"z\");\n        int n = v.value();\n    }\n    void num(W w) { w.value(3L); }\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.value"})
	if !strings.Contains(got, "value(String) — 1 call(s)") || !strings.Contains(got, "p/U.java: 5") || strings.Contains(got, "p/U.java: 5, 6") {
		t.Fatalf("other-arity value() listed, or the real call missing:\n%s", got)
	}
}

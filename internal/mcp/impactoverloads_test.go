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
	if strings.Contains(got, "mixed") {
		t.Fatalf("compiler lines leave nothing mixed:\n%s", got)
	}
	for _, want := range []string{
		"U.str", "[calls value(String)]",
		"U.flag", "[calls value(boolean)]",
		"U.num", "[calls value(long)]",
		"U.both", "[calls value(String), value(long)]",
		"callSitesByOverload (javac resolved each call (line:column of the method name) to its overload; nothing here is inferred)",
		"value(String) — 2 call(s)", "p/U.java: 4:23, 8:11",
		"value(boolean) — 1 call(s)", "p/U.java: 5:24",
		"value(long) — 2 call(s)", "p/U.java: 6:23, 9:11",
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
	if !strings.Contains(got, "value(String) — 1 call(s)") || !strings.Contains(got, "p/U.java: 5:11") || strings.Contains(got, "6:") {
		t.Fatalf("other-arity value() listed, or the real call missing:\n%s", got)
	}
}

// One line can call two overloads; javac resolves both, and both are
// listed (a file:line dedupe kept only the first).
func TestCallSitesByOverloadKeepsTwoOverloadsOnOneLine(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"p/W.java": "package p;\n\npublic class W {\n    public W value(String s) { return this; }\n    public W value(long n) { return this; }\n}\n",
		"p/U.java": "package p;\n\npublic class U {\n    void both(W w) { w.value(\"x\"); w.value(2L); }\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.value"})
	for _, want := range []string{"value(String) — 1 call(s)", "value(long) — 1 call(s)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q (both overloads are called on line 4):\n%s", want, got)
		}
	}
}

// A same-named call javac resolved to none of the overloads (another
// library's static value()) is named in a note, never put in the list an
// agent scripts as its edit map (the gson probe renamed one and broke the
// build).
func TestUnresolvedSameNamedCallStaysOutOfTheList(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"p/W.java": "package p;\n\npublic class W {\n    public W value(String s) { return this; }\n    public W value(long n) { return this; }\n}\n",
		"p/U.java": "package p;\n\npublic class U {\n    void use(W w) { w.value(\"x\"); }\n    void lone(W w) { w.value(2L); }\n}\n",
		"p/X.java": "package p;\n\nimport static q.Lib.value;\n\npublic class X {\n    Object f(Object o) { return value(o); }\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "W.value"})
	list := got
	if i := strings.Index(got, "callSitesByOverload"); i >= 0 {
		list = got[i:]
		if j := strings.Index(list, "\n//"); j >= 0 {
			list = list[:j]
		}
	}
	if strings.Contains(list, "p/X.java") {
		t.Fatalf("a call javac did not resolve to the overloads is in the edit list:\n%s", got)
	}
	if !strings.Contains(got, "NOT in callSitesByOverload: p/X.java:6") {
		t.Fatalf("the unresolved call is not named apart:\n%s", got)
	}
}

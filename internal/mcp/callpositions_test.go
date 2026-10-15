package mcp

import (
	"context"
	"fmt"
	"github.com/provasign/prism/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/provasign/prism/internal/grove"
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

// Same-named calls that are not the target are named, not listed as calls:
// part.match(/re/) on an untyped callback parameter cannot fit
// Router.match(method, path) (hono PatternRouter.add), and path.match on a
// string parameter went to another receiver. The regex literals' brackets
// must not upset the argument reading.
func TestChangeImpactNameMatchedPositionsNameMisfits(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"router.ts": "export class Router {\n  match(method: string, path: string): string[] {\n    return [method, path]\n  }\n}\n",
		"use.ts": "import { Router } from './router'\nexport function add(router: Router, path: string) {\n  const parts = path.match(/[^}]+(?:{x})?/g)\n" +
			"  const hit = router.match('GET', path)\n  const more = [path].map((part) => part.match(/[^}]x/))\n  return [parts, hit, more]\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Router.match"})
	if !strings.Contains(got, "[by name at 4:22; not fitting the signature: 5:42; same name, other receiver: 3:22]") {
		t.Fatalf("String.match calls should be named as not fitting / other receiver:\n%s", got)
	}
	if strings.Contains(got, "3: const parts") || strings.Contains(got, "5: const more") {
		t.Fatalf("a call that does not fit must not be shown as evidence:\n%s", got)
	}
}

// A same-named call whose receiver grove typed as another class is named
// as such, not listed as a call: req is a Request (from_values -> Self), so
// only resp.get_data() calls Response.get_data. A test that only uses req
// is no caller at all.
func TestChangeImpactNameMatchedPositionsNameOtherReceivers(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"wrappers.py": "import typing_extensions as te\n\nclass Request:\n    @classmethod\n    def from_values(cls, data=b'') -> te.Self:\n        return cls()\n\n" +
			"    def get_data(self, cache=True):\n        return b''\n\n\nclass Response:\n    def get_data(self, as_text=False):\n        return b''\n",
		"test_w.py": "from wrappers import Request, Response\n\n\ndef test_request_only():\n    req = Request.from_values(b'x')\n    assert req.get_data() == b''\n\n\n" +
			"def test_mixed():\n    resp = Response()\n    req = Request.from_values(b'x')\n    assert resp.get_data() == b''\n    assert req.get_data() == b''\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Response.get_data"})
	if !strings.Contains(got, "[by name at 12:17; same name, other receiver: 13:16]") {
		t.Fatalf("req.get_data() should be named as another receiver:\n%s", got)
	}
	if strings.Contains(got, "test_request_only") || strings.Contains(got, "13: assert req") {
		t.Fatalf("Request calls must not appear as Response.get_data callers or evidence:\n%s", got)
	}
}

// PHP: defaults and named arguments fit; extra or unknown named arguments do
// not. A plain count rejected get('k') against get($key, $default = null).
func TestChangeImpactPHPCallsCheckedAgainstSignature(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"Cache.php": "<?php\nclass Cache {\n    public function get(string $key, $default = null) {\n        return $default;\n    }\n}\n",
		"use.php": "<?php\nfunction useIt(Cache $cache) {\n    $a = $cache->get('k');\n    $b = $cache->get(key: 'k', default: 2);\n" +
			"    $c = $cache->get('k', 1, 2);\n    $d = $cache->get(name: 'x');\n    return [$a, $b, $c, $d];\n}\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Cache.get"})
	if !strings.Contains(got, "[by name at 3:18, 4:18; not fitting the signature: 5:18, 6:18]") {
		t.Fatalf("PHP calls not checked against the signature:\n%s", got)
	}
}

// Source in a language prism does not index is invisible to the graph. A
// name it carries is reported, and a compiler-backed "complete" answer is
// withdrawn: a TypeScript function's callers in .vue files are not counted.
func TestUnindexedLanguageMentionsAreReported(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"lib.ts":      "export function formatPrice(n: number): string {\n  return String(n)\n}\n",
		"use.ts":      "import { formatPrice } from './lib'\nexport const a = formatPrice(1)\n",
		"Price.vue":   "<script setup lang=\"ts\">\nimport { formatPrice } from './lib'\nconst p = formatPrice(2)\n</script>\n",
		"cache.rb":    "class Cache\n  def fetch(key)\n    key\n  end\nend\n",
		"Gemfile.txt": "fetch\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "formatPrice"})
	if !strings.Contains(got, "also appears in 1 file(s) in Vue, which prism does not index: Price.vue") {
		t.Fatalf("Vue mention not reported:\n%s", got)
	}
	if strings.Contains(got, "safeToClaimComplete: true") {
		t.Fatalf("an answer missing .vue callers must not claim completeness:\n%s", got)
	}
	got = callCompact(t, srv, "lookup", map[string]any{"name": []string{"Cache.fetch"}})
	if !strings.Contains(got, "in Ruby, which prism does not index: cache.rb") {
		t.Fatalf("Ruby definition not reported on a lookup miss:\n%s", got)
	}
}

// A member of the change set that itself calls into it carries those call
// positions: super().hl(text) inside an override changes with the
// signature. The override was listed, its call was not (19 of 21 Python
// misses on the wide bed, 2026-10-14).
func TestChangeImpactMembersShowTheirOwnCalls(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/h.py": "class Base:\n    def hl(self, text):\n        return text\n\n\nclass Json(Base):\n" +
			"    def hl(self, text):\n        super().hl(text)\n        return text\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Base.hl"})
	if !strings.Contains(got, "Json.hl  pkg/h.py:7  [itself calls the change set by name at 8:17]") {
		t.Fatalf("the override's super() call is missing:\n%s", got)
	}
}

// The caller header counts only compiler-resolved callers as resolved: an
// AST-narrowed guess through an untyped receiver is not a resolution.
func TestImpactCallerEvidenceCountsOnlyCompilerResolution(t *testing.T) {
	r := &grove.ChangeImpactResult{
		Callers: []grove.SymbolRecord{
			{ID: "a", Name: "a", QualifiedName: "a", FilePath: "a.ts", Span: grove.SpanInfo{Start: 1}},
			{ID: "b", Name: "b", QualifiedName: "b", FilePath: "b.ts", Span: grove.SpanInfo{Start: 2}},
			{ID: "c", Name: "c", QualifiedName: "c", FilePath: "c.ts", Span: grove.SpanInfo{Start: 3}},
		},
		CallerLines:        map[string][]string{"a": {"1:5"}},
		NameMatchedCallers: []string{"c"},
	}
	got := impactCallerEvidence(r)
	want := "1 of 3 callers resolved by the compiler; the others are matched by name and receiver inference; name-matched only (most likely false positives): c.ts:3:c"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	r.CallerLines = nil
	if got := impactCallerEvidence(r); got != "name-matched only (most likely false positives): c.ts:3:c" {
		t.Fatalf("no compiler: got %q", got)
	}
}

// A wide bare name over two local families is reported as two families even
// when the overrides sort before their interface: each override's own change
// set covers only itself, and querying them in path order hit the query cap,
// after which one "external" family was inferred from parameter shapes and
// the other family silently dropped (databind createContextual, 2026-10-14).
func TestWideBareNameKeepsEveryLocalFamily(t *testing.T) {
	files := map[string]string{
		"src/z/Contract.java":  "package z;\n\npublic interface Contract {\n    void handle(String s);\n}\n",
		"src/z/Other.java":     "package z;\n\npublic interface Other {\n    void handle(int a, int b);\n}\n",
		"src/z/OtherImpl.java": "package z;\n\npublic class OtherImpl implements Other {\n    public void handle(int a, int b) {}\n}\n",
	}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("A%02d", i)
		files["src/a/"+name+".java"] = "package a;\n\nimport z.Contract;\n\npublic class " + name + " implements Contract {\n    public void handle(String s) {}\n}\n"
	}
	srv := compactFixture(t, files)
	got := callCompactErr(t, srv, "change_impact", map[string]any{"name": "handle"})
	if !strings.Contains(got, "2 separate override families") || strings.Contains(got, "overridesExternal") {
		t.Fatalf("both local families must be named, none inferred external:\n%s", got)
	}
}

// Type.method on a type that only inherits the method still resolves when
// the type name is shared: a test's nested `class Flask(pkg.app.Flask)` and
// flask's Blueprint subclassing the sansio Blueprint made change_impact
// Flask.route and Blueprint.route fail while lookup resolved them.
func TestInheritedImpactWithSameNamedTypes(t *testing.T) {
	files := map[string]string{
		"pkg/__init__.py":          "",
		"pkg/sansio/__init__.py":   "",
		"pkg/sansio/scaffold.py":   "class Scaffold:\n    def route(self, rule):\n        return rule\n",
		"pkg/sansio/app.py":        "from .scaffold import Scaffold\n\n\nclass App(Scaffold):\n    pass\n",
		"pkg/sansio/blueprints.py": "from .scaffold import Scaffold\n\n\nclass Blueprint(Scaffold):\n    pass\n",
		"pkg/app.py":               "from .sansio.app import App\n\n\nclass Flask(App):\n    pass\n",
		"pkg/blueprints.py":        "from .sansio.blueprints import Blueprint as SansioBlueprint\n\n\nclass Blueprint(SansioBlueprint):\n    pass\n",
		"tests/test_cfg.py":        "import pkg.app\n\n\ndef test_sub():\n    class Flask(pkg.app.Flask):\n        pass\n",
	}
	srv := compactFixture(t, files)
	for _, q := range []string{"Flask.route", "Blueprint.route"} {
		got := callCompact(t, srv, "change_impact", map[string]any{"name": q})
		if !strings.Contains(got, "it is inherited from Scaffold.route") {
			t.Fatalf("%s: inherited answer missing:\n%s", q, got)
		}
	}
}

// A class body passing a method to property() references it on the
// argument's own line; the class is listed with that position.
func TestChangeImpactClassBodyPropertyReference(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"resp.py": "class Response:\n    def get_data(self):\n        return b''\n\n    def set_data(self, v):\n        pass\n\n" +
			"    data = property(\n        get_data,\n        set_data,\n    )\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Response.get_data"})
	if !strings.Contains(got, "Response  resp.py:1  [by name at 9:9]") {
		t.Fatalf("property(get_data, ...) reference missing:\n%s", got)
	}
}

// A Go free function's change set includes its uses as a value (the
// compiler resolves them), and with both calls and values listed it is
// complete like a method's (chi URLParam answered "callers-only").
func TestGoFreeFunctionValueReferencesAndCompleteness(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"go.mod":  "module ex\n\ngo 1.22\n",
		"lib.go":  "package ex\n\nfunc Handle(n int) int { return n }\n",
		"call.go": "package ex\n\nfunc Direct() int { return Handle(1) }\n",
		"val.go":  "package ex\n\nfunc apply(f func(int) int) int { return f(2) }\n\nfunc AsValue() int { return apply(Handle) }\n",
	})
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "Handle"})
	for _, want := range []string{"Direct  call.go:3  [at 3:28]", "AsValue  val.go:5  [at 5:35]", "safeToClaimComplete: true"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// A read is served while the first index still runs: it reads the file from
// disk. Every early call on a large cold repository used to fail after a
// minute, reads included.
func TestReadIsNotHeldBehindTheFirstIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(context.Background()); err != nil { // open, as before the first index starts
		t.Fatal(err)
	}
	defer gc.Shutdown()
	h := NewHandlerWithReady(config.Default(), root, gc, make(chan struct{})) // never ready
	done := make(chan any, 1)
	go func() {
		out, err := h.Invoke("prism_read", map[string]any{"file": "a.txt"})
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	select {
	case got := <-done:
		if err, isErr := got.(error); isErr {
			t.Fatalf("read failed while indexing: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read waited for the first index")
	}
}

// A delivered body keeps the first line's indentation: an agent copies it
// into an Edit, and a flush-left first line matched nothing in the file.
func TestLookupBodyKeepsFirstLineIndentation(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"a.py": "class A:\n    def run(self):\n        return 1\n",
	})
	got := callCompact(t, srv, "lookup", map[string]any{"name": []string{"A.run"}})
	if !strings.Contains(got, "2→    def run(self):") {
		t.Fatalf("first line lost its indentation:\n%s", got)
	}
}

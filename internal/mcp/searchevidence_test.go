package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestSearchEvidenceShowsLocalRelatedUseAndSkippedTest(t *testing.T) {
	root := t.TempDir()
	for file, body := range map[string]string{
		"src/config.py":        "class Config:\n    def __init__(self, tls_context):\n        self.tls_context = tls_context\n",
		"src/manager.py":       "from config import Config\n\ndef create(proxy_tls_context):\n    config = Config(proxy_tls_context)\n    return config\n",
		"src/worker.py":        "from config import Config\n\nclass Worker:\n    def __init__(self, config: Config):\n        self.config = config\n        self.tls_context = None\n\n    def connect(self, sock):\n        return wrap_socket(sock, tls_context=self.tls_context)\n\n    def tunnel(self, sock):\n        config = cast(Config, self.config)\n        tls_context = config.tls_context\n        return wrap_socket(sock, tls_context=tls_context)\n",
		"src/unrelated.py":     "def unrelated(sock):\n    return wrap_socket(sock, tls_context=self.tls_context)\n",
		"tests/test_worker.py": "def test_proxy_tls_context():\n    if proxy_tls_context:\n        pytest.skip('pending')\n    config = Config(proxy_tls_context)\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Index(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	out := map[string]any{"results": []map[string]any{
		{"query": "proxy_tls_context", "textHits": []map[string]any{
			{"file": "src/manager.py", "hits": []map[string]any{{"line": 3}, {"line": 4}}},
			{"file": "tests/test_worker.py", "hits": []map[string]any{{"line": 1}, {"line": 2}, {"line": 4}}},
		}},
		{"query": "create", "textHits": []map[string]any{
			{"file": "src/manager.py", "hits": []map[string]any{{"line": 3}}},
		}},
	}}
	got := h.compactSearchBodiesEnclosing(t.Context(), out)
	for _, want := range []string{"src/manager.py", "src/worker.py", "tls_context=self.tls_context",
		"config.tls_context", "indexed reference to Config", "relationship unverified", "tests/test_worker.py", "pytest.skip"} {
		if !strings.Contains(got, want) {
			t.Fatalf("search evidence missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "src/unrelated.py") {
		t.Fatalf("related spelling widened search to an unrelated file:\n%s", got)
	}
	out["scopeNote"] = "match lists restricted to path=[\"src/manager.py\"]"
	scoped := h.compactSearchBodiesEnclosing(t.Context(), out)
	if strings.Contains(scoped, "src/worker.py") {
		t.Fatalf("reference expansion crossed a requested scope:\n%s", scoped)
	}
}

func TestSearchEvidenceFieldMatchIncludesSmallOwner(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"Policy.java": `class Policy {
    private Mode SKIP;
    void unrelated() {}
    void applyPolicy() { System.out.println("behavioral sibling"); }
}
enum Mode { SKIP, KEEP }
`,
	})
	out, err := h.Invoke("prism_search", map[string]any{"query": []any{"SKIP", "Mode"}})
	if err != nil {
		t.Fatal(err)
	}
	got := h.compactSearchBodiesEnclosing(t.Context(), out.(map[string]any))
	if !strings.Contains(got, "applyPolicy") || !strings.Contains(got, "small enclosing type included") {
		t.Fatalf("field match did not include its lexical owner:\n%s", got)
	}
}

func TestEvidenceLineScorePrefersUseOverDeclarationAndComment(t *testing.T) {
	if evidenceLineScore("ssl_context=self.ssl_context,") <= evidenceLineScore("ssl_context: ssl.SSLContext | None = None,") ||
		evidenceLineScore("and proxy_config.use_forwarding_for_https") <= evidenceLineScore("# use_forwarding_for_https") {
		t.Fatal("executable use must rank above declarations and comments")
	}
}

// Reproduces pallets/click#3466 (src/click/shell_completion.py): a generic
// term ("elif") matches both a real Python elif clause and a Zsh elif
// embedded in a Python triple-quoted template string. evidenceLineScore must
// not systematically under-score the embedded-shell line just because it
// doesn't fit Python's own statement shapes ("if "/"return "/assignment),
// or the ranking promotes the unrelated Python branch as "Exact source" and
// buries the actually relevant template line as a locator.
func TestEvidenceLineScoreDoesNotPenalizeEmbeddedShellConditional(t *testing.T) {
	pythonElif := `elif "=" in incomplete and _start_of_option(ctx, incomplete):`
	zshElif := `elif [[ "$type" == "dir" ]]; then`
	if evidenceLineScore(zshElif) < evidenceLineScore(pythonElif) {
		t.Fatalf("embedded shell elif (%d) scored below unrelated Python elif (%d); "+
			"a generic term match should not be tie-broken in favor of host-language shape",
			evidenceLineScore(zshElif), evidenceLineScore(pythonElif))
	}
}

// The shape rules were written against Python/JS statements; every line here
// is an executable use in its own language that used to score like a bare
// declaration (0) or a comment (-3). Each must clear the declaration
// baseline, and the declaration/comment baselines must stay where they are.
func TestEvidenceLineScoreExecutableUseAcrossLanguages(t *testing.T) {
	declaration := evidenceLineScore("ssl_context: ssl.SSLContext | None = None,")
	if declaration != 0 {
		t.Fatalf("declaration baseline moved: %d", declaration)
	}
	uses := []string{
		// Go: `:=` is not a type annotation; pointer writes are not comments.
		`x, err := f()`, `for i := 0; i < n; i++ {`, `for _, v := range xs {`,
		`switch v := x.(type) {`, `ch <- v`, `*p = v`, `*count++`,
		// Rust / C++ / PHP: `::` paths and match arms.
		`Self::helper(x);`, `std::process::exit(1);`, `ns::call(x);`,
		`Foo::Bar => baz(x),`, `std::string s = f();`,
		// Typed bindings whose initializer is a call.
		`let x: Foo = f();`, `val x: Int = f()`, `const x: Foo = f()`, `x: int = f()`,
		// Branch and loop headers beyond if/return.
		`elif foo(x):`, `for item in items:`, `while pending:`, `with open(p) as fh:`,
		`except ValueError as e:`, `for (String s : list) {`, `elseif x then`,
		`for f in *.txt; do`, `while read line; do`, `case "$x" in`,
		// Paren-less and operator-only statements.
		`cond ? a : b`, `foo.bar arg`, `puts x`, `raise ArgumentError`,
		`echo "$x" | grep foo`, `cd dir && make`, `exit 1`,
	}
	for _, line := range uses {
		if got := evidenceLineScore(line); got <= declaration {
			t.Errorf("%q scored %d, not above a declaration (%d)", line, got, declaration)
		}
	}
	for _, line := range []string{
		`def foo(a, b):`, `foo(a: string): void {`, `"key": value,`, `x: Foo`,
	} {
		if got := evidenceLineScore(line); got != 0 {
			t.Errorf("declaration %q scored %d, want 0", line, got)
		}
	}
	for _, line := range []string{`# note`, `// note`, `-- note`, `* continuation`, `*/`, `:param x: the x`} {
		if got := evidenceLineScore(line); got != -3 {
			t.Errorf("comment %q scored %d, want -3", line, got)
		}
	}
	if evidenceLineScore(`x = f()`) <= evidenceLineScore(`if f(x):`) {
		t.Error("an assignment with a call must still outrank a branch header")
	}
}

package textsearch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture builds a small tree with known matches, a decoy in an excluded
// dir, and a binary file.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "node_modules/\n")
	write("a.go", "package a\n\n// NeedleOne appears here\nfunc NeedleOne() {}\n")
	write("sub/b.txt", "plain text with needleone lowercase\nno match line\n")
	write("node_modules/dep/c.js", "needleone should be excluded\n")
	write(".grove/state.json", "needleone in hidden dir\n")
	write("bin.dat", "needleone\x00binary\n")
	return dir
}

func hitFiles(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.File)
	}
	return out
}

// assertFixtureResult checks the invariants every backend must satisfy on
// the fixture: both real files found (case-insensitively), excluded and
// binary files absent, correct line numbers.
func assertFixtureResult(t *testing.T, r Result) {
	t.Helper()
	files := map[string]int{}
	for _, h := range r.Hits {
		files[h.File] = h.Line
	}
	if files["a.go"] == 0 {
		t.Errorf("a.go not found; hits: %v", hitFiles(r.Hits))
	}
	if files["sub/b.txt"] != 1 {
		t.Errorf("sub/b.txt line = %d, want 1 (case-insensitive match)", files["sub/b.txt"])
	}
	for f := range files {
		if strings.Contains(f, "node_modules") || strings.HasPrefix(f, ".grove") {
			t.Errorf("excluded path leaked into results: %s", f)
		}
		if f == "bin.dat" {
			t.Errorf("binary file matched: %s", f)
		}
	}
}

func TestNativeSearchFindsMatches(t *testing.T) {
	dir := fixture(t)
	r := nativeSearch(context.Background(), dir, "needleone", Options{}.withDefaults())
	if r.Backend != "native" {
		t.Fatalf("backend = %q", r.Backend)
	}
	assertFixtureResult(t, r)
}

func TestRgAndGrepMatchNativeSemantics(t *testing.T) {
	dir := fixture(t)
	if _, err := exec.LookPath("rg"); err == nil {
		r, ok := runRg(context.Background(), dir, "NeedleOne", Options{}.withDefaults())
		if !ok {
			t.Fatal("rg invocation failed")
		}
		assertFixtureResult(t, r)
	}
	if _, err := exec.LookPath("grep"); err == nil {
		r, ok := runGrep(context.Background(), dir, "NeedleOne", Options{}.withDefaults())
		if !ok {
			t.Fatal("grep invocation failed")
		}
		assertFixtureResult(t, r)
	}
}

// TestPatternCannotInjectFlags: a pattern that looks like an option must be
// searched literally, never parsed as a flag — the textsearch analogue of
// verify.go's git-base guard.
func TestPatternCannotInjectFlags(t *testing.T) {
	dir := t.TempDir()
	content := "the literal text --version appears here\nand -e too\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"--version", "-e"} {
		r := Search(context.Background(), dir, pattern, Options{})
		found := false
		for _, h := range r.Hits {
			if h.File == "f.txt" && strings.Contains(h.Text, pattern) {
				found = true
			}
		}
		if !found {
			t.Errorf("pattern %q (backend %s): not matched literally; hits=%v",
				pattern, r.Backend, r.Hits)
		}
	}
}

func TestSearchEmptyPatternReturnsNothing(t *testing.T) {
	dir := fixture(t)
	r := Search(context.Background(), dir, "  ", Options{})
	if len(r.Hits) != 0 {
		t.Errorf("empty pattern returned %d hits", len(r.Hits))
	}
}

func TestSearchNoMatchesIsEmptyNotError(t *testing.T) {
	dir := fixture(t)
	r := Search(context.Background(), dir, "zzz-no-such-string-zzz", Options{})
	if len(r.Hits) != 0 {
		t.Errorf("expected no hits, got %v", r.Hits)
	}
}

func TestMaxHitsTruncates(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("needle line\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	r := nativeSearch(context.Background(), dir, "needle", Options{MaxHits: 5, MaxPerFile: 3, Timeout: time.Second})
	if len(r.Hits) != 3 { // per-file cap binds first
		t.Errorf("got %d hits, want 3 (per-file cap)", len(r.Hits))
	}
}

func TestLongLinesAreTruncated(t *testing.T) {
	dir := t.TempDir()
	long := "needle " + strings.Repeat("x", 5000)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "needle", Options{})
	if len(r.Hits) == 0 {
		t.Fatal("no hits")
	}
	if len(r.Hits[0].Text) > maxLineLen+8 {
		t.Errorf("hit text length %d exceeds cap", len(r.Hits[0].Text))
	}
}

func TestBackendDetectReturnsKnownValue(t *testing.T) {
	switch Backend() {
	case "rg", "grep", "native":
	default:
		t.Errorf("unexpected backend %q", Backend())
	}
}

func TestRegexModeAllBackends(t *testing.T) {
	dir := t.TempDir()
	content := "alpha_handler_v2 here\nplain alpha here\n"
	if err := os.WriteFile(filepath.Join(dir, "r.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(name string, r Result) {
		t.Helper()
		if len(r.Hits) != 1 || r.Hits[0].Line != 1 {
			t.Errorf("%s: regex should match only line 1, got %v", name, r.Hits)
		}
	}
	opts := Options{Regex: true}.withDefaults()
	check("native", nativeSearch(context.Background(), dir, `alpha_\w+_v\d`, opts))
	if _, err := exec.LookPath("rg"); err == nil {
		r, ok := runRg(context.Background(), dir, `alpha_\w+_v\d`, opts)
		if !ok {
			t.Fatal("rg failed")
		}
		check("rg", r)
	}
	if _, err := exec.LookPath("grep"); err == nil {
		r, ok := runGrep(context.Background(), dir, `alpha_[a-z]+_v[0-9]`, opts) // POSIX-safe classes
		if !ok {
			t.Fatal("grep failed")
		}
		check("grep", r)
	}
}

func TestInvalidRegexFallsBackToLiteral(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a literal [broken( pattern\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "[broken(", Options{Regex: true})
	if len(r.Hits) != 1 {
		t.Errorf("invalid regex must degrade to literal match, got %v (backend %s)", r.Hits, r.Backend)
	}
}

// TestGitignoreIsHonoredNotPrismOpinion: an agent asking for a text search is
// asking for ripgrep's semantics. Prism skips what the PROJECT gitignores —
// not a hardcoded list of its own. A hardcoded list made prism unable to
// answer "where does this library define X" about code that is really there.
func TestGitignoreIsHonoredNotPrismOpinion(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x = st.checkbox(\"hi\")\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("dashboard.py")
	write("env/lib/python3.9/site-packages/streamlit/app.py")
	write("lib/helper.py") // NOT ignored: must remain searchable

	// gitignored -> skipped, exactly as ripgrep would.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("env/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Result{
		nativeSearch(context.Background(), dir, "st.checkbox", Options{}.withDefaults()),
		Search(context.Background(), dir, "st.checkbox", Options{}),
	} {
		files := map[string]bool{}
		for _, h := range r.Hits {
			files[h.File] = true
		}
		if files["env/lib/python3.9/site-packages/streamlit/app.py"] {
			t.Errorf("backend %s searched a gitignored tree", r.Backend)
		}
		if !files["dashboard.py"] || !files["lib/helper.py"] {
			t.Errorf("backend %s missed project source: %v", r.Backend, r.Hits)
		}
	}

	// NOT gitignored -> searched. The agent asked; prism does not second-guess.
	if err := os.Remove(filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	r := nativeSearch(context.Background(), dir, "st.checkbox", Options{}.withDefaults())
	found := false
	for _, h := range r.Hits {
		if strings.Contains(h.File, "site-packages") {
			found = true
		}
	}
	if !found {
		t.Error("a tree the project does NOT ignore must be searched — prism may not " +
			"substitute its own exclusion policy for the agent's request")
	}
}

// TestBinResolvesOnlyItsOwnBackend: bin() must not hand one engine's path to
// another — grep and rg take different flags, so the call just fails.
func TestBinResolvesOnlyItsOwnBackend(t *testing.T) {
	other := "grep"
	if Backend() == "grep" {
		other = "rg"
	}
	if got := bin(other); got != other {
		t.Errorf("bin(%q) = %q — returned the detected backend's path", other, got)
	}
}

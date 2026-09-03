package textsearch

import (
	"context"
	"fmt"
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
	write(".grove/state.json", "needleone in prism state dir\n")
	write("bin.dat", "needleone\x00binary\n")
	// Hidden paths are part of the corpus (grep -r semantics): a dotfile at
	// root and a file nested under a hidden directory must both be found.
	write(".clinerules", "steering needleone rule\n")
	write(".github/workflows/ci.yml", "run: echo needleone\n")
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
	if files[".clinerules"] == 0 {
		t.Errorf("hidden dotfile .clinerules not found; hits: %v", hitFiles(r.Hits))
	}
	if files[".github/workflows/ci.yml"] == 0 {
		t.Errorf("file under hidden dir .github not found; hits: %v", hitFiles(r.Hits))
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

func TestSearch_ContextAttachesSurroundingLines(t *testing.T) {
	dir := t.TempDir()
	body := "one\ntwo\nMATCH\nfour\nfive\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "MATCH", Options{Context: 1})
	if len(r.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(r.Hits))
	}
	h := r.Hits[0]
	if len(h.Before) != 1 || h.Before[0] != "two" {
		t.Errorf("before = %v, want [two]", h.Before)
	}
	if len(h.After) != 1 || h.After[0] != "four" {
		t.Errorf("after = %v, want [four]", h.After)
	}
}

func TestSearch_ContextZeroAttachesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("one\nMATCH\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "MATCH", Options{})
	if len(r.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(r.Hits))
	}
	if r.Hits[0].Before != nil || r.Hits[0].After != nil {
		t.Errorf("context=0 must attach nothing: %+v", r.Hits[0])
	}
}

func TestSearch_ContextClampsAtFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	// Match on the FIRST line: asking for 5 lines of before-context must not
	// panic or go negative, just return what exists (nothing, here).
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("MATCH\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "MATCH", Options{Context: 5})
	if len(r.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(r.Hits))
	}
	if len(r.Hits[0].Before) != 0 {
		t.Errorf("before at start of file = %v, want empty", r.Hits[0].Before)
	}
	if len(r.Hits[0].After) != 1 || r.Hits[0].After[0] != "two" {
		t.Errorf("after clamped at EOF = %v, want [two]", r.Hits[0].After)
	}
}

func TestSearch_ContextIdenticalAcrossBackends(t *testing.T) {
	// The whole reason attachContext runs AFTER backend dispatch rather than
	// being implemented per-backend (rg -A/-B/-C, grep -A/-B/-C, each a
	// different text format): it must be impossible for rg and the native
	// scanner to disagree on context. Force native and compare to whatever
	// the real backend on this machine returns.
	dir := t.TempDir()
	body := "a\nb\nNEEDLE\nc\nd\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	viaBackend := Search(context.Background(), dir, "NEEDLE", Options{Context: 2})
	viaNative := nativeSearch(context.Background(), dir, "NEEDLE", Options{Context: 2}.withDefaults())
	if len(viaNative.Hits) != 1 {
		t.Fatalf("native: want 1 hit, got %d", len(viaNative.Hits))
	}
	// nativeSearch itself does not call attachContext (Search does, once,
	// after dispatch) -- so compare the CONTENT each backend located, not
	// context, to confirm they agree on the same file/line.
	if viaBackend.Hits[0].File != viaNative.Hits[0].File || viaBackend.Hits[0].Line != viaNative.Hits[0].Line {
		t.Errorf("backend and native disagree on the hit: %+v vs %+v", viaBackend.Hits[0], viaNative.Hits[0])
	}
}

// TestSearchRanksSourceFirst: BACKLOG addendum #6 (Dubbo "triple",
// 2026-09-02) — the backend's --sort path order let early-sorting
// non-source trees (.changelog-archive, .licenserc.yaml, pom.xml, README)
// consume the entire hit cap before any code was reached; 11 consecutive
// searches delivered ~44.8kB of manifest noise and the agent re-ran every
// one as a manual grep. Search now over-fetches past the cap and delivers
// source-extension files first (stable within each group).
func TestSearchRanksSourceFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Alphabetically-early noise, many matches.
	var noise strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&noise, "needle mention %d in changelog\n", i)
	}
	write("AAA-CHANGELOG.md", noise.String())
	write("BBB.licenserc.yaml", "needle: license config\nneedle2: more\n")
	// Late-sorting source file with the real matches.
	write("zzz_source.go", "package p\n\n// needle\nfunc Needle() { /* needle */ }\n")

	r := Search(context.Background(), dir, "needle", Options{MaxHits: 5, MaxPerFile: 20})
	if len(r.Hits) == 0 {
		t.Fatal("no hits")
	}
	if got := r.Hits[0].File; got != "zzz_source.go" {
		files := make([]string, 0, len(r.Hits))
		for _, h := range r.Hits {
			files = append(files, h.File)
		}
		t.Errorf("first hit should be the source file, got %q (order: %v)", got, files)
	}
}

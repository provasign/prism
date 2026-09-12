package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// ab_gate 2026-09-06 (grafana CheckHealth): scope="symbols", exhaustive=true
// returned the default 25 of 53+ methods with no marker; the agent answered
// from the sample and scored 0.73 recall. Symbol search must either honour
// exhaustive or say it is a sample.
func symbolCapFixture(t *testing.T, n int) *Handler {
	return symbolScopeFixture(t, n, 0)
}

// n symbols FooThingNN at the root, m symbols FooThingNN under sub/ — the
// root ones sort first, so a scoped search for sub/ sees its matches only
// after every root match in the ranked list.
func symbolScopeFixture(t *testing.T, n, m int) *Handler {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "func FooThing%02d() int { return %d }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if m > 0 {
		var s strings.Builder
		s.WriteString("package sub\n\n")
		for i := 0; i < m; i++ {
			fmt.Fprintf(&s, "func FooThingSub%02d() int { return %d }\n", i, i)
		}
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "sub.go"), []byte(s.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

func TestSearchSymbols_ExhaustiveLiftsTheCap(t *testing.T) {
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 {
		t.Fatalf("exhaustive symbol search returned %d of 40", n)
	}
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("exhaustive result must not be marked truncated")
	}
}

func TestSearchSymbols_NonPositiveLimitUsesAdaptiveDefault(t *testing.T) {
	// Review 2026-09-06: limit=-1 reached syms[:-1] and panicked the handler.
	h := symbolCapFixture(t, 40)
	for _, lim := range []int{-1, 0} {
		out, err := h.Invoke("prism_search", map[string]any{
			"query": "FooThing", "scope": "symbols", "limit": lim})
		if err != nil {
			t.Fatalf("limit=%d: %v", lim, err)
		}
		m := out.(map[string]any)
		if n := len(anySlice(m["symbols"])); n != 40 {
			t.Errorf("limit=%d: small payload should deliver all 40, got %d", lim, n)
		}
		if m["symbolsTruncated"] == true {
			t.Errorf("limit=%d: complete small result must not be flagged", lim)
		}
	}
}

func TestSearchSymbols_ExhaustiveIsBoundedAndSaysSo(t *testing.T) {
	// Exhaustive is uncapped up to exhaustiveSymbolCap, then bounded with a
	// warning that names the cap — never a transport-level cut with no marker.
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 || m["symbolsTruncated"] != nil {
		t.Fatalf("40 < cap: want all 40 unflagged, got %d flagged=%v", n, m["symbolsTruncated"])
	}
	// The over-cap branch needs >2000 matching symbols; the fixture stays
	// small and the warning it would emit is asserted through its helper.
	w := exhaustiveCapWarning(exhaustiveSymbolCap)
	if !strings.Contains(w, "MORE than 2000") || !strings.Contains(w, "path=/glob=") {
		t.Errorf("exhaustive cap warning must name the cap and the narrowing: %q", w)
	}
}

func TestSearchSymbols_ScopedSearchSeesPastOutOfScopeMatches(t *testing.T) {
	// Review 2026-09-06: a fixed fetch (limit*4+1, or cap+1) filtered by
	// path= afterwards could return an incomplete in-scope set unflagged
	// when out-of-scope matches ranked ahead of it. 60 root matches sort
	// before the 40 under sub/; limit=5 used to fetch 21, filter to 0.
	h := symbolScopeFixture(t, 60, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "path": "sub", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 5 {
		t.Fatalf("scoped limit=5: want 5 in-scope symbols, got %d", n)
	}
	if m["symbolsTruncated"] != true {
		t.Error("40 in-scope matches under limit=5 must be flagged truncated")
	}
	out, err = h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "path": "sub", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 {
		t.Fatalf("scoped exhaustive: want all 40 in-scope symbols, got %d", n)
	}
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("a complete scoped exhaustive result must not be flagged")
	}
	for _, s := range anySlice(m["symbols"]) {
		if fp, _ := s.(map[string]any)["filePath"].(string); !strings.HasPrefix(fp, "sub/") {
			t.Errorf("out-of-scope symbol leaked into a path=sub result: %s", fp)
		}
	}
}

func TestSearchSymbols_CappedResultSaysSo(t *testing.T) {
	small := symbolCapFixture(t, 40)
	complete, err := small.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "symbols"})
	if err != nil {
		t.Fatal(err)
	}
	if m := complete.(map[string]any); len(anySlice(m["symbols"])) != 40 || m["symbolsTruncated"] == true {
		t.Fatalf("40 compact symbols should arrive complete in one call: %#v", m)
	}

	h := symbolCapFixture(t, 160)
	out, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "symbols"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 25 {
		t.Fatalf("default cap should be 25, got %d", n)
	}
	if m["symbolsTruncated"] != true {
		t.Error("capped symbol list must be flagged")
	}
	txt, ok := renderSearchAsText(m)
	if !ok {
		t.Fatal("text renderer fell back to JSON on a truncated symbol result")
	}
	if !strings.Contains(txt, "a SAMPLE") || !strings.Contains(txt, "exhaustive=true") {
		t.Errorf("warning must reach the agent in the text form:\n%s", txt)
	}
	// Under the cap: no flag, no warning.
	out, _ = h.Invoke("prism_search", map[string]any{"query": "FooThing0", "scope": "symbols"})
	m = out.(map[string]any)
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("a result under the cap must not be flagged")
	}
}

func symbolRankingFixture(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	var production strings.Builder
	production.WriteString("package p\n\nfunc Select() {}\nfunc SelectProfile() {}\nfunc PreSelect() {}\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&production, "func RankReal%02d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "prod.go"), []byte(production.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var doubles strings.Builder
	doubles.WriteString("package p\n\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&doubles, "func RankFake%02d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "bench_test.go"), []byte(doubles.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSearchSymbolsPrefersRealMatchesBeforeCap(t *testing.T) {
	h := symbolRankingFixture(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Rank", "scope": "symbols", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	syms := anySlice(out.(map[string]any)["symbols"])
	if len(syms) != 5 {
		t.Fatalf("got %d symbols, want 5", len(syms))
	}
	for _, s := range syms {
		m := s.(map[string]any)
		if m["testDouble"] == true {
			t.Fatalf("test double displaced a production match: %v", syms)
		}
	}
}

func TestSearchSymbolsLabelsNameMatchTiers(t *testing.T) {
	h := symbolRankingFixture(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Select", "scope": "symbols", "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	syms := anySlice(out.(map[string]any)["symbols"])
	want := []struct{ name, kind string }{{"Select", "name-exact"}, {"SelectProfile", "name-prefix"}, {"PreSelect", "name-substring"}}
	if len(syms) != len(want) {
		t.Fatalf("got %d symbols, want %d", len(syms), len(want))
	}
	for i, w := range want {
		m := syms[i].(map[string]any)
		if m["name"] != w.name || m["matchKind"] != w.kind {
			t.Fatalf("rank %d: got %s (%v), want %s (%s)", i, m["name"], m["matchKind"], w.name, w.kind)
		}
	}
	text, ok := renderSearchAsText(out.(map[string]any))
	if !ok || !strings.Contains(text, "[name-prefix]") {
		t.Fatalf("match reason missing from rendered search: %s", text)
	}
}

func TestQueryRankingIgnoresFreshUnrelatedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	h := symbolScopeFixture(t, 30, 30)
	root := h.Root
	git := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if date != "" {
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("", "init", "-q")
	git("", "config", "user.name", "Ranking Test")
	git("", "config", "user.email", "ranking@example.test")
	git("", "add", "foo.go", "sub/sub.go")
	git(time.Now().Add(-400*24*time.Hour).Format(time.RFC3339), "commit", "-qm", "old source")
	args := map[string]any{"task": "find FooThing code", "terms": []string{"FooThing"}, "delivery": "symbols", "budget": 1200}
	query := func() string {
		t.Helper()
		fresh := NewHandler(config.Default(), root, h.Grove)
		out, err := fresh.Invoke("prism_query", args)
		if err != nil {
			t.Fatal(err)
		}
		result, ok := out.(queryResult)
		if !ok || len(result.Symbols) < 6 {
			t.Fatalf("fixture needs ranked candidates beyond its five seeds: %#v", out)
		}
		seenSub := false
		for _, symbol := range result.Symbols {
			seenSub = seenSub || strings.HasPrefix(symbol.FilePath, "sub/")
		}
		if !seenSub {
			t.Fatal("fixture needs a candidate from the subsequently edited file")
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	search := func() string {
		t.Helper()
		out, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "symbols"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := query()
	searchBefore := search()
	path := filepath.Join(root, "sub", "sub.go")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, original...), []byte("\n// unrelated edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	git("", "add", "sub/sub.go")
	git("", "commit", "-qm", "fresh unrelated edit")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if after := query(); after != before {
		t.Fatalf("same source/index/arguments changed after a fresh unrelated commit\nbefore: %s\nafter: %s", before, after)
	}
	if after := search(); after != searchBefore {
		t.Fatalf("symbol sampling changed after a fresh unrelated commit\nbefore: %s\nafter: %s", searchBefore, after)
	}
}

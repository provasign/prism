package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// newTextSearchHandler returns a handler over a small real tree. The shared
// newTestHandler has a nil Grove, so only the scope="text" path (a pure rg
// pass over the root) is exercisable without standing up the engine — which
// is exactly the path batching changes.
func newTextSearchHandler(t *testing.T) *Handler {
	t.Helper()
	h := newTestHandler(t)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(h.Root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.go", "package p\n\nfunc Alpha() {}\n")
	write("beta.go", "package p\n\nfunc Beta() { Alpha() }\n")
	return h
}

func TestStringsArg_AcceptsStringAndArray(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{"single string", map[string]any{"query": "Alpha"}, []string{"Alpha"}},
		{"json array", map[string]any{"query": []any{"Alpha", "Beta"}}, []string{"Alpha", "Beta"}},
		{"go slice", map[string]any{"query": []string{"Alpha", "Beta"}}, []string{"Alpha", "Beta"}},
		{"trims and drops empties", map[string]any{"query": []any{" Alpha ", "", "   "}}, []string{"Alpha"}},
		{"drops duplicates", map[string]any{"query": []any{"A", "B", "A"}}, []string{"A", "B"}},
		{"ignores non-strings", map[string]any{"query": []any{"A", 7, nil}}, []string{"A"}},
		{"absent", map[string]any{}, nil},
		{"wrong type", map[string]any{"query": 7}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stringsArg(c.in, "query"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("stringsArg = %#v, want %#v", got, c.want)
			}
		})
	}
}

// A single term must keep the exact flat shape it had before batching landed:
// every existing agent, CLI formatter and test reads "symbols"/"textHits" at
// the top level, and silently nesting them would break all of them.
func TestToolSearch_SingleTermKeepsFlatShape(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", out)
	}
	if _, nested := m["results"]; nested {
		t.Error("single-term search returned the grouped shape; it must stay flat")
	}
	if _, ok := m["textHits"]; !ok {
		t.Error("single-term search lost its top-level textHits field")
	}
}

func TestToolSearch_MultiTermGroupsByTerm(t *testing.T) {
	h := newTextSearchHandler(t)
	terms := []any{"Alpha", "Beta"}
	out, err := h.Invoke("prism_search", map[string]any{"query": terms, "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m := out.(map[string]any)
	groups, ok := m["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results missing or wrong type: %T", m["results"])
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 result groups, got %d", len(groups))
	}
	// Order must follow the terms as given, and each group must name its term
	// — otherwise an agent cannot tell which hit answered which question.
	for i, want := range []string{"Alpha", "Beta"} {
		if got, _ := groups[i]["query"].(string); got != want {
			t.Errorf("group %d is for %q, want %q", i, got, want)
		}
		if _, ok := groups[i]["textHits"]; !ok {
			t.Errorf("group %q has no textHits field", want)
		}
	}
	if _, err := json.Marshal(out); err != nil {
		t.Errorf("result is not JSON-serialisable: %v", err)
	}
}

// Over the cap the extra terms are dropped — but never silently. A quiet
// truncation reads to the agent as "searched everything, found nothing".
func TestToolSearch_OverCapSaysSo(t *testing.T) {
	h := newTextSearchHandler(t)
	terms := make([]any, searchTermCap+3)
	for i := range terms {
		terms[i] = string(rune('a'+i)) + "Term"
	}
	out, err := h.Invoke("prism_search", map[string]any{"query": terms, "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m := out.(map[string]any)
	if got := len(m["results"].([]map[string]any)); got != searchTermCap {
		t.Errorf("searched %d terms, want the cap of %d", got, searchTermCap)
	}
	if note, _ := m["note"].(string); note == "" {
		t.Error("terms were dropped with no note — truncation must never be silent")
	}
}

func TestToolSearch_EmptyQueryStillErrors(t *testing.T) {
	h := newTestHandler(t)
	for _, bad := range []any{"", []any{}, []any{"  "}} {
		if _, err := h.Invoke("prism_search", map[string]any{"query": bad}); err == nil {
			t.Errorf("query=%#v was accepted; it must error", bad)
		}
	}
}

// --- path scoping ------------------------------------------------------
//
// The adoption lever. In the 2026-08-15 A/B cells, 12 of 20 grep invocations
// named a path, and the cells where the agent DECLINED prism opened with
// `grep -n alias octodns/manager.py` — a scoped search prism could not
// express. Whole-tree-only is why an agent that knows its file uses grep.

func TestToolSearch_PathScopeRestrictsResults(t *testing.T) {
	h := newTextSearchHandler(t)
	all, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(asHits(all)); n < 2 {
		t.Fatalf("fixture should match Alpha in both files, got %d", n)
	}
	// Scoped to the file that only MENTIONS Alpha, not the one defining it.
	one, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "path": "beta.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range hitFiles(one) {
		if f != "beta.go" {
			t.Errorf("path=beta.go returned a hit in %q — the scope was not applied", f)
		}
	}
	if len(hitFiles(one)) == 0 {
		t.Error("scoping to a single file returned nothing; rg/grep omit the filename for a " +
			"single file operand and the path:line:text parse drops every hit without --with-filename")
	}
}

func TestToolSearch_PathOutsideRootIsRefusedNotWidened(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "path": "/etc"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	// The dangerous failure is not an error, it is a WIDENING: answering a
	// question about /etc with hits from the whole project reads as success.
	if n := len(asHits(out)); n != 0 {
		t.Errorf("an out-of-root scope returned %d hits; it must return none", n)
	}
	if m["rejectedPaths"] == nil || m["warning"] == nil {
		t.Errorf("a dropped scope must be reported, got %v", m)
	}
}

func TestToolSearch_FilesOnlyDropsLines(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "files_only": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, ok := m["textHits"]; ok {
		t.Error("files_only must not return per-line hits")
	}
	files, _ := m["files"].([]string)
	if len(files) == 0 {
		t.Fatalf("files_only returned no files: %v", m)
	}
	for _, f := range files {
		if f == "" {
			t.Error("empty file path in files_only result")
		}
	}
}

func asHits(out any) []any {
	m, _ := out.(map[string]any)
	if m == nil {
		return nil
	}
	var n []any
	for _, g := range toSlice(m["textHits"]) {
		gm, _ := g.(map[string]any)
		n = append(n, toSlice(gm["hits"])...)
	}
	return n
}

func hitFiles(out any) []string {
	m, _ := out.(map[string]any)
	var fs []string
	for _, g := range toSlice(m["textHits"]) {
		if gm, ok := g.(map[string]any); ok {
			if f, _ := gm["file"].(string); f != "" {
				fs = append(fs, f)
			}
		}
	}
	return fs
}

func toSlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	}
	return nil
}

// Every advertised tool must be exempt from client-side schema deferral.
//
// Claude Code defers MCP schemas behind a ToolSearch hop. Frontier models
// make the hop; cheap tiers do not — measured on SWE-bench-Live (haiku, same
// task): 0 prism calls deferred, 5 loaded, and with the flag haiku opens with
// prism_query at 30 turns/$0.30 against 45/$0.53. v0.44.0 shipped this and
// v0.52.0 reverted it wholesale with the rest of the arc, not on its own
// evidence. Full deferral (2026-08-29, ab_deferral A/B) held on a bed whose
// guidance mandates an opening prism_query call. On realistic e2e tasks that
// mandate doesn't hold: 2026-08-30 mining of 48 e2e sessions found usage
// gated almost perfectly by whether the agent made the ToolSearch hop at
// all (25/25 hopped -> used prism, 23/23 didn't -> used none), not by tool
// choice once loaded. HYBRID residency (prism_query resident, the other
// five deferred) moved usage 8/12 -> 11/12 on a 12-task localized e2e A/B —
// but backfired on the wide-change bed (2026-09-01): sonnet never opens with
// prism_query even when it is resident (v0.55.10 all-resident cells: 23
// prism_search + 5 prism_read, prism_query ZERO), and the one visible tool
// masked the steering's deferred-tools clause, so 8/8 wide prism cells made
// zero prism calls and zero ToolSearch hops. Residency is now REMOVED
// entirely: no prism_* visible means the "they are DEFERRED, hop once"
// steering line is unambiguous, and the hop is the one mechanism measured
// to gate usage (25/25 hopped -> used, 23/23 didn't -> none). This guard
// checks that nothing is resident. If that changes, bring call-count
// evidence that agents open with the tool being made resident.
func TestToolSchemas_NoResidency(t *testing.T) {
	for _, tool := range ToolSchemas() {
		name, _ := tool["name"].(string)
		loaded := false
		if meta, ok := tool["_meta"].(map[string]any); ok {
			loaded, _ = meta["anthropic/alwaysLoad"].(bool)
		}
		if loaded {
			t.Errorf("%v: alwaysLoad=true — residency policy changed without fresh evidence?", name)
		}
	}
}

// Truncation must carry a denominator, and completeness must be askable.
//
// Before this, a capped search returned `truncated: true` and nothing else:
// an agent could not tell 22-of-25 from 22-of-990 (measured on this repo:
// "func " returned 22 hits against 1,311 real matches). A capped answer to
// "rewrite every call site" LOOKS complete, which is the expensive failure
// SWE-Explore measures -- missing evidence costs far more than noise.
func TestToolSearch_TruncationCarriesADenominator(t *testing.T) {
	h := newTextSearchHandler(t)
	// Fixture files both contain "package", so limit=1 forces truncation.
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "package", "scope": "text", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] != true {
		t.Fatalf("limit=1 over two matching files should truncate: %v", m)
	}
	total, _ := m["totalHits"].(int)
	if total < 2 {
		t.Errorf("totalHits = %v, want >= 2 — truncation without a denominator "+
			"reads to the agent as a complete answer", m["totalHits"])
	}
	if w, _ := m["warning"].(string); w == "" {
		t.Error("a truncated result must warn that it is a sample")
	}
}

func TestToolSearch_EmptyResultCarriesCompletionEvidence(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "ThisStringDoesNotExistAnywhere12345", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	text, ok := renderSearchAsText(m)
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	// A bare "no matches" is indistinguishable from a broken/incomplete
	// search; the rendered null must say the search actually finished, so
	// an agent doesn't have a rational reason to re-verify with grep.
	if !strings.Contains(text, "no matches") || !strings.Contains(text, "completed") {
		t.Errorf("empty result must state the search completed, got: %q", text)
	}
}

func TestToolSearch_ExhaustiveIsNotCapped(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "package", "scope": "text", "limit": 1, "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	// exhaustive is the agent DECLARING it needs completeness; an explicit
	// limit must not silently re-impose the cap.
	if m["truncated"] == true {
		t.Errorf("exhaustive=true still truncated: %v", m)
	}
}

// --- ranged reads ---------------------------------------------------------
//
// prism_read was whole-file only, and 50.6% of the 374 file reads measured
// across real agent runs are line-ranged (sed -n A,Bp 25.7%, native
// Read(offset,limit) 23.8%). That gap is why 87% of the reads prism never
// saw were ranged, and why its session ledger fired 0 times in 45 calls.

func TestToolRead_Range(t *testing.T) {
	h := newTextSearchHandler(t)
	body := ""
	for i := 1; i <= 20; i++ {
		body += "line" + string(rune('0'+i%10)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(h.Root, "r.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_read", map[string]any{"file": "r.txt", "offset": 5, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["startLine"] != 5 || m["endLine"] != 7 {
		t.Errorf("window = %v-%v, want 5-7", m["startLine"], m["endLine"])
	}
	if m["totalLines"] != 20 {
		t.Errorf("totalLines = %v, want 20 — a window without a denominator reads as the whole file",
			m["totalLines"])
	}
	c, _ := m["content"].(string)
	if strings.Count(c, "\n") != 3 {
		t.Errorf("want 3 lines, got %q", c)
	}
	if !strings.Contains(c, "5\t") {
		t.Errorf("lines must be numbered so the agent can cite them: %q", c)
	}
	if m["note"] == nil {
		t.Error("a partial read must say it is partial")
	}
}

func TestToolRead_RangePastEOFClampsNotErrors(t *testing.T) {
	h := newTextSearchHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "s.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A tool that errors is a tool agents route around; clamp and explain.
	out, err := h.Invoke("prism_read", map[string]any{"file": "s.txt", "offset": 999})
	if err != nil {
		t.Fatalf("out-of-range offset must not error: %v", err)
	}
	m := out.(map[string]any)
	if m["totalLines"] != 2 || m["warning"] == nil {
		t.Errorf("want totalLines and a warning, got %v", m)
	}
}

func TestToolRead_RangeNeedsNoIndex(t *testing.T) {
	// A line window is a file operation, not a graph one. It must work before
	// the index is built or when the engine is unavailable -- otherwise the
	// agent falls back to `sed -n A,Bp` exactly when prism is least able to
	// see what it read. (The whole-file path DOES need Grove for symbols;
	// TestToolRead_OK covers that.)
	h := newTextSearchHandler(t) // nil Grove
	out, err := h.Invoke("prism_read", map[string]any{"file": "alpha.go", "offset": 1, "limit": 2})
	if err != nil {
		t.Fatalf("ranged read must not require the index: %v", err)
	}
	if out.(map[string]any)["delivery"] != "range" {
		t.Errorf("want a range delivery, got %v", out)
	}
}

// --- context= (grep -A/-B/-C) --------------------------------------------
//
// Measured 2026-08-16: prism_search followed by a ranged prism_read (find
// the match, then pull the surrounding lines) occurred 13 times in real
// traces. context= collapses that into one call: measured 1,152B for one
// call at context=10 against 1,596B for the two-call sequence it replaces.

func TestToolSearch_ContextAddsSurroundingLines(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	groups := toSlice(m["textHits"])
	if len(groups) == 0 {
		t.Fatal("expected at least one file group")
	}
	gm := groups[0].(map[string]any)
	hits := toSlice(gm["hits"])
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	hm := hits[0].(map[string]any)
	// alpha.go is "package p\n\nfunc Alpha() {}\n" -- Alpha is on line 3,
	// so context=1 must include line 2 (blank) before it.
	if hm["before"] == nil {
		t.Errorf("expected before-context, got %v", hm)
	}
}

func TestToolSearch_NoContextMeansNoBeforeAfter(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	groups := toSlice(m["textHits"])
	gm := groups[0].(map[string]any)
	hm := toSlice(gm["hits"])[0].(map[string]any)
	if hm["before"] != nil || hm["after"] != nil {
		t.Errorf("context=0 (default) must not attach before/after: %v", hm)
	}
}

func TestToolSearch_ContextRenderedInPlainText(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := renderSearchAsText(out.(map[string]any))
	if !ok {
		t.Fatal("expected a plain-text rendering")
	}
	if !strings.Contains(text, "alpha.go:") {
		t.Errorf("context lines missing from rendering: %s", text)
	}
}

func TestToolSearch_ContextClampedWithNote(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "context": searchContextCap + 50})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	note, _ := m["note"].(string)
	if note == "" {
		t.Error("context was silently clamped — truncation must never be silent")
	}
	groups := toSlice(m["textHits"])
	gm := groups[0].(map[string]any)
	hm := toSlice(gm["hits"])[0].(map[string]any)
	before, _ := hm["before"].([]any)
	if len(before) > searchContextCap {
		t.Errorf("before-context has %d lines, want <= %d (the cap)", len(before), searchContextCap)
	}
}

// TestToolSearch_WarningPointsAtRollupWhenPresent drives a real truncated
// search through a Grove-indexed repo end to end (not a synthetic map, like
// TestRenderSearchAsText_HitRollup) and pins that the warning text changes
// shape once a rollup is actually available.
//
// 2026-09-02, real usage: a truncated OWNER-pattern search (543 reported vs
// 829 true matches, a security/access-control audit) rode with a hitRollup
// that already had the complete grouped answer -- but the warning text only
// said "raise limit=, narrow with path=/glob=, or use files_only=true",
// never mentioning hitRollup or exhaustive=true, so nothing pointed the
// agent at the free complete answer already sitting in the same response.
func TestToolSearch_WarningPointsAtRollupWhenPresent(t *testing.T) {
	dir := t.TempDir()
	// Two files, several calls each, so scope=text over limit=1 truncates
	// AND the calls land inside real indexed function bodies (hitRollup
	// groups by innermost enclosing symbol; a bare "package" line does not
	// count, functions do).
	mustWrite(t, dir, "alpha.go", `package p

func Alpha() {
	shared()
	shared()
}
`)
	mustWrite(t, dir, "beta.go", `package p

func Beta() {
	shared()
}

func shared() {}
`)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, err := h.Invoke("prism_search", map[string]any{
		"query": "shared", "scope": "text", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] != true {
		t.Fatalf("limit=1 over 3 matches should truncate: %v", m)
	}
	ru, _ := m["hitRollup"].([]map[string]any)
	warning, _ := m["warning"].(string)
	if warning == "" {
		t.Fatal("truncated result must carry a warning")
	}
	if len(ru) == 0 {
		t.Skip("no rollup produced for this fixture (e.g. grove indexing changed) — nothing to assert")
	}
	for _, want := range []string{"hitRollup", "COMPLETE"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning must point at the rollup that already answers completeness, missing %q in: %s", want, warning)
		}
	}
	if !strings.Contains(warning, "exhaustive=true") {
		t.Error("warning should still mention exhaustive=true as the fallback for raw lines")
	}
	// The old, rollup-blind wording must not survive alongside the new one --
	// it told the agent to re-query (limit=/path=/files_only=) instead of
	// reading the answer already in the response.
	if strings.Contains(warning, "Raise limit=") {
		t.Error("warning still leads with re-query advice even though a complete rollup is present")
	}
}

// TestToolSearch_RollupOnlyDropsSampleWhenRollupExists: real feedback
// (2026-09-02) on the fix above -- once hitRollup exists in the same
// response, ordering it earlier doesn't save tokens (deliveredTokens bills
// the whole response regardless of read order); the only real lever is not
// delivering the raw sample at all when the caller already knows the
// rollup alone answers "how many, and where do they cluster". rollup_only
// is the files_only-shaped sibling for that case.
func TestToolSearch_RollupOnlyDropsSampleWhenRollupExists(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "alpha.go", `package p

func Alpha() {
	shared()
	shared()
}
`)
	mustWrite(t, dir, "beta.go", `package p

func Beta() {
	shared()
}

func shared() {}
`)
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, err := h.Invoke("prism_search", map[string]any{
		"query": "shared", "scope": "text", "limit": 1, "rollup_only": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] != true {
		t.Fatalf("limit=1 over 3 matches should truncate: %v", m)
	}
	ru, _ := m["hitRollup"].([]map[string]any)
	if len(ru) == 0 {
		t.Skip("no rollup produced for this fixture — nothing to assert")
	}
	if _, present := m["textHits"]; present {
		t.Errorf("rollup_only with a rollup present must drop textHits from the response, got: %v", m)
	}
}

// TestToolSearch_RollupOnlyKeepsSampleWithoutRollup: rollup_only must never
// silently drop the only content in the response -- when there is nothing
// to roll up (no truncation, or truncation with no indexed symbols
// enclosing the hits), textHits stays.
func TestToolSearch_RollupOnlyKeepsSampleWithoutRollup(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "rollup_only": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] == true {
		t.Fatalf("this fixture's hit count should not truncate: %v", m)
	}
	if _, present := m["textHits"]; !present {
		t.Error("rollup_only must not drop textHits when there is no rollup to answer with instead")
	}
}

// TestToolSearch_AllEmptyCarriesRetryGuidance: transcript analysis
// (2026-09-02, grove wide-bed cell) caught the moment an agent abandoned
// prism for an entire task -- one multi-term search where every guessed
// term (punctuation-laden call patterns) returned empty. The result was
// honest but terminal: nothing distinguished "these exact strings don't
// exist, broaden and retry" from "this tool can't help here", and the
// agent chose the second reading, reverting to manual grep for 7 more
// files at ~2x the turns. An all-empty search must say what to do next
// and, when the index has near-miss candidates for the terms' identifier
// tokens, name them.
func TestToolSearch_AllEmptyCarriesRetryGuidance(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"e.AlphaZZZ(", "cg.BetaZZZ("}, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	note, _ := m["note"].(string)
	if !strings.Contains(note, "NOT that the tool is done") {
		t.Errorf("all-empty multi-term search must carry retry guidance, got note=%q", note)
	}
	// Single-term shape too.
	out, err = h.Invoke("prism_search", map[string]any{
		"query": "ZZZdoesNotExistZZZ", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	note, _ = m["note"].(string)
	if !strings.Contains(note, "NOT that the tool is done") {
		t.Errorf("all-empty single-term search must carry retry guidance, got note=%q", note)
	}
}

// TestToolSearch_PartialMatchesCarryNoRetryGuidance: the guidance is for
// the all-empty dead end only -- a search where any term matched must not
// be second-guessed.
func TestToolSearch_PartialMatchesCarryNoRetryGuidance(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"Alpha", "ZZZnotfoundZZZ"}, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if note, _ := m["note"].(string); strings.Contains(note, "NOT that the tool is done") {
		t.Errorf("a search with real matches must not carry the all-empty guidance, got note=%q", note)
	}
}

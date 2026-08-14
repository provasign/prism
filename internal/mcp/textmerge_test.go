package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/textsearch"
)

// TestMergeTextSearchNoGroveDeliversRawHits: with no Grove client every hit
// is undeliverable as a symbol promotion and must surface raw — text search
// keeps working even when the graph is down.
func TestMergeTextSearchNoGroveDeliversRawHits(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "notes.md"),
		[]byte("the frobnicate flag controls retries\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := h.mergeTextSearch(context.Background(), []string{"frobnicate"}, map[string]bool{})
	if len(res.rawHits) != 1 {
		t.Fatalf("rawHits = %v, want exactly the notes.md line", res.rawHits)
	}
	if res.rawHits[0].File != "notes.md" || res.rawHits[0].Line != 1 {
		t.Errorf("hit = %+v", res.rawHits[0])
	}
	if len(res.extraSeeds) != 0 {
		t.Errorf("no Grove: extraSeeds should be empty, got %v", res.extraSeeds)
	}
}

// TestRenderTextMatchesUsesSessionSHA: hits in a file already delivered this
// session (same content hash) are rendered as line numbers only — never a
// re-send of text the agent already has.
func TestRenderTextMatchesUsesSessionSHA(t *testing.T) {
	h := newTestHandler(t)
	content := "alpha needle\nbeta\ngamma needle\n"
	if err := os.WriteFile(filepath.Join(h.Root, "seen.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.Root, "fresh.txt"), []byte("delta needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a prior full delivery of seen.txt.
	h.Session.Record("seen.txt", compression.Hash(content), 10, "full-fresh")

	out := h.renderTextMatches([]textsearch.Hit{
		{File: "seen.txt", Line: 1, Text: "alpha needle"},
		{File: "seen.txt", Line: 3, Text: "gamma needle"},
		{File: "fresh.txt", Line: 1, Text: "delta needle"},
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (one cached summary, one verbatim hit): %v", len(lines), lines)
	}
	seenLine, freshLine := lines[0], lines[1]
	if !strings.HasPrefix(seenLine, "seen.txt:") || !strings.Contains(seenLine, "cached") ||
		!strings.Contains(seenLine, "1,3") {
		t.Errorf("seen.txt entry should be a cached summary naming both lines: %q", seenLine)
	}
	if strings.Contains(seenLine, "alpha needle") || strings.Contains(seenLine, "gamma needle") {
		t.Errorf("cached entry must not re-send text: %q", seenLine)
	}
	if freshLine != "fresh.txt:1:delta needle" {
		t.Errorf("fresh.txt should carry verbatim file:line:text, got %q", freshLine)
	}
}

// TestRenderTextMatchesCapsFilesAndHits: the delivered section is bounded so
// a broad term cannot crowd out the source windows.
func TestRenderTextMatchesCapsFilesAndHits(t *testing.T) {
	h := newTestHandler(t)
	var hits []textsearch.Hit
	for f := 0; f < textRenderFileCap+3; f++ {
		name := "f" + string(rune('a'+f)) + ".txt"
		for l := 1; l <= textRenderHitsPerFile+2; l++ {
			hits = append(hits, textsearch.Hit{File: name, Line: l, Text: "x"})
		}
	}
	out := h.renderTextMatches(hits)
	if !strings.Contains(out, "3 more files with matches omitted") {
		t.Errorf("expected an omission note for the 3 files beyond the cap: %q", out)
	}
	if !strings.Contains(out, "2 more matches omitted") {
		t.Errorf("expected per-file overflow to be counted, got: %q", out)
	}
}

// TestSearchScopeTextIsPureGrep: scope="text" must return only text hits —
// no symbol search (works with nil Grove), minimal envelope.
func TestSearchScopeTextIsPureGrep(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "cfg.yaml"),
		[]byte("retry_budget: 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_search", map[string]any{"query": "retry_budget", "scope": "text"})
	if err != nil {
		t.Fatalf("scope=text should not need Grove: %v", err)
	}
	m := out.(map[string]any)
	if _, hasSyms := m["symbols"]; hasSyms {
		t.Errorf("pure grep must not carry a symbols section: %v", m)
	}
	hits, ok := m["textHits"].(string)
	if !ok || !strings.HasPrefix(hits, "cfg.yaml:1:") {
		t.Errorf("expected exactly the cfg.yaml hit, got %v", m)
	}
}

// TestResolvedRefNoteSilentWithoutGrove: the note is advisory and must never
// error or fire when the graph cannot back it up.
func TestResolvedRefNoteSilentWithoutGrove(t *testing.T) {
	h := newTestHandler(t) // no Grove client
	hits := make([]textsearch.Hit, 20)
	if n := h.resolvedRefNote(context.Background(), "anything", hits); n != "" {
		t.Errorf("note must be silent without a graph, got %q", n)
	}
}

// TestResolvedRefNoteSilentOnSmallResult: below the hit floor there is
// nothing worth annotating.
func TestResolvedRefNoteSilentOnSmallResult(t *testing.T) {
	h := newTestHandler(t)
	if n := h.resolvedRefNote(context.Background(), "x", make([]textsearch.Hit, 3)); n != "" {
		t.Errorf("note must be silent on tiny results, got %q", n)
	}
}

// TestSourceDeliveryBoundedByGiantLine: generated dashboards embed a whole
// stylesheet as one string literal. A single 85,462-char line made a
// prism_query response 89KB; the host rejected the entire tool result and
// told the agent to "use grep on the file directly" — prism training agents
// away from itself. Both guards are load-bearing: clamp the line, and never
// let one file section blow the response budget.
func TestSourceDeliveryBoundedByGiantLine(t *testing.T) {
	long := strings.Repeat("div[data-testid=\"x\"] p { font-size: 1rem; } ", 2000)
	if got := clampSourceLine(long); len(got) > ranking.MaxRenderedLineChars+200 {
		t.Errorf("clampSourceLine left %d chars", len(got))
	} else if !strings.Contains(got, "line truncated by prism") {
		t.Error("truncation must be stated in-band, not silent")
	}
	if short := "a normal line"; clampSourceLine(short) != short {
		t.Error("normal lines must pass through byte-for-byte")
	}
	big := strings.Repeat("x\n", 60_000) // ~120KB section
	out := truncateSection(big, 500, "dashboard.py")
	if n := len(out); n > 500*4+300 {
		t.Errorf("truncateSection returned %d bytes for a 500-token ceiling", n)
	}
	if !strings.Contains(out, "dashboard.py") || !strings.Contains(out, "truncated") {
		t.Error("truncation must name the file and say it happened")
	}
}

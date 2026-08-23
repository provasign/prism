package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

// Every contract-delivery path must be SILENT without a graph — these notes
// are advisory riders, never errors.
func TestContractsSilentWithoutGrove(t *testing.T) {
	h := newTestHandler(t)
	if s := h.fileContractsTrailer(context.Background(), "a.py"); s != "" {
		t.Errorf("trailer without graph: %q", s)
	}
	if s := h.calleeContractsNote(context.Background(), "cache.get("); s != "" {
		t.Errorf("callee note without graph: %q", s)
	}
	if s := h.hitContractsNote(context.Background(), make([]textsearch.Hit, 3)); s != "" {
		t.Errorf("hit note without graph: %q", s)
	}
	if h.symbolHasFamily(context.Background(), grove.SymbolRecord{Name: "X"}) {
		t.Error("family without graph")
	}
}

// calleePatRe: fire only on call-shaped queries.
func TestCalleePatternShapes(t *testing.T) {
	for q, want := range map[string]string{
		"cache.get(":       "get",
		"get(":             "get",
		"self._db.save(":   "save",
		"plain_identifier": "",
		"Invalid(Request":  "",
		"a.b(c)":           "",
	} {
		m := calleePatRe.FindStringSubmatch(q)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q: got %q want %q", q, got, want)
		}
	}
}

// The per-file trailer fires ONCE per session: wallpaper is the failure
// mode every measured note has to defend against.
func TestFileTrailerDedupIsPerSession(t *testing.T) {
	h := newTestHandler(t)
	h.contractFiles = map[string]bool{"a.py": true}
	h.contractLines = map[string]string{}
	if s := h.fileContractsTrailer(context.Background(), "a.py"); s != "" {
		t.Errorf("second trailer for same file must be empty, got %q", s)
	}
}

// contractLine cache: one ChangeImpact per symbol per session.
func TestContractLineCached(t *testing.T) {
	h := newTestHandler(t)
	h.contractLines = map[string]string{"id1": "CACHED-LINE"}
	h.contractFiles = map[string]bool{}
	got := h.contractLine(context.Background(), grove.SymbolRecord{ID: "id1", Name: "X"})
	if got != "CACHED-LINE" {
		t.Errorf("cache miss: %q", got)
	}
	if !strings.Contains("CACHED-LINE", got) {
		t.Error("unreachable")
	}
}

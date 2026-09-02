package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestToolQuery_ExactTermMatchOutranksConfirmedSubstring is the regression
// for a real bug (2026-09-02, django BaseDatabaseOperations.quote_name):
// prism_query(terms=["quote_name"]) returned geo_quote_name (a DIFFERENT,
// unrelated method whose name merely CONTAINS "quote_name" as a substring)
// as every one of its top anchors -- the real quote_name family, seeded
// correctly at graph rank 100 (exact name match, ahead of geo_quote_name's
// rank 70 substring match), never appeared.
//
// Root cause: mergeTextSearch's confirmation grep is capped (40 hits) and,
// for this repo, happened to surface geo_quote_name's own definition lines
// within that cap while quote_name's did not. The single-term "confirmed"
// promotion path then did a GLOBAL reorder keyed only on confirmation,
// discarding the graph's own rank entirely -- so an unlucky cap boundary
// could put a substring match ahead of an exact one.
//
// Fix: an exact name/qualified-name match on the term is pinned ahead of
// the rest before confirmation is allowed to reorder anything. This test
// does not need to reproduce the cap-timing race to catch a regression --
// it only needs an exact match and a substring-superset match to both
// exist, and asserts the exact one leads regardless of which one the
// (real, unmocked) text-search confirmation pass happens to favor.
func TestToolQuery_ExactTermMatchOutranksConfirmedSubstring(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// quote_name: the exact target. Named "zzz_real.go" so --sort path (the
	// text-search backend forces deterministic path-sorted output, see
	// textsearch.go) scans it LAST -- reproducing the real bug's ordering,
	// not just its symptom.
	write("zzz_real.go", `package p

func quote_name(name string) string {
	return "[" + name + "]"
}
`)
	// geo_quote_name: an unrelated symbol whose name merely CONTAINS
	// "quote_name" as a substring. mergeTextSearch caps its confirmation
	// grep at 40 hits total (textHitsPerTerm) with a 20-per-file cap
	// (textsearch's MaxPerFile default), and the backend emits hits in
	// --sort path order -- deterministic, not the real bug's race, but the
	// same shape. "aaa_geo_0.go" packs 20 self-referential lines INSIDE
	// geo_quote_name's own body (each hit's enclosing symbol is
	// geo_quote_name itself, already seeded by the term match, so this
	// confirms it) and "aaa_geo_1.go" packs 20 more comment-only hits with
	// no enclosing symbol (raw hits that spend the rest of the cap without
	// confirming anything). 20+20 = 40 -- the cap is exhausted by two files
	// that both sort before "zzz_real.go", so quote_name's own definition
	// line is NEVER reached: it gets zero chance at confirmation, while
	// geo_quote_name gets confirmed from its own body alone. Without the
	// exact-match pin this is exactly what let geo_quote_name (rank 70) get
	// globally promoted ahead of quote_name (rank 100, but unconfirmed) in
	// the real bug.
	var geoBody strings.Builder
	geoBody.WriteString("package p\n\nfunc geo_quote_name(name string) string {\n")
	for l := 0; l < 20; l++ {
		fmt.Fprintf(&geoBody, "\t_ = \"geo_quote_name self-reference %d\"\n", l)
	}
	geoBody.WriteString("\treturn \"'\" + name + \"'\"\n}\n")
	write("aaa_geo_0.go", geoBody.String())

	var fillerBody strings.Builder
	fillerBody.WriteString("package p\n\n")
	for l := 0; l < 20; l++ {
		fmt.Fprintf(&fillerBody, "// geo_quote_name filler reference %d\n", l)
	}
	write("aaa_geo_1.go", fillerBody.String())

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, err := h.Invoke("prism_query", map[string]any{
		"task":     "how does quote_name work and who calls it",
		"terms":    []string{"quote_name"},
		"delivery": "symbols",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	qr, ok := out.(queryResult)
	if !ok {
		t.Fatalf("delivery=symbols should return the symbols struct, got %T", out)
	}
	if len(qr.Symbols) == 0 {
		t.Fatal("expected at least one symbol")
	}
	if qr.Symbols[0].Name != "quote_name" {
		t.Errorf("top anchor = %q, want the exact match %q to lead — an exact name "+
			"match must never rank below a substring match just because confirmation "+
			"found it first. Full order: %v",
			qr.Symbols[0].Name, "quote_name", symbolNames(qr.Symbols))
	}
}

func symbolNames(syms []rankedSymbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

// TestToolQuery_ManyExactTiesFallBackToConfirmation is the OTHER half of
// the regression above: pinning exact matches unconditionally ahead of
// confirmed-inexact ones was itself a real regression on the swebench
// oracle bed (2026-09-02, a2aproject/a2a-python-414, term "JSONRPC"). That
// repo declares a boilerplate `jsonrpc` field on 23 unrelated Pydantic
// models -- 23 "exact" ties that are not a real override family, just an
// incidental shared field name. The pin buried two symbols confirmation
// had correctly promoted (JsonRpcTransport, A2AClientJSONRPCError, each
// independently referenced elsewhere) under 20+ boilerplate declarations,
// dropping oracle recall 0.222 -> 0.111 on that task.
//
// Exactness is a strong signal with FEW ties (a real declaration/override
// family, see the sibling test's 5-tie django case) and a weak one with
// MANY (a common field/attribute name collision). This fixture reproduces
// the many-ties shape: 12 unrelated types each declaring an exact-name
// field (over exactTieLimit=10), plus one differently-named, independently
// referenced (confirmed) symbol that must still be able to lead.
func TestToolQuery_ManyExactTiesFallBackToConfirmation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Filler that consumes most of mergeTextSearch's 40-hit confirmation
	// cap before any exact-tie file is reached -- replicating a2a-python's
	// real dynamic, not just its tie count: none of that repo's 23
	// boilerplate `jsonrpc` field declarations got a chance at self-
	// confirmation either, because 40 hits were already spent elsewhere
	// before the text-search backend's --sort path order (see
	// textsearch.go) reached most of them. Two files x 18 comment-only
	// hits = 36, sorting first alphabetically.
	for f := 0; f < 2; f++ {
		var b strings.Builder
		b.WriteString("package p\n\n")
		for l := 0; l < 18; l++ {
			fmt.Fprintf(&b, "// handler filler reference %d-%d\n", f, l)
		}
		write(fmt.Sprintf("aaa_filler_%d.go", f), b.String())
	}
	// The genuinely relevant symbol: a different name (not an exact tie to
	// "handler"), self-referencing "handler" in its own body -- one hit,
	// landing inside the ~4 hits of cap the fillers left, so it DOES get
	// confirmed. Sorts before the exact ties below.
	write("bbb_request.go", `package p

func RequestHandler() string {
	return "handler debug reference"
}
`)
	// Exhausts what little cap room bbb_request.go left (36+1=37 of 40),
	// so no zzz_model file below gets even one hit -- otherwise the first
	// one or two alphabetically would self-confirm the same trivial way
	// bbb_request.go does, undermining the fixture's point.
	write("ccc_buffer.go", "package p\n\n// handler buffer 1\n// handler buffer 2\n// handler buffer 3\n")
	// 12 unrelated exact-name ties -- more than exactTieLimit (10), AND
	// sorted (zzz_ prefix) past the point the confirmation cap is spent,
	// so — like a2a-python's real fields — most never get a chance at
	// self-confirmation either.
	for i := 0; i < 12; i++ {
		write(fmt.Sprintf("zzz_model_%d.go", i), fmt.Sprintf(`package p

func handler() string {
	return "model %d"
}
`, i))
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

	out, err := h.Invoke("prism_query", map[string]any{
		"task":     "fix the handler bug",
		"terms":    []string{"handler"},
		"delivery": "symbols",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	qr, ok := out.(queryResult)
	if !ok {
		t.Fatalf("delivery=symbols should return the symbols struct, got %T", out)
	}
	if len(qr.Symbols) == 0 {
		t.Fatal("expected at least one symbol")
	}
	if qr.Symbols[0].Name != "RequestHandler" {
		t.Errorf("top anchor = %q, want %q to lead — with MANY exact ties (a name "+
			"collision, not a real family) confirmation must still be able to promote "+
			"a genuinely referenced symbol. Full order: %v",
			qr.Symbols[0].Name, "RequestHandler", symbolNames(qr.Symbols))
	}
}

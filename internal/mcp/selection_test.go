package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// promoteSingleTermSeeds is tested directly, as pure input/output, rather
// than through prism_query end to end against real files. A first version
// of these tests drove real repos through Grove and relied on the
// text-search backend's file-scan ORDER to force specific confirmation
// outcomes (e.g. "these hits land inside the cap, this one doesn't") --
// that held locally (ripgrep, --sort path is deterministic) and broke in CI
// (2026-09-02): neither GitHub-hosted ubuntu-latest nor macos-latest ships
// ripgrep, so both fell back to `grep -r`, whose traversal order carries no
// such guarantee, and the fixture's carefully-balanced hit-count arithmetic
// no longer landed the same way. Testing the function directly with
// hand-built confirmed maps removes the text-search backend from the
// picture entirely -- deterministic on any machine, any OS, any grep/rg
// availability.

func seedSym(id, name, qualified string) grove.SymbolRecord {
	return grove.SymbolRecord{ID: id, Name: name, QualifiedName: qualified}
}

func seedNames(syms []grove.SymbolRecord) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

// TestPromoteSingleTermSeeds_ExactBeatsConfirmedSubstring is the regression
// for a real bug (2026-09-02, django BaseDatabaseOperations.quote_name):
// prism_query(terms=["quote_name"]) returned geo_quote_name (a DIFFERENT,
// unrelated method whose name merely CONTAINS "quote_name" as a substring)
// as every one of its top anchors -- the real quote_name family, seeded
// correctly at graph rank 100 (exact name match, ahead of geo_quote_name's
// rank 70 substring match), never appeared.
//
// Root cause: mergeTextSearch's confirmation grep is capped (40 hits) and,
// for that repo, happened to surface geo_quote_name's own definition lines
// within that cap while quote_name's did not. The single-term "confirmed"
// promotion path then did a GLOBAL reorder keyed only on confirmation,
// discarding the graph's own rank entirely -- so an unlucky cap boundary
// could put a substring match ahead of an exact one.
func TestPromoteSingleTermSeeds_ExactBeatsConfirmedSubstring(t *testing.T) {
	seeds := []grove.SymbolRecord{
		seedSym("geo1", "geo_quote_name", "GisOps.geo_quote_name"),
		seedSym("geo2", "geo_quote_name", "OracleGis.geo_quote_name"),
		seedSym("real1", "quote_name", "BaseOps.quote_name"),
		seedSym("real2", "quote_name", "MysqlOps.quote_name"),
	}
	// geo_quote_name confirmed by an independent text hit (the cap-timing
	// race, real in production); quote_name never got the chance -- exactly
	// what the bug reproduced.
	confirmed := map[string]bool{"geo1": true, "geo2": true}

	got := promoteSingleTermSeeds(seeds, confirmed, "quote_name")
	if got[0].Name != "quote_name" {
		t.Errorf("top seed = %q, want the exact match to lead over a confirmed "+
			"substring match. Full order: %v", got[0].Name, seedNames(got))
	}
}

// TestPromoteSingleTermSeeds_ManyExactTiesFallBackToConfirmation is the
// OTHER half of the regression above: pinning exact matches unconditionally
// ahead of confirmed-inexact ones was itself a real regression, caught by
// rerunning prism's existing swebench query_oracle bed (2026-09-02,
// a2aproject/a2a-python-414, term "JSONRPC") -- not by a hand-picked
// repro. That repo declares a boilerplate `jsonrpc` field on 23 unrelated
// Pydantic models: 23 "exact" ties that are not a real override family,
// just an incidental shared field name. An unconditional pin buried two
// symbols confirmation had correctly promoted (JsonRpcTransport,
// A2AClientJSONRPCError, each independently referenced elsewhere) under
// 20+ boilerplate declarations, dropping oracle recall 0.222 -> 0.111.
//
// Exactness is a strong signal with FEW ties (a real declaration/override
// family, see the sibling test's 4-tie case) and a weak one with MANY (a
// common field/attribute name collision) -- this fixture reproduces the
// many-ties shape at exactTieLimit+1.
func TestPromoteSingleTermSeeds_ManyExactTiesFallBackToConfirmation(t *testing.T) {
	var seeds []grove.SymbolRecord
	confirmed := map[string]bool{}
	for i := 0; i < 11; i++ { // exactTieLimit (10) + 1
		id := "tie" + string(rune('a'+i))
		seeds = append(seeds, seedSym(id, "handler", "Model"+string(rune('A'+i))+".handler"))
	}
	seeds = append(seeds, seedSym("real", "RequestHandler", "RequestHandler"))
	confirmed["real"] = true // the one independently-referenced symbol

	got := promoteSingleTermSeeds(seeds, confirmed, "handler")
	if got[0].Name != "RequestHandler" {
		t.Errorf("top seed = %q, want %q to lead — with MANY exact ties (a name "+
			"collision, not a real family) confirmation must still be able to promote "+
			"a genuinely referenced symbol. Full order: %v",
			got[0].Name, "RequestHandler", seedNames(got))
	}
}

// TestPromoteSingleTermSeeds_NoConfirmationIsANoOp and
// TestPromoteSingleTermSeeds_FewExactTiesArePinnedEvenUnconfirmed round out
// the boundary: promotion must do nothing when there is nothing to promote,
// and the exact-match pin must hold at exactly exactTieLimit ties (the
// django case itself: 7 <= 10).
func TestPromoteSingleTermSeeds_NoConfirmationIsANoOp(t *testing.T) {
	seeds := []grove.SymbolRecord{seedSym("a", "foo", "A.foo"), seedSym("b", "bar", "B.bar")}
	got := promoteSingleTermSeeds(seeds, nil, "foo")
	if seedNames(got)[0] != "foo" || len(got) != 2 {
		t.Errorf("no confirmation should leave seed order untouched, got %v", seedNames(got))
	}
}

func TestPromoteSingleTermSeeds_FewExactTiesArePinnedEvenUnconfirmed(t *testing.T) {
	var seeds []grove.SymbolRecord
	for i := 0; i < 7; i++ { // django's real tie count
		id := "tie" + string(rune('a'+i))
		seeds = append(seeds, seedSym(id, "quote_name", "Backend"+string(rune('A'+i))+".quote_name"))
	}
	seeds = append(seeds, seedSym("substr", "geo_quote_name", "Gis.geo_quote_name"))
	confirmed := map[string]bool{"substr": true}

	got := promoteSingleTermSeeds(seeds, confirmed, "quote_name")
	if got[0].Name != "quote_name" {
		t.Errorf("top seed = %q, want the exact match to stay pinned at exactly "+
			"the django tie count (7). Full order: %v", got[0].Name, seedNames(got))
	}
}

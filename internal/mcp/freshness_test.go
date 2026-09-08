package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symbol added AFTER the last index must be visible to the index-backed
// read tools without the caller reindexing by hand.
//
// Proven by hand on the mason repo 2026-09-07: append a function, run
// `prism lookup` on it, and the answer was "no symbol named
// StalenessProbeXYZ in the index" — silently wrong. The whole-repo planning
// tools already delta-reindexed; the read tools that agents call constantly
// did not.
func TestReadToolsSeeEditsMadeAfterIndex(t *testing.T) {
	h := newDeliveryFixture(t) // indexes once
	dir := h.Root

	// APPEND to an already-indexed file — this is the shape that was proven
	// stale by hand. Creating a NEW file is not discriminating: some
	// resolution paths find it anyway, so a test built on that passes with
	// or without the fix and proves nothing.
	util := filepath.Join(dir, "util.go")
	src, err := os.ReadFile(util)
	if err != nil {
		t.Fatal(err)
	}
	src = append(src, []byte(`
// AddedAfterIndex was appended after the last index ran.
func AddedAfterIndex(n int) int { return n * 2 }
`)...)
	if err := os.WriteFile(util, src, 0o644); err != nil {
		t.Fatal(err)
	}

	out, lerr := h.Invoke("prism_lookup", map[string]any{"name": "AddedAfterIndex"})
	if lerr != nil {
		t.Fatalf("lookup: %v", lerr)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), "AddedAfterIndex") {
		t.Fatalf("lookup answered from a stale index: %s", b)
	}

	// prism_search must see it too (symbols come from the index).
	out, err = h.Invoke("prism_search", map[string]any{"query": "AddedAfterIndex"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	b, _ = json.Marshal(out)
	if !strings.Contains(string(b), "AddedAfterIndex") {
		t.Fatalf("search answered from a stale index: %s", b)
	}
}

// A DELETED symbol must stop resolving, too — staleness cuts both ways, and
// a phantom symbol is worse than a missing one because it reads as real.
func TestReadToolsDropSymbolsDeletedAfterIndex(t *testing.T) {
	h := newDeliveryFixture(t)
	dir := h.Root

	out, err := h.Invoke("prism_lookup", map[string]any{"name": "FormatGreeting"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(out); !strings.Contains(string(b), "FormatGreeting") {
		t.Fatalf("fixture symbol missing before deletion: %s", b)
	}

	// app.go calls it; remove both so the package stays coherent.
	os.Remove(filepath.Join(dir, "util.go"))
	os.Remove(filepath.Join(dir, "app.go"))
	os.Remove(filepath.Join(dir, "util_test.go"))

	out, err = h.Invoke("prism_lookup", map[string]any{"name": "FormatGreeting"})
	b, _ := json.Marshal(out)
	if err == nil && strings.Contains(string(b), "func FormatGreeting") {
		t.Fatalf("deleted symbol still resolves from a stale index: %s", b)
	}
}

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

func TestIntArg(t *testing.T) {
	if intArg(map[string]any{"x": 5}, "x", 0) != 5 {
		t.Error("int")
	}
	if intArg(map[string]any{"x": int64(7)}, "x", 0) != 7 {
		t.Error("int64")
	}
	if intArg(map[string]any{"x": 3.0}, "x", 0) != 3 {
		t.Error("float")
	}
	if intArg(map[string]any{"x": json.Number("9")}, "x", 0) != 9 {
		t.Error("json.Number")
	}
	if intArg(map[string]any{}, "x", 42) != 42 {
		t.Error("default")
	}
	if intArg(map[string]any{"x": "str"}, "x", 11) != 11 {
		t.Error("string ignored")
	}
}

func TestBaseName(t *testing.T) {
	if baseName("/a/b/c.go") != "c.go" {
		t.Error("with /")
	}
	if baseName("nofile") != "nofile" {
		t.Error("no /")
	}
}

func TestMinInt(t *testing.T) {
	if minInt(1, 2) != 1 || minInt(5, 3) != 3 {
		t.Error("minInt")
	}
}

func TestSortSymbolsByName(t *testing.T) {
	s := []grove.SymbolRecord{{Name: "C"}, {Name: "A"}, {Name: "B"}}
	sortSymbolsByName(s)
	if s[0].Name != "A" {
		t.Errorf("got %s", s[0].Name)
	}
}

func TestSummarize(t *testing.T) {
	if summarize("   abc   ", 100) != "abc" {
		t.Error("trim")
	}
	if summarize("abcdef", 3) != "abc…" {
		t.Error("trunc")
	}
}

func TestSafePathWithinRoot_Cov(t *testing.T) {
	root := t.TempDir()
	if _, _, err := safePathWithinRoot(root, "x.go"); err != nil {
		t.Error(err)
	}
	if _, _, err := safePathWithinRoot(root, "../escape"); err == nil {
		t.Error("expected escape error")
	}
}

func TestToolRead_OK(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	p := filepath.Join(h.Root, "f.go")
	_ = os.WriteFile(p, []byte("package x\nfunc F(){}\n"), 0o644)
	out, err := h.Invoke("prism_read", map[string]any{"file": "f.go"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
}

func TestToolRead_BadPath(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_read", map[string]any{"file": "../escape"}); err == nil {
		t.Error("expected err")
	}
}

func TestToolRead_NotFound(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_read", map[string]any{"file": "nope.go"}); err == nil {
		t.Error("expected err")
	}
}

// --- C: Anti-context manifest --------------------------------------------

func TestBuildAntiContextManifest_LowScoreExcluded(t *testing.T) {
	// Two candidates; one below excludeScoreThreshold, one above.
	candidates := []ranking.Candidate{
		{Symbol: grove.SymbolRecord{ID: "a", FilePath: "internal/legacy/old.go"}, Score: 0.04},
		{Symbol: grove.SymbolRecord{ID: "b", FilePath: "internal/core/main.go"}, Score: 0.50},
	}
	// Neither is in picked.
	manifest := buildAntiContextManifest(candidates, nil)

	if len(manifest) == 0 {
		t.Fatal("expected at least one excluded entry")
	}
	found := false
	for _, line := range manifest {
		if strings.Contains(line, "internal/legacy") {
			found = true
		}
	}
	if !found {
		t.Errorf("low-score path should appear in manifest, got: %v", manifest)
	}
	// High-score candidate should NOT be excluded.
	for _, line := range manifest {
		if strings.Contains(line, "internal/core") {
			t.Errorf("high-score path should not appear in manifest: %s", line)
		}
	}
}

func TestBuildAntiContextManifest_PickedNotExcluded(t *testing.T) {
	// Candidate with low score but already in picked — should not appear.
	sym := grove.SymbolRecord{ID: "x", FilePath: "internal/picked/file.go"}
	candidates := []ranking.Candidate{
		{Symbol: sym, Score: 0.02},
	}
	picked := []ranking.BudgetedSymbol{
		{Symbol: sym},
	}
	manifest := buildAntiContextManifest(candidates, picked)
	for _, line := range manifest {
		if strings.Contains(line, "internal/picked") {
			t.Errorf("picked symbol's dir should not appear in manifest: %s", line)
		}
	}
}

func TestBuildAntiContextManifest_GroupsByDir(t *testing.T) {
	// Multiple symbols in the same directory → one manifest line.
	candidates := []ranking.Candidate{
		{Symbol: grove.SymbolRecord{ID: "1", FilePath: "vendor/gopkg.in/a.go"}, Score: 0.01},
		{Symbol: grove.SymbolRecord{ID: "2", FilePath: "vendor/gopkg.in/b.go"}, Score: 0.02},
		{Symbol: grove.SymbolRecord{ID: "3", FilePath: "vendor/gopkg.in/c.go"}, Score: 0.03},
	}
	manifest := buildAntiContextManifest(candidates, nil)
	count := 0
	for _, line := range manifest {
		if strings.Contains(line, "vendor/gopkg.in") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("same directory should produce 1 manifest line, got %d: %v", count, manifest)
	}
}

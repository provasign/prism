package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestTaskOps_RecordedInLedger drives all five task-shaped ops end-to-end
// through the MCP handler and asserts each shows up in the ledger's call
// count. These ops have no token-savings baseline (the value is
// completeness, not cheaper context delivery), so — unlike prism_query or
// prism_read — nothing recorded their usage until RecordCall was added;
// this test is the regression guard for that gap.
func TestTaskOps_RecordedInLedger(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "doer.go", `package p

type Doer interface {
	Do() bool
}

type Impl struct{}

func (Impl) Do() bool { return true }

func run(d Doer) bool {
	return d.Do()
}

func unused() bool { return false }
`)
	mustWrite(t, dir, "doer_test.go", `package p

func TestRun(t interface{}) {
	run(Impl{})
}
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

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"prism_change_impact", map[string]any{"query": "Doer.Do"}},
		{"prism_missing_implementations", map[string]any{"query": "Doer.Do"}},
		{"prism_untested_surface", map[string]any{"query": "Doer.Do"}},
		{"prism_rename_plan", map[string]any{"query": "Doer.Do", "newName": "Execute"}},
		{"prism_dead_code", map[string]any{}},
	}
	for _, c := range calls {
		if _, err := h.Invoke(c.tool, c.args); err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
	}

	snap := h.Ledger.Snapshot()
	for _, c := range calls {
		stats, ok := snap.ByTool[c.tool]
		if !ok || stats.Calls != 1 {
			t.Errorf("%s: ByTool = %+v, want Calls=1", c.tool, stats)
		}
		// These ops have no savings baseline — recording their call count
		// must not touch the token ledger totals-based savings metric.
		if stats.Original != 0 || stats.Delivered != 0 {
			t.Errorf("%s: Original/Delivered = %d/%d, want 0/0 (no token baseline)", c.tool, stats.Original, stats.Delivered)
		}
	}
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

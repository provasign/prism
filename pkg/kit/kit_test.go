package kit

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsNonDirectory(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Open must reject a nonexistent directory")
	}
}

func TestLedgerPathIsPerRootAndStable(t *testing.T) {
	a1 := ledgerPath("/repo/a")
	a2 := ledgerPath("/repo/a")
	b := ledgerPath("/repo/b")
	if a1 != a2 {
		t.Fatal("ledger path must be stable for a root")
	}
	if a1 == b {
		t.Fatal("ledger path must differ per root")
	}
	if !strings.Contains(a1, filepath.Join("prism", "ledger")) {
		t.Fatalf("unexpected ledger location: %s", a1)
	}
}

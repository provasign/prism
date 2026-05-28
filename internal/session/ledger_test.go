package session

import (
	"math"
	"testing"
)

func TestLedgerSavings(t *testing.T) {
	l := NewLedger("s1")
	l.Record("prism_read", 1000, 250)
	l.Record("prism_query", 4000, 1000)
	if got := l.TotalDeliveredTokens(); got != 1250 {
		t.Fatalf("delivered: got %d", got)
	}
	want := 75.0
	if got := l.SavingsPercent(); math.Abs(got-want) > 1e-9 {
		t.Fatalf("savings: got %v want %v", got, want)
	}
	snap := l.Snapshot()
	if snap.ByTool["prism_read"].Calls != 1 {
		t.Fatalf("byTool missing prism_read")
	}
}

func TestLedgerEmpty(t *testing.T) {
	l := NewLedger("e")
	if l.SavingsPercent() != 0 {
		t.Fatal("empty ledger must report 0% savings")
	}
}

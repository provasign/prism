package session

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
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

func TestLedgerSaveLoadRoundTrip(t *testing.T) {
	l := NewLedger("rt-session")
	l.Record("prism_query", 4000, 1000)
	l.Record("prism_query", 2000, 500)
	l.Record("prism_read", 800, 800)

	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := l.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	l2, err := LoadLedger(path)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if l2.SessionID != "rt-session" {
		t.Errorf("SessionID: got %q want %q", l2.SessionID, "rt-session")
	}
	if l2.TotalOriginal != 6800 {
		t.Errorf("TotalOriginal: got %d want 6800", l2.TotalOriginal)
	}
	if l2.TotalDelivered != 2300 {
		t.Errorf("TotalDelivered: got %d want 2300", l2.TotalDelivered)
	}
	if l2.ByTool["prism_query"].Calls != 2 {
		t.Errorf("prism_query.Calls: got %d want 2", l2.ByTool["prism_query"].Calls)
	}
	if l2.ByTool["prism_read"].Calls != 1 {
		t.Errorf("prism_read.Calls: got %d want 1", l2.ByTool["prism_read"].Calls)
	}
}

func TestLedgerJSONKeysAreLowercase(t *testing.T) {
	l := NewLedger("keys-test")
	l.Record("prism_query", 100, 50)

	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := l.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// byTool entries must use lowercase field names.
	byTool := string(m["byTool"])
	for _, upper := range []string{`"Calls"`, `"Original"`, `"Delivered"`} {
		if strings.Contains(byTool, upper) {
			t.Errorf("ledger JSON contains uppercase key %s; want lowercase", upper)
		}
	}
	for _, lower := range []string{`"calls"`, `"original"`, `"delivered"`} {
		if !strings.Contains(byTool, lower) {
			t.Errorf("ledger JSON missing expected lowercase key %s", lower)
		}
	}
}

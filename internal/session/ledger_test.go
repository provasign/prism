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

func TestLedgerRecordCall(t *testing.T) {
	l := NewLedger("s2")
	l.Record("prism_read", 1000, 250)
	l.RecordCall("prism_change_impact")
	l.RecordCall("prism_change_impact")

	if got := l.ByTool["prism_change_impact"].Calls; got != 2 {
		t.Fatalf("prism_change_impact.Calls: got %d want 2", got)
	}
	// RecordCall must not touch tokens or dilute savings — it has no baseline.
	if got := l.ByTool["prism_change_impact"].Original; got != 0 {
		t.Fatalf("prism_change_impact.Original: got %d want 0", got)
	}
	if got := l.TotalDeliveredTokens(); got != 250 {
		t.Fatalf("delivered: got %d want 250 (RecordCall must not add to totals)", got)
	}
	if got, want := l.SavingsPercent(), 75.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("savings: got %v want %v (RecordCall must not change it)", got, want)
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

func TestRecordResultAndDeltaMerge(t *testing.T) {
	l := NewLedger("s1")
	l.RecordResult("prism_search", 100)
	l.RecordResult("prism_search", 50)
	l.RecordResult("prism_read", 400)
	if l.TotalResults != 550 {
		t.Fatalf("TotalResults = %d", l.TotalResults)
	}
	base := l.Snapshot()

	l.RecordResult("prism_read", 25)
	l.Record("prism_read", 1000, 300)
	d := l.DiffSince(base)
	if d.TotalResults != 25 || d.TotalDelivered != 300 {
		t.Fatalf("delta = %+v", d)
	}
	if d.ByTool["prism_read"].ResultTokens != 25 || d.ByTool["prism_read"].Calls != 1 {
		t.Fatalf("read delta = %+v", d.ByTool["prism_read"])
	}
	if _, ok := d.ByTool["prism_search"]; ok {
		t.Fatal("unchanged tool leaked into delta")
	}

	disk := NewLedger("disk")
	disk.RecordResult("prism_read", 7)
	disk.ApplyDelta(d)
	if disk.TotalResults != 32 || disk.ByTool["prism_read"].ResultTokens != 32 {
		t.Fatalf("merged = %+v", disk.Snapshot())
	}

	// Round-trips through Save/Load.
	path := t.TempDir() + "/ledger.json"
	if err := disk.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.TotalResults != 32 || back.ByTool["prism_read"].ResultTokens != 32 {
		t.Fatalf("round-trip lost result tokens: %+v", back.Snapshot())
	}
}

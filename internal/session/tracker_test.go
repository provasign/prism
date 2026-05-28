package session

import "testing"

func TestTrackerRecordLookup(t *testing.T) {
	tr := NewTracker(0)
	tr.Record("a.go", "h1", 100, "full")
	entry, found, same := tr.Lookup("a.go", "h1")
	if !found || !same || entry.TokenDistanceAtSend != 100 {
		t.Fatalf("got found=%v same=%v tokens=%d", found, same, entry.TokenDistanceAtSend)
	}
	if _, _, same2 := tr.Lookup("a.go", "h2"); same2 {
		t.Fatal("different hash must report sameHash=false")
	}
	if _, found, _ := tr.Lookup("missing.go", ""); found {
		t.Fatal("missing file must report found=false")
	}
}

func TestTrackerLRUEviction(t *testing.T) {
	tr := NewTracker(2)
	tr.Record("a.go", "h", 1, "full")
	tr.Record("b.go", "h", 1, "full")
	tr.Record("c.go", "h", 1, "full")
	if _, found, _ := tr.Lookup("a.go", "h"); found {
		t.Fatal("a should be evicted")
	}
	if _, found, _ := tr.Lookup("b.go", "h"); !found {
		t.Fatal("b must remain")
	}
	if tr.Len() != 2 {
		t.Fatalf("len=%d", tr.Len())
	}
}

func TestTrackerResetClears(t *testing.T) {
	tr := NewTracker(10)
	tr.Record("a.go", "h", 1, "full")
	tr.Reset()
	if tr.Len() != 0 {
		t.Fatal("reset must clear")
	}
}

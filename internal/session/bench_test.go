package session

import (
	"fmt"
	"testing"
)

// ── Tracker: core hot path ────────────────────────────────────────────────────

func BenchmarkTracker_RecordLookup(b *testing.B) {
	tr := NewTracker(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("pkg/file%d.go", i%200)
		tr.Record(path, "hashABC", int64(i*100), "full")
		tr.Lookup(path, "hashABC")
	}
}

func BenchmarkTracker_LookupHit(b *testing.B) {
	tr := NewTracker(1000)
	for i := 0; i < 100; i++ {
		tr.Record(fmt.Sprintf("pkg/f%d.go", i), "h", int64(i), "full")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.Lookup(fmt.Sprintf("pkg/f%d.go", i%100), "h")
	}
}

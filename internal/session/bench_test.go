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

// ── Cache: save/load round-trip ───────────────────────────────────────────────

func BenchmarkSaveCache_50Entries(b *testing.B) {
	root := b.TempDir()
	tr := NewTracker(200)
	for i := 0; i < 50; i++ {
		path := fmt.Sprintf("pkg/file%d.go", i)
		tr.Record(path, fmt.Sprintf("hash%d", i), int64(i*50), "full")
		tr.UpdateSymbolSHAs(path, map[string]string{
			"FuncA": "deadbeef",
			"FuncB": "cafebabe",
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SaveCache(tr, root, 500)
	}
}

func BenchmarkLoadCache_50Entries(b *testing.B) {
	root := b.TempDir()
	tr := NewTracker(200)
	for i := 0; i < 50; i++ {
		path := fmt.Sprintf("pkg/file%d.go", i)
		tr.Record(path, fmt.Sprintf("hash%d", i), int64(i*50), "full")
	}
	SaveCache(tr, root, 500) // write once

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr2 := NewTracker(200)
		LoadCache(tr2, root, 7)
	}
}

func BenchmarkSaveLoadRoundTrip_50Entries(b *testing.B) {
	root := b.TempDir()
	tr := NewTracker(200)
	for i := 0; i < 50; i++ {
		tr.Record(fmt.Sprintf("pkg/f%d.go", i), fmt.Sprintf("h%d", i), int64(i), "full")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SaveCache(tr, root, 500)
		tr2 := NewTracker(200)
		LoadCache(tr2, root, 7)
	}
}

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeTempRepo creates a throw-away directory to use as a fake repo root.
func makeTempRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	return d
}

func TestSaveLoadRoundTrip(t *testing.T) {
	repo := makeTempRepo(t)

	tr := NewTracker(10)
	tr.Record("pkg/foo.go", "hash1", 200, "full")
	tr.Record("pkg/bar.go", "hash2", 100, "signature")

	SaveCache(tr, repo, 5)

	tr2 := NewTracker(10)
	LoadCache(tr2, repo, 7)

	if tr2.Len() != 2 {
		t.Fatalf("expected 2 loaded entries, got %d", tr2.Len())
	}
	e, found, same := tr2.Lookup("pkg/foo.go", "hash1")
	if !found || !same {
		t.Fatalf("foo.go not found or hash mismatch: found=%v same=%v", found, same)
	}
	if e.AccessCount != 1 {
		t.Errorf("loaded entry should have AccessCount=1, got %d", e.AccessCount)
	}
	if e.TokenDistanceAtSend != 0 {
		t.Errorf("loaded entry should have TokenDistanceAtSend=0, got %d", e.TokenDistanceAtSend)
	}
	if e.DisclosureLevel != "full" {
		t.Errorf("disclosure level should be 'full', got %q", e.DisclosureLevel)
	}
}

func TestLoadCache_EmptyTrackerNoFile(t *testing.T) {
	repo := makeTempRepo(t)
	tr := NewTracker(10)
	// No cache file — should silently succeed with empty tracker.
	LoadCache(tr, repo, 7)
	if tr.Len() != 0 {
		t.Fatalf("expected 0 entries, got %d", tr.Len())
	}
}

func TestLoadCache_CorruptFile(t *testing.T) {
	repo := makeTempRepo(t)

	// Write garbage bytes to the expected cache file path.
	path, err := cacheFilePath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json {{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	tr := NewTracker(10)
	LoadCache(tr, repo, 7) // must not panic or error
	if tr.Len() != 0 {
		t.Fatalf("corrupt cache should yield empty tracker, got %d", tr.Len())
	}
}

func TestLoadCache_AgeCutoff(t *testing.T) {
	repo := makeTempRepo(t)

	// Manually write a cache file with one fresh and one stale entry.
	now := time.Now().UTC()
	fresh := now.Add(-24 * time.Hour).Format(time.RFC3339)     // 1 day old — within 7-day window
	stale := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339) // 8 days old — beyond cutoff

	raw, err := json.Marshal(cacheFile{
		RepoRoot: repo,
		SavedAt:  now.Format(time.RFC3339),
		Entries: []cacheEntry{
			{FilePath: "fresh.go", ContentHash: "h1", DisclosureLevel: "full", SavedAt: fresh},
			{FilePath: "stale.go", ContentHash: "h2", DisclosureLevel: "full", SavedAt: stale},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := cacheFilePath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	tr := NewTracker(10)
	LoadCache(tr, repo, 7)

	if tr.Len() != 1 {
		t.Fatalf("expected 1 entry (fresh only), got %d", tr.Len())
	}
	if _, found, _ := tr.Lookup("fresh.go", "h1"); !found {
		t.Fatal("fresh.go should be loaded")
	}
	if _, found, _ := tr.Lookup("stale.go", "h2"); found {
		t.Fatal("stale.go should be excluded by age cutoff")
	}
}

func TestSaveCache_SkipsEntriesWithNoHash(t *testing.T) {
	repo := makeTempRepo(t)

	tr := NewTracker(10)
	// Record one proper entry and one with empty hash (simulates never-read entry).
	tr.Record("good.go", "hash1", 50, "full")
	// Force a raw insert with empty ContentHash to simulate the edge case.
	tr.mu.Lock()
	e := &Entry{FilePath: "bad.go", ContentHash: ""}
	el := tr.lru.PushFront(e)
	tr.entries["bad.go"] = el
	tr.mu.Unlock()

	SaveCache(tr, repo, 100)

	tr2 := NewTracker(10)
	LoadCache(tr2, repo, 7)

	// Only good.go should be persisted.
	if tr2.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", tr2.Len())
	}
	if _, found, _ := tr2.Lookup("good.go", "hash1"); !found {
		t.Fatal("good.go should be loaded")
	}
}

func TestSaveCache_MaxEntriesRespected(t *testing.T) {
	repo := makeTempRepo(t)

	tr := NewTracker(20)
	for i := 0; i < 10; i++ {
		tr.Record(filepath.Join("pkg", "f"+string(rune('a'+i))+".go"), "h", 1, "full")
	}

	SaveCache(tr, repo, 3) // limit to 3

	tr2 := NewTracker(20)
	LoadCache(tr2, repo, 7)

	if tr2.Len() != 3 {
		t.Fatalf("expected 3 entries (maxEntries=3), got %d", tr2.Len())
	}
}

func TestLoadCache_RespectsTrackerCapacityKeepsMRU(t *testing.T) {
	repo := makeTempRepo(t)

	tr := NewTracker(20)
	for i := 0; i < 5; i++ {
		tr.Record(filepath.Join("pkg", "f"+string(rune('a'+i))+".go"), "h", 1, "full")
	}
	SaveCache(tr, repo, 5)

	tr2 := NewTracker(3)
	LoadCache(tr2, repo, 7)

	if tr2.Len() != 3 {
		t.Fatalf("expected tracker capacity to keep 3 entries, got %d", tr2.Len())
	}
	for _, path := range []string{"pkg/fe.go", "pkg/fd.go", "pkg/fc.go"} {
		if _, found, _ := tr2.Lookup(path, "h"); !found {
			t.Fatalf("expected recent entry %s to survive cache load", path)
		}
	}
	for _, path := range []string{"pkg/fb.go", "pkg/fa.go"} {
		if _, found, _ := tr2.Lookup(path, "h"); found {
			t.Fatalf("expected old entry %s to be evicted during cache load", path)
		}
	}
}

func TestSaveCache_SymbolSHAsPreserved(t *testing.T) {
	repo := makeTempRepo(t)

	tr := NewTracker(10)
	tr.Record("svc.go", "hashA", 300, "full")
	shas := map[string]string{"MyFunc": "deadbeef", "OtherFunc": "cafebabe"}
	tr.UpdateSymbolSHAs("svc.go", shas)

	SaveCache(tr, repo, 10)

	tr2 := NewTracker(10)
	LoadCache(tr2, repo, 7)

	e, found, _ := tr2.Lookup("svc.go", "hashA")
	if !found {
		t.Fatal("svc.go not found after reload")
	}
	if len(e.SymbolSHAs) != 2 {
		t.Fatalf("expected 2 symbol SHAs, got %d", len(e.SymbolSHAs))
	}
	if e.SymbolSHAs["MyFunc"] != "deadbeef" {
		t.Errorf("MyFunc SHA mismatch: %q", e.SymbolSHAs["MyFunc"])
	}
}

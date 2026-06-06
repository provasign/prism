package session

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// cacheEntry is the JSON-serialisable form of Entry persisted to disk.
// We intentionally drop TokenDistanceAtSend (session-relative) and reset
// AccessCount to 1 so a warm-loaded entry behaves like a single prior read.
type cacheEntry struct {
	FilePath        string            `json:"filePath"`
	ContentHash     string            `json:"contentHash"`
	DisclosureLevel string            `json:"disclosureLevel"`
	SymbolSHAs      map[string]string `json:"symbolSHAs,omitempty"`
	SavedAt         string            `json:"savedAt"`
}

// cacheFile is the top-level JSON structure written to disk.
type cacheFile struct {
	RepoRoot string       `json:"repoRoot"`
	SavedAt  string       `json:"savedAt"`
	Entries  []cacheEntry `json:"entries"`
}

// cacheDir returns the platform cache dir for prism session data.
// Uses $XDG_CACHE_HOME/prism/sessions on Linux, ~/Library/Caches on macOS,
// and %LOCALAPPDATA%\prism\sessions on Windows — all via os.UserCacheDir.
func cacheDir(repoRoot string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	// Key by a short SHA of the absolute repo root path so different repos
	// never share a cache file.
	h := sha1.New()
	h.Write([]byte(repoRoot))
	key := hex.EncodeToString(h.Sum(nil))[:16]
	return filepath.Join(base, "prism", "sessions", key), nil
}

func cacheFilePath(repoRoot string) (string, error) {
	dir, err := cacheDir(repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lru.json"), nil
}

// SaveCache persists the tracker's LRU entries to disk so a future session
// can start warm. At most maxEntries entries are written (MRU order).
// Errors are silently ignored — cache persistence is best-effort.
func SaveCache(t *Tracker, repoRoot string, maxEntries int) {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	path, err := cacheFilePath(repoRoot)
	if err != nil {
		return
	}

	t.mu.Lock()
	entries := make([]cacheEntry, 0, min(t.lru.Len(), maxEntries))
	count := 0
	for el := t.lru.Front(); el != nil && count < maxEntries; el = el.Next() {
		e := el.Value.(*Entry)
		if e.ContentHash == "" {
			continue // never fully recorded — skip
		}
		entries = append(entries, cacheEntry{
			FilePath:        e.FilePath,
			ContentHash:     e.ContentHash,
			DisclosureLevel: e.DisclosureLevel,
			SymbolSHAs:      e.SymbolSHAs,
			SavedAt:         time.Now().UTC().Format(time.RFC3339),
		})
		count++
	}
	t.mu.Unlock()

	cf := cacheFile{
		RepoRoot: repoRoot,
		SavedAt:  time.Now().UTC().Format(time.RFC3339),
		Entries:  entries,
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o600)
}

// LoadCache populates tracker with previously-seen entries from the on-disk
// cache. Each loaded entry starts with AccessCount=1 (one prior read) and
// TokenDistanceAtSend=0 (session-relative counter reset). The caller should
// call LoadCache right after NewTracker, before any tool dispatches.
//
// Entries older than maxAgeDays are skipped. Pass 0 to use the default (7 days).
// Errors (missing file, corrupt JSON) are silently ignored.
func LoadCache(t *Tracker, repoRoot string, maxAgeDays int) {
	if maxAgeDays <= 0 {
		maxAgeDays = 7
	}
	path, err := cacheFilePath(repoRoot)
	if err != nil {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return // cache miss — normal on first run
	}
	var cf cacheFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return // corrupt cache — ignore
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	t.mu.Lock()
	defer t.mu.Unlock()
	// Insert in reverse order with PushFront so the most-recently-used entry
	// ends up at the front after reload. If the tracker capacity is smaller
	// than the persisted cache, evicting from Back then drops the oldest entry.
	for i := len(cf.Entries) - 1; i >= 0; i-- {
		ce := cf.Entries[i]
		if ce.FilePath == "" || ce.ContentHash == "" {
			continue
		}
		ce.FilePath = normalizeFilePath(ce.FilePath)
		// Skip stale entries.
		if saved, err := time.Parse(time.RFC3339, ce.SavedAt); err == nil {
			if saved.Before(cutoff) {
				continue
			}
		}
		if _, exists := t.entries[ce.FilePath]; exists {
			continue // already loaded (shouldn't happen, but be safe)
		}
		e := &Entry{
			FilePath:            ce.FilePath,
			ContentHash:         ce.ContentHash,
			TokenDistanceAtSend: 0,
			DisclosureLevel:     ce.DisclosureLevel,
			AccessCount:         1,
			SymbolSHAs:          ce.SymbolSHAs,
		}
		el := t.lru.PushFront(e)
		t.entries[ce.FilePath] = el
		if t.lru.Len() > t.maxFiles {
			// Trim oldest to stay within cap.
			oldest := t.lru.Back()
			if oldest != nil {
				t.lru.Remove(oldest)
				delete(t.entries, oldest.Value.(*Entry).FilePath)
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

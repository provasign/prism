// Package session implements Prism's per-session state: an O(1) LRU file
// tracker (for delivery deduplication) and a token ledger (for savings
// reporting). Both are concurrency-safe and live for the lifetime of one
// MCP session or one CLI/agent session.
package session

import (
	"container/list"
	"path/filepath"
	"sync"
)

// Entry records what was delivered for one file.
type Entry struct {
	FilePath            string
	ContentHash         string // SHA-256 of source content
	TokenDistanceAtSend int64  // cumulative tokens delivered when this was sent
	DisclosureLevel     string
	AccessCount         int
	SymbolSHAs          map[string]string // symbolName → sha(RawText); used for semantic delta encoding
}

// Tracker is an O(1) LRU cache keyed by file path.
type Tracker struct {
	mu       sync.Mutex
	maxFiles int
	entries  map[string]*list.Element // filePath → list element holding *Entry
	lru      *list.List
}

// NewTracker creates a tracker with the given capacity.
func NewTracker(maxFiles int) *Tracker {
	if maxFiles <= 0 {
		maxFiles = 50000
	}
	return &Tracker{
		maxFiles: maxFiles,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

// Record marks filePath as delivered with the given metadata. If the file is
// already tracked, AccessCount is incremented and metadata is updated.
func (t *Tracker) Record(filePath, contentHash string, tokensDelivered int64, level string) {
	filePath = normalizeFilePath(filePath)
	t.mu.Lock()
	defer t.mu.Unlock()
	if el, ok := t.entries[filePath]; ok {
		e := el.Value.(*Entry)
		e.ContentHash = contentHash
		e.TokenDistanceAtSend = tokensDelivered
		e.DisclosureLevel = level
		e.AccessCount++
		t.lru.MoveToFront(el)
		return
	}
	e := &Entry{
		FilePath:            filePath,
		ContentHash:         contentHash,
		TokenDistanceAtSend: tokensDelivered,
		DisclosureLevel:     level,
		AccessCount:         1,
	}
	el := t.lru.PushFront(e)
	t.entries[filePath] = el
	if t.lru.Len() > t.maxFiles {
		t.evictOldest()
	}
}

// Lookup returns the entry for filePath if it exists. The second return
// value matches whether the stored contentHash is the same as the supplied
// one — false means the file changed since last seen.
func (t *Tracker) Lookup(filePath, contentHash string) (*Entry, bool, bool) {
	filePath = normalizeFilePath(filePath)
	t.mu.Lock()
	defer t.mu.Unlock()
	el, ok := t.entries[filePath]
	if !ok {
		return nil, false, false
	}
	e := el.Value.(*Entry)
	t.lru.MoveToFront(el)
	return e, true, e.ContentHash == contentHash
}

// Len returns the number of tracked files.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lru.Len()
}

// Reset clears all entries.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = make(map[string]*list.Element)
	t.lru = list.New()
}

// UpdateSymbolSHAs stores a symbolName→sha map for a file that has already
// been tracked. Called after delivering a semantic delta so future re-reads
// can diff at symbol granularity. No-op if the file isn't tracked.
func (t *Tracker) UpdateSymbolSHAs(filePath string, shas map[string]string) {
	filePath = normalizeFilePath(filePath)
	t.mu.Lock()
	defer t.mu.Unlock()
	if el, ok := t.entries[filePath]; ok {
		el.Value.(*Entry).SymbolSHAs = shas
	}
}

func (t *Tracker) evictOldest() {
	el := t.lru.Back()
	if el == nil {
		return
	}
	t.lru.Remove(el)
	delete(t.entries, el.Value.(*Entry).FilePath)
}

func normalizeFilePath(p string) string {
	return filepath.ToSlash(p)
}

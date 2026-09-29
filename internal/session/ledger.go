package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ToolStats records token accounting for one MCP tool.
type ToolStats struct {
	Calls     int   `json:"calls"`
	Original  int64 `json:"original"`
	Delivered int64 `json:"delivered"`
	// ResultTokens is the size of every rendered result this tool returned,
	// as delivered to the agent (final text, post-compression). Independent
	// of Original/Delivered, which track the compression baseline only.
	ResultTokens int64 `json:"resultTokens,omitempty"`
	// ResultCalls counts dispatches that produced a result — one per tool
	// call at the server, regardless of which internal paths also Recorded.
	ResultCalls int64 `json:"resultCalls,omitempty"`
}

// Ledger tracks per-session token savings.
type Ledger struct {
	mu             sync.Mutex
	SessionID      string
	TotalOriginal  int64
	TotalDelivered int64
	ByTool         map[string]*ToolStats
	StartTime      time.Time
	TotalResults   int64
}

// NewLedger constructs a new ledger.
func NewLedger(sessionID string) *Ledger {
	return &Ledger{
		SessionID: sessionID,
		ByTool:    make(map[string]*ToolStats),
		StartTime: time.Now(),
	}
}

// Record adds tokens to the ledger for the given tool.
func (l *Ledger) Record(tool string, originalTokens, deliveredTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats, ok := l.ByTool[tool]
	if !ok {
		stats = &ToolStats{}
		l.ByTool[tool] = stats
	}
	stats.Calls++
	stats.Original += int64(originalTokens)
	stats.Delivered += int64(deliveredTokens)
	l.TotalOriginal += int64(originalTokens)
	l.TotalDelivered += int64(deliveredTokens)
}

// RecordCall counts an invocation of a tool that has no token-savings
// baseline (e.g. a deterministic task op like change-impact, where the
// value is completeness, not cheaper delivery of the same context). Unlike
// Record, it leaves Original/Delivered and the ledger totals untouched, so
// it cannot dilute SavingsPercent — it only makes the tool's call count
// visible in ByTool.
func (l *Ledger) RecordCall(tool string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats, ok := l.ByTool[tool]
	if !ok {
		stats = &ToolStats{}
		l.ByTool[tool] = stats
	}
	stats.Calls++
}

// RecordResult adds the rendered size of one tool result — the bytes that
// actually land in the agent's context window. This is the number token-cost
// analysis needs (result size compounds via cache re-reads on every later
// turn); Record's Original/Delivered pair measures compression savings and
// says nothing about absolute cost.
func (l *Ledger) RecordResult(tool string, tokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats, ok := l.ByTool[tool]
	if !ok {
		stats = &ToolStats{}
		l.ByTool[tool] = stats
	}
	stats.ResultTokens += int64(tokens)
	stats.ResultCalls++
	l.TotalResults += int64(tokens)
}

// DiffSince returns per-tool deltas accumulated since prev (a snapshot of
// this ledger taken earlier). Used by the long-running MCP server to merge
// its in-memory counts into the shared on-disk ledger without clobbering
// other sessions' contributions.
func (l *Ledger) DiffSince(prev Summary) Summary {
	cur := l.Snapshot()
	d := Summary{SessionID: cur.SessionID, ByTool: map[string]ToolStats{}}
	d.TotalOriginal = cur.TotalOriginal - prev.TotalOriginal
	d.TotalDelivered = cur.TotalDelivered - prev.TotalDelivered
	d.TotalResults = cur.TotalResults - prev.TotalResults
	for tool, s := range cur.ByTool {
		p := prev.ByTool[tool]
		delta := ToolStats{
			Calls:        s.Calls - p.Calls,
			Original:     s.Original - p.Original,
			Delivered:    s.Delivered - p.Delivered,
			ResultTokens: s.ResultTokens - p.ResultTokens,
			ResultCalls:  s.ResultCalls - p.ResultCalls,
		}
		if delta != (ToolStats{}) {
			d.ByTool[tool] = delta
		}
	}
	return d
}

// ApplyDelta adds another session's per-tool deltas into this ledger.
func (l *Ledger) ApplyDelta(d Summary) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.TotalOriginal += d.TotalOriginal
	l.TotalDelivered += d.TotalDelivered
	l.TotalResults += d.TotalResults
	for tool, ds := range d.ByTool {
		stats, ok := l.ByTool[tool]
		if !ok {
			stats = &ToolStats{}
			l.ByTool[tool] = stats
		}
		stats.Calls += ds.Calls
		stats.Original += ds.Original
		stats.Delivered += ds.Delivered
		stats.ResultTokens += ds.ResultTokens
		stats.ResultCalls += ds.ResultCalls
	}
}

// TotalDeliveredTokens returns running total of delivered tokens.
func (l *Ledger) TotalDeliveredTokens() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.TotalDelivered
}

// SavingsPercent returns 1 - delivered/original.
func (l *Ledger) SavingsPercent() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.TotalOriginal == 0 {
		return 0
	}
	return (1.0 - float64(l.TotalDelivered)/float64(l.TotalOriginal)) * 100.0
}

// Summary returns a snapshot of ledger state suitable for JSON encoding.
type Summary struct {
	SessionID      string               `json:"sessionId"`
	StartTime      string               `json:"startTime"`
	TotalOriginal  int64                `json:"totalOriginalTokens"`
	TotalDelivered int64                `json:"totalDeliveredTokens"`
	TotalResults   int64                `json:"totalResultTokens,omitempty"`
	SavingsPercent float64              `json:"savingsPercent"`
	ByTool         map[string]ToolStats `json:"byTool"`
}

// Snapshot returns an immutable view of the ledger.
func (l *Ledger) Snapshot() Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	tools := make(map[string]ToolStats, len(l.ByTool))
	for k, v := range l.ByTool {
		tools[k] = *v
	}
	saving := 0.0
	if l.TotalOriginal > 0 {
		saving = (1.0 - float64(l.TotalDelivered)/float64(l.TotalOriginal)) * 100.0
	}
	return Summary{
		SessionID:      l.SessionID,
		StartTime:      l.StartTime.Format(time.RFC3339),
		TotalOriginal:  l.TotalOriginal,
		TotalDelivered: l.TotalDelivered,
		TotalResults:   l.TotalResults,
		SavingsPercent: saving,
		ByTool:         tools,
	}
}

// Save writes the current ledger snapshot to disk as JSON.
func (l *Ledger) Save(path string) error {
	s := l.Snapshot()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadLedger loads a persisted ledger from disk.
func LoadLedger(path string) (*Ledger, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	start := time.Now()
	if t, err := time.Parse(time.RFC3339, s.StartTime); err == nil {
		start = t
	}
	l := &Ledger{
		SessionID:      s.SessionID,
		TotalOriginal:  s.TotalOriginal,
		TotalDelivered: s.TotalDelivered,
		TotalResults:   s.TotalResults,
		ByTool:         make(map[string]*ToolStats, len(s.ByTool)),
		StartTime:      start,
	}
	for k, v := range s.ByTool {
		vv := v
		l.ByTool[k] = &vv
	}
	if l.SessionID == "" {
		l.SessionID = time.Now().Format("20060102-150405")
	}
	return l, nil
}

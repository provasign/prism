package session

import (
	"sync"
	"time"
)

// ToolStats records token accounting for one MCP tool.
type ToolStats struct {
	Calls     int
	Original  int64
	Delivered int64
}

// Ledger tracks per-session token savings.
type Ledger struct {
	mu             sync.Mutex
	SessionID      string
	TotalOriginal  int64
	TotalDelivered int64
	ByTool         map[string]*ToolStats
	StartTime      time.Time
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
		SavingsPercent: saving,
		ByTool:         tools,
	}
}

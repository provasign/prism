package mcp

import (
	"strings"
	"testing"
)

// Graph deliveries live in the same session LRU as file deliveries, keyed
// under a "graph://" scheme. StaleContextWarning used to ReadFile those keys,
// fail forever, and warn on every call that a "file" had changed — advising
// prism_read on an unreadable key. Graph entries must be skipped.
func TestStaleContextWarningSkipsGraphDeliveries(t *testing.T) {
	h := newTestHandler(t)
	h.Session.Record(`graph://prism_map?{"depth":2}`, "abc123", 5000, "full")
	if w := h.StaleContextWarning(); w != "" {
		t.Errorf("graph delivery flagged as stale file: %q", w)
	}
	// A genuinely missing tracked FILE still warns.
	h.Session.Record("no/such/file.go", "def456", 100, "full")
	w := h.StaleContextWarning()
	if !strings.Contains(w, "no/such/file.go") {
		t.Errorf("missing file not flagged: %q", w)
	}
	if strings.Contains(w, "graph://") {
		t.Errorf("graph key leaked into warning: %q", w)
	}
}

// prism_drift shares the LRU sweep and had the same defect.
func TestToolDriftSkipsGraphDeliveries(t *testing.T) {
	h := newTestHandler(t)
	h.Session.Record(`graph://prism_dead_code?{}`, "abc123", 5000, "full")
	out, err := h.toolDrift(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.CheckedFiles != 0 || report.ChangedFiles != 0 {
		t.Errorf("graph delivery counted as drifted file: %+v", report)
	}
}

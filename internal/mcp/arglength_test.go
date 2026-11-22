package mcp

import (
	"strings"
	"testing"
)

// BUG-21: an over-long name is rejected with a short preview, not echoed.
func TestOverLongNameIsNotEchoed(t *testing.T) {
	h := newTestHandler(t)
	long := strings.Repeat("A", 100000)
	_, err := h.Invoke("prism_lookup", map[string]any{"name": long})
	if err == nil || len(err.Error()) > 200 || !strings.Contains(err.Error(), "100000 characters") {
		t.Fatalf("err (%d bytes) = %.300v", len(err.Error()), err)
	}
	if err := checkArgLengths(map[string]any{"name": []any{"ok", strings.Repeat("b", 1001)}}); err == nil {
		t.Fatal("list item over the cap was accepted")
	}
	if err := checkArgLengths(map[string]any{"name": strings.Repeat("c", 1000)}); err != nil {
		t.Fatalf("1000 characters rejected: %v", err)
	}
}

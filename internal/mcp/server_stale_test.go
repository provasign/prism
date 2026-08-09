package mcp

import "testing"

// The note must fire once, and only when the binary on disk is newer than
// the process — never for a current server.
func TestStaleBinaryNoteQuietWhenCurrent(t *testing.T) {
	staleBinaryWarned = false
	if n := staleBinaryNote(); n != "" {
		t.Errorf("current binary must produce no note, got %q", n)
	}
}

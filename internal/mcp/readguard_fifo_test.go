//go:build unix

package mcp

import (
	"path/filepath"
	"syscall"
	"testing"
)

// SEC-04: read refuses a FIFO instead of blocking on it (Unix only).
func TestReadGuardRefusesFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	g, err := readGuarded(fifo, "pipe", false, 0, 0)
	if err != nil || g.refused != "not a regular file" {
		t.Fatalf("fifo: %+v %v", g, err)
	}
}

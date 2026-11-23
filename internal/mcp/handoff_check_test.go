//go:build unix

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SEC-08: a group- or world-writable replacement binary is not run.
func TestHandoffBinaryOK(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prism")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := handoffBinaryOK(p); err != nil {
		t.Fatalf("own 0755 binary refused: %v", err)
	}
	if err := os.Chmod(p, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := handoffBinaryOK(p); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("world-writable binary accepted: %v", err)
	}
}

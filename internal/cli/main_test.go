package cli

import (
	"os"
	"testing"
)

// The per-user registry of known repositories must never be written by
// tests: point it at a directory that is removed afterwards.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "prism-state-")
	if err != nil {
		panic(err)
	}
	os.Setenv("PRISM_STATE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

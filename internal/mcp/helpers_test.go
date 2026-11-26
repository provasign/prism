package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/session"
)

func newTestHandler(t *testing.T) *core.Handler {
	t.Helper()
	return core.NewHandlerWithLedger(config.Default(), t.TempDir(), nil, session.NewLedger("test-session"))
}

// newTextSearchHandler is a handler over a tiny repository text search can
// answer without an index.
func newTextSearchHandler(t *testing.T) *core.Handler {
	t.Helper()
	h := newTestHandler(t)
	mustWrite(t, h.Root, "alpha.go", "package p\n\nfunc Alpha() {}\n")
	mustWrite(t, h.Root, "beta.go", "package p\n\nfunc Beta() { Alpha() }\n")
	return h
}

func mustWrite(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

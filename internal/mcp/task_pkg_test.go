package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A corrupt task package must be REPORTED, not silently ignored. Both
// failures used to return a bare nil, so a repo whose package had been
// truncated or hand-edited quietly stopped cross-checking obligations while
// everyone believed the check was running.
func TestLoadTaskPackageDistinguishesMissingFromCorrupt(t *testing.T) {
	root := t.TempDir()
	h := &Handler{Root: root}

	if pkg, why := h.loadTaskPackage(); pkg != nil || why != "" {
		t.Fatalf("absent package: got (%v, %q), want (nil, \"\") — no prepare ran is normal", pkg, why)
	}

	path := filepath.Join(root, ".grove", "task-package.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"task": "truncated`), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, why := h.loadTaskPackage()
	if pkg != nil {
		t.Errorf("corrupt package returned a value: %+v", pkg)
	}
	if why == "" {
		t.Fatal("corrupt package reported no reason — the cross-check stopped silently")
	}
	for _, want := range []string{"did NOT run", "re-run prepare"} {
		if !strings.Contains(why, want) {
			t.Errorf("reason %q does not mention %q", why, want)
		}
	}
}

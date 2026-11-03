package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/textsearch"
)

// A timeout with ripgrep in use must not blame a missing ripgrep, and names
// the folders that hold the bulk of the searched text.
func TestTextTimeoutWarningNamesHeavyDirsAndRightCause(t *testing.T) {
	rg := textTimeoutWarning(textsearch.Result{Backend: "rg", TimedOut: true,
		HeavyDirs: []textsearch.HeavyDir{{Dir: "exports/", Files: 8214, Bytes: 3 << 30}, {Dir: "src/", Files: 120, Bytes: 2 << 20}}})
	for _, want := range []string{"INCOMPLETE", "absence is not established", "exports/ (8214 files, 3.0GB)", "src/ (120 files, 2MB)", ".groveignore"} {
		if !strings.Contains(rg, want) {
			t.Fatalf("rg warning missing %q:\n%s", want, rg)
		}
	}
	if strings.Contains(rg, "installing ripgrep") {
		t.Fatalf("rg timeout blamed a missing ripgrep:\n%s", rg)
	}
	defer func(f func() string) { detectedEngine = f }(detectedEngine)
	detectedEngine = func() string { return "native" }
	native := textTimeoutWarning(textsearch.Result{Backend: "native", TimedOut: true})
	if !strings.Contains(native, "installing ripgrep") || strings.Contains(native, ".groveignore") {
		t.Fatalf("native warning:\n%s", native)
	}
	// ripgrep is installed but failed, and the built-in scanner took over:
	// the advice must not be to install it (2026-11-02).
	detectedEngine = func() string { return "rg" }
	fellBack := textTimeoutWarning(textsearch.Result{Backend: "native", TimedOut: true})
	if strings.Contains(fellBack, "installing ripgrep") || !strings.Contains(fellBack, "rg did not finish") {
		t.Fatalf("fallback warning:\n%s", fellBack)
	}
}

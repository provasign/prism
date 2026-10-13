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
	native := textTimeoutWarning(textsearch.Result{Backend: "native", TimedOut: true})
	if !strings.Contains(native, "installing ripgrep") || strings.Contains(native, ".groveignore") {
		t.Fatalf("native warning:\n%s", native)
	}
}

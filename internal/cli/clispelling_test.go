package cli

import (
	"strings"
	"testing"
)

// CON-03: CLI text output speaks CLI. Note lines never carry MCP call
// spellings (prism_lookup, op=read, include_tests=true); lines carrying
// repository text are left alone.
func TestCLIOutputHasNoMCPVocabulary(t *testing.T) {
	dir := setupCLIProject(t)
	writeCLIFile(t, dir, "go.mod", "module example.com/sp\n\ngo 1.22\n")
	// Source that itself mentions MCP names must print unchanged.
	writeCLIFile(t, dir, "tools.go", "package main\n\n// Tools lists prism_lookup and op=read for the gateway.\nfunc Tools() []string { return []string{\"prism_lookup\", \"include_tests=true\"} }\n")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	runs := [][]string{
		{"search", "Main", "--dir", dir, "--format", "text"},
		{"search", "Main", "--rollup-only", "--dir", dir, "--format", "text"},
		{"search", "[x", "--regex", "--dir", dir, "--format", "text"},
		{"query", "--terms", "Main", dir, "--format", "text"},
		{"map", dir},
		{"change-impact", "Main", dir},
		{"lookup", "Tools", dir, "--format", "text"},
	}
	for _, args := range runs {
		var out string
		errOut := captureStderr(func() { out = captureStdout(func() { _ = Run(args) }) })
		for _, line := range strings.Split(out+errOut, "\n") {
			if sourceRow.MatchString(line) {
				continue
			}
			for _, bad := range []string{"prism_", "op=", "=true", "=false"} {
				if strings.Contains(line, bad) {
					t.Errorf("%v: note line has %q: %s", args, bad, line)
				}
			}
		}
		if args[0] == "lookup" && !strings.Contains(out, `"prism_lookup", "include_tests=true"`) {
			t.Errorf("source text was rewritten:\n%s", out)
		}
	}
}

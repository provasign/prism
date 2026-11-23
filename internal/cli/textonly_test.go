package cli

import (
	"strings"
	"testing"
)

// CON-02: --format text never prints JSON, error and edge paths included.
func TestFormatTextNeverPrintsJSON(t *testing.T) {
	dir := setupCLIProject(t)
	writeCLIFile(t, dir, "go.mod", "module example.com/tx\n\ngo 1.22\n")
	writeCLIFile(t, dir, "b.go", "package main\n\ntype Box struct{}\n\nfunc (b Box) Main() int { return 1 }\n")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	runs := map[string]func() int{
		"search regex error": func() int { return cmdSearch([]string{"[unclosed", "--regex", "--dir", dir, "--format", "text"}) },
		"search rollup":      func() int { return cmdSearch([]string{"Main", "--rollup-only", "--dir", dir, "--format", "text"}) },
		"search no match":    func() int { return cmdSearch([]string{"zzzNoSuch", "--dir", dir, "--format", "text"}) },
		"query symbols": func() int {
			return cmdQuery([]string{"--terms", "Main", "--delivery", "symbols", dir, "--format", "text"})
		},
		"query no match":      func() int { return cmdQuery([]string{"--terms", "zzzNoSuch", dir, "--format", "text"}) },
		"change-impact":       func() int { return cmdChangeImpact([]string{"Box.Main", dir, "--format", "text"}) },
		"change-impact ambig": func() int { return cmdChangeImpact([]string{"Main", dir, "--format", "text"}) },
		"rename-plan":         func() int { return cmdRenamePlan([]string{"Box.Main", "Run", dir, "--format", "text"}) },
		"missing-impls":       func() int { return cmdMissingImplementations([]string{"Box.Main", dir, "--format", "text"}) },
		"lookup":              func() int { return cmdLookup([]string{"Main", dir, "--format", "text"}) },
		"lookup missing":      func() int { return cmdLookup([]string{"zzzNoSuch", dir, "--format", "text"}) },
		"resolve":             func() int { return cmdResolve([]string{"Main", dir, "--format", "text"}) },
		"edges":               func() int { return cmdEdges([]string{"Main", dir, "--format", "text"}) },
		"references":          func() int { return cmdReferences([]string{"Main", dir, "--format", "text"}) },
		"node":                func() int { return cmdNode([]string{"Main", dir, "--format", "text"}) },
		"dead-code":           func() int { return cmdDeadCode([]string{dir, "--format", "text"}) },
	}
	for name, run := range runs {
		out := captureStdout(func() { _ = captureStderr(func() { run() }) })
		trimmed := strings.TrimSpace(out)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			t.Errorf("%s printed JSON with --format text:\n%.300s", name, out)
		}
	}
}

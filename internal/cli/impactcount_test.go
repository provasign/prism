package cli

import (
	"regexp"
	"testing"
)

// BUG-19: the header count and the editSites count are the same number.
func TestChangeImpactHeaderMatchesEditSites(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, dir, "go.mod", "module example.com/cnt\n\ngo 1.22\n")
	writeCLIFile(t, dir, "a.go", "package cnt\n\nfunc Target() int { return 1 }\n\nfunc Twice() int {\n\treturn Target() + Target()\n}\n\nfunc Once() int {\n\treturn Target()\n}\n")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	var rc int
	out := captureStdout(func() { rc = cmdChangeImpact([]string{"Target", dir, "--format", "text"}) })
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	head := regexp.MustCompile(`change-impact: (\d+) edit site\(s\)`).FindStringSubmatch(out)
	edits := regexp.MustCompile(`editSites \((\d+)`).FindStringSubmatch(out)
	if head == nil || edits == nil || head[1] != edits[1] {
		t.Fatalf("header %v vs editSites %v:\n%s", head, edits, out)
	}
}

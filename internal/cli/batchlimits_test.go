package cli

import (
	"strings"
	"testing"
)

// query rejects what the compact MCP gateway rejects: more than 10 terms.
// search stays uncapped (TestSearchCLIDoesNotInheritCompactListCaps).
func TestQueryRejectsMoreThanTenTerms(t *testing.T) {
	terms := strings.Split("a,b,c,d,e,f,g,h,i,j,k", ",")
	if rc := cmdQuery([]string{"--terms", strings.Join(terms, ","), t.TempDir()}); rc != 2 {
		t.Errorf("query with 11 terms rc=%d, want 2", rc)
	}
}

// Several paths are several files (the gateway's ranges read), not "the last
// one is the directory".
func TestReadSeveralFiles(t *testing.T) {
	dir := setupCLIProject(t)
	writeCLIFile(t, dir, "a.go", "package main\n\nfunc alpha() {}\n")
	writeCLIFile(t, dir, "b.go", "package main\n\nfunc beta() {}\n")
	out := captureStdout(func() {
		if rc := cmdRead([]string{"a.go", "b.go", "--dir", dir}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "func alpha()") || !strings.Contains(out, "func beta()") {
		t.Fatalf("both files not delivered:\n%s", out)
	}
	if rc := cmdRead([]string{"a.go", "b.go", "--from", "1", "--to", "2", "--dir", dir}); rc != 2 {
		t.Errorf("line range with several files rc=%d, want 2", rc)
	}
}

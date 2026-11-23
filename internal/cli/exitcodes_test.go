package cli

import (
	"errors"
	"strings"
	"testing"
)

// CON-04: one exit-code table. Not found is 4, ambiguous 5, a tool failure
// 70; search and query stay 0 on no match.
func TestExitCodes(t *testing.T) {
	dir := setupCLIProject(t)
	writeCLIFile(t, dir, "go.mod", "module example.com/ex\n\ngo 1.22\n")
	writeCLIFile(t, dir, "two.go", "package main\n\ntype A struct{}\ntype B struct{}\n\nfunc (A) Run() {}\nfunc (B) Run() {}\n")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"lookup", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"lookup", "Main", dir}, exitOK},
		{[]string{"node", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"resolve", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"edges", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"edges", "Main", dir}, exitOK},
		{[]string{"references", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"read", "no/such.go", "--dir", dir}, exitNotFound},
		{[]string{"change-impact", "zzzNoSuch", dir}, exitNotFound},
		{[]string{"change-impact", "Run", dir}, exitAmbiguous},
		{[]string{"search", "zzzNoSuch", "--dir", dir}, exitOK},
		{[]string{"query", "--terms", "zzzNoSuch", dir}, exitOK},
		{[]string{"help", "exit-codes"}, exitOK},
	}
	for _, c := range cases {
		var rc int
		_ = captureStderr(func() { _ = captureStdout(func() { rc = Run(c.args) }) })
		if rc != c.want {
			t.Errorf("%v: exit %d, want %d", c.args, rc, c.want)
		}
	}
	if exitForError(errors.New("boom")) != exitInternal {
		t.Error("an unexplained tool error is 70")
	}
	if !strings.Contains(exitCodesHelp, "4   not found") {
		t.Error("exit-codes help lists 4")
	}
}

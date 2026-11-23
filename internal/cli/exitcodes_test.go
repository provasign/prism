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

// CON-10: a batch whose only item failed takes that error's code.
func TestExitForAnswerBatchItemError(t *testing.T) {
	usage := map[string]any{"results": []any{map[string]any{"name": "a.go", "error": "a.go is a file, not a symbol: ..."}}}
	if rc := exitForAnswer("lookup", usage); rc != exitUsage {
		t.Errorf("file as name: rc=%d", rc)
	}
	mixed := map[string]any{"results": []any{
		map[string]any{"name": "a.go", "error": "a.go is a file, not a symbol"},
		map[string]any{"name": "B", "result": map[string]any{"matched": true}},
	}}
	if rc := exitForAnswer("lookup", mixed); rc != exitOK {
		t.Errorf("one answered: rc=%d", rc)
	}
	if rc := exitForError(errors.New(`missing-implementations: type "X" is not indexed`)); rc != exitNotFound {
		t.Errorf("not indexed: rc=%d", rc)
	}
}

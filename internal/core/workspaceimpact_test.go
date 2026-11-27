package core

import (
	"os"
	"path/filepath"
	"testing"
)

// mayReference decides whether a linked repository is searched at all. A
// package inside a module with a short path (two segments, as vanity and
// company domains have) was skipped as "not required".
func TestMayReferenceMatchesModulesOfAnyLength(t *testing.T) {
	root := t.TempDir()
	gomod := `module example.com/app

go 1.22

require (
	go.uber.org/zap v1.27.0 // indirect
	k8s.io/client-go v0.31.0
	github.com/acme/payments v1.4.0
)

require example.com/lib v0.1.0

replace example.com/lib => ../lib
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]bool{
		"go:example.com/lib#F":                        true,
		"go:example.com/lib/store#Put":                true, // the bug: a subpackage of a two-segment module
		"go:go.uber.org/zap/zapcore#Core":             true,
		"go:k8s.io/client-go/kubernetes#NewForConfig": true,
		"go:github.com/acme/payments/api#Charge":      true,
		"go:github.com/acme/payments2/api#Charge":     false, // a different module sharing a prefix
		"go:example.com/library#F":                    false,
		"go:github.com/other/repo#F":                  false,
	} {
		if got := mayReference(root, []string{key}); got != want {
			t.Errorf("mayReference(%s) = %v, want %v", key, got, want)
		}
	}
}

func TestGoModPaths(t *testing.T) {
	got := goModPaths("module example.com/app\n\ngo 1.22\ntoolchain go1.22.4\n\nrequire (\n\tk8s.io/api v0.31.0 // indirect\n)\nreplace example.com/lib v0.1.0 => ../lib\nreplace old.org/x => new.org/x v1.0.0\n")
	want := map[string]bool{"example.com/app": true, "k8s.io/api": true, "example.com/lib": true, "old.org/x": true, "new.org/x": true}
	for _, p := range got {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("missing %v in %v", want, got)
	}
	for _, p := range got {
		if p == "../lib" || p == "v0.31.0" {
			t.Fatalf("a local path or version read as a module: %v", got)
		}
	}
}

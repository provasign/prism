package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestWatchTreeFollowsIndexRules(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, root, ".gitignore", "out/\n*.log\n")
	writeCLIFile(t, root, "src/a.go", "package src\n")
	writeCLIFile(t, root, "src/.gitignore", "gen/\n")
	writeCLIFile(t, root, "src/gen/g.go", "package gen\n")
	writeCLIFile(t, root, "build/b.go", "package build\n") // indexed, so watched
	writeCLIFile(t, root, "out/o.go", "package out\n")
	writeCLIFile(t, root, "node_modules/x/i.js", "x\n")
	writeCLIFile(t, root, ".tool/state.go", "package tool\n")

	var added []string
	tree := newWatchTree(root, func(p string) error {
		rel, _ := filepath.Rel(root, p)
		added = append(added, filepath.ToSlash(rel))
		return nil
	})
	tree.addTree(root)
	sort.Strings(added)
	if got := strings.Join(added, ","); got != ".,build,src" {
		t.Fatalf("watched %s, want .,build,src", got)
	}
	j := func(p string) string { return filepath.Join(root, p) }
	if !tree.relevant(j("src/a.go")) || tree.relevant(j("src/x.log")) || tree.relevant(j(".tool/state.go")) || tree.relevant(j(".grove/grove.db")) {
		t.Fatal("event filter disagrees with the index rules")
	}

	// A directory created later under an ignored one is not watched; one
	// under src is.
	writeCLIFile(t, root, "src/gen/deep/d.go", "package deep\n")
	writeCLIFile(t, root, "src/newpkg/n.go", "package newpkg\n")
	if tree.addTree(j("src/gen/deep")) {
		t.Fatal("watched a directory under an ignored one")
	}
	if !tree.addTree(j("src/newpkg")) {
		t.Fatal("new package not watched")
	}
}

func TestWatchTreeReportsFailedWatches(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, root, "a/x.go", "package a\n")
	writeCLIFile(t, root, "b/x.go", "package b\n")
	tree := newWatchTree(root, func(p string) error {
		if filepath.Base(p) == "b" {
			return errors.New("too many open files")
		}
		return nil
	})
	tree.addTree(root)
	var out bytes.Buffer
	tree.warnFailures(&out)
	tree.warnFailures(&out)
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "watching 2 of 3 directories") || !strings.Contains(got, "ulimit -n") {
		t.Fatalf("warning = %q", got)
	}
}

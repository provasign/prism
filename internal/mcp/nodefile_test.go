package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// The node file view must work for a file the extractor produces no symbols
// for — configs, markdown, fixtures, unsupported languages. Its content and
// dependents are exactly what the view is for, and neither needs the file to
// define anything; requiring indexed symbols sent every such path to the
// symbol branch, which answered "no symbol or indexed file named X" about a
// file sitting right there.
func TestNodeFileBranchAcceptsSymbollessFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[a]\nb = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Root: root}
	if !h.fileExists("config.toml") {
		t.Fatal("fileExists said no to a regular file in the root")
	}
}

// prism_node is a read tool. A read tool that opens ../../.ssh/id_rsa on
// request is an exfiltration primitive, so the file branch must refuse any
// path that leaves the root — absolute or traversing.
func TestNodeFileBranchRefusesEscapingPaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fileExists is the whole gate for the file branch: toolNode only takes
	// that branch when it returns true, so confining it confines the branch.
	h := &Handler{Root: root}
	for _, p := range []string{outside, "../secret", "sub/../../secret", ""} {
		if h.fileExists(p) {
			t.Errorf("fileExists accepted out-of-root path %q", p)
		}
	}
}

package textsearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestNativeSearchDoesNotFollowSymlinksOutsideRoot: a repository symlink to a
// file outside the search root must not expose that file's contents, while
// in-root symlinks and regular files keep working.
func TestNativeSearchDoesNotFollowSymlinksOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	root := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("hunter2-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("hunter2-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "leak.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "inside.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}

	res := nativeSearch(context.Background(), root, "hunter2-marker", Options{MaxHits: 50, MaxPerFile: 10})
	seen := map[string]bool{}
	for _, h := range res.Hits {
		seen[h.File] = true
	}
	if seen["leak.txt"] {
		t.Fatalf("out-of-root symlink content exposed: %+v", res.Hits)
	}
	if !seen["inside.txt"] {
		t.Fatalf("regular in-root file missing from results: %+v", res.Hits)
	}
	if !seen["alias.txt"] {
		t.Fatalf("in-root symlink was wrongly rejected: %+v", res.Hits)
	}
}

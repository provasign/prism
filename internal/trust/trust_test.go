package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustPrecedenceAndRepoCannotGrantItself(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PRISM_UNTRUSTED", "")
	SetOverride("")
	root := t.TempDir()
	// A hostile repository claims trust in its own config; it is never read.
	if err := os.WriteFile(filepath.Join(root, "prism.yaml"), []byte("trusted: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := For(root); d.Untrusted || d.Source != "default" {
		t.Fatalf("default: %v", d)
	}
	if err := Set(root, Untrusted); err != nil {
		t.Fatal(err)
	}
	if d := For(root); !d.Untrusted || d.Source != "trust file" {
		t.Fatalf("after untrust: %v", d)
	}
	SetOverride(Trusted)
	if d := For(root); d.Untrusted {
		t.Fatalf("--trusted must override the file: %v", d)
	}
	SetOverride("")
	if err := Set(root, Trusted); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISM_UNTRUSTED", "1")
	if d := For(root); !d.Untrusted || d.Source != "PRISM_UNTRUSTED" {
		t.Fatalf("env must override the file: %v", d)
	}
	if fi, err := os.Stat(Path()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("trust file mode: %v %v", fi, err)
	}
}

func TestNoticeOnceAndTempRootsNotRecorded(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PRISM_UNTRUSTED", "")
	SetOverride("")
	// A root outside the temp directory is recorded after the first note.
	home, _ := os.UserHomeDir()
	real := filepath.Join(home, ".prism-trust-test-root-does-not-need-to-exist")
	if n := NoticeOnce(real); !strings.Contains(n, "prism untrust") {
		t.Fatalf("first notice: %q", n)
	}
	if n := NoticeOnce(real); n != "" {
		t.Fatalf("second notice: %q", n)
	}
	tmp := t.TempDir()
	if NoticeOnce(tmp) == "" || NoticeOnce(tmp) == "" {
		t.Fatal("temp roots are not recorded, so the note repeats")
	}
	for _, r := range Roots() {
		if strings.HasPrefix(r[0], filepath.Clean(os.TempDir())) {
			t.Fatalf("temp root recorded: %v", r)
		}
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_Help(t *testing.T) {
	if Run([]string{}) != 0 {
		t.Error("no args")
	}
	if Run([]string{"--help"}) != 0 {
		t.Error("--help")
	}
	if Run([]string{"-h"}) != 0 {
		t.Error("-h")
	}
	if Run([]string{"help"}) != 0 {
		t.Error("help")
	}
	if Run([]string{"version"}) != 0 {
		t.Error("version")
	}
	if Run([]string{"nonsense-cmd"}) != 2 {
		t.Error("unknown")
	}
}

func TestMustAbs(t *testing.T) {
	absIn := filepath.Join(t.TempDir(), "x")
	if got := mustAbs(absIn); got != absIn {
		t.Errorf("abs: got %q want %q", got, absIn)
	}
	if !filepath.IsAbs(mustAbs("rel")) {
		t.Error("rel→abs")
	}
}

func TestDirArg(t *testing.T) {
	if dirArg([]string{"a", "b"}, 0, "def") != "a" {
		t.Error("get")
	}
	if dirArg([]string{"-flag"}, 0, "def") != "def" {
		t.Error("flag skipped")
	}
	if dirArg([]string{}, 0, "def") != "def" {
		t.Error("empty")
	}
}

func TestPrintJSON(t *testing.T) {
	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printJSON(map[string]int{"a": 1})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	var m map[string]int
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["a"] != 1 {
		t.Errorf("got %+v", m)
	}
}

func TestLedgerPathForRoot(t *testing.T) {
	p := ledgerPathForRoot("/x/y/z")
	if !strings.Contains(p, "prism") {
		t.Errorf("got %s", p)
	}
	if !strings.HasSuffix(p, ".json") {
		t.Errorf("got %s", p)
	}
}

func TestPruneOldLedgers_Cov(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	_ = os.WriteFile(old, []byte("{}"), 0o644)
	past := time.Now().Add(-60 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)

	fresh := filepath.Join(dir, "fresh.json")
	_ = os.WriteFile(fresh, []byte("{}"), 0o644)

	// other files ignored
	_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)

	pruneOldLedgers(dir, 30*24*time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old not pruned")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh pruned")
	}
}

func TestPruneOldLedgers_BadDir_Cov(t *testing.T) {
	pruneOldLedgers("/nonexistent/path/xxxx", time.Hour)
}

func TestCmdConfig(t *testing.T) {
	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := cmdConfig([]string{})
	_ = w.Close()
	os.Stdout = old
	_, _ = io.Copy(io.Discard, r)
	if rc != 0 {
		t.Errorf("rc %d", rc)
	}
}

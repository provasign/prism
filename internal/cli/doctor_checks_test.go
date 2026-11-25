package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorIntegrity(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, dir, "go.mod", "module example.com/d\n\ngo 1.22\n")
	writeCLIFile(t, dir, "a.go", "package d\n\nfunc A() {}\n\nfunc B() { A() }\n")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	doctor := func() (int, map[string]any) {
		var rc int
		out := captureStdout(func() { _ = captureStderr(func() { rc = cmdDoctor([]string{dir, "--integrity", "--format", "json"}) }) })
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("doctor output is not JSON: %v\n%s", err, out)
		}
		return rc, m
	}
	rc, m := doctor()
	integrity, _ := m["integrity"].(map[string]any)
	if rc != 0 || integrity["status"] != "ok" {
		t.Fatalf("fresh index: rc=%d integrity=%v", rc, m["integrity"])
	}

	db := filepath.Join(dir, ".grove", "grove.db")
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096; i < len(data); i++ {
		data[i] = 0xA5
	}
	if err := os.WriteFile(db, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(db + "-wal")
	_ = os.Remove(db + "-shm")
	rc, m = doctor()
	integrity, _ = m["integrity"].(map[string]any)
	if integrity == nil || integrity["status"] == "ok" {
		t.Fatalf("corrupt index reported as sound: rc=%d %v", rc, m)
	}
	if !strings.Contains(strings.Join(warningsOf(m), "\n"), "prism index --rebuild") {
		t.Fatalf("no repair hint in warnings: %v", m["warnings"])
	}
}

func TestLargeUntrackedWarnings(t *testing.T) {
	old := largeUntrackedLimit
	largeUntrackedLimit = 1000
	t.Cleanup(func() { largeUntrackedLimit = old })

	dir := t.TempDir()
	writeCLIFile(t, dir, "a.go", "package d\n")
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "base")
	big := strings.Repeat("x", 2000)
	writeCLIFile(t, dir, "runs/out/a.txt", big)
	writeCLIFile(t, dir, "small/a.txt", "x")
	writeCLIFile(t, dir, "ignored/a.txt", big)
	writeCLIFile(t, dir, "mixed/keep.md", "x")
	writeCLIFile(t, dir, "mixed/results/a.txt", big)
	writeCLIFile(t, dir, ".gitignore", "ignored/\nmixed/results/\n")

	got := largeUntrackedWarnings(dir)
	if len(got) != 1 || !strings.HasPrefix(got[0], "runs ") {
		t.Fatalf("warnings = %q, want only runs", got)
	}
	if w := largeUntrackedWarnings(t.TempDir()); w != nil {
		t.Fatalf("outside git: %q", w)
	}
}

func warningsOf(m map[string]any) []string {
	var out []string
	ws, _ := m["warnings"].([]any)
	for _, w := range ws {
		s, _ := w.(string)
		out = append(out, s)
	}
	return out
}

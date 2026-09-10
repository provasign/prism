package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A current server must stay quiet; replacement behavior is covered below.
func TestStaleBinaryNoteQuietWhenCurrent(t *testing.T) {
	staleBinaryWarned = false
	if n := staleBinaryNote(); n != "" {
		t.Errorf("current binary must produce no note, got %q", n)
	}
}

func TestBinarySnapshotChangedAfterAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prism")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := binarySnapshot{path: path, info: info}

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if !binarySnapshotChanged(snapshot) {
		t.Fatal("atomic binary replacement must mark the running server stale")
	}
}

func TestStaleBinaryNoteFiresOnceAfterReplacement(t *testing.T) {
	originalSnapshot := startupBinarySnapshot
	originalWarned := staleBinaryWarned
	t.Cleanup(func() {
		startupBinarySnapshot = originalSnapshot
		staleBinaryWarned = originalWarned
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "prism")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	startupBinarySnapshot = binarySnapshot{path: path, info: info}
	staleBinaryWarned = false

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	if note := staleBinaryNote(); !strings.Contains(note, "stale server is exiting") {
		t.Fatalf("stale server note did not explain retirement: %q", note)
	}
	if note := staleBinaryNote(); note != "" {
		t.Fatalf("stale server note must be emitted once, got %q", note)
	}
}

func TestServerRetiresBeforeDispatchAfterBinaryReplacement(t *testing.T) {
	originalSnapshot := startupBinarySnapshot
	originalWarned := staleBinaryWarned
	t.Cleanup(func() {
		startupBinarySnapshot = originalSnapshot
		staleBinaryWarned = originalWarned
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "prism")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	startupBinarySnapshot = binarySnapshot{path: path, info: info}
	staleBinaryWarned = false

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n")
	var output bytes.Buffer
	if err := NewServer(nil).Serve(input, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, `"code":-32001`) || !strings.Contains(got, `"id":1`) {
		t.Fatalf("stale server did not return the retirement error: %s", got)
	}
	if strings.Contains(got, `"id":2`) {
		t.Fatalf("stale server dispatched a second request instead of exiting: %s", got)
	}
}

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
	snapshot := snapshotBinary(path)
	if snapshot.info == nil {
		t.Fatal("failed to snapshot original binary")
	}

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
	startupBinarySnapshot = snapshotBinary(path)
	if startupBinarySnapshot.info == nil {
		t.Fatal("failed to snapshot original binary")
	}
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
	startupBinarySnapshot = snapshotBinary(path)
	if startupBinarySnapshot.info == nil {
		t.Fatal("failed to snapshot original binary")
	}
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

// TestHandoffHelperProcess is the "upgraded binary" for the handoff test: the
// test executable re-run as a plain MCP server.
func TestHandoffHelperProcess(t *testing.T) {
	if os.Getenv("PRISM_HANDOFF_HELPER") != "1" {
		return
	}
	startupBinarySnapshot = binarySnapshot{}
	_ = NewServer(&Handler{}).Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}

func TestServerHandsOffToUpgradedBinary(t *testing.T) {
	originalSnapshot := startupBinarySnapshot
	originalWarned := staleBinaryWarned
	originalArgs := handoffArgs
	t.Cleanup(func() {
		startupBinarySnapshot = originalSnapshot
		staleBinaryWarned = originalWarned
		handoffArgs = originalArgs
	})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISM_HANDOFF_HELPER", "1")
	handoffArgs = func() []string { return []string{"-test.run=^TestHandoffHelperProcess$"} }

	// The launch path is a symlink, as with Homebrew; the upgrade repoints it.
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "old")
	if err := os.WriteFile(oldBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(dir, "prism")
	if err := os.Symlink(oldBin, launch); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	startupBinarySnapshot = snapshotBinary(launch)
	staleBinaryWarned = false
	next := filepath.Join(dir, "next")
	if err := os.Symlink(self, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, launch); err != nil {
		t.Fatal(err)
	}

	handedOff := false
	srv := NewServer(nil)
	srv.OnHandoff = func() { handedOff = true }
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n")
	var output bytes.Buffer
	if err := srv.Serve(input, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !handedOff {
		t.Error("OnHandoff was not called")
	}
	if strings.Contains(got, `-32001`) || strings.Contains(got, handoffInitID) {
		t.Fatalf("client saw the retirement error or the replayed handshake: %s", got)
	}
	if !strings.Contains(got, `{"id":1,"jsonrpc":"2.0","result"`) || !strings.Contains(got, `{"id":2,"jsonrpc":"2.0","result"`) {
		t.Fatalf("upgraded server did not answer both requests: %s", got)
	}
}

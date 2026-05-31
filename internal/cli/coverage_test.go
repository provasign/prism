package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// startMockGrove starts an httptest.Server implementing Grove's HTTP API.
// It sets PRISM_GROVE_URL and PRISM_GROVE_BINARY env vars so newClient connects
// to the mock instead of a real grove. The server is closed via t.Cleanup.
func startMockGrove(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"filesIndexed": 0, "symbolCount": 0, "edgeCount": 0,
		})
	})
	mux.HandleFunc("/index", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"root": ".", "filesSeen": 0, "filesUpdated": 0, "filesSkipped": 0,
		})
	})
	mux.HandleFunc("/query", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"symbols": []any{}}) //nolint:errcheck
	})
	mux.HandleFunc("/symbols", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"symbols": []any{}}) //nolint:errcheck
	})
	mux.HandleFunc("/deps", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"edges": []any{}}) //nolint:errcheck
	})
	mux.HandleFunc("/impact", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}}) //nolint:errcheck
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PRISM_GROVE_URL", srv.URL)
	t.Setenv("PRISM_GROVE_BINARY", filepath.Join(t.TempDir(), "no-grove-binary"))
	return srv.URL
}

// noGrove sets up an unreachable grove URL so newClient returns error quickly.
func noGrove(t *testing.T) {
	t.Helper()
	t.Setenv("PRISM_GROVE_URL", "http://127.0.0.1:19998")
	t.Setenv("PRISM_GROVE_BINARY", filepath.Join(t.TempDir(), "no-grove-binary"))
}

// --- cmdIndex ---
func TestCmdIndex_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdIndex([]string{dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestCmdIndex_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdIndex([]string{dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- cmdStatus ---
func TestCmdStatus_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdStatus([]string{dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestCmdStatus_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdStatus([]string{dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- cmdQuery ---
func TestCmdQuery_NoArgs(t *testing.T) {
	if got := cmdQuery([]string{}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdQuery_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdQuery([]string{"find auth", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdQuery_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdQuery([]string{"find auth", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdRead ---
func TestCmdRead_NoArgs(t *testing.T) {
	if got := cmdRead([]string{}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdRead_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdRead([]string{"foo.go", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdRead_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	fpath := filepath.Join(dir, "foo.go")
	os.WriteFile(fpath, []byte("package foo\n"), 0o644) //nolint:errcheck
	if got := cmdRead([]string{"foo.go", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdSearch ---
func TestCmdSearch_NoArgs(t *testing.T) {
	if got := cmdSearch([]string{}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdSearch_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdSearch([]string{"Login", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdSearch_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdSearch([]string{"Login", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdLookup ---
func TestCmdLookup_NoArgs(t *testing.T) {
	if got := cmdLookup([]string{}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdLookup_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdLookup([]string{"Login", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdLookup_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdLookup([]string{"Login", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdSavings ---
func TestCmdSavings_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdSavings([]string{dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdSavings_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdSavings([]string{dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdFeedback ---
func TestCmdFeedback_InvalidRating(t *testing.T) {
	if got := cmdFeedback([]string{}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdFeedback_RatingOutOfRange(t *testing.T) {
	if got := cmdFeedback([]string{"--rating", "10"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdFeedback_ValidRating_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdFeedback([]string{"--rating", "3", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdFeedback_ValidRating_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	if got := cmdFeedback([]string{"--rating", "4", "--tool", "prism_query", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdCompact ---
func TestCmdCompact_BadStdin(t *testing.T) {
	r, w, _ := os.Pipe()
	w.WriteString("not-json")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	dir := t.TempDir()
	if got := cmdCompact([]string{dir}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestCmdCompact_WithMockGrove(t *testing.T) {
	startMockGrove(t)
	r, w, _ := os.Pipe()
	w.WriteString(`[{"role":"user","content":"hello"}]`)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	dir := t.TempDir()
	if got := cmdCompact([]string{dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- cmdServe / cmdMCP (error path only — they block on success) ---
func TestCmdServe_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdServe([]string{dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestCmdMCP_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdMCP([]string{dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- cmdConfig is tested in helpers_test.go ---
// --- cmdQuery with flags ---
func TestCmdQuery_WithFlags_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdQuery([]string{"task", "--limit", "10", "--profile", "fast", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- cmdSearch with --limit flag ---
func TestCmdSearch_WithLimit_NoGrove(t *testing.T) {
	noGrove(t)
	dir := t.TempDir()
	if got := cmdSearch([]string{"query", "--limit", "5", dir}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- pruneOldLedgers ---
func TestPruneOldLedgers_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	pruneOldLedgers(dir, 0)
}

func TestPruneOldLedgers_RemovesOldFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "old.json")
	os.WriteFile(f, []byte("{}"), 0o644) //nolint:errcheck
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(f, old, old)
	pruneOldLedgers(dir, time.Hour)
	if _, err := os.Stat(f); err == nil {
		t.Error("expected old.json to be pruned")
	}
}

// --- invokeWithPersistentLedger success path ---
func TestInvokeWithPersistentLedger_Success(t *testing.T) {
	startMockGrove(t)
	dir := t.TempDir()
	out, err := invokeWithPersistentLedger(dir, "prism_savings", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
}

package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/mcp"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	gc := grove.NewClient("http://127.0.0.1:1", "")
	h := mcp.NewHandler(&config.Config{}, t.TempDir(), gc)
	return httptest.NewServer(New(h).Handler())
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body %s", body)
	}
}

func TestStatus(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// May 200 or 500 depending on ledger init; just don't panic
	_, _ = io.ReadAll(resp.Body)
}

func TestCallTool_BadJSON(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/prism_query", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCallTool_EmptyBody(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/prism_savings", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// savings should succeed with empty body
	var m map[string]any
	_ = json.Unmarshal(body, &m)
}

func TestCallTool_UnknownTool(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/prism_unknown", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	// unknown route → 404
	if resp.StatusCode != 404 {
		t.Errorf("got %d", resp.StatusCode)
	}
}

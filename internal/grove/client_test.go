package grove

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFake(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestClient_AllEndpoints(t *testing.T) {
	srv := newFake(t, 200, map[string]any{
		"status":  "ok",
		"symbols": []map[string]any{{"id": "s1"}},
		"edges":   []map[string]any{{"from": "a", "to": "b"}},
		"nodes":   []map[string]any{{"id": "n1"}},
		"results": []map[string]any{{"id": "r1", "score": 0.9}},
		"tests":   []map[string]any{{"id": "t1"}},
	})
	defer srv.Close()
	c := NewClient(srv.URL, "/bin/true")
	ctx := context.Background()
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Error(err)
	}
	if _, err := c.Index(ctx, "/x"); err != nil {
		t.Error(err)
	}
	if _, err := c.QueryByIntent(ctx, "q", 5); err != nil {
		t.Error(err)
	}
	if _, err := c.SearchSymbols(ctx, "x", 5); err != nil {
		t.Error(err)
	}
	if _, err := c.Deps(ctx, "f.go"); err != nil {
		t.Error(err)
	}
	if _, err := c.Impact(ctx, "x", 3); err != nil {
		t.Error(err)
	}
	if _, err := c.Semantic(ctx, "q", 5); err != nil {
		t.Error(err)
	}
	if _, err := c.Tests(ctx, "q"); err != nil {
		t.Error(err)
	}
	if c.BaseURL() != srv.URL {
		t.Error("BaseURL")
	}
}

func TestClient_HealthError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "")
	if err := c.Health(context.Background()); err == nil {
		t.Error("expected err")
	}
}

func TestClient_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	if err := c.Health(context.Background()); err == nil {
		t.Error("health 500")
	}
	if _, err := c.Status(context.Background()); err == nil {
		t.Error("status 500")
	}
	if _, err := c.Index(context.Background(), "/x"); err == nil {
		t.Error("index 500")
	}
}

func TestClient_TokenAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".grove"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".grove", ".token"), []byte("secret123\n"), 0o600)
	c := NewClient(srv.URL, "").WithTokenFromDir(root)
	_, _ = c.Status(context.Background())
	if !strings.Contains(gotAuth, "Bearer secret123") {
		t.Errorf("auth header %q", gotAuth)
	}
}

func TestClient_WithTokenFromDir_MissingFile(t *testing.T) {
	c := NewClient("http://x", "").WithTokenFromDir(t.TempDir())
	if c == nil {
		t.Error("nil")
	}
}

func TestPortFromURL(t *testing.T) {
	if got := portFromURL("http://x:8888"); got != "8888" {
		t.Errorf("got %q", got)
	}
	if got := portFromURL("http://x"); got != "7777" {
		t.Errorf("default: %q", got)
	}
	if got := portFromURL("::::bad"); got != "7777" {
		t.Errorf("bad: %q", got)
	}
}

func TestEnsureRunning_AlreadyHealthy(t *testing.T) {
	srv := newFake(t, 200, map[string]string{})
	defer srv.Close()
	c := NewClient(srv.URL, "/bin/true")
	if err := c.EnsureRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRunning_AutoStartDisabled(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "/bin/true")
	c.autoStart = false
	if err := c.EnsureRunning(context.Background()); err == nil {
		t.Error("expected err")
	}
}

func TestEnsureRunning_StartFails(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "/nonexistent/binary")
	if err := c.EnsureRunning(context.Background()); err == nil {
		t.Error("expected err")
	}
}

func TestShutdown_NoPid(t *testing.T) {
	c := NewClient("http://x", "")
	c.Shutdown() // no-op
}

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestSearchEvidenceShowsLocalRelatedUseAndSkippedTest(t *testing.T) {
	root := t.TempDir()
	for file, body := range map[string]string{
		"src/config.py":        "class Config:\n    def __init__(self, tls_context):\n        self.tls_context = tls_context\n",
		"src/manager.py":       "from config import Config\n\ndef create(proxy_tls_context):\n    config = Config(proxy_tls_context)\n    return config\n",
		"src/worker.py":        "from config import Config\n\nclass Worker:\n    def __init__(self, config: Config):\n        self.config = config\n        self.tls_context = None\n\n    def connect(self, sock):\n        return wrap_socket(sock, tls_context=self.tls_context)\n\n    def tunnel(self, sock):\n        config = cast(Config, self.config)\n        tls_context = config.tls_context\n        return wrap_socket(sock, tls_context=tls_context)\n",
		"src/unrelated.py":     "def unrelated(sock):\n    return wrap_socket(sock, tls_context=self.tls_context)\n",
		"tests/test_worker.py": "def test_proxy_tls_context():\n    if proxy_tls_context:\n        pytest.skip('pending')\n    config = Config(proxy_tls_context)\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Index(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	out := map[string]any{"results": []map[string]any{
		{"query": "proxy_tls_context", "textHits": []map[string]any{
			{"file": "src/manager.py", "hits": []map[string]any{{"line": 3}, {"line": 4}}},
			{"file": "tests/test_worker.py", "hits": []map[string]any{{"line": 1}, {"line": 2}, {"line": 4}}},
		}},
		{"query": "create", "textHits": []map[string]any{
			{"file": "src/manager.py", "hits": []map[string]any{{"line": 3}}},
		}},
	}}
	got := h.compactSearchBodiesEnclosing(t.Context(), out)
	for _, want := range []string{"src/manager.py", "src/worker.py", "tls_context=self.tls_context",
		"config.tls_context", "indexed reference to Config", "relationship unverified", "tests/test_worker.py", "pytest.skip"} {
		if !strings.Contains(got, want) {
			t.Fatalf("search evidence missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "src/unrelated.py") {
		t.Fatalf("related spelling widened search to an unrelated file:\n%s", got)
	}
	out["scopeNote"] = "match lists restricted to path=[\"src/manager.py\"]"
	scoped := h.compactSearchBodiesEnclosing(t.Context(), out)
	if strings.Contains(scoped, "src/worker.py") {
		t.Fatalf("reference expansion crossed a requested scope:\n%s", scoped)
	}
}

func TestEvidenceLineScorePrefersUseOverDeclarationAndComment(t *testing.T) {
	if evidenceLineScore("ssl_context=self.ssl_context,") <= evidenceLineScore("ssl_context: ssl.SSLContext | None = None,") ||
		evidenceLineScore("and proxy_config.use_forwarding_for_https") <= evidenceLineScore("# use_forwarding_for_https") {
		t.Fatal("executable use must rank above declarations and comments")
	}
}

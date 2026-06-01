package grove

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Client is a thin HTTP wrapper around Grove's REST endpoints.
type Client struct {
	baseURL    string
	http       *http.Client
	token      string // shared-secret read from .grove/.token
	groveBin   string // path to grove binary for auto-start
	root       string // project root; passed to grove serve so token lands in <root>/.grove/.token
	autoStart  bool
	startedPid int
}

// NewClient creates a Grove client. baseURL is the form "http://host:port".
func NewClient(baseURL, groveBin string) *Client {
	return &Client{
		baseURL:   baseURL,
		http:      &http.Client{Timeout: 30 * time.Second},
		groveBin:  groveBin,
		autoStart: true,
	}
}

// WithTokenFromDir loads the shared-secret token from <root>/.grove/.token and
// attaches it to the client. All subsequent requests carry
// "Authorization: Bearer <token>". Also stores root so EnsureRunning can pass
// it to `grove serve`, ensuring the token is created in the right place even
// when the process working directory differs (e.g. when Claude Code spawns prism).
// Safe to call even if the file doesn't exist yet (e.g. before the first grove serve run).
func (c *Client) WithTokenFromDir(root string) *Client {
	c.root = root
	path := filepath.Join(root, ".grove", ".token")
	data, err := os.ReadFile(path)
	if err == nil {
		c.token = strings.TrimSpace(string(data))
	}
	return c
}

// BaseURL returns the configured Grove base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// Health returns nil if Grove is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grove /health: %d", resp.StatusCode)
	}
	return nil
}

// EnsureRunning probes /health; if unreachable, exec the grove binary and
// wait up to 10s for it to become healthy.
func (c *Client) EnsureRunning(ctx context.Context) error {
	if err := c.Health(ctx); err == nil {
		return nil
	}
	if !c.autoStart {
		return errors.New("grove not reachable and auto-start disabled")
	}
	port := portFromURL(c.baseURL)
	// Pass the project root so grove writes its token to <root>/.grove/.token.
	// Without this, grove defaults to cwd which may not be the project root when
	// spawned indirectly (e.g. Claude Code spawning the prism MCP server).
	args := []string{"serve", "--port", port}
	if c.root != "" {
		args = append(args, c.root)
	}
	cmd := exec.Command(c.groveBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start grove: %w", err)
	}
	go func() { _ = cmd.Wait() }() // reap child process to prevent zombie on exit
	c.startedPid = cmd.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(ctx); err == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("grove failed to become healthy within 10s")
}

// Shutdown kills the Grove subprocess if Prism started it.
func (c *Client) Shutdown() {
	if c.startedPid > 0 {
		_ = killProcess(c.startedPid)
	}
}

func killProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// Status calls GET /status.
func (c *Client) Status(ctx context.Context) (*StatusResult, error) {
	var out StatusResult
	if err := c.get(ctx, "/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Index calls POST /index.
func (c *Client) Index(ctx context.Context, dir string) (*IndexResult, error) {
	var out IndexResult
	if err := c.post(ctx, "/index", map[string]string{"dir": dir}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryByIntent calls POST /query.
func (c *Client) QueryByIntent(ctx context.Context, intent string, limit int) ([]SymbolRecord, error) {
	var out struct {
		Symbols []SymbolRecord `json:"symbols"`
	}
	if err := c.post(ctx, "/query", map[string]any{"intent": intent, "limit": limit}, &out); err != nil {
		return nil, err
	}
	return out.Symbols, nil
}

// SearchSymbols calls POST /symbols.
func (c *Client) SearchSymbols(ctx context.Context, query string, limit int) ([]SymbolRecord, error) {
	var out struct {
		Symbols []SymbolRecord `json:"symbols"`
	}
	if err := c.post(ctx, "/symbols", map[string]any{"query": query, "limit": limit}, &out); err != nil {
		return nil, err
	}
	return out.Symbols, nil
}

// Deps calls POST /deps for a file path.
func (c *Client) Deps(ctx context.Context, file string) ([]Edge, error) {
	var out struct {
		Edges []Edge `json:"edges"`
	}
	if err := c.post(ctx, "/deps", map[string]string{"file": file}, &out); err != nil {
		return nil, err
	}
	return out.Edges, nil
}

// Impact calls POST /impact for a symbol-or-file query.
func (c *Client) Impact(ctx context.Context, query string, maxDepth int) ([]SymbolRecord, error) {
	var out struct {
		Nodes []SymbolRecord `json:"nodes"`
	}
	if err := c.post(ctx, "/impact", map[string]any{"query": query, "maxDepth": maxDepth}, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// Semantic calls POST /semantic (Grove's TF-IDF semantic search).
func (c *Client) Semantic(ctx context.Context, query string, limit int) ([]SemanticResult, error) {
	var out struct {
		Results []SemanticResult `json:"results"`
	}
	if err := c.post(ctx, "/semantic", map[string]any{"query": query, "limit": limit}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Tests calls POST /tests.
func (c *Client) Tests(ctx context.Context, query string) ([]SymbolRecord, error) {
	var out struct {
		Tests []SymbolRecord `json:"tests"`
	}
	if err := c.post(ctx, "/tests", map[string]string{"query": query}, &out); err != nil {
		return nil, err
	}
	return out.Tests, nil
}

// --- internal helpers ---

func (c *Client) addAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grove GET %s: %d %s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) post(ctx context.Context, path string, in any, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grove POST %s: %d %s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func portFromURL(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		if p := parsed.Port(); p != "" {
			return p
		}
	}
	return "7777"
}

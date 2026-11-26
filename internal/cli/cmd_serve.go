package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/httpapi"
	"github.com/provasign/prism/internal/httpguard"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/workspace"
)

func cmdServe(args []string) int {
	port := 0 // resolved after config load: flag > prism.yaml port > 8888
	dir, dirSet := ".", false
	listen, tokenFile := "127.0.0.1", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--listen":
			v, err := flagValue("serve", args, i)
			if err != nil {
				return usageError(err)
			}
			listen = v
			i++
		case a == "--token-file":
			v, err := flagValue("serve", args, i)
			if err != nil {
				return usageError(err)
			}
			tokenFile = v
			i++
		case a == "--port":
			p, err := positiveIntFlag("serve", args, i)
			if err == nil && p > 65535 {
				err = fmt.Errorf("serve: --port must be 1-65535, got %d", p)
			}
			if err != nil {
				return usageError(err)
			}
			port = p
			i++
		case strings.HasPrefix(a, "-"):
			return rejectUnknownFlag("serve", a)
		default:
			if err := positionalDir("serve", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	// SEC-02: a non-loopback listener needs a token the operator chose and
	// can hand to its clients; the loopback default gets a fresh one.
	if !httpguard.LoopbackHost(listen) && tokenFile == "" {
		return usageError(fmt.Errorf("serve: --listen %s is not a loopback address; it requires --token-file", listen))
	}
	cfg, client, err := newClient(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	if port == 0 { // no --port flag: prism.yaml port, then the documented 8888
		port = cfg.Port
		if port == 0 {
			port = 8888
		}
	}
	h := core.NewHandler(cfg, mustAbs(dir), client)

	// Auto-index on startup so the first query has something to work with.
	if _, err := client.Index(context.Background(), mustAbs(dir)); err != nil {
		fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
	}

	chosen, err := pickPort(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "port:", err)
		return 1
	}
	port = chosen

	token, tokenPath, err := serveToken(mustAbs(dir), tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	addr := net.JoinHostPort(listen, strconv.Itoa(port))
	guard := httpguard.Options{Token: token, Open: map[string]bool{"/health": true}}
	if !httpguard.LoopbackHost(listen) {
		guard.AllowHost = func(host string) bool {
			h, _, err := net.SplitHostPort(host)
			if err != nil {
				h = host
			}
			return strings.EqualFold(strings.Trim(h, "[]"), strings.Trim(listen, "[]"))
		}
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpguard.Wrap(httpapi.New(h).Handler(), guard),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// A shell starts a background job with SIGINT ignored; Notify installs
	// a handler, so Ctrl+C / kill -INT stop the server either way (BUG-21).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	fmt.Fprintln(os.Stderr, "prism HTTP listening on", addr)
	if tokenFile == "" {
		fmt.Fprintf(os.Stderr, "token (send as \"Authorization: Bearer <token>\"): %s\n  also in %s\n", token, tokenPath)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

// serveToken reads the token from tokenFile, or generates one and writes it
// to <root>/.grove/serve.token (0600) so a local client can read it.
func serveToken(root, tokenFile string) (token, path string, err error) {
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", "", fmt.Errorf("--token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
		if len(token) < 16 {
			return "", "", fmt.Errorf("--token-file %s: token shorter than 16 characters", tokenFile)
		}
		return token, tokenFile, nil
	}
	if token, err = httpguard.NewToken(); err != nil {
		return "", "", err
	}
	path = filepath.Join(root, ".grove", "serve.token")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return token, path, nil
}

func cmdMCP(args []string) int {
	compact := true
	positional := make([]string, 0, 1)
	for _, arg := range args {
		switch {
		case arg == "--compact":
			compact = true
		case arg == "--legacy":
			compact = false
		case strings.HasPrefix(arg, "-"):
			return rejectUnknownFlag("mcp", arg)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 1 {
		fmt.Fprintln(os.Stderr, "usage: prism mcp [--compact|--legacy] [dir]")
		return 2
	}
	dir := "."
	if len(positional) == 1 {
		dir = positional[0]
	}
	root := mustAbs(dir)

	// Validate the project root up front. Without this, a bad path would block
	// in Serve (reading stdin) instead of failing fast, and the embedded Grove
	// engine would error mid-handshake.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "mcp: project root is not a directory:", root)
		return 1
	}

	// Load config and create the Grove client without connecting yet — the MCP
	// handshake (initialize / tools/list) must be serviced immediately or
	// Claude Code will time out and never load the tools.
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root))

	// Open the embedded Grove engine and run the initial index in the
	// background so the MCP handshake is serviced without waiting on I/O.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan struct{}) // closed once Grove engine is open (ready for queries)
	doneCh := make(chan struct{})  // closed once the goroutine fully exits
	go func() {
		defer close(doneCh)
		if err := client.EnsureRunning(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "warning: grove not reachable:", err)
			close(readyCh)
			return
		}
		// Ready means INDEXED, not merely open. This used to close readyCh
		// before the initial index ran, so every early tool call raced the
		// background refresh and could answer from a partial graph — measured
		// 2026-08-26: five identical `prism query` invocations on an
		// unchanged, pre-indexed worktree produced FOUR different context
		// selections (28KB with the right anchors down to 3.5KB with wrong
		// ones), because seed search hit the graph mid-mutation. Correct and
		// slow beats fast and silently wrong: the first tool call on a large
		// cold repo now waits for the index, and the MCP handshake is still
		// served immediately (readyCh gates tool calls only).
		if _, err := client.Index(ctx, root); err != nil {
			fmt.Fprintln(os.Stderr, "warning: initial index failed:", err)
		}
		close(readyCh)
	}()

	// Stop background work and close the embedded engine before returning so no
	// SQLite handles or .grove files linger — otherwise a caller that removes
	// the project directory (e.g. a test using t.TempDir) races file creation
	// and fails with "directory not empty" on Linux or a lock error on Windows.
	// Also run when an upgraded binary takes over the session mid-Serve.
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			<-doneCh
			client.Shutdown()
		})
	}

	h := core.NewHandlerWithReady(cfg, root, client, readyCh)
	srv := mcp.NewServer(h)
	if compact {
		srv = mcp.NewCompactServer(h)
	}
	srv.OnHandoff = shutdown
	serveErr := srv.Serve(os.Stdin, os.Stdout)
	shutdown()

	if serveErr != nil {
		fmt.Fprintln(os.Stderr, "mcp:", serveErr)
		return 1
	}
	return 0
}

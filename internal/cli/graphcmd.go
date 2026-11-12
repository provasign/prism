package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/provasign/prism/internal/graphui"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/version"
)

// defaultGraphPort is tried first so the page's address is stable across
// runs; when it is taken and --port was not given, the OS picks a free one.
const defaultGraphPort = 8787

// cmdGraph serves the interactive graph explorer on localhost and opens it
// in the browser. It runs until interrupted.
func cmdGraph(args []string) int {
	dir, dirSet := ".", false
	port, portSet := defaultGraphPort, false
	open, tests := true, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--port":
			p, err := positiveIntFlag("graph", args, i)
			if err == nil && p > 65535 {
				err = fmt.Errorf("graph: --port must be 1-65535, got %d", p)
			}
			if err != nil {
				return usageError(err)
			}
			port, portSet = p, true
			i++
		case "--no-open":
			open = false
		case "--tests":
			tests = true
		default:
			if len(a) > 0 && a[0] == '-' {
				return rejectUnknownFlag("graph", a)
			}
			if err := positionalDir("graph", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}

	// Run from a subdirectory: serve the whole repository, open on that
	// directory.
	root, sub := cliRoot(dir)
	cfg, client, err := newClientMode(root, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "graph:", err)
		return 1
	}
	defer client.Shutdown()
	ctx := context.Background()
	// Bring the index up to date with the working tree (a delta run; a
	// first run indexes everything). A failed refresh is not fatal: the
	// stored graph is still worth looking at.
	fmt.Fprintln(os.Stderr, "prism graph: checking the index…")
	if _, err := client.Index(ctx, root); err != nil {
		fmt.Fprintln(os.Stderr, "graph: warning: index refresh failed, showing the stored graph:", err)
	}
	snapshot := func(ctx context.Context) ([]grove.SymbolRecord, []grove.Edge, error) {
		return client.SnapshotGraph(ctx)
	}
	reindex := func(ctx context.Context) error {
		_, err := client.Index(ctx, root)
		return err
	}
	// Change impact is the same answer `prism change-impact` gives: the
	// MCP tool, run in process.
	handler := mcp.NewHandler(cfg, root, client)
	impact := func(_ context.Context, query, file string) (map[string]any, error) {
		args := map[string]any{"query": query}
		if file != "" {
			args["file"] = file
		}
		out, err := handler.Invoke("prism_change_impact", args)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("change impact: unexpected answer: %w", err)
		}
		return m, nil
	}
	srv, err := graphui.NewServer(ctx, root, version.Version, snapshot, reindex, impact)
	if err != nil {
		fmt.Fprintln(os.Stderr, "graph:", err)
		return 1
	}
	meta := srv.Model().Meta()
	if meta.Symbols == 0 {
		fmt.Fprintln(os.Stderr, "graph: the index holds no code symbols for", root, "— run `prism index` and check `prism doctor`")
		return 1
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && !portSet {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "graph:", err)
		return 1
	}
	page := graphURL(ln.Addr().(*net.TCPAddr).Port, sub, tests)
	fmt.Fprintf(os.Stderr, "prism graph: %d files, %d symbols, %d dependency edges in %s\n",
		meta.Files, meta.Symbols, meta.Edges, root)
	fmt.Fprintln(os.Stderr, "open", page, " (Ctrl+C to stop)")
	fmt.Println(page)
	if open {
		if err := openBrowser(page); err != nil {
			fmt.Fprintln(os.Stderr, "graph: could not open a browser:", err)
		}
	}

	server := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	select {
	case <-stop:
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shut)
		return 0
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "graph:", err)
			return 1
		}
		return 0
	}
}

// graphURL builds the page address. View state lives in the URL fragment,
// which the page reads: s = scope, t = include tests.
func graphURL(port int, sub string, tests bool) string {
	frag := url.Values{}
	if sub != "" {
		frag.Set("s", "d:"+sub)
	}
	if tests {
		frag.Set("t", "1")
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if len(frag) > 0 {
		u += "#" + frag.Encode()
	}
	return u
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

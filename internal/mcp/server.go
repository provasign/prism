// Package mcp is Prism's MCP stdio transport: message framing, the request
// loop (concurrency, cancellation, panics) and the handoff to an upgraded
// binary. What each method answers is Dispatcher.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/version"
)

// Server is the JSON-RPC stdio server.
type Server struct {
	d *Dispatcher

	// OnHandoff, when set, releases this process's engine once an upgraded
	// binary has taken over the session.
	OnHandoff func()

	initParams json.RawMessage // last initialize params, replayed on handoff
}

// NewServer wires a Handler into a stdio JSON-RPC server.
func NewServer(h *core.Handler) *Server { return &Server{d: NewDispatcher(h)} }

// NewCompactServer exposes one prism gateway instead of six separate MCP tool
// schemas. The underlying handlers and result renderers remain identical.
func NewCompactServer(h *core.Handler) *Server { return &Server{d: NewCompactDispatcher(h)} }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Serve reads framed JSON-RPC messages from r and writes responses to w.
// Returns on EOF or fatal IO error. Delivery state stays in this process
// because MCP does not provide a stable conversation identifier.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	var wmu sync.Mutex
	write := func(id any, result any, rpcErr *RPCError) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeMessage(w, id, result, rpcErr)
	}
	// tools/call runs on its own goroutine so ping and cancellation are
	// serviced while a minutes-long verify or index runs (SEC-06). At most
	// mcpConcurrency calls run at once; the engine serializes index writes.
	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, mcpConcurrency)
	var cmu sync.Mutex
	cancels := map[string]context.CancelFunc{}
	for {
		msg, err := readMessage(reader)
		if err == io.EOF {
			return nil
		}
		if errors.Is(err, errFrame) {
			if werr := write(nil, nil, &RPCError{Code: -32600, Message: err.Error()}); werr != nil {
				return werr
			}
			continue
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			// A client waiting for a reply hung forever (SEC-05).
			if werr := write(nil, nil, &RPCError{Code: -32700, Message: "parse error: " + err.Error()}); werr != nil {
				return werr
			}
			continue
		}
		if req.ID == nil {
			// Notification — no response.
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID any `json:"requestId"`
				}
				_ = json.Unmarshal(req.Params, &p)
				cmu.Lock()
				if cancel := cancels[fmt.Sprint(p.RequestID)]; cancel != nil {
					cancel()
				}
				cmu.Unlock()
			}
			continue
		}
		// Never dispatch a request through superseded code. This check precedes
		// JSON-RPC method handling because the new client may send a parameter or
		// method the old server cannot understand well enough to warn about.
		if note := staleBinaryNote(); note != "" {
			wg.Wait() // in-flight calls finish before the replacement takes the stream
			if req.Method == "initialize" {
				s.initParams = req.Params
			}
			cmd, childIn, childOut, err := s.startHandoff()
			if err == nil {
				if s.OnHandoff != nil {
					s.OnHandoff()
				}
				return relayToReplacement(cmd, childIn, childOut, msg, reader, w)
			}
			fmt.Fprintln(os.Stderr, "mcp: handoff to upgraded binary failed:", err)
			if err := write(req.ID, nil, &RPCError{Code: -32001, Message: note}); err != nil {
				return err
			}
			return nil
		}
		if req.Method == "initialize" {
			s.initParams = req.Params
		}
		if req.Method != "tools/call" {
			result, rpcErr := s.safeDispatch(context.Background(), req.Method, req.Params)
			if err := write(req.ID, result, rpcErr); err != nil {
				return err
			}
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		key := fmt.Sprint(req.ID)
		cmu.Lock()
		cancels[key] = cancel
		cmu.Unlock()
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			defer func() {
				cmu.Lock()
				delete(cancels, key)
				cmu.Unlock()
				cancel()
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			if d := testToolCallDelay; d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
				}
			}
			result, rpcErr := s.safeDispatch(ctx, req.Method, req.Params)
			if errors.Is(ctx.Err(), context.Canceled) {
				result, rpcErr = nil, &RPCError{Code: -32800, Message: "request cancelled"}
			}
			_ = write(req.ID, result, rpcErr)
		}(req)
	}
}

// testToolCallDelay slows every tools/call in tests of the request loop.

var testToolCallDelay time.Duration

// mcpConcurrency bounds concurrent tools/call requests.
const mcpConcurrency = 4

// safeDispatch turns a panic anywhere in a request into a -32603 reply; one
// bad request used to end the whole session (SEC-05).
func (s *Server) safeDispatch(ctx context.Context, method string, params json.RawMessage) (result any, rpcErr *RPCError) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "mcp: %s panicked: %v\n", method, r)
			result, rpcErr = nil, &RPCError{Code: -32603, Message: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	return s.d.Dispatch(ctx, method, params)
}

type binarySnapshot struct {
	path string
	info os.FileInfo
}

// startupBinarySnapshot records the configured launch path, not only
// os.Executable(). Homebrew starts Prism through a stable symlink while
// os.Executable may resolve to a versioned Cellar path; watching that resolved
// path misses a symlink switch to the newly installed release.
var startupBinarySnapshot = snapshotLaunchBinary()

func snapshotLaunchBinary() binarySnapshot {
	var path string
	if len(os.Args) > 0 {
		path, _ = exec.LookPath(os.Args[0])
	}
	if path == "" {
		path, _ = os.Executable()
	}
	if path == "" {
		return binarySnapshot{}
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return snapshotBinary(path)
}

func snapshotBinary(path string) binarySnapshot {
	info, err := os.Stat(path)
	if err != nil {
		return binarySnapshot{}
	}
	// Windows loads the file ID lazily from FileInfo's saved path. Freeze it
	// while this file still occupies that path so a later atomic replacement
	// cannot make both snapshots resolve to the replacement's ID.
	os.SameFile(info, info)
	return binarySnapshot{path: path, info: info}
}

func binarySnapshotChanged(snapshot binarySnapshot) bool {
	if snapshot.path == "" || snapshot.info == nil {
		return false
	}
	current, err := os.Stat(snapshot.path)
	if err != nil {
		// A package manager may remove the old versioned path after switching its
		// stable launcher. The running image is still valid, but it is superseded.
		return true
	}
	return !os.SameFile(snapshot.info, current) || current.ModTime().After(snapshot.info.ModTime())
}

var staleBinaryWarned bool

// staleBinaryNote reports once when the configured executable was replaced
// after this server started. Serve first hands the session to the new binary;
// the note is the client-facing error only when that handoff fails.
func staleBinaryNote() string {
	if staleBinaryWarned || !binarySnapshotChanged(startupBinarySnapshot) {
		return ""
	}
	staleBinaryWarned = true
	return "⚠ prism was upgraded on disk after this MCP server started (running " +
		version.Version + "). This request was not run with old behavior; the stale server is exiting so the client can respawn it. " +
		"Restart the agent if Prism tools do not reconnect automatically."
}

// readMessage parses a Content-Length framed JSON-RPC message. For convenience
// it also accepts a single line of JSON (line-delimited fallback used by
// many test harnesses).
// errFrame marks a malformed frame the server answers and skips.
var errFrame = errors.New("invalid frame")

// maxFrame bounds a Content-Length frame.
const maxFrame = 16 << 20

func readMessage(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.ToLower(line), "content-length:") {
		return []byte(strings.TrimSpace(line)), nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:")))
	if err != nil {
		return nil, err
	}
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	if n < 0 || n > maxFrame {
		// make([]byte, n) panicked on a negative length and allocated
		// without bound on a large one (SEC-05).
		if n > 0 {
			_, _ = io.CopyN(io.Discard, r, int64(n))
		}
		return nil, fmt.Errorf("%w: Content-Length %d outside 0..%d", errFrame, n, maxFrame)
	}
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return buf, err
}

func writeMessage(w io.Writer, id any, result any, rpcErr *RPCError) error {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	// MCP stdio transport requires newline-delimited JSON (one compact JSON
	// object per line, no embedded newlines). json.Marshal already produces a
	// compact, newline-free payload. Emitting LSP-style "Content-Length"
	// framing here makes every newline-delimited MCP client (Claude Code,
	// Cursor, VS Code, Copilot) block waiting for a terminating newline and
	// time out the connection.
	_, err = fmt.Fprintf(w, "%s\n", payload)
	return err
}

// Package mcp implements Prism's JSON-RPC 2.0 server (stdio transport)
// exposing the prism_* tools (the primary set advertised via tools/list; the
// auxiliary compact/savings/feedback/evidence/cycles tools stay dispatchable
// for the CLI and HTTP surfaces without spending schema tokens in every MCP
// session). The
// on-the-wire format is the Model Context Protocol stdio transport:
// newline-delimited JSON (one compact JSON object per line). The reader
// additionally tolerates legacy "Content-Length: N\r\n\r\n{json}" framing
// for backward compatibility with older test harnesses.
package mcp

import (
	"os"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/version"
)

// Server is the JSON-RPC stdio server.
type Server struct {
	handler *Handler

	// Ledger persistence for the long-running server: CLI invocations save
	// per call, but an MCP session historically recorded nothing durable —
	// which is why token-cost analysis had to be done by mining agent
	// transcripts. The server now merges its in-memory deltas into the
	// shared per-root ledger file after each tool call, under the same file
	// lock CLI processes use, so concurrent sessions add rather than
	// clobber.
	ledgerPath string
	ledgerMu   sync.Mutex
	lastSaved  session.Summary
}

// NewServer wires a Handler into a stdio JSON-RPC server.
func NewServer(h *Handler) *Server { return &Server{handler: h} }

// WithLedgerPersistence enables per-call delta merging into the shared
// ledger file at path (see Server.ledgerPath).
func (s *Server) WithLedgerPersistence(path string) *Server {
	s.ledgerPath = path
	s.lastSaved = s.handler.Ledger.Snapshot()
	return s
}

// persistLedgerDelta merges counts accumulated since the last save into the
// on-disk ledger. Best-effort: a lock timeout or IO error drops one delta,
// never blocks a tool response.
func (s *Server) persistLedgerDelta() {
	if s.ledgerPath == "" {
		return
	}
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	delta := s.handler.Ledger.DiffSince(s.lastSaved)
	_ = session.WithFileLock(s.ledgerPath+".lock", 2*time.Second, func() error {
		disk, err := session.LoadLedger(s.ledgerPath)
		if err != nil {
			disk = session.NewLedger(time.Now().Format("20060102-150405"))
		}
		disk.ApplyDelta(delta)
		return disk.Save(s.ledgerPath)
	})
	s.lastSaved = s.handler.Ledger.Snapshot()
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads framed JSON-RPC messages from r and writes responses to w.
// Returns on EOF or fatal IO error. Delivery state stays in this process
// because MCP does not provide a stable conversation identifier.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		msg, err := readMessage(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		if req.ID == nil {
			// Notification — no response.
			continue
		}
		result, rpcErr := s.dispatch(req.Method, req.Params)
		if err := writeMessage(w, req.ID, result, rpcErr); err != nil {
			return err
		}
	}
}

// defaultProtocolVersion is the latest MCP revision these servers target.
const defaultProtocolVersion = "2025-03-26"

// supportedProtocolVersions are the MCP revisions this server can speak.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// startupBinaryModTime is the mtime of the executable when this process
// started; a later mtime on disk means the binary was replaced (brew
// upgrade, go build) and this server is running superseded behavior.
var startupBinaryModTime = func() int64 {
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}()

var staleBinaryWarned bool

// staleBinaryNote reports once per session when the on-disk binary is newer
// than the running server.
func staleBinaryNote() string {
	if staleBinaryWarned || startupBinaryModTime == 0 {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil || fi.ModTime().Unix() <= startupBinaryModTime {
		return ""
	}
	staleBinaryWarned = true
	return "⚠ prism was upgraded on disk after this MCP server started (running " +
		version.Version + "). This server keeps serving the OLD behavior — " +
		"including any bugs fixed since — until the session restarts. " +
		"Tell the user to restart their agent to pick up the new binary."
}

// negotiateProtocolVersion echoes the client's requested protocolVersion when
// it is one we support (required by the MCP spec), otherwise falls back to our
// latest. Maximizes compatibility across clients (Claude Code, Cursor, VS Code,
// Copilot) that each pin different revisions.
func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && supportedProtocolVersions[p.ProtocolVersion] {
		return p.ProtocolVersion
	}
	return defaultProtocolVersion
}

func (s *Server) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": negotiateProtocolVersion(params),
			"serverInfo":      map[string]string{"name": "prism", "version": version.Version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": ToolSchemas()}, nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		out, err := s.handler.Invoke(call.Name, call.Arguments)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		// MCP content is free-form text — JSON is not required, only what
		// this server has always sent by default. prism_search gets a plain
		// grep-style rendering instead: measured 1.19-1.32x fewer bytes for
		// identical hits (BACKLOG.md item 1), on the highest-call-count tool
		// in the system, where the saving is paid back on every later turn
		// the result sits in cache. Falls back to JSON for any shape the
		// renderer does not fully recognise (symbol-bearing results, or an
		// unexpected field) rather than risk dropping content silently.
		var text string
		var rendered bool
		if m, ok := out.(map[string]any); ok {
			switch call.Name {
			case "prism_search":
				text, rendered = renderSearchAsText(m)
			case "prism_read":
				// +7-18% JSON escaping over whole source bodies, on the
				// highest-call-count tool (56% of prism calls in full38).
				text, rendered = renderReadAsText(m)
			case "prism_change_impact":
				// 3.1x measured: symbol-record lists are the most
				// repetitive JSON this server emits (graphtext.go).
				text, rendered = renderChangeImpactAsText(m)
			case "prism_lookup":
				// 4.8x measured: the JSON form shipped the body twice
				// plus index internals (graphtext.go).
				text, rendered = renderLookupAsText(m)
			case "prism_verify":
				text, rendered = renderVerifyAsText(m)
			case "prism_query":
				// source delivery only; the symbols-delivery struct is not
				// a map and correctly falls through to JSON.
				text, rendered = renderQuerySourceAsText(m)
			}
		}
		if !rendered {
			// Compact JSON: results land in an agent's context window, and
			// indentation is pure token overhead.
			encoded, _ := json.Marshal(out)
			text = string(encoded)
		}
		// Result-size accounting: this is the number that compounds via
		// cache re-reads on every later turn (measured: median 4.4x, mean
		// 11x effective multiplier across real sessions).
		s.handler.Ledger.RecordResult(call.Name, ranking.EstimateTokens(text))
		s.persistLedgerDelta()
		content := []map[string]string{{"type": "text", "text": text}}
		// Stale-context delivery: when any recently delivered file changed
		// on disk, every context-bearing response carries the warning, so
		// the agent learns mid-task instead of at merge time. Cheap probe
		// (bounded hash comparison); prism_drift gives symbol-level detail.
		if contextBearingTool(call.Name) {
			if warning := s.handler.StaleContextWarning(); warning != "" {
				content = append(content, map[string]string{"type": "text", "text": warning})
			}
		}
		// Stale-SERVER delivery: an MCP server outlives upgrades. During one
		// audit, the session's server silently dropped a parameter added two
		// releases earlier and emitted warnings pointing at a tool that no
		// longer existed — fixed on disk, live in the process, with nothing
		// anywhere saying so. One stat per call is the price of saying it.
		if note := staleBinaryNote(); note != "" {
			content = append(content, map[string]string{"type": "text", "text": note})
		}
		return map[string]any{"content": content}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

// contextBearingTool reports whether a tool delivers code context the agent
// may go on to rely on — the calls worth annotating with staleness warnings.
func contextBearingTool(name string) bool {
	switch name {
	case "prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_node", // symbol bodies + edge file:lines, both index-derived
		"prism_rename_plan", // its edits carry index-derived line numbers — stale index means wrong-line edits applied verbatim
		"prism_map", "prism_cycles": // their sites carry index-derived file:line evidence
		return true
	default:
		return false
	}
}

// readMessage parses a Content-Length framed JSON-RPC message. For convenience
// it also accepts a single line of JSON (line-delimited fallback used by
// many test harnesses).
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
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return buf, err
}

func writeMessage(w io.Writer, id any, result any, rpcErr *rpcError) error {
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

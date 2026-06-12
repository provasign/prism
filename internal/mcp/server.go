// Package mcp implements Prism's JSON-RPC 2.0 server (stdio transport) exposing
// the 8 prism_* tools. The on-the-wire format is the Model Context Protocol
// stdio transport: newline-delimited JSON (one compact JSON object per line).
// The reader additionally tolerates legacy "Content-Length: N\r\n\r\n{json}"
// framing for backward compatibility with older test harnesses.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/version"
)

// Server is the JSON-RPC stdio server.
type Server struct {
	handler *Handler
}

// NewServer wires a Handler into a stdio JSON-RPC server.
func NewServer(h *Handler) *Server { return &Server{handler: h} }

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
// Returns on EOF or fatal IO error. On return, the warm LRU cache is flushed
// to disk so the next session starts with sha-pointer-level deduplication.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	defer s.handler.SaveSessionCache()
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
		// MCP expects content array with text parts. Compact JSON: results
		// land in an agent's context window, and indentation is pure token
		// overhead.
		encoded, _ := json.Marshal(out)
		content := []map[string]string{{"type": "text", "text": string(encoded)}}
		// Stale-context delivery: when any recently delivered file changed
		// on disk, every context-bearing response carries the warning, so
		// the agent learns mid-task instead of at merge time. Cheap probe
		// (bounded hash comparison); prism_drift gives symbol-level detail.
		if contextBearingTool(call.Name) {
			if warning := s.handler.StaleContextWarning(); warning != "" {
				content = append(content, map[string]string{"type": "text", "text": warning})
			}
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
	case "prism_query", "prism_read", "prism_search", "prism_lookup":
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

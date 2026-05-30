// Package mcp implements Prism's JSON-RPC 2.0 server (stdio framing) exposing
// the 8 prism_* tools. The on-the-wire format matches the Model Context
// Protocol "Content-Length: N\r\n\r\n{json}" framing used by Grove.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tabladrum/grove-suite/prism/internal/version"
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
// Returns on EOF or fatal IO error.
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

func (s *Server) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "prism", "version": version.Version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}, nil
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
		// MCP expects content array with text parts.
		encoded, _ := json.MarshalIndent(out, "", "  ")
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(encoded)}},
		}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
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
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
	return err
}

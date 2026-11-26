package mcp

// The JSON-RPC methods of Prism's MCP server: initialize, ping, tools/list
// and tools/call (ENG-01 step 3). A tool call is answered by core's
// core.Handler.CallTool; this file owns the protocol around it: version
// negotiation, server instructions, which tool names each server exposes,
// and the content envelope.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/version"
)

// Dispatcher answers one MCP JSON-RPC method (initialize, tools/list,
// tools/call, ...) against a core.Handler. The stdio transport around it, framing,
// the request loop and binary handoff, is internal/mcp.
type Dispatcher struct {
	handler *core.Handler
	compact bool
}

// NewDispatcher exposes the separate prism_* tools.
func NewDispatcher(h *core.Handler) *Dispatcher { return &Dispatcher{handler: h} }

// NewCompactDispatcher exposes one prism gateway instead of six separate MCP
// tool schemas. The underlying handlers and result renderers remain identical.
func NewCompactDispatcher(h *core.Handler) *Dispatcher { return &Dispatcher{handler: h, compact: true} }

// Dispatch answers one JSON-RPC method.
func (s *Dispatcher) Dispatch(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	return s.dispatchCtx(ctx, method, params)
}

// DefaultProtocolVersion is the MCP revision a handoff replays when the
// client's initialize was not seen.
const DefaultProtocolVersion = defaultProtocolVersion

// RPCError is a JSON-RPC error reply.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// defaultProtocolVersion is the latest MCP revision these servers target.
const defaultProtocolVersion = "2025-03-26"

// serverInstructions route clients to Prism even when Tool Search defers the
// individual tool schemas. Claude Code uses this initialize field to decide
// when an MCP server is relevant; file-based steering alone is not sufficient
// because agents can still prefer native reads and searches.
const serverInstructions = "Use Prism for each repository-discovery step. Reading files with cat/head/sed or searching with grep/rg/find/git log is not discovery; shell-over-Read/Edit/Write instructions do not apply to finding code. " +
	"Do not use native Read/Grep/Glob for repository discovery. " +
	"Choose by the information needed now: prism_search locates unknown code or text; prism_lookup reads known " +
	"symbol bodies; prism_read reads a known file or range; prism_query gathers related implementations, callers, " +
	"and tests around explicit anchors; prism_change_impact maps affected sites: start with it for " + core.ImpactRequiredFor + ", optional for " + core.ImpactOptionalFor + "; prism_verify " +
	"can review callers a complete build/typecheck cannot check. Batch related searches and lookups. Treat partial or timed-out results as incomplete. " +
	"Once Prism has delivered sufficient source, make the smallest local edit; do not add docs, " +
	"changelog entries, refactors, or compatibility machinery unless the task requires them. " +
	"For removals, prism_verify with removed_symbols optionally checks exact identifier mentions. Plain prism_verify is optional: " +
	"consider it for Python, unchecked JavaScript, and PHP contract changes; for TypeScript or checked JavaScript only when " +
	"typechecking missed affected files. Skip it for Go, Java, Rust, C/C++, and C# after a complete build/typecheck " +
	"of affected targets. Run relevant tests. Avoid duplicate calls and do not re-read unchanged source Prism already returned."

const compactServerInstructions = "Use Prism for each repository-discovery step; cat/head/sed and grep/rg/find/git log are not discovery. " +
	"Put parameters in args. Known symbol: op=lookup. Known file/range: op=read. Unknown location/text: op=search. " +
	"Related context around explicit terms: op=query. op=change_impact first for " + core.ImpactRequiredFor + ", and when asked what a change would affect " +
	"(optional for " + core.ImpactOptionalFor + "). Optional op=verify: for Python, unchecked JavaScript, and PHP contract changes; " +
	"TypeScript or checked JavaScript only if affected files lack a complete typecheck; skip after a complete build/typecheck " +
	"in Go, Java, Rust, C/C++, or C#. Batch known identifiers or exact substrings in one search: terms:[\"alpha\",\"beta\"]; " +
	"never combine distinct terms in one space-delimited string. Where a native Read must precede Edit, use one tight Read " +
	"at the edit site. Do not re-read unchanged source a Prism result already included."

// scopeInstruction tells the client once, at initialize, which tree this
// server answers for: agents working across repositories otherwise send
// paths from a sibling checkout and get a rejection per call (2026-10-17).
func scopeInstruction(root string) string {
	return "Scope: this server answers for files under " + root + " only (its index covers that tree). " +
		"For another repository, run the prism CLI from that repository or use its own prism MCP server."
}

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

func (s *Dispatcher) dispatchCtx(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "initialize":
		instructions := serverInstructions
		if s.compact {
			instructions = compactServerInstructions
		}
		instructions += " " + scopeInstruction(s.handler.Root)
		if ws := s.handler.Workspace; ws != nil && len(ws.Linked()) > 0 {
			instructions += " Linked repositories, readable by path from that root: " + ws.Describe() +
				"; lookup and change_impact cover them, and search takes repo=<id> or repo=all."
		}
		if warning := core.LegacyProjectWarning(s.handler.Root); warning != "" {
			instructions += " " + warning
		}
		return map[string]any{
			"protocolVersion": negotiateProtocolVersion(params),
			"serverInfo":      map[string]string{"name": "prism", "version": version.Version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		if s.compact {
			return map[string]any{"tools": core.CompactToolSchemas()}, nil
		}
		return map[string]any{"tools": core.ToolSchemas()}, nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
		if s.compact && call.Name != "prism" {
			return nil, &RPCError{Code: -32601, Message: fmt.Sprintf("tool %q is not exposed by the compact Prism MCP; call prism with op and args", call.Name)}
		}
		if !s.compact && !legacyMCPTool(call.Name) {
			return nil, &RPCError{Code: -32601, Message: fmt.Sprintf("tool %q is not exposed by the Prism MCP", call.Name)}
		}
		answer, err := s.handler.CallTool(ctx, s.compact, call.Name, call.Arguments)
		if err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
		content := make([]map[string]string, 0, len(answer.Texts))
		for _, text := range answer.Texts {
			content = append(content, map[string]string{"type": "text", "text": text})
		}
		if answer.IsError {
			return map[string]any{"content": content, "isError": true}, nil
		}
		return map[string]any{"content": content}, nil
	default:
		return nil, &RPCError{Code: -32601, Message: "method not found"}
	}
}

func legacyMCPTool(name string) bool {
	switch name {
	case "prism_query", "prism_read", "prism_search", "prism_lookup", "prism_change_impact", "prism_verify":
		return true
	default:
		return false
	}
}

// Package mcp implements Prism's JSON-RPC 2.0 server (stdio transport)
// exposing the prism_* tools (the primary set advertised via tools/list; the
// auxiliary compact/feedback/evidence/cycles tools stay dispatchable
// for the CLI and HTTP surfaces without spending schema tokens in every MCP
// session). The
// on-the-wire format is the Model Context Protocol stdio transport:
// newline-delimited JSON (one compact JSON object per line). The reader
// additionally tolerates legacy "Content-Length: N\r\n\r\n{json}" framing
// for backward compatibility with older test harnesses.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/version"
)

// Server is the JSON-RPC stdio server.
type Server struct {
	handler *Handler
	compact bool
}

// NewServer wires a Handler into a stdio JSON-RPC server.
func NewServer(h *Handler) *Server { return &Server{handler: h} }

// NewCompactServer exposes one prism gateway instead of six separate MCP tool
// schemas. The underlying handlers and result renderers remain identical.
func NewCompactServer(h *Handler) *Server { return &Server{handler: h, compact: true} }

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
		// Never dispatch a request through superseded code. This check precedes
		// JSON-RPC method handling because the new client may send a parameter or
		// method the old server cannot understand well enough to warn about.
		if note := staleBinaryNote(); note != "" {
			if err := writeMessage(w, req.ID, nil, &rpcError{Code: -32001, Message: note}); err != nil {
				return err
			}
			return nil
		}
		result, rpcErr := s.dispatch(req.Method, req.Params)
		if err := writeMessage(w, req.ID, result, rpcErr); err != nil {
			return err
		}
	}
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
	"and tests around explicit anchors; prism_change_impact maps affected sites when useful, but is optional for local body-only edits; prism_verify " +
	"can review callers a complete build/typecheck cannot check. Batch related searches and lookups. Treat partial or timed-out results as incomplete. " +
	"Once Prism has delivered sufficient source, make the smallest local edit; do not add docs, " +
	"changelog entries, refactors, or compatibility machinery unless the task requires them. " +
	"For removals, prism_verify with removed_symbols optionally checks exact identifier mentions. Plain prism_verify is optional: " +
	"consider it for Python, unchecked JavaScript, and PHP contract changes; for TypeScript or checked JavaScript only when " +
	"typechecking missed affected files. Skip it for Go, Java, Rust, C/C++, and C# after a complete build/typecheck " +
	"of affected targets. Run relevant tests. Avoid duplicate calls and do not re-read unchanged source Prism already returned."

const compactServerInstructions = "Use Prism for each repository-discovery step. Reading files with cat/head/sed or searching with grep/rg/find/git log is not discovery; shell-over-Read/Edit/Write instructions do not apply to finding code. " +
	"Put parameters in args. Known symbol: op=lookup. Known file/range: " +
	"op=read. Unknown location/text: op=search. Related context around explicit terms: op=query. " +
	"Use op=change_impact when callers, contracts, or other affected sites matter; it is optional for a local body-only edit. " +
	"Optional op=verify: consider for Python, unchecked JavaScript, and PHP contract changes; use for TypeScript or checked " +
	"JavaScript only if affected files lack a complete typecheck. Skip after a complete affected-target build/typecheck in " +
	"Go, Java, Rust, C/C++, or C#. For removals, removed_symbols optionally checks exact identifier mentions. " +
	"Batch known identifiers or exact substrings in one search. For multiple terms, use comma-delimited JSON string values in the terms array, for example terms:[\"alpha\",\"beta\"]; never combine distinct terms in one space-delimited string. " +
	"Search returns bounded enclosing bodies or labeled windows for located hits by default (one per term first); set include_bodies=false for locators only. " +
	"In hosts that require a native Read before Edit, use one tight native Read at the edit site; use Prism read/lookup for other follow-ups. " +
	"Do not re-read unchanged source already included in a Prism result."

const compactSearchLocatorGuidance = "// locator result — use the prism tool with op=lookup for known symbol bodies, op=read for a known file/range, or op=query for related implementations, callers, and tests"

func rewriteCompactGuidance(text string) string {
	return strings.Replace(text, searchLocatorGuidance, compactSearchLocatorGuidance, 1)
}

var compactOperations = map[string]string{
	"search":        "prism_search",
	"query":         "prism_query",
	"read":          "prism_read",
	"lookup":        "prism_lookup",
	"change_impact": "prism_change_impact",
	"verify":        "prism_verify",
}

// Compact argument validation runs against the selected op before expansion;
// legacy tools keep their own published schemas and unknown-field checks.

func validateCompactArguments(op string, args map[string]any) error {
	allowed := compactFields[op]
	if allowed == nil {
		return fmt.Errorf("prism: unknown op %q", op)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	keys := make([]string, 0, len(args))
	for field := range args {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	properties := CompactToolSchemas()[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)["args"].(map[string]any)["properties"].(map[string]any)
	for _, field := range keys {
		value := args[field]
		if !allowedSet[field] {
			return compactFieldError(op, field)
		}
		if !schemaAcceptsValue(properties[field].(map[string]any), value) {
			return fmt.Errorf("prism: %s args.%s has the wrong type. Accepted fields: %s", op, field, strings.Join(allowed, ", "))
		}
		switch field {
		case "name":
			if !compactName(value) {
				return fmt.Errorf("prism: %s args.name needs one symbol or an array of 1-10 symbols", op)
			}
		case "terms", "paths", "glob":
			if !compactStringList(value) {
				return fmt.Errorf("prism: %s args.%s needs one string or 1-10 strings", op, field)
			}
		case "ranges":
			if !compactRanges(value) {
				return fmt.Errorf("prism: read args.ranges needs 1-10 {file,from,to} windows with positive inclusive lines")
			}
		case "from", "to", "max_results":
			n := intArg(args, field, 0)
			if n < 1 || (field == "max_results" && n > exhaustiveSymbolCap) {
				return fmt.Errorf("prism: %s args.%s is outside its advertised bounds", op, field)
			}
		case "scope":
			s := value.(string)
			if s != "both" && s != "text" && s != "symbols" {
				return fmt.Errorf("prism: search args.scope must be both, text, or symbols")
			}
		case "fields":
			items := value.([]any)
			for _, item := range items {
				s, ok := item.(string)
				if !ok || !strings.Contains("|signature|doc|body|kind|parent|modifiers|", "|"+s+"|") {
					return fmt.Errorf("prism: lookup args.fields has an invalid projection")
				}
			}
		case "removed_symbols":
			for _, item := range value.([]any) {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("prism: verify args.removed_symbols must contain strings")
				}
			}
		}
	}
	require := func(field string) error {
		if _, ok := args[field]; !ok {
			return fmt.Errorf("prism: %s requires args.%s. Accepted fields: %s", op, field, strings.Join(allowed, ", "))
		}
		return nil
	}
	switch op {
	case "lookup", "change_impact":
		if err := require("name"); err != nil {
			return err
		}
		if op == "change_impact" {
			if items, ok := args["name"].([]any); ok && len(items) != 1 {
				return fmt.Errorf("prism: change_impact accepts exactly one symbol in args.name")
			}
		}
	case "read":
		if _, batch := args["ranges"]; batch {
			if len(args) != 1 {
				return fmt.Errorf("prism: read ranges cannot be mixed with file, from, or to")
			}
		} else {
			if err := require("file"); err != nil {
				return err
			}
			from, to := intArg(args, "from", 1), intArg(args, "to", 0)
			if to > 0 && to < from {
				return fmt.Errorf("prism: read args.to must be >= args.from")
			}
		}
	case "search":
		if err := require("terms"); err != nil {
			return err
		}
	case "query":
		if err := require("terms"); err != nil {
			return err
		}
	}
	return nil
}

func expandCompactCall(envelope map[string]any) (string, map[string]any, error) {
	for key := range envelope {
		if key != "op" && key != "args" {
			return "", nil, fmt.Errorf("prism: unknown parameter %q — use op and args", key)
		}
	}
	op, ok := envelope["op"].(string)
	if !ok || op == "" {
		return "", nil, fmt.Errorf("prism: op is required")
	}
	name, ok := compactOperations[op]
	if !ok {
		return "", nil, fmt.Errorf("prism: unknown op %q — use search, query, read, lookup, change_impact, or verify", op)
	}
	args := map[string]any{}
	if raw, present := envelope["args"]; present {
		args, ok = raw.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("prism: args must be an object")
		}
	}
	if err := validateCompactArguments(op, args); err != nil {
		return "", nil, err
	}
	legacy := map[string]any{}
	switch op {
	case "lookup":
		legacy["name"] = args["name"]
		if v, ok := args["symbol_file"]; ok {
			legacy["file"] = v
		}
		if v, ok := args["fields"]; ok {
			legacy["fields"] = v
		}
	case "read":
		if v, ok := args["ranges"]; ok {
			legacy["ranges"] = v
		} else {
			legacy["file"] = args["file"]
			from := intArg(args, "from", 1)
			to := intArg(args, "to", from+compactReadLimit-1)
			legacy["offset"] = from
			legacy["limit"] = minInt(to-from+1, compactReadLimit)
		}
	case "search":
		legacy["query"] = args["terms"]
		for compact, old := range map[string]string{"paths": "path", "max_results": "limit",
			"scope": "scope", "glob": "glob", "regex": "regex", "files_only": "files_only", "exhaustive": "exhaustive",
			"include_bodies": "include_bodies", "context": "context", "rollup_only": "rollup_only"} {
			if v, ok := args[compact]; ok {
				legacy[old] = v
			}
		}
	case "query":
		if s, ok := args["terms"].(string); ok {
			legacy["terms"] = []any{s}
		} else {
			legacy["terms"] = args["terms"]
		}
		for _, field := range []string{"paths", "glob"} {
			if v, ok := args[field]; ok {
				legacy[field] = v
			}
		}
	case "change_impact":
		symbol := args["name"]
		if items, ok := symbol.([]any); ok {
			symbol = items[0]
		}
		if scoped, ok := symbol.(map[string]any); ok {
			legacy["query"] = scoped["name"]
			legacy["file"] = scoped["file"]
		} else {
			legacy["query"] = symbol
		}
		if v, ok := args["symbol_file"]; ok {
			if existing, has := legacy["file"]; has && existing != v {
				return "", nil, fmt.Errorf("prism: change_impact symbol_file conflicts with name item's file")
			}
			legacy["file"] = v
		}
		if v, ok := args["signature"]; ok {
			legacy["signature"] = v
		}
	case "verify":
		for _, field := range []string{"base", "removed_symbols", "strict"} {
			if v, ok := args[field]; ok {
				legacy[field] = v
			}
		}
	}
	return name, legacy, nil
}

func schemaAcceptsValue(schema map[string]any, value any) bool {
	if alternatives, ok := schema["oneOf"].([]map[string]any); ok {
		for _, alternative := range alternatives {
			if schemaAcceptsValue(alternative, value) {
				return true
			}
		}
		return false
	}
	var types []string
	switch declared := schema["type"].(type) {
	case string:
		types = []string{declared}
	case []string:
		types = declared
	default:
		return true
	}
	for _, declared := range types {
		switch declared {
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
			if _, ok := value.([]string); ok {
				return true
			}
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "integer":
			switch number := value.(type) {
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
				return true
			case float64:
				if !math.IsNaN(number) && !math.IsInf(number, 0) && math.Trunc(number) == number {
					return true
				}
			}
		}
	}
	return false
}

// supportedProtocolVersions are the MCP revisions this server can speak.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
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
// after this server started.
func staleBinaryNote() string {
	if staleBinaryWarned || !binarySnapshotChanged(startupBinarySnapshot) {
		return ""
	}
	staleBinaryWarned = true
	return "⚠ prism was upgraded on disk after this MCP server started (running " +
		version.Version + "). This request was not run with old behavior; the stale server is exiting so the client can respawn it. " +
		"Restart the agent if Prism tools do not reconnect automatically."
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
		instructions := serverInstructions
		if s.compact {
			instructions = compactServerInstructions
		}
		if warning := legacyProjectWarning(s.handler.Root); warning != "" {
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
			return map[string]any{"tools": CompactToolSchemas()}, nil
		}
		return map[string]any{"tools": ToolSchemas()}, nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		if s.compact && call.Name != "prism" {
			return nil, &rpcError{Code: -32601, Message: fmt.Sprintf("tool %q is not exposed by the compact Prism MCP; call prism with op and args", call.Name)}
		}
		if !s.compact && !legacyMCPTool(call.Name) {
			return nil, &rpcError{Code: -32601, Message: fmt.Sprintf("tool %q is not exposed by the Prism MCP", call.Name)}
		}
		actualName := call.Name
		actualArgs := call.Arguments
		if call.Name == "prism" {
			var err error
			actualName, actualArgs, err = expandCompactCall(call.Arguments)
			if err != nil {
				return nil, &rpcError{Code: -32602, Message: err.Error()}
			}
		}
		invokeName := actualName
		if s.compact && actualName == "prism_read" {
			if _, batch := actualArgs["ranges"]; batch {
				invokeName = "prism_read_compact"
			}
		}
		if s.compact && actualName == "prism_query" && (actualArgs["paths"] != nil || actualArgs["glob"] != nil) {
			invokeName = "prism_query_compact"
		}
		out, err := s.handler.Invoke(invokeName, actualArgs)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		if s.compact && actualName == "prism_read" && invokeName == actualName {
			compactSingleReadNote(out, call.Arguments)
		}
		// MCP content is free-form text — JSON is not required, only what
		// this server has always sent by default. prism_search gets a plain
		// grep-style rendering instead: measured 1.19-1.32x fewer bytes for
		// identical hits, on the highest-call-count tool
		// in the system, where the saving is paid back on every later turn
		// the result sits in cache. Falls back to JSON for any shape the
		// renderer does not fully recognise (symbol-bearing results, or an
		// unexpected field) rather than risk dropping content silently.
		var text string
		var rendered bool
		if m, ok := out.(map[string]any); ok {
			switch actualName {
			case "prism_search":
				if s.compact {
					text, rendered = s.handler.RenderCompactSearchText(context.Background(), m, actualArgs)
				} else {
					text, rendered = renderSearchAsText(m)
					if rendered {
						text = s.handler.once.apply(text)
					}
				}
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
		s.handler.Ledger.RecordResult(actualName, ranking.EstimateTokens(text))
		content := []map[string]string{{"type": "text", "text": text}}
		// Stale-context delivery: when any recently delivered file changed
		// on disk, every context-bearing response carries the warning, so
		// the agent learns mid-task instead of at merge time. Cheap probe
		// (bounded hash comparison); prism_drift gives symbol-level detail.
		if contextBearingTool(actualName) {
			if warning := s.handler.StaleContextWarning(); warning != "" {
				content = append(content, map[string]string{"type": "text", "text": warning})
			}
		}
		if note := conflictingInstallationNote(); note != "" {
			content = append(content, map[string]string{"type": "text", "text": note})
		}
		return map[string]any{"content": content}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
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

func compactSearchCanIncludeBodies(args map[string]any) bool {
	if _, explicit := args["context"]; explicit {
		return false
	}
	return !boolArg(args, "files_only") && !boolArg(args, "rollup_only") &&
		!boolArg(args, "exhaustive") &&
		len(stringsArg(args, "query")) > 0
}

// RenderCompactSearchText is the canonical compact search renderer shared by
// MCP and CLI. The input args use the normalized legacy prism_search fields.
func (h *Handler) RenderCompactSearchText(ctx context.Context, out map[string]any, args map[string]any) (string, bool) {
	text, rendered := renderSearchAsText(out)
	if !rendered {
		return "", false
	}
	text = rewriteCompactGuidance(text)
	_, explicitBodies := args["include_bodies"]
	if compactSearchCanIncludeBodies(args) && !explicitBodies {
		bodyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		bodies, delivered := h.compactSearchBodiesPicked(bodyCtx, out)
		if bodies != "" {
			withoutInline := make(map[string]any, len(out))
			for key, value := range out {
				if key != "inlineBodies" {
					withoutInline[key] = value
				}
			}
			text, rendered = renderSearchAsText(withoutInline)
			if !rendered {
				return "", false
			}
			text = rewriteCompactGuidance(text)
			if _, batched := out["results"]; !batched && !strings.Contains(bodies, "other matches remain locators") {
				text = strings.Replace(text, compactSearchLocatorGuidance, "", 1)
			}
			text += bodies
			text += h.compactSearchBodiesEnclosingExcept(bodyCtx, out, delivered)
		}
	}
	return h.once.apply(text), true
}

// compactSearchBodies folds the common search-then-Read pair into one MCP
// turn when a complete, small literal result lands inside a bounded function
// or method. Batched searches may also inline the first term's unique exact
// symbol; other broad, partial, and explicitly shaped hits stay locators.
func (h *Handler) compactSearchBodies(ctx context.Context, out map[string]any) string {
	body, _ := h.compactSearchBodiesPicked(ctx, out)
	return body
}

// compactSearchBodiesPicked also reports the symbols whose source it delivered
// so the default enclosing pass can serve the remaining hits without repeating
// them.
func (h *Handler) compactSearchBodiesPicked(ctx context.Context, out map[string]any) (string, []grove.SymbolRecord) {
	if h.Grove == nil {
		return "", nil
	}
	// A batched search may have many substring hits yet one exact match for
	// its first, most specific term. A qualified query such as "class Foo"
	// can also have one unambiguous small symbol without equalling its name.
	// Deliver that body without implying the other terms were read.
	if results, ok := out["results"].([]map[string]any); ok && len(results) > 0 {
		first := results[0]
		if boolArg(first, "symbolsTruncated") {
			return "", nil
		}
		term, _ := first["query"].(string)
		symbols, _ := first["symbols"].([]map[string]any)
		var exact map[string]any
		for _, item := range symbols {
			if item["qualifiedName"] != term {
				continue
			}
			if exact != nil {
				return "", nil // ambiguous exact name
			}
			exact = item
		}
		selected := exact
		label := "the unique exact-name match in the first batched term; other hits remain locators"
		if selected == nil && len(symbols) == 1 {
			selected = symbols[0]
			label = "the unique small symbol match in the first batched term; other hits remain locators"
		}
		if selected == nil {
			return "", nil
		}
		file, _ := selected["filePath"].(string)
		name, _ := selected["qualifiedName"].(string)
		span, _ := selected["span"].(map[string]any)
		if file == "" || name == "" || span == nil {
			return "", nil
		}
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			return "", nil
		}
		for _, sym := range syms {
			if sym.QualifiedName == name && sym.Span.Start == intArg(span, "start", 0) &&
				sym.Span.End == intArg(span, "end", 0) {
				if exact == nil && (sym.Span.End-sym.Span.Start+1 > 40 || len(sym.RawText) > 2400) {
					return "", nil
				}
				pick := ranking.BudgetedSymbol{Symbol: sym, Score: 1,
					Category: ranking.CategoryTarget, Disclosure: ranking.DisclosureFull}
				return h.renderCompactSearchBodies([]ranking.BudgetedSymbol{pick}, label), []grove.SymbolRecord{sym}
			}
		}
		return "", nil
	}
	if symbols, ok := out["symbols"].([]map[string]any); ok && len(symbols) == 1 && !boolArg(out, "symbolsTruncated") {
		picked := make([]ranking.BudgetedSymbol, 0, len(symbols))
		for _, item := range symbols {
			file, _ := item["filePath"].(string)
			span, _ := item["span"].(map[string]any)
			name, _ := item["qualifiedName"].(string)
			if file == "" || span == nil || name == "" {
				picked = nil
				break
			}
			syms, err := h.Grove.FileSymbols(ctx, file)
			if err != nil {
				picked = nil
				break
			}
			found := false
			for _, sym := range syms {
				if sym.QualifiedName == name && sym.Span.Start == intArg(span, "start", 0) &&
					sym.Span.End == intArg(span, "end", 0) {
					picked = append(picked, ranking.BudgetedSymbol{Symbol: sym, Score: 1,
						Category: ranking.CategoryTarget, Disclosure: ranking.DisclosureFull})
					found = true
					break
				}
			}
			if !found {
				picked = nil
				break
			}
		}
		if len(picked) == len(symbols) {
			if body := h.renderCompactSearchBodies(picked); body != "" {
				return body, budgetedSymbols(picked)
			}
		}
	}
	if !boolArg(out, "resultsComplete") {
		return "", nil
	}
	groups, ok := out["textHits"].([]map[string]any)
	if !ok {
		return "", nil
	}
	totalHits := 0
	for _, group := range groups {
		totalHits += len(anySlice(group["hits"]))
	}
	if totalHits != 1 {
		return "", nil
	}

	seen := map[string]bool{}
	var picked []ranking.BudgetedSymbol
	for _, group := range groups {
		file, _ := group["file"].(string)
		if file == "" {
			continue
		}
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			continue
		}
		for _, raw := range anySlice(group["hits"]) {
			hit, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			line := intArg(hit, "line", 0)
			sym := tightestEnclosingSymbol(syms, line)
			if sym == nil || (sym.Kind != "function" && sym.Kind != "method") ||
				sym.Span.End-sym.Span.Start+1 > 160 || len(sym.RawText) > 10000 {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d", sym.FilePath, sym.Span.Start, sym.Span.End)
			if seen[key] {
				continue
			}
			seen[key] = true
			picked = append(picked, ranking.BudgetedSymbol{
				Symbol: *sym, Score: 1, Category: ranking.CategoryTarget,
				Disclosure: ranking.DisclosureFull,
			})
		}
	}
	if len(picked) == 0 || len(picked) > 2 {
		return "", nil
	}

	return h.renderCompactSearchBodies(picked), budgetedSymbols(picked)
}

func budgetedSymbols(picked []ranking.BudgetedSymbol) []grove.SymbolRecord {
	syms := make([]grove.SymbolRecord, 0, len(picked))
	for _, item := range picked {
		syms = append(syms, item.Symbol)
	}
	return syms
}

type searchSourceRegion struct {
	file         string
	hit          int
	start        int
	end          int
	symbol       grove.SymbolRecord
	hasSymbol    bool
	window       bool
	evidence     string
	ownerContext bool
}

// compactSearchBodiesEnclosing delivers bounded source for visible file:line
// hits even when the enclosing symbol is too large for a full body. The
// inventory's own completeness and scope labels remain intact.
func (h *Handler) compactSearchBodiesEnclosing(ctx context.Context, out map[string]any) string {
	return h.compactSearchBodiesEnclosingExcept(ctx, out, nil)
}

// compactSearchBodiesEnclosingExcept is compactSearchBodiesEnclosing with
// symbols already delivered by another rule left out.
//
// Allocation: a batched query is several questions, so every term gets one
// region before any term gets a second (a live click cell batched four terms
// and only the first was ever served). A single term keeps its two regions.
// Slots count delivered regions, not visited candidates: a symbol reached
// through both its text hit and its symbols entry must not consume two slots.
func (h *Handler) compactSearchBodiesLegacy(ctx context.Context, out map[string]any, skip []grove.SymbolRecord) string {
	if h.Grove == nil {
		return ""
	}
	const maxBodyLines = searchFullBodyMaxLines
	const maxBodyBytes = searchFullBodyMaxBytes
	const perTermRegions = 2
	const maxRegions = 4
	const maxVisited = 128
	files := map[string][]grove.SymbolRecord{}
	var picked []searchSourceRegion
	visitedHits := 0
	full := func() bool { return len(picked) >= maxRegions || visitedHits >= maxVisited }
	skipped := func(file string, start, end int) bool {
		for _, sym := range skip {
			if sym.FilePath == file && sym.Span.Start <= start && end <= sym.Span.End {
				return true
			}
		}
		return false
	}
	fileSymbols := func(file string) []grove.SymbolRecord {
		if file == "" {
			return nil
		}
		if syms, ok := files[file]; ok {
			return syms
		}
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			syms = nil
		}
		files[file] = syms
		return syms
	}
	// add reports whether a new region was picked for this hit.
	add := func(file string, hit int, sym *grove.SymbolRecord) bool {
		if file == "" || hit < 1 || full() {
			return false
		}
		visitedHits++
		region := searchSourceRegion{file: file, hit: hit}
		if sym != nil && sym.Span.Start > 0 && sym.Span.End >= sym.Span.Start {
			region.symbol, region.hasSymbol = *sym, true
			if sym.Span.End-sym.Span.Start+1 <= maxBodyLines && len(sym.RawText) <= maxBodyBytes {
				region.start, region.end = sym.Span.Start, sym.Span.End
			} else {
				region.window = true
				region.start = maxInt(sym.Span.Start, hit-50)
				region.end = minInt(sym.Span.End, hit+50)
			}
		} else {
			region.window = true
			region.start = maxInt(1, hit-50)
			region.end = hit + 50
		}
		if skipped(file, hit, hit) {
			return false // delivered by the small-result rule already
		}
		for _, selected := range picked {
			if selected.file == file && region.start <= selected.end && selected.start <= region.end {
				if hit >= selected.start && hit <= selected.end {
					return false // this hit's source was already delivered
				}
				// Nearby windows can overlap while the second hit itself is
				// still outside the first. Trim only the repeated part.
				region.window = true
				if hit > selected.end {
					region.start = selected.end + 1
				} else {
					region.end = selected.start - 1
				}
			}
		}
		if region.start > region.end || hit < region.start || hit > region.end {
			return false
		}
		picked = append(picked, region)
		return true
	}
	// visit picks up to allow regions from one term's result.
	visit := func(result map[string]any, allow int, productionOnly bool) int {
		taken := 0
		for _, group := range anySlice(result["textHits"]) {
			g, ok := group.(map[string]any)
			if !ok {
				continue
			}
			file, _ := g["file"].(string)
			if productionOnly && !isProductionSourcePath(file) {
				continue
			}
			for _, raw := range anySlice(g["hits"]) {
				hit, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				line := intArg(hit, "line", 0)
				if add(file, line, tightestEnclosingSymbol(fileSymbols(file), line)) {
					taken++
				}
				if taken >= allow || full() {
					return taken
				}
			}
		}
		for _, item := range anySlice(result["symbols"]) {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			file, _ := entry["filePath"].(string)
			if productionOnly && !isProductionSourcePath(file) {
				continue
			}
			name, _ := entry["qualifiedName"].(string)
			span, _ := entry["span"].(map[string]any)
			line := intArg(span, "start", 0)
			var matched *grove.SymbolRecord
			for i := range fileSymbols(file) {
				sym := &files[file][i]
				if sym.QualifiedName == name && span != nil &&
					sym.Span.Start == line && sym.Span.End == intArg(span, "end", 0) {
					matched = sym
					break
				}
			}
			if add(file, line, matched) {
				taken++
			}
			if taken >= allow || full() {
				return taken
			}
		}
		return taken
	}
	if groups, ok := out["results"].([]map[string]any); ok {
		served := make([]int, len(groups))
		for _, productionOnly := range []bool{true, false} {
			for pass := 1; pass <= perTermRegions && !full(); pass++ {
				for i, group := range groups {
					if served[i] < pass {
						served[i] += visit(group, pass-served[i], productionOnly)
					}
					if full() {
						break
					}
				}
			}
			if full() {
				break
			}
		}
	} else {
		visit(out, perTermRegions, true)
		if !full() && len(picked) < perTermRegions {
			visit(out, perTermRegions-len(picked), false)
		}
	}
	if len(picked) == 0 {
		return ""
	}
	return h.renderEnclosingSearchBodies(picked)
}

// renderEnclosingSearchBodies uses exact source spans. Oversized symbols are
// represented by labeled windows rather than silently disappearing.
func (h *Handler) renderEnclosingSearchBodies(picked []searchSourceRegion) string {
	const budget = 2400
	var sections []string
	var commits []func()
	tokens := 0
	for _, region := range picked {
		data, err := os.ReadFile(filepath.Join(h.Root, filepath.FromSlash(region.file)))
		if err != nil {
			continue
		}
		content := string(data)
		lines := strings.Split(content, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if region.hit > len(lines) || region.start < 1 {
			continue
		}
		start, end, isWindow := region.start, minInt(region.end, len(lines)), region.window
		if end < start {
			continue
		}
		fullBodyLines := 160
		if region.ownerContext {
			fullBodyLines = 240
		}
		if !isWindow && (end-start+1 > fullBodyLines || len(strings.Join(lines[start-1:end], "\n")) > 10000) {
			isWindow = true
			start = maxInt(region.symbol.Span.Start, region.hit-50)
			end = minInt(len(lines), minInt(region.symbol.Span.End, region.hit+50))
		}
		for start <= end {
			var b strings.Builder
			fmt.Fprintf(&b, "**`%s`**\n", region.file)
			if isWindow {
				if region.hasSymbol {
					fmt.Fprintf(&b, "// WINDOW %s:%d-%d around hit line %d; enclosing %s spans %d-%d (full body not included)\n",
						region.file, start, end, region.hit, region.symbol.QualifiedName,
						region.symbol.Span.Start, region.symbol.Span.End)
				} else {
					fmt.Fprintf(&b, "// WINDOW %s:%d-%d around hit line %d; no indexed enclosing symbol for this hit\n",
						region.file, start, end, region.hit)
				}
			} else {
				fmt.Fprintf(&b, "// Full enclosing body %s spans %d-%d\n",
					region.symbol.QualifiedName, region.symbol.Span.Start, region.symbol.Span.End)
			}
			if region.evidence != "" {
				fmt.Fprintf(&b, "// Evidence: %s\n", region.evidence)
			}
			fmt.Fprintf(&b, "\n```%s\n", langTag(region.file))
			for line := start; line <= end; line++ {
				fmt.Fprintf(&b, "%d\t%s\n", line, clampSourceLine(lines[line-1]))
			}
			b.WriteString("```\n\n")
			section := b.String()
			if len(section) <= 10000 && tokens+ranking.EstimateTokens(section) <= budget {
				tokens += ranking.EstimateTokens(section)
				sections = append(sections, section)
				path, hash, delivered := region.file, compression.Hash(content), lineWindow{start: start, end: end}
				commits = append(commits, func() {
					h.recordDeliveredRanges(path, hash, []lineWindow{delivered})
				})
				break
			}
			if !isWindow {
				isWindow = true
				start = maxInt(region.symbol.Span.Start, region.hit-50)
				end = minInt(len(lines), minInt(region.symbol.Span.End, region.hit+50))
				continue
			}
			if end-start+1 <= 20 {
				break // a smaller window is not worth a slot; leave the hit as a locator
			}
			if region.hit-start >= end-region.hit {
				start++
			} else {
				end--
			}
		}
	}
	if len(sections) == 0 {
		return ""
	}
	for _, commit := range commits {
		commit()
	}
	return "\n// Exact source for bounded enclosing hits selected by evidence rank; other matches remain locators, and inventory completeness is reported above:\n" +
		strings.Join(sections, "")
}

func (h *Handler) renderCompactSearchBodies(picked []ranking.BudgetedSymbol, note ...string) string {
	const budget = 4000
	for _, item := range picked {
		if item.Symbol.Span.End-item.Symbol.Span.Start+1 > 160 || len(item.Symbol.RawText) > 10000 {
			return ""
		}
	}
	var sections []string
	var commits []func()
	tokens := 0
	for _, group := range groupPickedByFile(picked) {
		section, commit, ok := h.renderFileSection(group)
		if !ok {
			return ""
		}
		tokens += ranking.EstimateTokens(section)
		if tokens > budget {
			return ""
		}
		sections = append(sections, section)
		commits = append(commits, commit)
	}
	if len(sections) == 0 {
		return ""
	}
	for _, commit := range commits {
		commit()
	}
	label := "this small, complete locator result"
	if len(note) > 0 {
		label = note[0]
	}
	return "\n// Exact source for " + label + " (already read; do not fetch it again):\n" + strings.Join(sections, "")
}

// contextBearingTool reports whether a tool delivers code context the agent
// may go on to rely on — the calls worth annotating with staleness warnings.
func contextBearingTool(name string) bool {
	switch name {
	case "prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_node",                // symbol bodies + edge file:lines, both index-derived
		"prism_rename_plan",         // its edits carry index-derived line numbers — stale index means wrong-line edits applied verbatim
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

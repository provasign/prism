package core

import (
	"context"
	"encoding/json"

	"github.com/provasign/prism/internal/ranking"
)

// ToolAnswer is one tool call's reply as the text blocks a client shows the
// model. IsError marks a tool failure, which the model reads as a result.
type ToolAnswer struct {
	Texts   []string
	IsError bool
}

// CallTool answers one tool call the way the MCP server delivers it: the
// compact "prism" envelope is expanded, the tool runs, and its result is
// rendered as text with path notes, the stale-context warning and the
// installation note attached. compact selects the compact gateway's routing
// and wording. The error is a malformed call (an unknown op or field), which
// the transport reports as invalid params; the JSON-RPC layer around this is
// internal/mcp (ENG-01 step 3).
func (h *Handler) CallTool(ctx context.Context, compact bool, name string, args map[string]any) (ToolAnswer, error) {
	actualName := name
	actualArgs := args
	if name == "prism" {
		var err error
		actualName, actualArgs, err = expandCompactCall(args)
		if err != nil {
			return ToolAnswer{}, err
		}
	}
	invokeName := actualName
	if compact && actualName == "prism_read" {
		if _, batch := actualArgs["ranges"]; batch {
			invokeName = "prism_read_compact"
		}
	}
	if compact && actualName == "prism_query" && (actualArgs["paths"] != nil || actualArgs["glob"] != nil) {
		invokeName = "prism_query_compact"
	}
	pathNotes := resolveMissingArgPaths(h.Root, invokeName, actualArgs)
	out, err := h.InvokeContext(ctx, invokeName, actualArgs)
	if err != nil {
		// A tool failure is a result the model reads, not a protocol
		// error (MCP spec; SEC-05). -32xxx stays for protocol errors.
		return ToolAnswer{Texts: []string{err.Error()}, IsError: true}, nil
	}
	if compact && actualName == "prism_read" && invokeName == actualName {
		compactSingleReadNote(out, args)
	}
	// MCP content is free-form text — JSON is not required, only what
	// this server has always sent by default. prism_search gets a plain
	// grep-style rendering instead: measured 1.19-1.32x fewer bytes for
	// identical hits, on the highest-call-count tool
	// in the system, where the saving is paid back on every later turn
	// the result sits in cache. Falls back to JSON for any shape the
	// renderer does not fully recognise (symbol-bearing results, or an
	// unexpected field) rather than risk dropping content silently.
	text, rendered := h.RenderToolText(compact, actualName, actualArgs, out)
	if !rendered {
		// Compact JSON: results land in an agent's context window, and
		// indentation is pure token overhead.
		encoded, _ := json.Marshal(out)
		text = string(encoded)
	}
	for i := len(pathNotes) - 1; i >= 0; i-- {
		text = "// " + pathNotes[i] + "\n" + text
	}
	// Result-size accounting: this is the number that compounds via
	// cache re-reads on every later turn (measured: median 4.4x, mean
	// 11x effective multiplier across real sessions).
	h.Ledger.RecordResult(actualName, ranking.EstimateTokens(text))
	texts := []string{text}
	// Stale-context delivery: when any recently delivered file changed
	// on disk, every context-bearing response carries the warning, so
	// the agent learns mid-task instead of at merge time. Cheap probe
	// (bounded hash comparison); prism_drift gives symbol-level detail.
	if contextBearingTool(actualName) {
		if warning := h.StaleContextWarning(); warning != "" {
			if compact {
				warning = compactToolWording(warning)
			}
			texts = append(texts, warning)
		}
	}
	if note := conflictingInstallationNote(); note != "" {
		texts = append(texts, note)
	}
	return ToolAnswer{Texts: texts}, nil
}

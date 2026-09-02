package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/textsearch"
	"regexp"
)

// Handler holds the shared backend state used by the prism_* tools.
type Handler struct {
	Cfg     *config.Config
	Root    string
	Grove   *grove.Client
	Session *session.Tracker
	Ledger  *session.Ledger
	Signals *ranking.SignalComputer
	Weights *ranking.LearnedWeights // A: per-repo outcome-conditioned weights

	// driftBase records the symbols delivered with each full file read this
	// session, so prism_drift can diff structurally (renames, breaking
	// changes) via Grove's GraphDiff instead of comparing hashes.
	driftMu   sync.Mutex
	driftBase map[string][]grove.SymbolRecord

	// readyCh is closed when the background Grove connection + initial index
	// completes. Nil means no deferred init (Grove is already ready).
	readyCh <-chan struct{}

	// Feedback store (in-memory; persisted across MCP calls in one session).
	fbMu     sync.Mutex
	feedback []FeedbackEntry
}

// NewHandler constructs a handler with sensible defaults.
func NewHandler(cfg *config.Config, root string, client *grove.Client) *Handler {
	return NewHandlerWithLedger(cfg, root, client, nil)
}

// NewHandlerWithReady constructs a handler that defers the Grove connection.
// readyCh must be closed by the caller once Grove is reachable and indexed;
// Invoke will block until then (or until its own 60-second timeout fires).
func NewHandlerWithReady(cfg *config.Config, root string, client *grove.Client, readyCh <-chan struct{}) *Handler {
	h := NewHandlerWithLedger(cfg, root, client, nil)
	h.readyCh = readyCh
	return h
}

// NewHandlerWithLedger constructs a handler and optionally reuses an existing ledger.
func NewHandlerWithLedger(cfg *config.Config, root string, client *grove.Client, ledger *session.Ledger) *Handler {
	tr := session.NewTracker(cfg.MaxCacheFiles)
	if ledger == nil {
		ledger = session.NewLedger(time.Now().Format("20060102-150405"))
	}
	h := &Handler{
		Cfg:       cfg,
		Root:      root,
		Grove:     client,
		Session:   tr,
		Ledger:    ledger,
		driftBase: map[string][]grove.SymbolRecord{},
		Weights:   ranking.LoadLearnedWeights(root), // A: load per-repo learned weights
	}
	h.Signals = ranking.NewSignalComputer(root)
	return h
}

// confidenceFor estimates whether previously delivered content for entry is
// still visible in the agent's window. The ledger delta only counts Prism's
// own deliveries; when the agent reported context_used both now and at send
// time, the larger of the two deltas wins — the agent's own count sees
// tokens Prism never delivered (shell output, edits, other servers).
func (h *Handler) confidenceFor(entry *session.Entry, contextUsed int64, window int) session.Confidence {
	tokensSince := h.Ledger.TotalDeliveredTokens() - entry.TokenDistanceAtSend
	if tokensSince < 0 {
		tokensSince = 0
	}
	if contextUsed > 0 && entry.ContextUsedAtSend > 0 {
		if d := contextUsed - entry.ContextUsedAtSend; d > tokensSince {
			tokensSince = d
		}
	}
	return session.EstimateConfidence(tokensSince, window)
}

// setDriftBase records the symbols delivered with a full read of file, the
// structural baseline prism_drift diffs against.
func (h *Handler) setDriftBase(file string, syms []grove.SymbolRecord) {
	h.driftMu.Lock()
	h.driftBase[file] = syms
	h.driftMu.Unlock()
}

// driftBaseFor returns the delivered-symbol baseline for file, if any.
func (h *Handler) driftBaseFor(file string) []grove.SymbolRecord {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	return h.driftBase[file]
}

// FeedbackEntry is one user rating of a tool response.
type FeedbackEntry struct {
	Tool      string `json:"tool"`
	QueryID   string `json:"queryId,omitempty"`
	Rating    int    `json:"rating"`
	Notes     string `json:"notes,omitempty"`
	Timestamp string `json:"timestamp"`
}

// --- Tool dispatch -------------------------------------------------------

// DispatchableTools returns every tool name Invoke can route — the single
// source of truth for surfaces that expose tools by name (the HTTP server).
// The httpapi route list was maintained by hand and drifted so far that six
// tools 404'd for months; anything that needs "all tools" derives from this.
func DispatchableTools() []string {
	return []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_node", "prism_references", "prism_resolve", "prism_edges",
		"prism_change_impact", "prism_missing_implementations",
		"prism_dead_code", "prism_rename_plan",
		"prism_map", "prism_cycles", "prism_verify", "prism_arch_check",
		"prism_index", "prism_drift",
		"prism_compact", "prism_savings", "prism_feedback",
	}
}

// Invoke routes a tools/call to the right handler.
func (h *Handler) Invoke(name string, args map[string]any) (out any, err error) {
	// A panicking tool must fail THAT CALL, not the process: the stdio MCP
	// server is the agent's whole session, and one nil-pointer in one tool
	// killed all of it (found by the dispatch-pinning test — toolDeadCode
	// panics on a Grove-less handler rather than erroring).
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%s: internal error: %v", name, r)
		}
	}()
	// Most tools are interactive-latency operations; 60s is a generous cap.
	// The whole-repo operations need the same headroom prism_index already
	// got: verify now delta-reindexes first and then runs a per-seed impact
	// walk, and the unified task tool does prepare+verify in one call, so on
	// a monorepo both can exceed a minute legitimately. Timing out mid-verify
	// surfaces as a tool error, which is the CI gate failing for the wrong
	// reason.
	timeout := 60 * time.Second
	switch name {
	case "prism_verify", "prism_index", "prism_map", "prism_cycles", "prism_dead_code":
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// In MCP mode, Grove connection and initial index run in the background so
	// the MCP handshake (initialize / tools/list) can complete immediately.
	// Wait here until Grove is ready before dispatching any tool call.
	if h.readyCh != nil {
		select {
		case <-h.readyCh:
		case <-ctx.Done():
			return nil, errors.New("timed out waiting for Grove to become ready")
		}
	}
	// Every tool — including prism_index — resolves against the root the
	// server was started with; the Grove client and its store are bound to it.
	// A different "dir" used to be silently ignored, producing empty results —
	// and for prism_index it was worse: the engine walked the foreign tree
	// while writing into this root's database, pruning every record the other
	// tree didn't contain. Reject it loudly instead.
	if dir := stringArg(args, "dir", ""); dir != "" && !sameRoot(dir, h.Root) {
		return nil, fmt.Errorf("server is rooted at %s and cannot serve dir %s; restart with `prism mcp %s` or run the prism CLI from that directory", h.Root, dir, dir)
	}
	switch name {
	case "prism_query":
		return h.toolQuery(ctx, args)
	case "prism_read":
		return h.toolRead(ctx, args)
	case "prism_search":
		return h.toolSearch(ctx, args)
	case "prism_lookup":
		return h.toolLookup(ctx, args)
	case "prism_index":
		return h.toolIndex(ctx, args)
	case "prism_compact":
		return h.toolCompact(ctx, args)
	case "prism_savings":
		return h.toolSavings(ctx, args)
	case "prism_feedback":
		return h.toolFeedback(ctx, args)
	case "prism_drift":
		return h.toolDrift(ctx, args)
	case "prism_references":
		return h.toolReferences(ctx, args)
	case "prism_resolve":
		return h.toolResolve(ctx, args)
	case "prism_edges":
		return h.toolEdges(ctx, args)
	// The whole-repo graph tools delta-reindex FIRST, for the same reason
	// toolVerify does: a stale index silently computes yesterday's blast
	// radius (measured live: an added caller was invisible to change_impact
	// until reindex — and the delivery dedupe then pointered the stale
	// result as "recomputed"). Delta indexing is cheap; trusting stale data
	// on the tools agents PLAN edits with is not.
	case "prism_change_impact":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolChangeImpact(ctx, args))
	case "prism_missing_implementations":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolMissingImplementations(ctx, args))
	case "prism_node":
		return h.toolNode(ctx, args)
	case "prism_dead_code":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolDeadCode(ctx, args))
	case "prism_rename_plan":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolRenamePlan(ctx, args))
	case "prism_map":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolMap(ctx, args))
	case "prism_cycles":
		stale := h.refreshIndexBestEffort(ctx)
		return h.graphDelivery(name, args, stale)(h.toolCycles(ctx, args))
	case "prism_arch_check":
		// A gate must not validate rules against yesterday's graph: refresh
		// first (best-effort) and surface a failed refresh on the verdict.
		stale := h.refreshIndexBestEffort(ctx)
		res, err := h.toolArchCheck(ctx, args)
		if err != nil {
			return nil, err
		}
		return attachStaleWarning(res, stale), nil
	case "prism_verify":
		return h.toolVerify(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// sameRoot reports whether dir and root name the same directory once both are
// absolute, cleaned, and symlink-resolved (macOS aliases /var to /private/var,
// which must not read as a mismatch).
func sameRoot(dir, root string) bool {
	a, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	b, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		b = r
	}
	return a == b
}

// ToolSchemas returns the schema list for tools/list.
func ToolSchemas() []map[string]any {
	// The agent-facing surface, cut to six (2026-08-15). The 190-cell paired
	// A/B in research/harness/runs/swebench-live measured which of the
	// fourteen advertised tools agents actually reach for: search 95/190,
	// read 53, lookup 29, query 35, change_impact 2 — and map, dead_code,
	// rename_plan, missing_implementations, arch_check, node and index at
	// ZERO calls across all 190 cells. Eight tools were paying ~9.4 KB of
	// schema per session to never be called, and a long menu measurably
	// mis-routes the ones that are.
	//
	// So: the four measured-routing tools, plus change_impact (rarely
	// reached but carrying the whole concentrated win — 4.2 turns/$0.27 vs
	// grep's 26.8/$1.66, RESULTS.md §9.1) and verify (the pre-commit
	// completeness gate, 258 bytes).
	//
	// Everything dropped is still a CLI command and still Invoke-able over
	// HTTP — this narrows the agent menu, not the product. Do not re-add a
	// tool here without call-count evidence that agents reach for it.
	names := []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_change_impact", "prism_verify",
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		entry := map[string]any{
			"name":        n,
			"description": toolDescription(n),
			"inputSchema": toolSchema(n),
		}
		// NO RESIDENCY (2026-09-01): nothing is always-loaded; all six stay
		// deferred behind the ToolSearch hop. The 2026-08-30 hybrid (query
		// alone resident) backfired on the wide-change bed: across every
		// transcript with all six resident (v0.55.10 cells) sonnet's entry
		// point was prism_search — 23 search + 5 read calls, prism_query
		// ZERO — so residency backed the one tool agents never open with.
		// Worse, the visible prism_query masked deferral: steering's "if
		// you do not see prism_* they are DEFERRED" clause never fired
		// because one prism tool WAS visible, and 8/8 wide prism cells +
		// a post-fix probe made zero prism calls and zero ToolSearch hops.
		// With nothing resident the deferred-tools clause is unambiguous
		// (no prism_* visible at all -> hop), which is the one mechanism
		// measured to gate usage (48 e2e sessions: 25/25 that hopped used
		// prism, 23/23 that didn't used none). Do not re-add residency for
		// any tool without call-count evidence that agents actually open
		// with THAT tool when it is resident.
		out = append(out, entry)
	}
	return out
}

// modelProp is the shared "model" property injected into prism_query and
// prism_read. Agents must pass their current model ID so Prism can correctly
// size the context budget and session confidence thresholds.
var modelProp = map[string]any{
	"type":        "string",
	"description": "Your model ID (e.g. \"claude-sonnet-4-6\", \"gpt-4o\"). Sizes context budgets. Optional.",
}

// contextUsedProp lets agents report how many tokens their context window
// already holds. Prism's ledger only sees its own deliveries; this hint
// keeps re-read confidence honest when most context flows through other
// tools (shell output, edits, other MCP servers).
var contextUsedProp = map[string]any{
	"type":        "integer",
	"description": "Tokens currently in your context window. Improves re-read confidence. Optional.",
}

func toolSchema(name string) map[string]any {
	open := map[string]any{"type": "object", "additionalProperties": true}
	switch name {
	case "prism_query":
		return map[string]any{
			"type":     "object",
			"required": []string{"task", "terms"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "What you are trying to do. A label for the response header — it does not affect retrieval, ranking or sizing.",
				},
				"terms": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "REQUIRED: search terms (e.g. [\"AccessCount\"]), expanded via the call " +
						"graph. No name yet? Guess ONE keyword — there is no no-terms fallback.",
				},
				"include": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": []string{"graph", "docs"}},
					"description": "Categories: graph (callers/callees), docs (filenames only). Default: [\"graph\"].",
				},
				"delivery": map[string]any{
					"type":        "string",
					"enum":        []string{"source", "symbols"},
					"description": "source = line-numbered windows + callers (edit-ready); symbols = compact list. Default: source for bug-fix/implement tasks.",
				},
				"max_files": map[string]any{
					"type":        "integer",
					"description": "source delivery only: max files shown as windows (rest listed by name). Default 5.",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
				"profile":      map[string]any{"type": "string", "description": "Ranking profile: default|implement_feature|fix_bug|code_review"},
				"budget":       map[string]any{"type": "integer", "description": "Token budget, honored exactly. Default 8000 for every task."},
				"limit":        map[string]any{"type": "integer", "description": "Max candidate symbols considered before ranking/budget cutoff. Default 50."},
			},
		}
	case "prism_read":
		return map[string]any{
			"type":     "object",
			"required": []string{"file"},
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "File path relative to project root.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "First line to return (1-based). Use with limit for an exact window instead of pulling a whole file to see part of it.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "How many lines to return from offset. Omit both for the whole file.",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
				"task":         map[string]any{"type": "string", "description": "Current task, used for relevance ranking."},
			},
		}
	case "prism_search":
		return map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        []string{"string", "array"},
					"items":       map[string]any{"type": "string"},
					"description": "One term or an array of up to 10, searched in one call — batch them. A regular expression when regex=true.",
				},
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{"both", "text", "symbols"},
					"description": "\"text\" = pure grep, cheapest. \"symbols\" = indexed symbols only. Default \"both\".",
				},
				"regex": map[string]any{
					"type":        "boolean",
					"description": "Treat query as a regex for the text pass (invalid patterns fall back to literal).",
				},
				"path": map[string]any{
					"type":        []string{"string", "array"},
					"items":       map[string]any{"type": "string"},
					"description": "Restrict to these repo-relative files or directories (e.g. \"src/manager.py\" or [\"src/\",\"tests/\"]).",
				},
				"glob": map[string]any{
					"type":        []string{"string", "array"},
					"items":       map[string]any{"type": "string"},
					"description": "Only search files matching these globs, e.g. \"*.py\" (grep --include / rg --glob).",
				},
				"exhaustive": map[string]any{
					"type":        "boolean",
					"description": "Return EVERY match, uncapped — required for completeness questions (\"rewrite every call site\"), where a capped answer looks complete and is not. Pair with files_only or path=.",
				},
				"files_only": map[string]any{
					"type":        "boolean",
					"description": "Return matching file paths without the matching lines — the cheapest answer to \"where does this live\".",
				},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 25)."},
				"context": map[string]any{
					"type":        "integer",
					"description": "Lines of surrounding source on each side of a match (grep -C N) — instead of a follow-up read. Clamped to 15. Guessing a large number to capture a whole named function/class? Use prism_lookup(name) instead — the exact body, no guessing.",
				},
			},
		}
	case "prism_lookup":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Symbol name, optionally package-qualified ('internal/cli.Run' or bare 'Run').",
				},
				"fields": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": []string{"signature", "doc", "body", "kind", "parent", "modifiers"}},
					"description": "Which columns to read. Omit for the full body. e.g. [signature] for just the contract.",
				},
				"file": map[string]any{
					"type":        "string",
					"description": "Disambiguate a name shared across packages: file path (or substring, as shown in prism_search results).",
				},
			},
		}
	case "prism_references":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Symbol name to find usages of (a class/type/function/constant).",
				},
			},
		}
	case "prism_resolve":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Bare or qualified name (e.g. \"Get\" or \"SecretsKVStoreSQL.Get\").",
				},
			},
		}
	case "prism_edges":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Seed symbol (bare or Type.Method)."},
				"direction": map[string]any{"type": "string", "enum": []string{"out", "in", "both"}, "description": "out = edges from the seed (callees, uses-type); in = edges into it (callers, tests). Default both."},
				"kinds":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"calls", "uses-type", "implements", "extends", "overrides", "contains", "defines", "imports"}}, "description": "Edge kinds to return. Default [calls]."},
			},
		}
	case "prism_index":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir": map[string]any{"type": "string", "description": "Directory to index (default: project root)."},
			},
		}
	case "prism_drift":
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	case "prism_node":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "A symbol name (e.g. \"JsonNode.get\") OR a repo-relative file path (e.g. \"internal/store/store.go\").",
				},
				"file": map[string]any{
					"type":        "string",
					"description": "Optional disambiguator when several symbols share the name: the file path the intended one lives in.",
				},
			},
		}
	case "prism_change_impact", "prism_missing_implementations":
		return map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Type.method or Type.method(ParamType, ...) — e.g. \"JsonSerializer.serialize(T, JsonGenerator, SerializerProvider)\". A bare member name (\"serialize\") or file:line (\"src/Foo.java:120\") also works when it resolves to exactly one symbol; if several match, the error lists the candidates.",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
			},
		}
	case "prism_rename_plan":
		return map[string]any{
			"type":     "object",
			"required": []string{"query", "newName"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Type.method or Type.method(ParamType, ...) — the member being renamed.",
				},
				"newName": map[string]any{
					"type":        "string",
					"description": "The new member name (bare identifier).",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
			},
		}
	case "prism_dead_code":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"roots": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Extra entry-point symbol names beyond the defaults (main/init, tests, exported symbols) — e.g. framework hooks registered by name.",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
			},
		}
	case "prism_verify":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"base": map[string]any{
					"type":        "string",
					"description": "Git ref to diff the working tree against (default \"HEAD\"). The change-set is computed relative to this.",
				},
			},
		}
	case "prism_arch_check":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"base": map[string]any{
					"type":        "string",
					"description": "Optional git ref: report only violations INTRODUCED since it, instead of every current violation.",
				},
			},
		}
	case "prism_cycles":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"depth": map[string]any{
					"type":        "integer",
					"description": "Truncate components to the first N path segments before cycle detection (0 = one component per directory).",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
			},
		}
	case "prism_map":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"depth": map[string]any{
					"type":        "integer",
					"description": "Truncate components to the first N path segments (0 = one component per directory).",
				},
				"max_sites": map[string]any{
					"type":        "integer",
					"description": "Constituent sites kept per induced edge (default 5); weight always carries the full count.",
				},
				"component": map[string]any{
					"type":        "string",
					"description": "Only return edges touching this component.",
				},
				"include_tests": map[string]any{
					"type":        "boolean",
					"description": "Include test files (excluded by default — the map shows the production shape; the result reports how many were excluded).",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
				"from": map[string]any{
					"type":        "string",
					"description": "With to: expand one induced edge into its FULL constituent site list.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "With from: the target component of the edge to expand.",
				},
			},
		}
	default:
		return open
	}
}

func toolDescription(name string) string {
	switch name {
	case "prism_query":
		return "Edit-ready context for the symbols named in terms=[...] — the ONLY retrieval key " +
			"(task is a label): finds them, expands one hop through the call graph, adds a " +
			"full-text pass, returns line-numbered source windows plus callers. No separate grep " +
			"needed; do not re-read the files it shows. Size with budget= and max_files=. " +
			"To merely locate something, use prism_search."
	case "prism_read":
		return "Read a file, whole or by line range (offset/limit — the `sed -n A,Bp` shape, " +
			"line-numbered). A repeat read of an UNCHANGED file returns a one-line " +
			"`// [prism:cached]` pointer instead of the body — not an error: use the copy you " +
			"already have. For a single function use prism_lookup."
	case "prism_search":
		return "Locate things: symbol names/signatures/docstrings AND raw source text (real " +
			"rg/grep) in one call. query = one term or an array of up to 10 — batch what you know " +
			"you need. scope=\"text\" is pure grep, cheapest — use it wherever you would run " +
			"grep/rg (regex=true for patterns). Narrow with path=/glob=/files_only, exactly the " +
			"scoping you would write after a grep pattern. context=N adds surrounding lines " +
			"(grep -C) in the same call — no follow-up read. exhaustive=true lifts the hit cap " +
			"for completeness questions. Pure-text results are plain `path:line: text` lines, " +
			"not JSON."
	case "prism_lookup":
		return "Read one symbol by qualified name (e.g. 'ranking.Select', 'kvstore.Store.Get'). " +
			"fields=[...] narrows to signature/doc/body/kind/parent/modifiers; omit it for the whole " +
			"body. The returned file:line is authoritative — go straight there, do not re-grep."
	case "prism_resolve":
		return "Disambiguate a name you ALREADY HAVE into the symbol(s) it could be — each with kind and " +
			"exact file:line, test doubles tagged and last. Then prism_edges/prism_lookup the one you want. " +
			"The file:line is AUTHORITATIVE — trust it, don't re-grep to verify. " +
			"NOTE: resolve does not DISCOVER. If you don't yet know a symbol name (you only have a concept, " +
			"like 'where a secret is read'), first FIND the anchor with grep/prism_search/prism_references, " +
			"then resolve/traverse from it. Never guess names by trying resolve repeatedly."
	case "prism_edges":
		return "Walk the code graph one hop from a symbol. The graph has these edge kinds: " +
			"calls (X calls Y), uses-type (X mentions a type), " +
			"implements/extends/overrides, contains, defines, imports. direction=out gives edges FROM " +
			"the seed (its callees, the types it uses); direction=in gives edges INTO it (its " +
			"callers). Recipes: what does X call → (out, [calls]); who calls X → (in, [calls]); " +
			"interface dispatch resolves: (out, [calls]) returns the " +
			"implementors actually called. Results are grouped by '<kind> <direction>' and capped with a " +
			"true total. Each neighbor's file:line is AUTHORITATIVE — trust it, don't re-grep to verify. " +
			"This is the precise primitive — prefer it over prism_query when you know the anchor."
	case "prism_references":
		return "Find where a symbol (class/type/function/constant) is USED across the codebase — " +
			"every code occurrence of the name, grouped by file, excluding comments and strings. " +
			"Use for 'where is X used' and 'is X still used / safe to delete'. " +
			"Reports 'ambiguous' when several definitions share the name. " +
			"Catches syntactic uses only — reflection/dynamic usage is not seen, so an empty " +
			"result is best-effort, not proof of dead code."
	case "prism_index":
		return "Delta-index the workspace through Grove. Indexing is AUTOMATIC — " +
			"the server indexes at startup, a never-indexed repo indexes itself on " +
			"first query, and whole-repo graph ops delta-refresh before they run. " +
			"Call this ONLY after a stale-context warning or an explicit " +
			"empty-index failure, never routinely."
	case "prism_compact":
		return "Compress a conversation history JSON array. " +
			"Call when the context window is near capacity to summarize older turns " +
			"while preserving recent ones."
	case "prism_savings":
		return "Return this session's token-savings dashboard: total delivered, " +
			"percentage saved, per-tool breakdown."
	case "prism_drift":
		return "Check whether the ground shifted under you: re-verify every file " +
			"delivered in this session against the working tree and report, symbol " +
			"by symbol, what changed/was removed/was added since you saw it — with " +
			"merge provenance when a Fuse merge caused it. Call this when a stale-" +
			"context warning appears, before editing files you read a while ago, " +
			"or after another agent's branch lands."
	case "prism_feedback":
		return "Record a 0–5 quality rating for the last prism_query result. " +
			"0 = completely wrong context, 5 = perfect. Optional notes field."
	case "prism_change_impact":
		return "Every site that must change when a symbol does. Pass 'Type.method' and get, in " +
			"one call: declarations, the full override/implementation family, breaking sibling " +
			"contracts (supers), all resolved callers, and declaringTypes. Reach for this before " +
			"editing any existing symbol. completeness:'closed' = authoritative; 'project-local' " +
			"+ overridesExternal = the method implements an external contract whose signature " +
			"must not change. Relay the set as-is — re-filtering through grep drops real sites."
	case "prism_missing_implementations":
		return "The interface-evolution companion to prism_change_impact: pass 'Type.method' " +
			"and get every type in the subtype closure that FAILS to implement the member — " +
			"the types the compiler will reject once the member is required. Use when adding " +
			"a method to an interface/base class ('which implementors are now broken?'), " +
			"auditing a contract, or after change_impact to plan the implementation work. " +
			"Result groups: missing (concrete types with no implementation, own or inherited " +
			"— each is a compile error), abstractMissing (abstract classes, informational), " +
			"unverifiable (superclass chain leaves the index; an external base may provide " +
			"it — verify before treating as broken), implementedCount (coverage evidence). " +
			"defaultProvided=true means the contract ships a body: nothing is broken today, and " +
			"'missing' reads as 'inherits the default — breaks if the member becomes required'. " +
			"Same completeness reporting as change_impact. RELAY the result as-is: do not " +
			"re-verify through grep — the closure and inheritance walk are already solved."
	case "prism_node":
		return "One-shot orientation on a single thing. Pass a SYMBOL name and get its source " +
			"plus its immediate graph neighbours (callers, callees, implementors) in one call — " +
			"the 'what is this and what touches it' view, without a lookup-then-edges round-trip. " +
			"Pass a repo-relative FILE PATH instead and get the file's source, the symbols it " +
			"defines, and the files that DEPEND on it. Ambiguous symbol names return the candidate " +
			"list unchanged rather than guessing. Use this to orient; use change_impact when you " +
			"need the complete set of sites a signature change must touch."
	case "prism_rename_plan":
		return "The rename executed as a plan: pass 'Type.method' and newName, get the " +
			"complete change-impact set converted to concrete line edits — file, line, " +
			"before, after — for every declaration, override, and resolved call site. " +
			"Your job becomes review-and-apply, not discover: apply 'edits' as-is, then " +
			"check 'ambiguous' (lines in methods that ALSO call a same-named method on an " +
			"unrelated type — verify the receiver type before editing those). Same " +
			"completeness reporting as change_impact; if completeness is 'project-local' " +
			"the member overrides an external contract and must NOT be renamed. RELAY and " +
			"apply the edits as given: do not re-derive the set through grep — the " +
			"traversal is already solved and re-processing measurably corrupts it."
	case "prism_dead_code":
		return "Deletion-candidate list: production functions/methods unreachable from every " +
			"entry point (main/init, tests, exported symbols, plus optional roots=[...] for " +
			"framework hooks registered by name). Precision-first: a symbol is 'dead' only if " +
			"it is unreachable AND non-exported AND its name appears nowhere else in the " +
			"codebase text — so callbacks passed as values are never flagged, and every entry " +
			"is safe to delete without breaking compilation (transitively-dead clusters " +
			"surface top-down across re-runs). exportedUnreferenced lists public API with " +
			"zero in-project references — dead only if nothing external links against it; " +
			"do not delete those without checking consumers. ALWAYS relay the caveats field: " +
			"reflection, DI, serialization hooks, and codegen call symbols invisibly."
	case "prism_map":
		return "Architecture map in ONE call: the repository's components (directories) and " +
			"every component-level dependency, aggregated from the real call/import/type edges " +
			"crossing between them — with weights, per-kind breakdown, dependency cycles, and " +
			"the evidence tier of every claim. Use for: 'map/explain this repo', refactor and " +
			"extraction planning, layering questions, 'what depends on X'. Every edge is " +
			"evidence-backed, not narrative: pass from+to to expand one edge into the full " +
			"list of concrete crossing sites (file:line). depth=1 gives the top-level view of " +
			"a large repo. The result is complete over indexed project edges at the reported " +
			"tier; external dependencies are excluded — this is the project's internal shape. " +
			"A repeat call whose recomputed result is IDENTICAL to one already delivered this " +
			"session returns a one-line [prism:cached] pointer — use the prior delivery."
	}
	return "Prism tool: " + name
}

// --- Tool implementations -----------------------------------------------

type queryResult struct {
	BudgetUsed int            `json:"budgetUsed"`
	Symbols    []rankedSymbol `json:"symbols"`
	// TextMatches are full-text hits outside any indexed symbol (comments,
	// configs, docs) — the grep half of the merged search.
	TextMatches []map[string]any `json:"textMatches,omitempty"`
	TextBackend string           `json:"textBackend,omitempty"`
	// Note explains an empty result so agents can tell "wrong root" or
	// "term typo" apart from "genuinely no matches" without guessing.
	Note string `json:"note,omitempty"`
}

type rankedSymbol struct {
	Name          string         `json:"name"`
	QualifiedName string         `json:"qualifiedName"`
	FilePath      string         `json:"filePath"`
	Kind          string         `json:"kind"`
	Category      string         `json:"category"`
	Content       string         `json:"content"`
	Span          grove.SpanInfo `json:"span"`
}

func (h *Handler) toolQuery(ctx context.Context, args map[string]any) (any, error) {
	task := stringArg(args, "task", stringArg(args, "intent", ""))
	if task == "" {
		return nil, errors.New("task is required")
	}
	timing := os.Getenv("PRISM_TIMING") != ""
	tQuery := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing] %-22s %8.0fms\n", stage, float64(time.Since(tQuery).Milliseconds()))
		}
	}
	defer stamp("toolQuery total")

	// --- Agent-directed parameters ---

	// terms: agent-supplied grep-style search terms used to seed retrieval
	// instead of relying purely on TF-IDF over the task string. When provided,
	// Prism searches for each term as a symbol name/substring and uses the
	// matches as seeds — same precision as the agent's own grep, plus graph expansion.
	var terms []string
	if raw, ok := args["terms"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, t := range v {
				if s, ok := t.(string); ok && s != "" {
					terms = append(terms, s)
				}
			}
		case []string:
			terms = v
		}
	}

	// include: controls which result categories are returned.
	// Accepted values: "graph" (code + callers/callees), "docs".
	// Default when omitted: ["graph"].
	includeSet := map[string]bool{}
	if raw, ok := args["include"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, t := range v {
				if s, ok := t.(string); ok {
					includeSet[s] = true
				}
			}
		case []string:
			for _, s := range v {
				includeSet[s] = true
			}
		}
	}
	if len(includeSet) == 0 {
		includeSet = map[string]bool{"graph": true}
	}

	// Note: graph_depth was removed from the schema — it was advertised for
	// months while no code path read it, so tuning it silently did nothing.
	// Expansion is a fixed one-hop typed call neighborhood (selectContext).
	stamp("pre-selectContext")
	sel, err := h.selectContext(ctx, selectParams{
		task:            task,
		terms:           terms,
		minedTerms:      mineTaskIdentifiers(task, terms),
		includeSet:      includeSet,
		explicitProfile: stringArg(args, "profile", ""),
		limit:           intArg(args, "limit", 50),
		contextUsed:     int64(intArg(args, "context_used", 0)),
		model:           stringArg(args, "model", ""),
		budgetArg:       intArg(args, "budget", 0),
	})
	if err != nil {
		return nil, err
	}
	stamp("post-selectContext")

	// Delivery: "source" = verbatim line-numbered windows + anchor summary
	// (edit-ready); "symbols" = the compact per-symbol list.
	//
	// Source is the default, unconditionally. It used to depend on
	// DetectPhase(task) — so "fix the timeout bug" returned editable windows
	// and "look at the timeout handling" returned a symbol list, from the same
	// seeds. Delivering context for an edit is what this tool is for; ask for
	// delivery="symbols" when you want the compact list.
	delivery := stringArg(args, "delivery", "")
	if delivery == "" && len(sel.picked) > 0 {
		delivery = "source"
	}
	if delivery == "source" {
		out := h.deliverSource(ctx, task, sel, intArg(args, "max_files", 0), sel.budget)
		if tm := h.renderTextMatches(sel.textHits); tm != nil {
			out["textMatches"] = tm
			out["textBackend"] = sel.textBackend
		}
		delivered, _ := out["deliveredTokens"].(int)
		h.Ledger.Record("prism_query", h.queryBaselineTokens(sel.picked, delivered), delivered)
		return out, nil
	}
	picked := sel.picked

	// Build response.
	used := 0
	out := queryResult{
		Symbols: make([]rankedSymbol, 0, len(picked)),
	}
	for _, p := range picked {
		used += p.TokenCost
		out.Symbols = append(out.Symbols, rankedSymbol{
			Name:          p.Symbol.Name,
			QualifiedName: p.Symbol.QualifiedName,
			FilePath:      p.Symbol.FilePath,
			Kind:          p.Symbol.Kind,
			Category:      string(p.Category),
			Content:       ranking.Render(p.Symbol, p.Disclosure),
			Span:          p.Symbol.Span,
		})
	}
	out.BudgetUsed = used
	if tm := h.renderTextMatches(sel.textHits); tm != nil {
		out.TextMatches = tm
		out.TextBackend = sel.textBackend
	}

	if len(out.Symbols) == 0 && len(out.TextMatches) == 0 {
		switch {
		case len(sel.seeds) == 0 && len(terms) > 0:
			out.Note = fmt.Sprintf("no symbols matched terms %v under project root %s; check term spelling and that the code lives under this root", terms, h.Root)
		case len(sel.seeds) == 0:
			out.Note = fmt.Sprintf("no symbols matched this task under project root %s", h.Root)
		default:
			out.Note = "seeds matched but nothing fit the requested include categories/budget; try include=[\"graph\"] or a larger budget"
		}
	}

	// Baseline for the savings ledger: the token cost of reading each
	// containing file once in full — what assembling the same context by
	// file reads would have cost. Measured from on-disk sizes, never assumed.
	h.Ledger.Record("prism_query", h.queryBaselineTokens(picked, used), used)
	return out, nil
}

// queryBaselineTokens estimates what the delivered context would have cost
// without graph-ranked selection: one full read of every distinct file a
// selected symbol lives in. Files that cannot be stat'ed contribute nothing;
// the result is never below the delivered token count, so savings are never
// invented when measurement fails.
func (h *Handler) queryBaselineTokens(picked []ranking.BudgetedSymbol, delivered int) int {
	seen := map[string]bool{}
	total := 0
	for _, p := range picked {
		fp := normalizePath(p.Symbol.FilePath)
		if fp == "" || seen[fp] {
			continue
		}
		seen[fp] = true
		if info, err := os.Stat(filepath.Join(h.Root, filepath.FromSlash(fp))); err == nil {
			total += int(info.Size() / 4) // ~4 bytes/token, same estimate as EstimateTokens
		}
	}
	if total < delivered {
		return delivered
	}
	return total
}

// ("file.go::Name@abc123"), leaving the stable "file.go::Name" identity.

// readRange delivers an explicit line window, verbatim and line-numbered —
// the shape `sed -n A,Bp` and native Read(offset,limit) produce, which
// agents reach for on half their reads.
//
// totalLines is always reported: a window without a denominator reads as the
// whole file, which is the silent-narrowing failure this codebase keeps
// re-learning. Out-of-range requests clamp and say so rather than erroring —
// a tool that errors is a tool agents route around.
func (h *Handler) readRange(sessionPath, content string, offset, limit int) (any, error) {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // trailing newline is not a line
	}
	total := len(lines)
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return map[string]any{
			"file": sessionPath, "totalLines": total, "delivery": "range",
			"warning": fmt.Sprintf("offset %d is past end of file (%d lines); nothing to show",
				offset, total),
		}, nil
	}
	end := total
	if limit > 0 && offset-1+limit < total {
		end = offset - 1 + limit
	}
	var b strings.Builder
	width := len(strconv.Itoa(end))
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%*d\t%s\n", width, i+1, lines[i])
	}
	out := map[string]any{
		"file":       sessionPath,
		"delivery":   "range",
		"startLine":  offset,
		"endLine":    end,
		"totalLines": total,
		"content":    b.String(),
	}
	if end < total || offset > 1 {
		out["note"] = fmt.Sprintf("lines %d-%d of %d — this is a WINDOW, not the file",
			offset, end, total)
	}
	return out, nil
}

func (h *Handler) toolRead(ctx context.Context, args map[string]any) (any, error) {
	path := stringArg(args, "file", stringArg(args, "path", ""))
	if path == "" {
		return nil, errors.New("file is required")
	}
	task := stringArg(args, "task", "")
	abs, sessionPath, err := safePathWithinRoot(h.Root, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Line-ranged reads. Measured over 374 real file reads by unaided and
	// prism-armed agents: 50.6% are line-ranged (sed -n A,Bp 25.7%, native
	// Read offset+limit 23.8%) and prism_read could express NONE of it. That
	// gap is why 87% of the reads prism never saw were ranged, and why its
	// session ledger fired 0 times in 45 calls -- the agent cannot route a
	// ranged read through a whole-file tool, so the ledger never sees the
	// repeats it exists to collapse.
	//
	// Whole-file reads keep the compression path unchanged; a ranged read is
	// delivered verbatim and line-numbered, because slicing lines and THEN
	// compressing them would be two lossy steps on the same content.
	offset, limit := intArg(args, "offset", 0), intArg(args, "limit", 0)
	if offset > 0 || limit > 0 {
		return h.readRange(sessionPath, string(data), offset, limit)
	}
	// The file's currently indexed symbols, by exact path (Grove v0.6.1).
	fileSyms, err := h.Grove.FileSymbols(ctx, normalizePath(sessionPath))
	if err != nil {
		return nil, fmt.Errorf("grove symbols: %w", err)
	}
	readCfg := h.Cfg.WithModel(stringArg(args, "model", ""))
	contextUsed := int64(intArg(args, "context_used", 0))
	confidence := session.Low
	if entry, seen, _ := h.Session.Lookup(sessionPath, ""); seen {
		confidence = h.confidenceFor(entry, contextUsed, readCfg.ContextWindow())
	}
	res := compression.CompressFileRead(sessionPath, string(data), compression.Options{
		Task:            task,
		Symbols:         fileSyms,
		Session:         h.Session,
		Ledger:          h.Ledger,
		TokenLedgerName: "prism_read",
		Confidence:      confidence,
		ContextUsed:     contextUsed,
	})
	// Record the structural baseline for prism_drift: these are the symbols
	// the agent's copy of the file reflects as of this delivery.
	if len(fileSyms) > 0 {
		h.setDriftBase(sessionPath, fileSyms)
	}
	return map[string]any{
		"file":            res.FilePath,
		"strategy":        res.Strategy,
		"originalTokens":  res.OriginalTokens,
		"deliveredTokens": res.DeliveredTokens,
		"savingsPercent":  res.SavingsPercent,
		"content":         res.Content,
	}, nil
}

// searchTermCap bounds how many terms one prism_search call will run. The
// point of batching is to collapse a run of turns, not to let one result
// become the payload that dominates every later turn (cache reads compound:
// a result at turn 3 of 28 is paid ~25 times). Ten covers every observed run
// in the A/B — the longest was 10 — and anything past it is a different kind
// of request that should be narrowed, not widened.
const searchTermCap = 10

// searchContextCap bounds context= lines per hit. Unbounded, an agent
// pairing a large context with exhaustive=true (or a wide path/no path) can
// turn one call into the token cost of reading whole files hit-by-hit --
// measured 2026-08-30: a context=30 call over a whole source file inside an
// already-expensive debugging loop, on a task where the extra payload did
// not shorten the loop.
//
// Tightened 30 -> 15 the same day, by binary search over 75 real context=
// requests mined from e2e sessions: 15-19 are behaviorally identical (no
// request in that range all day), so 15 sits at the true edge of normal
// usage -- 86% of all requests already fall at or under it. Every one of
// the 10 requests this tightening newly clamps (20,20,22,25,30x4,40x2) is
// the SAME misuse pattern: a single named function/class captured by
// guessing a context= large enough, rather than prism_lookup(name) -- the
// exact case prism_lookup's routing fix (same day) targets directly. Past
// 15, the caller wants prism_lookup for one symbol or prism_read for a
// file, not more context.
const searchContextCap = 15

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func (h *Handler) toolSearch(ctx context.Context, args map[string]any) (any, error) {
	queries := stringsArg(args, "query")
	if len(queries) == 0 {
		// The schema declares query required; without this an empty string
		// returned an arbitrary slice of the index as if it were a result.
		return nil, errors.New("query is required (a name, a name fragment, or an array of them)")
	}
	var termNote string
	if len(queries) > searchTermCap {
		// Never drop silently. A filter that quietly narrows the request is
		// the failure mode SWE-Explore measures as the expensive one: missing
		// evidence costs far more than noise.
		termNote = fmt.Sprintf("only the first %d of %d terms were searched; re-run with the rest",
			searchTermCap, len(queries))
		queries = queries[:searchTermCap]
	}
	limit := intArg(args, "limit", 25)
	scope := stringArg(args, "scope", "both")
	regex := boolArg(args, "regex")
	reqContext := intArg(args, "context", 0)
	if reqContext > searchContextCap {
		termNote = appendNote(termNote, fmt.Sprintf(
			"context clamped to %d (asked for %d) — for more than a function's worth, use prism_read on the file",
			searchContextCap, reqContext))
		reqContext = searchContextCap
	}
	sc := searchScope{
		paths:      stringsArg(args, "path"),
		glob:       stringsArg(args, "glob"),
		filesOnly:  boolArg(args, "files_only"),
		exhaustive: boolArg(args, "exhaustive"),
		context:    reqContext,
	}

	// Multi-term: run each term through the same single-term path and group
	// the results under the term that produced them, so an agent can tell
	// which hit answered which question. One term keeps the flat shape
	// verbatim — every existing caller and test sees no change.
	if len(queries) > 1 {
		perTerm := make([]map[string]any, len(queries))
		errs := make([]error, len(queries))
		var wg sync.WaitGroup
		for i, q := range queries {
			wg.Add(1)
			go func(i int, q string) {
				defer wg.Done()
				r, err := h.searchOne(ctx, q, scope, limit, regex, sc)
				if err != nil {
					errs[i] = err
					return
				}
				r["query"] = q
				perTerm[i] = r
			}(i, q)
		}
		wg.Wait()
		out := map[string]any{}
		results := make([]map[string]any, 0, len(queries))
		var failed []string
		for i := range queries {
			if errs[i] != nil {
				// One bad term must not lose the other nine's results.
				failed = append(failed, fmt.Sprintf("%s: %v", queries[i], errs[i]))
				continue
			}
			results = append(results, perTerm[i])
		}
		out["results"] = results
		if len(failed) > 0 {
			out["failedTerms"] = failed
		}
		if termNote != "" {
			out["note"] = termNote
		}
		return out, nil
	}

	out, err := h.searchOne(ctx, queries[0], scope, limit, regex, sc)
	if err != nil {
		return nil, err
	}
	if termNote != "" {
		out["note"] = termNote
	}
	return out, nil
}

// searchOne is the single-term search, unchanged in behaviour and shape from
// when prism_search took exactly one query.
type searchScope struct {
	paths      []string
	glob       []string
	filesOnly  bool
	exhaustive bool
	context    int
}

func (h *Handler) searchOne(ctx context.Context, q, scope string, limit int, regex bool, sc searchScope) (map[string]any, error) {

	// scope="text": the agent asked for a PURE grep — exactly what rg
	// returns, no symbol search, no graph, minimal envelope. This is the
	// agent pricing its own request (measured: routing every locate through
	// the enriched path cost ~1.5× on ordinary bug fixes for zero benefit).
	if scope == "text" {
		r := textsearch.Search(ctx, h.Root, q, textsearch.Options{
			MaxHits: limit, Timeout: textSearchTimeout, Regex: regex,
			Paths: sc.paths, Glob: sc.glob, FilesOnly: sc.filesOnly,
			Exhaustive: sc.exhaustive, Context: sc.context,
		})
		out := map[string]any{
			"textHits":    h.renderTextMatches(r.Hits),
			"textBackend": r.Backend,
			"truncated":   r.Truncated,
		}
		if r.Truncated {
			// Truncation always carries a denominator. Without one the agent
			// cannot tell a complete answer from a 2% sample, and the failure
			// is silent: it reads the capped list as the whole picture.
			out["totalHits"] = r.TotalHits
			out["filesMatched"] = r.FilesMatched
			// The graph's organization of the WHOLE hit set rides along with
			// the sample, so the narrowing decision can happen this turn
			// (rollup.go — 27% of real searches truncate). Compute it BEFORE
			// the warning so the warning can point at it: on a security/audit
			// query (access-control patterns, ownership) the complete grouped
			// answer is already in this same response, and telling the agent
			// to "raise limit= / narrow" first — without mentioning it — was
			// measured (2026-09-02, real usage) sending the agent past it
			// unread and toward a re-query or, worse, treating the sample as
			// the whole picture for a correctness-critical check.
			ru := h.hitRollup(ctx, q, sc, regex)
			if len(ru) > 0 {
				out["hitRollup"] = ru
				out["warning"] = fmt.Sprintf(
					"showing %d of AT LEAST %d matches across %d files — this is a SAMPLE, not "+
						"the full set. The graph's COMPLETE breakdown of all %d hits, grouped by "+
						"enclosing symbol, is in hitRollup below — check it before re-querying, "+
						"it usually answers 'where are the rest' without another call. Need every "+
						"raw line instead of grouped counts? exhaustive=true.",
					len(r.Hits), r.TotalHits, r.FilesMatched, r.TotalHits)
			} else {
				out["warning"] = fmt.Sprintf(
					"showing %d of AT LEAST %d matches across %d files — this is a SAMPLE, not the "+
						"full set. Raise limit=, narrow with path=/glob=, or use files_only=true to "+
						"see the spread before drawing conclusions. Need every raw line? exhaustive=true.",
					len(r.Hits), r.TotalHits, r.FilesMatched)
			}
		}
		if sc.filesOnly {
			// Locations without lines: the cheapest answer to "where does
			// this live". Done here rather than via rg --files-with-matches,
			// whose bare-path output breaks the shared line parser.
			seen := map[string]bool{}
			var files []string
			for _, hit := range r.Hits {
				if !seen[hit.File] {
					seen[hit.File] = true
					files = append(files, hit.File)
				}
			}
			delete(out, "textHits")
			out["files"] = files
			out["fileCount"] = len(files)
		}
		if len(r.RejectedPaths) > 0 {
			// Never let a dropped scope pass as a completed search.
			out["rejectedPaths"] = r.RejectedPaths
			out["warning"] = fmt.Sprintf(
				"these path= entries resolve outside the project root and were NOT searched: %v",
				r.RejectedPaths)
		}
		if r.TimedOut {
			out["timedOut"] = true
			out["warning"] = "the text search hit its deadline before finishing — " +
				"these results are INCOMPLETE and an empty list does NOT mean the " +
				"string is absent. The built-in scanner is in use because neither " +
				"ripgrep nor grep was found on this server's PATH; installing " +
				"ripgrep fixes both the speed and this warning."
		}
		// Structural hint first: the fan-out of the symbol this term names
		// (implementations + callers with sites) outranks the noise-ratio
		// note — full38 showed missed fan-out sites, not noisy greps, are
		// where searches go wrong.
		if n := h.structuralNote(ctx, q); n != "" {
			out["resolvedNote"] = n
		} else if n := h.resolvedRefNote(ctx, q, r.Hits); n != "" {
			out["resolvedNote"] = n
		}
		return out, nil
	}

	// Grove's symbol search is ranked (exact name > prefix > substring,
	// v0.6.0) — deliver it directly, matching this tool's contract of
	// searching symbol names rather than re-ranking semantically.
	syms, err := h.Grove.SearchSymbols(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	syms = filterGeneratedPrismContext(syms)
	// path=/glob= are honored by the TEXT pass (textsearch.Options) but were
	// silently dropped by the SYMBOL pass — the agent's narrowing simply did
	// not apply to half its own result. Measured on jackson (2026-08-25): a
	// search for "anySetter" scoped to src/main/java returned 25 symbols, 8
	// of them from src/test/java, and the agent re-grepped to recover. A
	// scope the tool advertises and then ignores is worse than no scope.
	syms = filterSymbolsByScope(syms, sc)
	// Real implementations first, test doubles tagged and last — the
	// disambiguation prism_resolve used to provide, folded into the one
	// locate tool so agents never need a second call to tell them apart.
	annotated := make([]map[string]any, 0, len(syms))
	var doubles []map[string]any
	for _, s := range syms {
		var m map[string]any
		if b, err := json.Marshal(s); err == nil {
			_ = json.Unmarshal(b, &m)
		}
		if m == nil {
			continue
		}
		if isTestDouble(s.FilePath) {
			m["testDouble"] = true
			doubles = append(doubles, m)
		} else {
			annotated = append(annotated, m)
		}
	}
	annotated = append(annotated, doubles...)
	// LOCATE returns locations, not bodies. The text renderer already drops
	// rawText (v0.55.6), but the JSON payload still carried whole symbol
	// bodies — measured on jackson (2026-08-25): one `search anySetter`
	// returned 95,315 bytes of class bodies for a locate question, in a
	// language where a class body runs hundreds of lines. Python hid this;
	// Java made it the dominant cost. Bodies remain one prism_lookup away,
	// and the pointer says so.
	for _, m := range annotated {
		delete(m, "rawText")
		delete(m, "callSites")
		delete(m, "blobSha")
		delete(m, "id")
		delete(m, "imports")
	}
	out := map[string]any{"symbols": annotated}
	if len(annotated) > 0 {
		out["note"] = "locations only — prism_lookup <name> for a symbol's body, prism_read for a file"
	}
	// Merged full-text search: the same query as a literal, so a string
	// that names no symbol (an error message, a config key) still lands.
	// scope="symbols" skips it on request.
	if scope != "symbols" {
		// The merged pass previously ran at MaxHits 50 — double the symbol
		// limit the caller asked for, on the default scope of the highest-
		// call-count tool. The caller's limit bounds both passes now.
		if r := textsearch.Search(ctx, h.Root, q, textsearch.Options{
			MaxHits: limit, Timeout: textSearchTimeout, Regex: regex,
			Paths: sc.paths, Glob: sc.glob, FilesOnly: sc.filesOnly,
			Exhaustive: sc.exhaustive, Context: sc.context,
		}); len(r.Hits) > 0 {
			out["textHits"] = h.renderTextMatches(r.Hits)
			out["textBackend"] = r.Backend
		}
	}
	// Same structural hint as scope=text: the symbol list above says the
	// name exists, but not that changing it fans out — and the fan-out is
	// the part agents were measured never to ask for on their own.
	if n := h.structuralNote(ctx, q); n != "" {
		out["resolvedNote"] = n
	}
	return out, nil
}

func (h *Handler) toolReferences(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	res, err := h.Grove.References(ctx, name)
	if err != nil {
		return nil, err
	}
	// Group by file for a compact, agent-friendly shape.
	byFile := map[string][]map[string]any{}
	for _, r := range res.Refs {
		entry := map[string]any{"line": r.Line}
		if r.Enclosing != "" {
			entry["in"] = r.Enclosing
		}
		byFile[r.File] = append(byFile[r.File], entry)
	}
	out := map[string]any{
		"name":        res.Name,
		"count":       len(res.Refs),
		"definitions": res.DefCount,
		"ambiguous":   res.Ambiguous,
		"byFile":      byFile,
	}
	if res.SkippedFiles > 0 {
		out["skippedFiles"] = res.SkippedFiles
		out["warning"] = fmt.Sprintf("%d file(s) could not be read — this reference "+
			"set may be incomplete", res.SkippedFiles)
	}
	return out, nil
}

func (h *Handler) toolResolve(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	cands, err := h.Grove.Resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		entry := map[string]any{"name": c.Name, "kind": c.Kind, "file": c.File, "line": c.Line}
		if c.TestDouble {
			entry["testDouble"] = true
		}
		out = append(out, entry)
	}
	return map[string]any{"name": name, "count": len(out), "candidates": out}, nil
}

func (h *Handler) toolEdges(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	direction := stringArg(args, "direction", "both")
	var kinds []string
	if raw, ok := args["kinds"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					kinds = append(kinds, s)
				}
			}
		}
	}
	edges, err := h.Grove.Edges(ctx, name, direction, kinds)
	if err != nil {
		return nil, err
	}
	// Group by "<edgeType> <direction>" (e.g. "calls out") and cap each group,
	// reporting the true total so a hot symbol can't dump 300 callers.
	const cap = 50
	type group struct {
		shown []map[string]any
		total int
	}
	groups := map[string]*group{}
	order := []string{}
	for _, e := range edges {
		key := e.EdgeType + " " + e.Direction
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		g.total++
		if len(g.shown) < cap {
			entry := map[string]any{"name": e.Name, "file": e.File, "line": e.Line, "kind": e.Kind}
			if e.TestDouble {
				entry["testDouble"] = true
			}
			g.shown = append(g.shown, entry)
		}
	}
	rel := map[string]any{}
	for _, key := range order {
		g := groups[key]
		m := map[string]any{"shown": len(g.shown), "total": g.total, "symbols": g.shown}
		rel[key] = m
	}
	return map[string]any{"name": name, "direction": direction, "edges": rel}, nil
}

// dedupeSymbolsByID drops duplicate symbols (same ID) while preserving order,
// used after merging two symbol searches into one candidate pool.
func dedupeSymbolsByID(syms []grove.SymbolRecord) []grove.SymbolRecord {
	seen := make(map[string]bool, len(syms))
	out := syms[:0]
	for _, s := range syms {
		if s.ID != "" && seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out
}

// isTestDouble reports whether a file path looks like a test double (mock/fake/
// stub) or a test file, so lookup can prefer the real implementation when
// several symbols share a name.
func isTestDouble(path string) bool {
	lp := strings.ToLower(filepath.ToSlash(path))
	return strings.HasSuffix(lp, "_test.go") ||
		strings.Contains(lp, "mock") || strings.Contains(lp, "fake") ||
		strings.Contains(lp, "stub") || strings.Contains(lp, "/testdata/")
}

// projectSymbol returns only the requested columns of a symbol. file, line and
// name are always included as identity. Recognized fields: signature, doc, body,
// kind, parent, modifiers. An empty list means "default" (caller adds the body).
func projectSymbol(s grove.SymbolRecord, fields []string) map[string]any {
	qn := s.QualifiedName
	if qn == "" {
		qn = s.Name
	}
	out := map[string]any{"name": qn, "file": s.FilePath, "line": s.Span.Start}
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "signature", "sig":
			out["signature"] = s.Signature
		case "doc", "docstring":
			out["doc"] = s.Docstring
		case "body", "source":
			out["body"] = s.RawText
		case "kind":
			out["kind"] = string(s.Kind)
		case "parent":
			out["parent"] = s.ParentSymbol
		case "modifiers":
			out["modifiers"] = s.Modifiers
		case "name", "file", "line":
			// already included as identity
		}
	}
	return out
}

// toolNode is the one-shot orientation view: everything you need about ONE
// symbol (or ONE file) without a chain of round-trips. Composed entirely from
// existing primitives — lookup + edges for a symbol, read + file symbols +
// dependents for a file. No new graph machinery, no stored state.
//
// The argument is a symbol name OR a repo-relative file path; a path that the
// index knows takes the file branch.
func (h *Handler) toolNode(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", stringArg(args, "symbol", stringArg(args, "file", "")))
	if name == "" {
		return nil, errors.New("name is required (a symbol name or a repo-relative file path)")
	}
	// File branch: when the index knows this path, OR the path exists on
	// disk inside the root. Requiring indexed SYMBOLS meant every file the
	// extractor produces none for — configs, markdown, data fixtures,
	// sources in an unsupported language — fell through to the symbol
	// branch and came back "no symbol or indexed file named X" for a file
	// sitting right there. Its content and dependents are exactly what the
	// file view is for, and neither needs the file to define anything.
	if syms, err := h.Grove.FileSymbols(ctx, name); err == nil && len(syms) > 0 {
		return h.nodeFile(ctx, name, syms)
	}
	if h.fileExists(name) {
		return h.nodeFile(ctx, name, nil)
	}
	return h.nodeSymbol(ctx, name, args)
}

// fileExists reports whether name is a regular file inside the root. Used to
// decide the node file branch, so it must refuse paths that escape the root:
// prism_node is a read tool, and a read tool that will open ../../.ssh/id_rsa
// on request is an exfiltration primitive.
func (h *Handler) fileExists(name string) bool {
	if name == "" || filepath.IsAbs(name) {
		return false
	}
	root := filepath.Clean(h.Root)
	full := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(full)
	return err == nil && fi.Mode().IsRegular()
}

// nodeSymbol renders a symbol's source plus its immediate graph neighbours.
func (h *Handler) nodeSymbol(ctx context.Context, name string, args map[string]any) (any, error) {
	lookupArgs := map[string]any{"name": name}
	if f, ok := args["file"]; ok {
		lookupArgs["file"] = f
	}
	looked, err := h.toolLookup(ctx, lookupArgs)
	if err != nil {
		return nil, err
	}
	out, _ := looked.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	// Ambiguous or unmatched: relay lookup's candidate list untouched rather
	// than guessing which symbol the caller meant.
	if amb, _ := out["ambiguous"].(bool); amb {
		return out, nil
	}
	if matched, present := out["matched"].(bool); present && !matched {
		return out, nil
	}
	// Nothing resolved at all: say so instead of returning an empty shell the
	// caller has to interpret.
	if sym, present := out["symbol"]; !present || sym == nil {
		return map[string]any{
			"view":    "symbol",
			"name":    name,
			"matched": false,
			"note": fmt.Sprintf("no symbol or indexed file named %q — try prism_search for a name fragment, "+
				"or pass a repo-relative file path for the file view", name),
		}, nil
	}
	out["view"] = "symbol"

	// Neighbours in both directions — the "where do I go next" menu. Feed
	// edges the RESOLVED symbol's bare name: lookup accepts qualified names
	// ("Render.Render") but edges resolves bare ones ("Render"), so passing
	// the caller's string through would silently return nothing.
	edgeName := name
	if sym, ok := out["symbol"].(grove.SymbolRecord); ok && sym.Name != "" {
		edgeName = sym.Name
	} else if sm, ok := out["symbol"].(map[string]any); ok {
		if n, _ := sm["name"].(string); n != "" {
			edgeName = n
		}
	}
	// Orientation wants the relationship kinds, not the structural ones
	// (contains/defines/imports would bury the signal).
	kinds := []any{"calls", "implements", "extends", "overrides", "uses-type"}
	if edges, err := h.toolEdges(ctx, map[string]any{
		"name": edgeName, "direction": "both", "kinds": kinds,
	}); err == nil {
		if em, ok := edges.(map[string]any); ok {
			out["edges"] = em["edges"]
		}
	}
	h.Ledger.RecordCall("prism_node")
	return out, nil
}

// nodeFile renders a file's source, the symbols it defines, and the files that
// depend on it.
func (h *Handler) nodeFile(ctx context.Context, path string, syms []grove.SymbolRecord) (any, error) {
	out := map[string]any{"view": "file", "file": path}

	read, err := h.toolRead(ctx, map[string]any{"file": path})
	if err != nil {
		return nil, err
	}
	if rm, ok := read.(map[string]any); ok {
		for _, k := range []string{"content", "strategy", "deliveredTokens", "originalTokens", "savingsPercent"} {
			if v, ok := rm[k]; ok {
				out[k] = v
			}
		}
	}

	defined := make([]map[string]any, 0, len(syms))
	for _, s := range syms {
		defined = append(defined, map[string]any{
			"name": displayQN(s), "kind": s.Kind, "line": s.Span.Start,
		})
	}
	out["defines"] = defined

	// Dependents: edges whose TO side is a symbol in this file, grouped by the
	// depending file. Deps returns every edge touching the path in either
	// direction, so the To-side filter is what isolates "who needs me".
	if edges, err := h.Grove.Deps(ctx, path); err == nil {
		prefix := path + "::"
		seen := map[string]bool{}
		dependents := []string{}
		for _, e := range edges {
			if !strings.HasPrefix(e.To, prefix) {
				continue
			}
			from := e.From
			if i := strings.Index(from, "::"); i >= 0 {
				from = from[:i]
			}
			from = strings.TrimPrefix(from, "file:")
			if from == "" || from == path || seen[from] {
				continue
			}
			seen[from] = true
			dependents = append(dependents, from)
		}
		sort.Strings(dependents)
		out["dependents"] = dependents
		out["dependentCount"] = len(dependents)
	}
	h.Ledger.RecordCall("prism_node")
	return out, nil
}

func (h *Handler) toolLookup(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", stringArg(args, "qualifiedName", ""))
	if name == "" {
		return nil, errors.New("name is required")
	}
	// Optional column projection: return only the requested fields (signature,
	// doc, body, kind, parent, modifiers) instead of the full source body.
	var fields []string
	if raw, ok := args["fields"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				if sv, ok := v.(string); ok {
					fields = append(fields, sv)
				}
			}
		}
	}
	// Optional file disambiguator: when several symbols share a qualified name
	// (e.g. two packages with a Service.DecryptedValues), pass the file path (or
	// any substring of it, as shown in prism_search results) to pick the right one.
	fileHint := strings.ToLower(stringArg(args, "file", ""))

	// Accept "pkg/path.SymbolName" and "github.com/mod/pkg/path.SymbolName".
	// Split on the last '.' whose right side contains no '/' (i.e. is a symbol
	// name, not a URL segment) to get the bare search term and an optional
	// package-path hint used to disambiguate when multiple packages export a
	// symbol with the same name.
	searchName := name
	pkgHint := ""
	if idx := strings.LastIndex(name, "."); idx > 0 {
		right := name[idx+1:]
		if !strings.Contains(right, "/") {
			searchName = right
			pkgHint = name[:idx]
		}
	}

	// typeQualified is the last two dotted segments ("Service.DecryptedValues"),
	// matched against Grove's Type.Method QualifiedName. This lets a caller pass
	// a type-qualified name (pkg.Type.Method) and still hit the right method when
	// several types in the repo declare a method of the same bare name.
	typeQualified := ""
	if parts := strings.Split(name, "."); len(parts) >= 2 {
		last := parts[len(parts)-1]
		if !strings.Contains(last, "/") {
			typeQualified = parts[len(parts)-2] + "." + last
		}
	}

	syms, err := h.Grove.SearchSymbols(ctx, searchName, 25)
	if err != nil {
		return nil, err
	}
	// A bare-name search ("Get") caps at 25 alphabetically-early hits, which can
	// exclude the intended Type.Method entirely. When a Type.Method hint is
	// present, search Grove for that qualified form too (its searchRank matches
	// qualified_name exactly) and prepend it so the precise method is in the
	// candidate pool before ranking.
	if typeQualified != "" {
		if extra, qerr := h.Grove.SearchSymbols(ctx, typeQualified, 25); qerr == nil {
			syms = append(extra, syms...)
		}
	}
	syms = dedupeSymbolsByID(filterGeneratedPrismContext(syms))

	// File disambiguator: restrict to candidates whose path contains the hint, so
	// a name shared across packages resolves to the one the agent means. Ignored
	// if it would empty the set (a stale/typo'd hint shouldn't lose the symbol).
	if fileHint != "" {
		var kept []grove.SymbolRecord
		for _, s := range syms {
			if strings.Contains(strings.ToLower(s.FilePath), fileHint) {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			syms = kept
		}
	}

	// pkgMatches returns true when s lives in the package identified by pkgHint.
	// pkgHint may be a short path ("internal/cli") or a full module path
	// ("github.com/provasign/prism/internal/cli"); both are matched against the
	// file's directory using a suffix check with a slash guard.
	pkgMatches := func(s grove.SymbolRecord) bool {
		if pkgHint == "" {
			return true
		}
		dir := filepath.ToSlash(filepath.Dir(s.FilePath))
		return dir == pkgHint || strings.HasSuffix(pkgHint, "/"+dir)
	}

	// Rank the candidates. A precise Type.Method (typeQualified) match dominates a
	// bare-name match, so "kvstore.SecretsKVStoreSQL.Get" resolves to that exact
	// method and not one of the thousands of other Get's. Package-hint and
	// real-vs-test-double then break ties, so a name still lands on the
	// production symbol rather than a mock that shares it.
	score := func(s grove.SymbolRecord) int {
		sc := 0
		switch {
		case typeQualified != "" && s.QualifiedName == typeQualified:
			sc += 1000
		case s.QualifiedName == searchName:
			sc += 500
		case s.Name == searchName:
			sc += 1
		default:
			return -1 // not an exact match at all
		}
		if pkgMatches(s) {
			sc += 100
		}
		if isTestDouble(s.FilePath) {
			sc -= 10
		}
		return sc
	}
	bestIdx, bestScore, tied := -1, 0, 0
	for i := range syms {
		sc := score(syms[i])
		if sc < 0 {
			continue
		}
		switch {
		case bestIdx == -1 || sc > bestScore:
			bestIdx, bestScore, tied = i, sc, 1
		case sc == bestScore:
			tied++
		}
	}
	if bestIdx >= 0 {
		var out map[string]any
		if len(fields) > 0 {
			// Column projection requested: return just those fields.
			out = projectSymbol(syms[bestIdx], fields)
		} else {
			out = map[string]any{"symbol": syms[bestIdx], "content": syms[bestIdx].RawText}
		}
		// A real tie at the top (same score, different symbols sharing the name)
		// is genuine ambiguity the qualifier couldn't resolve — surface it with
		// candidates rather than silently picking one.
		if tied > 1 {
			cands := make([]string, 0, tied)
			for i := range syms {
				if score(syms[i]) == bestScore {
					n := syms[i].QualifiedName
					if n == "" {
						n = syms[i].Name
					}
					cands = append(cands, n+" ("+syms[i].FilePath+")")
				}
			}
			out["ambiguous"] = true
			out["candidates"] = cands
		}
		return out, nil
	}
	if len(syms) > 0 {
		// No exact match — returning the closest hit silently would hand the
		// agent the wrong symbol body. Flag it and list the alternatives.
		candidates := make([]string, 0, minInt(5, len(syms)))
		for _, s := range syms[:minInt(5, len(syms))] {
			n := s.QualifiedName
			if n == "" {
				n = s.Name
			}
			candidates = append(candidates, n+" ("+s.FilePath+")")
		}
		return map[string]any{
			"symbol":     syms[0],
			"content":    syms[0].RawText,
			"matched":    false,
			"candidates": candidates,
		}, nil
	}
	// Nothing matched at all. A bare {"symbol": null} tells the caller
	// nothing about WHY or where to go next, so offer the nearest names the
	// search index does know — the common cause is a typo or a qualified
	// name that does not exist in this repo.
	out := map[string]any{
		"symbol":  nil,
		"name":    name,
		"matched": false,
		"note": fmt.Sprintf("no symbol named %q in the index — check the spelling, "+
			"or use prism_search for a name fragment", name),
	}
	if near := h.nearbyNames(ctx, searchName); len(near) > 0 {
		out["candidates"] = near
		out["note"] = fmt.Sprintf("no symbol named %q in the index — did you mean one of the candidates?", name)
	}
	return out, nil
}

// nearbyNames returns up to five indexed symbols whose names look like term,
// for the "did you mean" list on a failed lookup. Best-effort: a search error
// just means no suggestions.
func (h *Handler) nearbyNames(ctx context.Context, term string) []string {
	if len(term) < 3 {
		return nil
	}
	syms, err := h.Grove.SearchSymbols(ctx, term, 5)
	if err != nil || len(syms) == 0 {
		return nil
	}
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		n := s.QualifiedName
		if n == "" {
			n = s.Name
		}
		out = append(out, fmt.Sprintf("%s (%s:%d)", n, s.FilePath, s.Span.Start))
	}
	return out
}

func (h *Handler) toolIndex(_ context.Context, _ map[string]any) (any, error) {
	// Always index the server's own root: Invoke already rejected any foreign
	// dir, and the engine's store is bound to h.Root regardless of the arg.
	// Indexing large codebases can take several minutes; use a fresh context
	// with an extended deadline instead of the 60-second Invoke-level one.
	idxCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := h.Grove.Index(idxCtx, h.Root)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// toolCompact takes an array of conversation "turns" and emits a compressed
// view. Each turn is { "role": string, "content": string, "kind"?: string }.
func (h *Handler) toolCompact(_ context.Context, args map[string]any) (any, error) {
	turnsRaw, ok := args["turns"]
	if !ok {
		return nil, errors.New("turns is required (array)")
	}
	buf, _ := json.Marshal(turnsRaw)
	var turns []map[string]any
	if err := json.Unmarshal(buf, &turns); err != nil {
		return nil, fmt.Errorf("turns: %w", err)
	}
	out := make([]map[string]any, 0, len(turns))
	keepFullFromIdx := len(turns) - 3
	if keepFullFromIdx < 0 {
		keepFullFromIdx = 0
	}
	// Deduplicate exact file-read content by keeping only the most recent.
	seen := map[string]int{} // content hash → index in out
	originalTokens, deliveredTokens := 0, 0
	for i, t := range turns {
		content, _ := t["content"].(string)
		kind, _ := t["kind"].(string)
		originalTokens += ranking.EstimateTokens(content)
		if i >= keepFullFromIdx {
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(content)
			continue
		}
		switch kind {
		case "exploration", "file_read", "search":
			// Compress to a single-line reference summary.
			ref := "[" + kind + "] " + summarize(content, 120)
			t["content"] = ref
			h := compression.Hash(content)
			if prev, ok := seen[h]; ok {
				out[prev] = map[string]any{"role": "system", "content": "[dedup] previous " + kind + " repeated"}
			} else {
				seen[h] = len(out)
			}
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(ref)
		case "implementation", "edit":
			t["content"] = summarize(content, 400)
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(t["content"].(string))
		default:
			t["content"] = summarize(content, 200)
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(t["content"].(string))
		}
	}
	savings := 0.0
	if originalTokens > 0 {
		savings = (1.0 - float64(deliveredTokens)/float64(originalTokens)) * 100.0
	}
	h.Ledger.Record("prism_compact", originalTokens, deliveredTokens)
	return map[string]any{
		"compressedTurns": out,
		"originalTokens":  originalTokens,
		"deliveredTokens": deliveredTokens,
		"savingsPercent":  savings,
	}, nil
}

func (h *Handler) toolSavings(_ context.Context, _ map[string]any) (any, error) {
	return h.Ledger.Snapshot(), nil
}

func (h *Handler) toolFeedback(_ context.Context, args map[string]any) (any, error) {
	tool := stringArg(args, "tool", "")
	queryID := stringArg(args, "queryId", "")
	rating := intArg(args, "rating", -1)
	notes := stringArg(args, "notes", "")
	if rating < 0 || rating > 5 {
		return nil, errors.New("rating must be in [0,5]")
	}
	entry := FeedbackEntry{
		Tool:      tool,
		QueryID:   queryID,
		Rating:    rating,
		Notes:     notes,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	h.fbMu.Lock()
	h.feedback = append(h.feedback, entry)
	h.fbMu.Unlock()

	// A: treat explicit low rating (0-1) as a weak negative outcome signal
	// and high rating (4-5) as a weak positive one, applied to the default profile.
	if tool == "prism_query" {
		if rating <= 1 {
			h.Weights.RecordOutcome("default", nil, nil, false)
		} else if rating >= 4 {
			h.Weights.RecordOutcome("default", []string{"__positive_feedback__"}, []string{"__positive_feedback__"}, false)
		}
	}

	return map[string]any{"recorded": entry, "totalRatings": len(h.feedback)}, nil
}

func (h *Handler) toolChangeImpact(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	r, err := h.Grove.ChangeImpact(ctx, query)
	if err != nil {
		return nil, err // grove already prefixes; re-wrapping tripled the message
	}
	h.Ledger.RecordCall("prism_change_impact")
	// The member being changed, used to locate its call sites inside callers.
	targetLeaf := r.Query
	if i := strings.IndexByte(targetLeaf, '('); i >= 0 {
		targetLeaf = targetLeaf[:i]
	}
	targetLeaf = leafOf(strings.TrimSpace(targetLeaf))

	compactWithScope := func(syms []grove.SymbolRecord, annotate bool) []map[string]any {
		out := make([]map[string]any, 0, len(syms))
		for _, s := range syms {
			qn := s.QualifiedName
			if qn == "" {
				qn = s.Name
			}
			entry := map[string]any{
				"name":          s.Name,
				"qualifiedName": qn,
				"filePath":      s.FilePath,
				"line":          s.Span.Start,
				"kind":          s.Kind,
				"signature":     s.Signature,
			}
			// Locality hint: grove attributes a call made inside a closure to
			// the enclosing declaration, so name the nested scope that
			// actually holds it. Absent for the common non-nested case, which
			// therefore renders exactly as before.
			if annotate {
				if via := nestedScopeFor(s, targetLeaf); via != "" {
					entry["via"] = via
				}
			}
			out = append(out, entry)
		}
		return out
	}
	compact := func(syms []grove.SymbolRecord) []map[string]any {
		return compactWithScope(syms, false)
	}
	out := map[string]any{
		"query":        r.Query,
		"declarations": compact(r.Declarations),
		"supers":       compact(r.Supers),
		"family":       compact(r.Family),
		"callers":      compactWithScope(r.Callers, true),
		"totalSites":   len(r.Declarations) + len(r.Family) + len(r.Callers) + len(r.DeclaringTypes),
	}
	if len(r.DeclaringTypes) > 0 {
		out["declaringTypes"] = compact(r.DeclaringTypes)
		out["declaringTypesNote"] = "these type declaration blocks contain member " +
			"signatures that must change (Go/TS interface members are not separate " +
			"symbols, so the type itself is the change site) — include each as a " +
			"site in your answer"
	}
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	if r.HasHeuristicRefs {
		out["hasHeuristicRefs"] = true
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
		out["warning"] = "the queried method belongs to an external supertype's contract (" +
			strings.Join(r.OverridesExternal, ", ") + "); changing its signature breaks that " +
			"contract, and this change-set is the project-local closure only — call sites " +
			"typed against the external supertype are not included"
	}
	// Anchor-selection guard: a precisely-computed answer to the wrong anchor
	// is still wrong. If a same-named symbol holds a strictly larger closed
	// family (interface vs concrete implementation), say so.
	if hint := h.widerAnchorHint(ctx, r); hint != nil {
		out["widerAnchor"] = hint
	}
	return out, nil
}

func (h *Handler) toolMissingImplementations(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	r, err := h.Grove.MissingImplementations(ctx, query)
	if err != nil {
		return nil, err // grove already prefixes; re-wrapping tripled the message
	}
	h.Ledger.RecordCall("prism_missing_implementations")
	compact := func(syms []grove.SymbolRecord) []map[string]any {
		out := make([]map[string]any, 0, len(syms))
		for _, s := range syms {
			qn := s.QualifiedName
			if qn == "" {
				qn = s.Name
			}
			out = append(out, map[string]any{
				"name":          s.Name,
				"qualifiedName": qn,
				"filePath":      s.FilePath,
				"line":          s.Span.Start,
				"kind":          s.Kind,
				"signature":     s.Signature,
			})
		}
		return out
	}
	out := map[string]any{
		"query":            r.Query,
		"contract":         compact(r.Contract),
		"missing":          compact(r.Missing),
		"implementedCount": r.ImplementedCount,
	}
	if len(r.AbstractMissing) > 0 {
		out["abstractMissing"] = compact(r.AbstractMissing)
	}
	if len(r.Unverifiable) > 0 {
		out["unverifiable"] = compact(r.Unverifiable)
		out["unverifiableNote"] = "these types have no visible implementation but their " +
			"superclass chain leaves the index — an external base class may provide the " +
			"member; verify before treating them as broken"
	}
	if r.DefaultProvided {
		out["defaultProvided"] = true
		out["note"] = "the contract supplies a body every subtype inherits, so nothing is " +
			"compile-broken today — 'missing' lists the types that inherit the default and " +
			"would break if the member became abstract/required"
	}
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
	}
	return out, nil
}

// identRe: rename targets must be bare identifiers — a path or expression
// in the newName slot produces garbage edits framed as authoritative.
var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func (h *Handler) toolRenamePlan(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	newName := stringArg(args, "newName", "")
	if query == "" || newName == "" {
		return nil, errors.New("query and newName are required")
	}
	if !identRe.MatchString(newName) {
		return nil, fmt.Errorf("newName must be a bare identifier, got %q "+
			"(did a directory path land in the newName position?)", newName)
	}
	r, err := h.Grove.RenamePlan(ctx, query, newName)
	if err != nil {
		return nil, err // grove already prefixes; re-wrapping tripled the message
	}
	h.Ledger.RecordCall("prism_rename_plan")
	out := map[string]any{
		"query":      r.Query,
		"newName":    r.NewName,
		"totalSites": r.SitesTotal,
		"edits":      r.Edits,
	}
	if len(r.Unresolved) > 0 {
		out["unresolved"] = r.Unresolved
		out["unresolvedNote"] = "no line edit could be derived for these " +
			"change-set sites — handle them manually; totalSites = edit sites " +
			"+ ambiguous sites + unresolved"
	}
	if len(r.Ambiguous) > 0 {
		out["ambiguous"] = r.Ambiguous
		out["ambiguousNote"] = "these lines sit in methods that also call a same-named " +
			"method on an unrelated type — verify the receiver resolves to the renamed " +
			"member before applying"
	}
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
		out["warning"] = "the member overrides an external contract — renaming it breaks " +
			"that contract; do not proceed"
	}
	return out, nil
}

func (h *Handler) toolDeadCode(ctx context.Context, args map[string]any) (any, error) {
	var roots []string
	if raw, ok := args["roots"].([]any); ok {
		for _, v := range raw {
			if sv, ok := v.(string); ok && sv != "" {
				roots = append(roots, sv)
			}
		}
	}
	r, err := h.Grove.DeadCode(ctx, roots)
	if err != nil {
		return nil, fmt.Errorf("dead-code: %w", err)
	}
	h.Ledger.RecordCall("prism_dead_code")
	site := func(s grove.SymbolRecord) map[string]any {
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		return map[string]any{
			"name": s.Name, "qualifiedName": qn, "filePath": s.FilePath,
			"line": s.Span.Start, "kind": s.Kind,
		}
	}
	dead := make([]map[string]any, 0, len(r.Dead))
	for _, s := range r.Dead {
		dead = append(dead, site(s))
	}
	exported := make([]map[string]any, 0, len(r.ExportedUnreferenced))
	for _, s := range r.ExportedUnreferenced {
		exported = append(exported, site(s))
	}
	return map[string]any{
		"rootCount":            r.RootCount,
		"reachableCount":       r.ReachableCount,
		"considered":           r.Considered,
		"dead":                 dead,
		"exportedUnreferenced": exported,
		"caveats":              r.Caveats,
	}, nil
}

// --- helpers -------------------------------------------------------------

func categorize(s grove.SymbolRecord) ranking.Category {
	// Tests usually live in language-specific test file patterns.
	p := strings.ToLower(s.FilePath)
	if strings.Contains(p, "_test.") || strings.Contains(p, ".test.") ||
		strings.Contains(p, ".spec.") || strings.Contains(p, "/__tests__/") ||
		strings.HasSuffix(p, "_test.py") ||
		strings.HasSuffix(p, "test.java") || strings.HasSuffix(p, "tests.java") ||
		strings.Contains(p, "/tests/") || strings.Contains(p, "/test/") ||
		strings.HasSuffix(p, "_test.rs") || strings.HasSuffix(p, "tests.rs") ||
		strings.HasSuffix(p, "_test.c") || strings.HasSuffix(p, "_test.h") ||
		strings.HasSuffix(p, "_test.cc") || strings.HasSuffix(p, "_test.cpp") ||
		strings.HasSuffix(p, "test.cs") || strings.HasSuffix(p, "tests.cs") ||
		strings.HasSuffix(p, "test.php") || strings.HasSuffix(p, "tests.php") {
		return ranking.CategoryTest
	}
	if s.Kind == "namespace" || strings.HasSuffix(p, ".md") {
		return ranking.CategoryDoc
	}
	if s.Docstring != "" && s.Signature == "" {
		return ranking.CategoryDoc
	}
	// Consts whose value is a large multi-line string containing markdown
	// markers (e.g. steeringInstructions) are documentation, not code.
	if s.Kind == "const" && isMarkdownStringConst(s.RawText) {
		return ranking.CategoryDoc
	}
	return ranking.CategoryDependency
}

// isMarkdownStringConst reports whether raw is a const declaration whose
// value is a multi-line string with 3+ markdown structural markers.
func isMarkdownStringConst(raw string) bool {
	if strings.Count(raw, "\n") < 5 {
		return false
	}
	markers := 0
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "##") || strings.HasPrefix(l, "|---") ||
			strings.HasPrefix(l, "| ---") || (strings.HasPrefix(l, "- ") && len(l) > 4) {
			markers++
		}
	}
	return markers >= 3
}

func filterDocSeeds(in []grove.SymbolRecord) []grove.SymbolRecord {
	out := in[:0]
	for _, s := range in {
		if categorize(s) != ranking.CategoryDoc {
			out = append(out, s)
		}
	}
	return out
}

func filterGeneratedPrismContext(in []grove.SymbolRecord) []grove.SymbolRecord {
	out := in[:0]
	for _, sym := range in {
		if isGeneratedPrismContext(sym) {
			continue
		}
		out = append(out, sym)
	}
	return out
}

func isGeneratedPrismContext(sym grove.SymbolRecord) bool {
	p := strings.TrimPrefix(filepath.ToSlash(sym.FilePath), "./")
	switch p {
	case ".mcp.json",
		".cursor/mcp.json",
		".windsurf/mcp.json",
		".vscode/mcp.json",
		".kiro/settings/mcp.json",
		"prism.yaml":
		return true
	case "CLAUDE.md",
		"AGENTS.md",
		"GEMINI.md",
		".cursorrules",
		".windsurfrules",
		".clinerules",
		".amp/instructions.md",
		".devin/instructions.md",
		".github/copilot-instructions.md",
		".kiro/steering/prism.md",
		".kiro/steering/provasign.md":
		text := sym.RawText
		if text == "" {
			text = sym.Docstring
		}
		return strings.Contains(text, "## Prism — context delivery")
	}
	return false
}

func stringArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

// stringsArg reads a value that may be a single string OR an array of them,
// returning the non-empty entries in order with duplicates dropped.
//
// This is what lets prism_search take several terms in one call. Measured on
// the 190-cell A/B: 490 of 900 search calls (54%) sat in back-to-back runs of
// 2-10, each a separate turn whose result is then re-read on every later turn.
// One call with N terms is one turn and one result.
func stringsArg(args map[string]any, key string) []string {
	var raw []any
	switch v := args[key].(type) {
	case string:
		raw = []any{v}
	case []string:
		for _, s := range v {
			raw = append(raw, s)
		}
	case []any:
		raw = v
	default:
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return def
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func safePathWithinRoot(root, p string) (abs string, sessionPath string, err error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}
	// Resolve symlinks so /tmp and /private/tmp compare equal on macOS.
	if resolved, e := filepath.EvalSymlinks(rootAbs); e == nil {
		rootAbs = resolved
	}
	rootAbs = filepath.Clean(rootAbs)

	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
	} else {
		candidate = filepath.Clean(filepath.Join(rootAbs, p))
	}
	// Resolve symlinks on the FINAL joined path, not just absolute inputs —
	// a relative path like "leak.go" that is itself a symlink to outside
	// root joined to a containment check on the unresolved join, then
	// os.ReadFile followed the link at read time, serving the external
	// file's content under the in-repo name. EvalSymlinks fails harmlessly
	// for a not-yet-existing path; the unresolved candidate falls through
	// to the containment check below and then to a normal "not found" at
	// the read site, so this changes no behavior for missing files.
	if resolved, e := filepath.EvalSymlinks(candidate); e == nil {
		candidate = resolved
	}

	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q is outside workspace root", p)
	}

	return candidate, normalizePath(rel), nil
}

func normalizePath(p string) string {
	p = filepath.Clean(p)
	return filepath.ToSlash(p)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}


// filterSymbolsByScope applies the same path=/glob= narrowing to indexed
// symbols that textsearch applies to raw hits. Empty scope passes through.
func filterSymbolsByScope(syms []grove.SymbolRecord, sc searchScope) []grove.SymbolRecord {
	if len(sc.paths) == 0 && len(sc.glob) == 0 {
		return syms
	}
	// NOT syms[:0]: reusing the caller's backing array rewrites the very
	// slice we are filtering (caught by TestFilterSymbolsByScope — the
	// second call in one test saw data the first had clobbered).
	keep := make([]grove.SymbolRecord, 0, len(syms))
	for _, s := range syms {
		p := filepath.ToSlash(s.FilePath)
		ok := len(sc.paths) == 0
		for _, want := range sc.paths {
			w := strings.TrimSuffix(filepath.ToSlash(want), "/")
			if p == w || strings.HasPrefix(p, w+"/") {
				ok = true
				break
			}
		}
		if ok && len(sc.glob) > 0 {
			ok = false
			for _, g := range sc.glob {
				if m, _ := filepath.Match(g, filepath.Base(p)); m {
					ok = true
					break
				}
				if m, _ := filepath.Match(g, p); m {
					ok = true
					break
				}
			}
		}
		if ok {
			keep = append(keep, s)
		}
	}
	return keep
}


// mineTaskIdentifiers lifts identifier-shaped tokens (CamelCase, snake_case,
// Dotted.Names, backtick-quoted) out of a task description, excluding the
// caller's explicit terms. These seed AFTER explicit terms — pure fallback
// signal, capped small.
func mineTaskIdentifiers(task string, explicit []string) []string {
	have := make(map[string]bool, len(explicit))
	for _, t := range explicit {
		have[strings.ToLower(t)] = true
	}
	re := regexp.MustCompile("[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*")
	seen := map[string]bool{}
	var out []string
	for _, tok := range re.FindAllString(task, -1) {
		if len(out) >= 4 {
			break
		}
		lt := strings.ToLower(tok)
		if len(tok) < 5 || have[lt] || seen[lt] {
			continue
		}
		// identifier-shaped: mixed case beyond the first rune, an
		// underscore, or a dotted path — plain words don't qualify.
		mixed := strings.ToLower(tok) != tok && strings.ToUpper(tok) != tok
		if !mixed && !strings.ContainsAny(tok, "._") {
			continue
		}
		seen[lt] = true
		out = append(out, tok)
	}
	return out
}

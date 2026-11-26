package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/lru"
	"github.com/provasign/prism/internal/sensitive"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/workspace"
)

// Handler holds the shared backend state used by the prism_* tools.
type Handler struct {
	Cfg     *config.Config
	Root    string
	Grove   *grove.Client
	Session *session.Tracker
	Ledger  *session.Ledger
	// Workspace is the set of other repositories linked to Root, or nil.
	Workspace *workspace.Workspace
	// toolSurface is true when answers go to an MCP client, false for the
	// CLI: a hint must name the argument the reader can actually pass.
	toolSurface bool
	// wsStamp is workspace.Stamp when Workspace was loaded. A server runs
	// for hours; a link added or removed meanwhile must take effect without
	// a restart.
	wsMu    sync.Mutex
	wsStamp string
	// fresh caches freshenLinked per linked root for a few seconds in a
	// long-lived server: git status on every linked repository for every
	// call is the single largest cost of a cross-repository answer.
	freshMu sync.Mutex
	fresh   map[string]freshness

	// driftBase records the symbols delivered with each full file read this
	// session, so prism_drift can diff structurally (renames, breaking
	// changes) via Grove's GraphDiff instead of comparing hashes.
	driftMu   sync.Mutex
	driftBase *lru.Cache[string, []grove.SymbolRecord]

	// readyCh is closed when the background Grove connection + initial index
	// completes. Nil means no deferred init (Grove is already ready).
	readyCh <-chan struct{}

	// hypLedger accumulates per-session evidence for the scope note
	// (hypothesisledger.go).
	hypLedger hypothesisLedger

	// Feedback store (in-memory; persisted across MCP calls in one session).
	fbMu     sync.Mutex
	feedback []FeedbackEntry

	// once: notes already said this session (oncenotes.go).
	once onceNotes

	// deliveredRanges records source windows already placed in this session's
	// context by prism_query/prism_read. Whole-file session caching alone cannot
	// recognize a later ranged read of a query window.
	rangeMu         sync.Mutex
	deliveredRanges *lru.Cache[string, deliveredFileRanges]

	// indexErrs: per-file index failures the previous refresh reported
	// (indexerrors.go).
	indexErrs indexErrorTracker
}

// sessionFileCacheLimit caps the per-file session maps (drift baselines and
// delivered ranges). A long session on a large repository otherwise keeps
// every file it ever read; past the cap the oldest file falls back to the
// paths a fresh file takes (hash drift, a full read).
const sessionFileCacheLimit = 4000

// NewHandler constructs a handler with sensible defaults.
func NewHandler(cfg *config.Config, root string, client *grove.Client) *Handler {
	if cfg != nil {
		sensitive.Add(cfg.SensitivePaths...)
	}
	return NewHandlerWithLedger(cfg, root, client, nil)
}

// NewHandlerWithReady constructs a handler that defers the Grove connection.
// readyCh must be closed by the caller once Grove is reachable and indexed;
// Invoke will block until then (or until its own 60-second timeout fires).
func NewHandlerWithReady(cfg *config.Config, root string, client *grove.Client, readyCh <-chan struct{}) *Handler {
	h := NewHandlerWithLedger(cfg, root, client, nil)
	h.readyCh = readyCh
	h.toolSurface = true
	return h
}

// NewHandlerWithLedger constructs a handler and optionally reuses an existing ledger.
func NewHandlerWithLedger(cfg *config.Config, root string, client *grove.Client, ledger *session.Ledger) *Handler {
	tr := session.NewTracker(cfg.MaxCacheFiles)
	if ledger == nil {
		ledger = session.NewLedger(time.Now().Format("20060102-150405"))
	}
	h := &Handler{
		Cfg:             cfg,
		Root:            root,
		Grove:           client,
		Session:         tr,
		Ledger:          ledger,
		driftBase:       lru.New[string, []grove.SymbolRecord](sessionFileCacheLimit),
		deliveredRanges: lru.New[string, deliveredFileRanges](sessionFileCacheLimit),
		Workspace:       workspace.Load(root, cfg),
		wsStamp:         workspace.Stamp(root),
	}
	return h
}

// reloadWorkspace re-reads the links when a file that defines them changed.
func (h *Handler) reloadWorkspace() {
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	stamp := workspace.Stamp(h.Root)
	if stamp == h.wsStamp {
		return
	}
	h.wsStamp = stamp
	cfg, err := config.LoadFromDir(h.Root)
	if err != nil {
		return
	}
	if h.Cfg != nil {
		h.Cfg.WorkspaceRepos, h.Cfg.WorkspaceServer = cfg.WorkspaceRepos, cfg.WorkspaceServer
	}
	h.Workspace = workspace.Load(h.Root, cfg)
	h.freshMu.Lock()
	h.fresh = nil
	h.freshMu.Unlock()
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
	h.driftBase.Put(file, syms)
	h.driftMu.Unlock()
}

// driftBaseFor returns the delivered-symbol baseline for file, if any.
func (h *Handler) driftBaseFor(file string) []grove.SymbolRecord {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	syms, _ := h.driftBase.Get(file)
	return syms
}

// FeedbackEntry is one user rating of a tool response.
type FeedbackEntry struct {
	Tool      string `json:"tool"`
	QueryID   string `json:"queryId,omitempty"`
	Rating    int    `json:"rating"`
	Notes     string `json:"notes,omitempty"`
	Timestamp string `json:"timestamp"`
}

// maxNameArg bounds a symbol name, query or path argument. A 100 KB name
// was echoed back in full by every error that quoted it (BUG-21).
const maxNameArg = 1000

// checkArgLengths rejects an over-long name, query or path argument and
// quotes only its first characters.
func checkArgLengths(args map[string]any) error {
	check := func(key, v string) error {
		if len(v) <= maxNameArg {
			return nil
		}
		return fmt.Errorf("%s is %d characters (%q…); names, queries and paths are at most %d", key, len(v), clipRunes(v, 40), maxNameArg)
	}
	for _, key := range []string{"name", "query", "symbol", "symbol_file", "file", "new_name", "newName", "terms", "path"} {
		switch v := args[key].(type) {
		case string:
			if err := check(key, v); err != nil {
				return err
			}
		case []string:
			for _, item := range v {
				if err := check(key, item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range v {
				if str, ok := item.(string); ok {
					if err := check(key, str); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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
		"prism_index", "prism_drift", "prism_test_plan",
		"prism_compact", "prism_savings", "prism_feedback",
	}
}

// Invoke routes a tools/call to the right handler.
func (h *Handler) Invoke(name string, args map[string]any) (out any, err error) {
	return h.InvokeContext(context.Background(), name, args)
}

// InvokeContext is Invoke under a caller's context: the MCP server cancels
// it when the client sends notifications/cancelled (SEC-06).
func (h *Handler) InvokeContext(parent context.Context, name string, args map[string]any) (out any, err error) {
	h.reloadWorkspace()
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
	if err := checkArgLengths(args); err != nil {
		return nil, err
	}
	timeout := 60 * time.Second
	switch name {
	case "prism_verify", "prism_test_plan", "prism_change_impact_diff", "prism_index", "prism_map", "prism_cycles", "prism_dead_code":
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// In MCP mode, Grove connection and initial index run in the background so
	// the MCP handshake (initialize / tools/list) can complete immediately.
	// Wait here until Grove is ready before dispatching any tool call.
	// A read serves file text from disk and needs no graph, so it is not
	// held behind the first index: on a large cold repository every early
	// call, reads included, failed after a minute (2026-10-15 replay).
	if h.readyCh != nil && name != "prism_read" && name != "prism_read_compact" {
		select {
		case <-h.readyCh:
		case <-ctx.Done():
			return nil, errors.New("the first index of this repository is still running (a never-built project compiles its dependencies once; this can take several minutes) — it continues in the background; retry this call shortly. op=read works meanwhile")
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
	// Unknown-parameter rejection. Measured (2026-09-02, b8mjxuh6 #26): an
	// agent called prism_verify with query="Type.method" — a parameter the
	// tool does not have — got the whole-repo verdict back with no warning,
	// and read it as if it were scoped to that symbol. Silently accepted
	// wrong parameters produce confidently misread answers; an error that
	// names the valid parameters produces a one-step correction instead.
	// Validated against the tool's own published schema (the same source
	// tools/list serves), so the check can never drift from what agents see.
	if name != "prism_read_compact" && name != "prism_query_compact" {
		if err := rejectUnknownArgs(name, args); err != nil {
			return nil, err
		}
	}
	switch name {
	// The index-backed READ tools delta-reindex first, for the same reason
	// the whole-repo tools below do: an answer computed from an index that
	// predates the caller's own edit is a wrong answer delivered silently.
	// prism_read is absent on purpose — it reads the file from disk.
	case "prism_query":
		return h.freshened(ctx, func() (any, error) { return h.toolQuery(ctx, args) })
	case "prism_read":
		return h.toolRead(ctx, args)
	case "prism_read_compact":
		return h.readRanges(ctx, args)
	case "prism_query_compact":
		return h.freshened(ctx, func() (any, error) {
			return h.toolQueryScoped(ctx, args, searchScope{paths: stringsArg(args, "paths"), glob: stringsArg(args, "glob")})
		})
	case "prism_search":
		if stringArg(args, "scope", "both") == "text" {
			return h.freshenedWithin(ctx, textRefreshBudget, func() (any, error) { return h.toolSearch(ctx, args) })
		}
		return h.freshened(ctx, func() (any, error) { return h.toolSearch(ctx, args) })
	case "prism_lookup":
		return h.freshened(ctx, func() (any, error) { return h.toolLookup(ctx, args) })
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
		return h.freshened(ctx, func() (any, error) { return h.toolReferences(ctx, args) })
	case "prism_resolve":
		return h.freshened(ctx, func() (any, error) { return h.toolResolve(ctx, args) })
	case "prism_edges":
		return h.freshened(ctx, func() (any, error) { return h.toolEdges(ctx, args) })
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
		return h.freshened(ctx, func() (any, error) { return h.toolNode(ctx, args) })
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
	case "prism_test_plan":
		return h.toolTestPlan(ctx, args)
	case "prism_change_impact_diff":
		// CLI-only, like test-plan: not in any tool schema (GAP-05).
		return h.toolChangeImpactDiff(ctx, args)
	case "prism_callers":
		// CLI-only (GAP-03): not in any tool schema.
		return h.toolCallers(ctx, args)
	case "prism_call_path":
		return h.toolCallPath(ctx, args)
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

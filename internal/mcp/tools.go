package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
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

	// semScores holds the current query's semantic similarity scores from
	// Grove (model2vec, vectors cached by symbol ID inside the engine).
	semMu     sync.Mutex
	semScores map[string]float64

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
	// H: warm-load the persisted LRU so this session starts at sha-pointer
	// level for files the agent has seen recently and that haven't changed.
	session.LoadCache(tr, root, 0 /* default 7 days */)
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
	h.Signals = ranking.NewSignalComputer(root, semanticAdapter{h: h})
	return h
}

// SaveSessionCache flushes the LRU tracker to disk. Called by the MCP server
// on shutdown so the next session opens warm.
func (h *Handler) SaveSessionCache() {
	session.SaveCache(h.Session, h.Root, 500)
}

// loadSemanticScores fetches Grove's semantic ranking for task and caches
// the scores by symbol ID for this query's signal computation. The engine
// caches embedding vectors by symbol ID across index rebuilds, so only
// changed files' symbols are re-embedded — Prism keeps no corpus of its own.
func (h *Handler) loadSemanticScores(ctx context.Context, task string) {
	scored, err := h.Grove.Semantic(ctx, task, 200)
	h.semMu.Lock()
	defer h.semMu.Unlock()
	h.semScores = map[string]float64{}
	if err != nil {
		return
	}
	for _, sc := range scored {
		h.semScores[sc.Symbol.ID] = sc.Score
	}
}

// semanticAdapter exposes the per-query Grove semantic scores to the ranker.
// Symbols outside the fetched top-N score 0 (graph distance and the other
// signals still rank them).
type semanticAdapter struct{ h *Handler }

func (a semanticAdapter) Similarity(_ string, sym grove.SymbolRecord) float64 {
	a.h.semMu.Lock()
	defer a.h.semMu.Unlock()
	return a.h.semScores[sym.ID]
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

// Invoke routes a tools/call to the right handler.
func (h *Handler) Invoke(name string, args map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	// Every tool except prism_index resolves against the root the server was
	// started with; the Grove client is bound to it. A different "dir" used to
	// be silently ignored, producing empty results — reject it loudly instead.
	if name != "prism_index" {
		if dir := stringArg(args, "dir", ""); dir != "" && !sameRoot(dir, h.Root) {
			return nil, fmt.Errorf("server is rooted at %s and cannot serve dir %s; restart with `prism mcp %s` or run the prism CLI from that directory", h.Root, dir, dir)
		}
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
	case "prism_evidence":
		return h.toolEvidence(ctx, args)
	case "prism_drift":
		return h.toolDrift(ctx, args)
	case "prism_references":
		return h.toolReferences(ctx, args)
	case "prism_resolve":
		return h.toolResolve(ctx, args)
	case "prism_edges":
		return h.toolEdges(ctx, args)
	case "prism_change_impact":
		return h.toolChangeImpact(ctx, args)
	case "prism_missing_implementations":
		return h.toolMissingImplementations(ctx, args)
	case "prism_untested_surface":
		return h.toolUntestedSurface(ctx, args)
	case "prism_affected":
		return h.toolAffected(ctx, args)
	case "prism_dead_code":
		return h.toolDeadCode(ctx, args)
	case "prism_rename_plan":
		return h.toolRenamePlan(ctx, args)
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
	names := []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_references", "prism_resolve", "prism_edges", "prism_change_impact",
		"prism_missing_implementations", "prism_untested_surface", "prism_dead_code",
		"prism_rename_plan", "prism_affected",
		"prism_index", "prism_drift",
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{
			"name":        n,
			"description": toolDescription(n),
			"inputSchema": toolSchema(n),
		})
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
			"required": []string{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "What you are trying to do.",
				},
				"terms": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Your grep/rg search terms (e.g. [\"AccessCount\"]). Prism searches these then expands via call graph.",
				},
				"include": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": []string{"graph", "tests", "docs", "coverage_gaps"}},
					"description": "Categories: graph (callers/callees), tests, docs (filenames only), coverage_gaps (untested symbols; audits only the seeds + blast radius, so use 1-2 terms per query and union results). Default: [\"graph\",\"tests\"].",
				},
				"graph_depth": map[string]any{
					"type":        "integer",
					"description": "BFS hops: 1=immediate callers, 2=default, 3+=blast radius.",
				},
				"model":        modelProp,
				"context_used": contextUsedProp,
				"profile":      map[string]any{"type": "string", "description": "Ranking profile: default|implement_feature|fix_bug|code_review"},
				"budget":       map[string]any{"type": "integer", "description": "Token budget. Explicit values are honored exactly; default 8000."},
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
					"type":        "string",
					"description": "Substring matched against symbol names, signatures, and docstrings.",
				},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 25)."},
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
					"description": "Disambiguate a name shared across packages: file path (or substring, as shown by prism_resolve).",
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
				"kinds":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"calls", "tests", "uses-type", "implements", "extends", "overrides", "contains", "defines", "imports"}}, "description": "Edge kinds to return. Default [calls,tests]."},
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
	case "prism_evidence":
		return map[string]any{
			"type":     "object",
			"required": []string{"claims"},
			"properties": map[string]any{
				"claims": map[string]any{
					"type":        "array",
					"description": "Array of {claim, file, lineStart?, lineEnd?, symbolName?} objects.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"claim":      map[string]any{"type": "string"},
							"file":       map[string]any{"type": "string"},
							"lineStart":  map[string]any{"type": "integer"},
							"lineEnd":    map[string]any{"type": "integer"},
							"symbolName": map[string]any{"type": "string"},
						},
					},
				},
			},
		}
	case "prism_change_impact", "prism_missing_implementations", "prism_untested_surface":
		return map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Type.method or Type.method(ParamType, ...) — e.g. \"JsonSerializer.serialize(T, JsonGenerator, SerializerProvider)\".",
				},
			},
		}
	case "prism_affected":
		return map[string]any{
			"type":     "object",
			"required": []string{"files"},
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Repo-relative changed files (e.g. from `git diff --name-only`).",
				},
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
			},
		}
	default:
		return open
	}
}

func toolDescription(name string) string {
	switch name {
	case "prism_query":
		return "Call AFTER grep/rg locates an anchor. Pass the same terms=[...] you searched — " +
			"Prism finds those symbols then expands through the call graph (callers, callees, tests). " +
			"Use include=[\"coverage_gaps\"] when writing or fixing code. " +
			"Use include=[\"docs\"] for doc filenames only."
	case "prism_read":
		return "Whole-file read with session compression: full content on first read; a repeat read of " +
			"an UNCHANGED file returns a one-line `// [prism:cached] <file> @sha:… (prior delivery still " +
			"in context)` pointer INSTEAD of the body — this is not an error or an empty file: you already " +
			"received this file earlier in the session, so use that copy and do NOT re-fetch. " +
			"For a single function use prism_lookup (~5× cheaper)."
	case "prism_search":
		return "DISCOVERY: substring search over indexed symbol names, signatures, and docstrings — " +
			"the on-ramp when you only have a concept and need to FIND an anchor symbol. " +
			"Does NOT search source code text — for that, use grep (also fine for discovery). " +
			"Workflow: search/grep to FIND an anchor, then prism_resolve/prism_edges/prism_lookup to TRAVERSE " +
			"and READ from it. Don't guess names with resolve when you haven't searched yet."
	case "prism_lookup":
		return "Read one symbol by qualified name (e.g. 'ranking.Select', " +
			"'kvstore.SecretsKVStoreSQL.Get'). Choose which COLUMNS to read with fields=[...]: " +
			"signature (the contract, cheap), doc, body (full source), kind, parent, modifiers — " +
			"omit fields to get the whole body. Every result includes the exact file:line, which is " +
			"AUTHORITATIVE: navigate straight to it, do not re-confirm with grep. ~5× cheaper than " +
			"reading the whole file; fields=[signature] is cheaper still."
	case "prism_resolve":
		return "Disambiguate a name you ALREADY HAVE into the symbol(s) it could be — each with kind and " +
			"exact file:line, test doubles tagged and last. Then prism_edges/prism_lookup the one you want. " +
			"The file:line is AUTHORITATIVE — trust it, don't re-grep to verify. " +
			"NOTE: resolve does not DISCOVER. If you don't yet know a symbol name (you only have a concept, " +
			"like 'where a secret is read'), first FIND the anchor with grep/prism_search/prism_references, " +
			"then resolve/traverse from it. Never guess names by trying resolve repeatedly."
	case "prism_edges":
		return "Walk the code graph one hop from a symbol. The graph has these edge kinds: " +
			"calls (X calls Y), tests (a test exercises Y), uses-type (X mentions a type), " +
			"implements/extends/overrides, contains, defines, imports. direction=out gives edges FROM " +
			"the seed (its callees, the types it uses); direction=in gives edges INTO it (its callers, " +
			"its tests). Recipes: what does X call → (out, [calls]); who calls X → (in, [calls]); " +
			"what tests X → (in, [tests]); interface dispatch resolves: (out, [calls]) returns the " +
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
		return "Delta-index the workspace through Grove. " +
			"Call once at session start or after significant file changes. " +
			"Do not call on every step — delta indexing runs automatically."
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
	case "prism_evidence":
		return "Convert a sub-agent prose summary into typed {claim, file, line} citations. " +
			"Send to parent agent instead of prose. Each claim is dereferenceable via prism_lookup."
	case "prism_change_impact":
		return "Deterministic change-set for a method signature change: pass 'Type.method' or " +
			"'Type.method(ParamType, ...)' and get back the exact declaration(s), every " +
			"override/implementation in the subtype closure (family), super-declarations, and " +
			"all resolved callers — in one engine call, milliseconds, no token cost. " +
			"Use this instead of prism_references + manual override hunting when you need to " +
			"find every site affected by a method signature change. Result groups: declarations " +
			"(the method itself), family (overrides + implementations), supers (same-member " +
			"declarations on other contracts — sibling interfaces satisfied by the same " +
			"implementations break under the change too), callers (call sites into the set), " +
			"declaringTypes (the interface/type declaration blocks that textually change " +
			"because their member specs are not separate symbols — Go/TS; ALWAYS include " +
			"these as change sites). Check 'completeness': 'closed' " +
			"means the set is authoritative; 'project-local' + 'overridesExternal' means the " +
			"method belongs to an external (JDK/dependency) contract — its signature cannot " +
			"safely change, and calls typed against the external supertype are not included. " +
			"Querying an external type directly (e.g. 'Iterator.next') returns the project's " +
			"implementation closure of that contract — use for deprecation/migration sweeps. " +
			"RELAY the returned set as-is: do not re-verify, re-filter, or transform it " +
			"through shell pipelines — re-processing a solved traversal measurably drops " +
			"real sites and adds spurious ones."
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
	case "prism_untested_surface":
		return "Coverage partition of a change-set: pass 'Type.method' and get the same " +
			"change-set as prism_change_impact, split into covered (a test reaches the site " +
			"within 3 resolved caller hops; up to 3 example tests + true count) and untested " +
			"(no such test — the sites a signature change can break silently). THE pre-refactor " +
			"pipeline: change_impact to see the blast radius, untested_surface to know which of " +
			"those sites to write tests for FIRST. 'untested' means no test within the resolved " +
			"caller horizon — dynamic dispatch the graph cannot see (reflection, framework " +
			"executors) may still exercise the site, so treat it as a work list, not proof. " +
			"RELAY the partition as-is; do not re-derive coverage via grep."
	case "prism_affected":
		return "The file-diff form of test selection: pass the CHANGED FILES (repo-relative, " +
			"e.g. `git diff --name-only`) and get back exactly the tests that cover any symbol " +
			"defined in them — grouped by test file, so a CI step can run ONLY the affected " +
			"tests instead of the whole suite. Coverage follows the same evidence-backed graph " +
			"traversal as untested_surface (a test reaches a changed symbol within the resolved " +
			"caller horizon); low-confidence edges are excluded so unrelated tests are not swept " +
			"in. Dynamic dispatch the graph cannot see (reflection, framework executors) may " +
			"still exercise a site, so treat the set as a high-signal selection, not a proof of " +
			"total isolation. RELAY the result as-is."
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
	}
	return "Prism tool: " + name
}

// --- Tool implementations -----------------------------------------------

type queryResult struct {
	BudgetUsed   int            `json:"budgetUsed"`
	Symbols      []rankedSymbol `json:"symbols"`
	CoverageGaps []coverageGap  `json:"coverageGaps,omitempty"`
	// Note explains an empty result so agents can tell "wrong root" or
	// "term typo" apart from "genuinely no matches" without guessing.
	Note string `json:"note,omitempty"`
}

// coverageGap is a code symbol in the query blast radius that has no test
// edges in the graph. Returned only when include contains "coverage_gaps".
type coverageGap struct {
	Name     string         `json:"name"`
	QualName string         `json:"qualifiedName,omitempty"`
	FilePath string         `json:"filePath"`
	Kind     string         `json:"kind"`
	Span     grove.SpanInfo `json:"span,omitempty"`
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
	// Accepted values: "graph" (code + callers/callees), "tests", "docs".
	// Default when omitted: ["graph", "tests"].
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
		includeSet = map[string]bool{"graph": true, "tests": true}
	}

	// graph_depth: BFS depth for Impact() calls. Default 2.
	graphDepth := intArg(args, "graph_depth", 2)
	if graphDepth < 1 {
		graphDepth = 1
	}
	if graphDepth > 5 {
		graphDepth = 5
	}

	// B: phase-aware budget shaping — infer the agent work phase from the task
	// description and auto-select a matching profile + budget multiplier.
	// An explicit "profile" arg always wins; otherwise let phase detection decide.
	explicitProfile := stringArg(args, "profile", "")
	phase := ranking.DetectPhase(task)
	phaseProfileHint, phaseBudgetMult := ranking.ShapeForPhase(phase)
	profileName := explicitProfile
	if profileName == "" {
		profileName = phaseProfileHint
	}
	if profileName == "" {
		profileName = h.Cfg.Profile
	}
	callCfg := h.Cfg.WithModel(stringArg(args, "model", ""))
	limit := intArg(args, "limit", 50)
	contextUsed := int64(intArg(args, "context_used", 0))

	// Semantic similarity scores for this task, served from Grove's cached
	// embedding index (one engine call; no corpus rebuild in Prism).
	h.loadSemanticScores(ctx, task)
	var seeds []grove.SymbolRecord

	if len(terms) > 0 {
		// Term-seeded retrieval: search for each agent-supplied term and union
		// the results. This gives grep-level precision as the entry point.
		seenTermSeeds := map[string]bool{}
		for _, term := range terms {
			matches, err := h.Grove.SearchSymbols(ctx, term, 10)
			if err != nil {
				continue
			}
			// Prioritise symbols whose Name/QualifiedName contains the term
			// (grep-level precision). Content-only matches (term appears only
			// in RawText) are capped at 3 to suppress doc-string noise.
			termLower := strings.ToLower(term)
			var nameHits, contentHits []grove.SymbolRecord
			for _, m := range matches {
				if strings.Contains(strings.ToLower(m.Name), termLower) ||
					strings.Contains(strings.ToLower(m.QualifiedName), termLower) {
					nameHits = append(nameHits, m)
				} else {
					contentHits = append(contentHits, m)
				}
			}
			if len(contentHits) > 3 {
				contentHits = contentHits[:3]
			}
			// Prefer real implementations over test doubles among name hits, so a
			// term like "DecryptedValues" seeds the graph on the real Service
			// method (and expands its call chain) rather than on a mock that
			// shares the name — which would leave the real chain out of reach.
			var realHits, doubleHits []grove.SymbolRecord
			for _, m := range nameHits {
				if isTestDouble(m.FilePath) {
					doubleHits = append(doubleHits, m)
				} else {
					realHits = append(realHits, m)
				}
			}
			nameHits = append(realHits, doubleHits...)
			for _, m := range append(nameHits, contentHits...) {
				if !seenTermSeeds[m.ID] {
					seenTermSeeds[m.ID] = true
					seeds = append(seeds, m)
				}
			}
		}
		seeds = filterGeneratedPrismContext(seeds)
	} else {
		// Intent-ranked fallback (Grove Query) when no terms provided.
		var err error
		seeds, err = h.Grove.QueryByIntent(ctx, task, limit)
		if err != nil {
			return nil, fmt.Errorf("grove query: %w", err)
		}
		seeds = filterGeneratedPrismContext(seeds)
		seeds = filterDocSeeds(seeds)
	}
	// Build candidates: treat first 5 as seeds (distance 0), remainder as candidates.
	seedCount := minInt(5, len(seeds))
	seedSyms := seeds[:seedCount]
	candidateSyms := seeds[seedCount:]

	profile := ranking.SelectProfile(profileName)
	profile = h.Weights.Apply(profile)

	// For test-writing tasks, boost TestRelevance so test symbols rank higher
	// in scoring. The budget expansion happens after callerBudget is parsed.
	if explicitProfile == "" && isTestWritingTask(task) && profile.TestRelevance < 0.45 {
		profile.TestRelevance = minFloat(profile.TestRelevance*2.0, 0.45)
	}

	graphDist := make(map[string]int)
	hasTestEdgeID := make(map[string]bool)
	testFilePaths := make(map[string]bool)

	seenIDs := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seenIDs[s.ID] = true
	}
	var graphExtra []grove.SymbolRecord

	for _, seed := range seedSyms {
		// Expand by qualified name when the symbol has one: bare names
		// ("Get", "Keys") collide across packages on large repos and drag
		// unrelated symbols' callers and tests into the result set.
		seedQuery := seed.QualifiedName
		if seedQuery == "" {
			seedQuery = seed.Name
		}
		if includeSet["graph"] {
			// Use the typed call neighborhood (callees + callers, test doubles
			// excluded) rather than Grove.Impact's flat blast radius. Impact
			// traverses calls AND uses-type together and erases edge types, which
			// floods the result with type-mention noise and buries the actual
			// call chain; CallNeighbors returns exactly the resolved calls edges.
			if neighbors, err := h.Grove.CallNeighbors(ctx, seedQuery); err == nil {
				for _, nb := range neighbors {
					if _, exists := graphDist[nb.ID]; !exists {
						graphDist[nb.ID] = 1
					}
					if !seenIDs[nb.ID] {
						seenIDs[nb.ID] = true
						graphExtra = append(graphExtra, nb)
					}
				}
			}
		}
		if includeSet["tests"] {
			if tests, err := h.Grove.Tests(ctx, seedQuery); err == nil {
				for _, tst := range tests {
					hasTestEdgeID[tst.ID] = true
					testFilePaths[tst.FilePath] = true
					if _, exists := graphDist[tst.ID]; !exists {
						graphDist[tst.ID] = 1
					}
					if !seenIDs[tst.ID] {
						seenIDs[tst.ID] = true
						graphExtra = append(graphExtra, tst)
					}
				}
			}
		}
	}

	// Merge candidates and graph-enriched symbols, then filter by include set.
	merged := make([]grove.SymbolRecord, 0, len(candidateSyms)+len(graphExtra))
	merged = append(merged, candidateSyms...)
	merged = append(merged, graphExtra...)

	// Drop categories the agent did not request.
	if len(includeSet) > 0 {
		filtered := merged[:0]
		for _, sym := range merged {
			cat := string(categorize(sym))
			switch {
			case cat == string(ranking.CategoryTest) && !includeSet["tests"]:
				continue
			case cat == string(ranking.CategoryDoc) && !includeSet["docs"]:
				continue
			case (cat == string(ranking.CategoryTarget) || cat == string(ranking.CategoryDependency)) && !includeSet["graph"]:
				continue
			}
			filtered = append(filtered, sym)
		}
		merged = filtered
	}

	candidates := make([]ranking.Candidate, 0, len(merged))
	for i, sym := range merged {
		dist, inGraph := graphDist[sym.ID]
		if !inGraph {
			// Not reached by BFS: fall back to retrieval position as distance
			// proxy so semantically adjacent symbols still score above
			// unrelated ones.
			dist = 3 + (i / 10)
		}
		sv := h.Signals.Compute(ctx, task, sym, dist, hasTestEdgeID[sym.ID], testFilePaths[sym.FilePath])
		score := ranking.Score(sv, profile)
		cat := categorize(sym)
		sessionPath := normalizePath(sym.FilePath)
		entry, seen, _ := h.Session.Lookup(sessionPath, "")
		conf := session.Low
		if seen {
			conf = h.confidenceFor(entry, contextUsed, callCfg.ContextWindow())
		}
		candidates = append(candidates, ranking.Candidate{
			Symbol:         sym,
			Score:          score,
			Category:       cat,
			PreviouslySeen: seen,
			Confidence:     string(conf),
		})
	}
	// Default budget is task-sized (8k tokens), not context-window-sized.
	// The score-cliff cutoff in Select() stops early when relevance drops off,
	// so the ceiling here is a safety cap, not a fill target.
	const defaultTaskBudget = 8000
	callerBudget := intArg(args, "budget", 0)
	var budget int
	if callerBudget > 0 {
		// An explicit budget is a contract: honor it exactly — no floor, no
		// phase shaping. The caller knows its token constraints best.
		budget = callerBudget
	} else {
		budget = defaultTaskBudget
		// B: apply phase-derived budget multiplier (e.g. 0.60 for code_review),
		// floored so a shaped default never starves the response.
		if phaseBudgetMult > 0 && phaseBudgetMult != 1.0 {
			shaped := int(float64(budget) * phaseBudgetMult)
			if shaped < 4000 {
				shaped = 4000
			}
			budget = shaped
		}
		// Expand budget for test-writing tasks so the test category gets more
		// absolute token room (20% share of a larger total = more test content).
		if explicitProfile == "" && isTestWritingTask(task) {
			budget = int(float64(budget) * 1.25)
		}
	}
	picked := ranking.Select(seedSyms, candidates, budget)

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

	if len(out.Symbols) == 0 {
		switch {
		case len(seeds) == 0 && len(terms) > 0:
			out.Note = fmt.Sprintf("no symbols matched terms %v under project root %s; check term spelling and that the code lives under this root", terms, h.Root)
		case len(seeds) == 0:
			out.Note = fmt.Sprintf("no symbols matched this task under project root %s", h.Root)
		default:
			out.Note = "seeds matched but nothing fit the requested include categories/budget; try include=[\"graph\",\"tests\"] or a larger budget"
		}
	}

	// Coverage gaps: code symbols in the blast radius with no test edges.
	// Only computed when the agent explicitly requests include=["coverage_gaps"].
	if includeSet["coverage_gaps"] {
		out.CoverageGaps = buildCoverageGaps(ctx, h.Grove, seedSyms, graphExtra)
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

// buildCoverageGaps returns code symbols (seeds + blast-radius) that have no
// direct `tests` edge pointing at them in Grove's graph. Grove scopes test
// edges through the import graph and backs them with call-site evidence
// (v0.6.0), so the edge itself is the authority — no name heuristics. Cost is
// one Deps() call per distinct file.
func buildCoverageGaps(ctx context.Context, g *grove.Client, seeds []grove.SymbolRecord, blastRadius []grove.SymbolRecord) []coverageGap {
	var gaps []coverageGap
	seen := make(map[string]bool)
	tested := newTestedChecker(g)

	isCodeSym := func(s grove.SymbolRecord) bool {
		cat := categorize(s)
		if cat == ranking.CategoryTest || cat == ranking.CategoryDoc {
			return false
		}
		if s.Kind != "function" && s.Kind != "method" {
			return false
		}
		if strings.HasPrefix(s.Name, "New") {
			return false
		}
		return isExportedName(s.Name)
	}

	for _, group := range [][]grove.SymbolRecord{seeds, blastRadius} {
		for _, s := range group {
			if seen[s.ID] || !isCodeSym(s) {
				continue
			}
			seen[s.ID] = true
			if !tested.covered(ctx, s) {
				gaps = append(gaps, coverageGap{
					Name:     s.Name,
					QualName: s.QualifiedName,
					FilePath: s.FilePath,
					Kind:     s.Kind,
					Span:     s.Span,
				})
			}
		}
	}

	return gaps
}

// testedChecker answers "does a direct tests edge point at this symbol?"
// with a per-file edge cache so each file's edges are fetched once.
type testedChecker struct {
	g      *grove.Client
	byFile map[string]map[string]bool // file → set of tested edge-target keys
}

func newTestedChecker(g *grove.Client) *testedChecker {
	return &testedChecker{g: g, byFile: map[string]map[string]bool{}}
}

func (t *testedChecker) covered(ctx context.Context, sym grove.SymbolRecord) bool {
	targets, ok := t.byFile[sym.FilePath]
	if !ok {
		targets = map[string]bool{}
		if edges, err := t.g.Deps(ctx, sym.FilePath); err == nil {
			for _, e := range edges {
				if e.Type != "tests" {
					continue
				}
				// Grove v0.7.0 tiers tests edges by confidence: ≥0.8 is a
				// direct relation; lower tiers (helper-transitive 0.6–0.75,
				// one-hop-past-entry 0.55) mean "possibly related" and must
				// not silence a coverage gap.
				if e.Confidence < 0.8 {
					continue
				}
				targets[e.To] = true
				// Also key by the SHA-independent form so a record from an
				// older snapshot still matches after the blob hash moved.
				targets[trimSymbolID(e.To)] = true
			}
		}
		t.byFile[sym.FilePath] = targets
	}
	if targets[sym.ID] {
		return true
	}
	if sym.QualifiedName != "" && targets[sym.FilePath+"::"+sym.QualifiedName] {
		return true
	}
	return targets[sym.FilePath+"::"+sym.Name]
}

// trimSymbolID strips the trailing "@<blobSHA>[#n]" from a Grove symbol ID
// ("file.go::Name@abc123"), leaving the stable "file.go::Name" identity.
func trimSymbolID(id string) string {
	if i := strings.LastIndex(id, "@"); i > 0 {
		return id[:i]
	}
	return id
}

func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	ch := name[0]
	return ch >= 'A' && ch <= 'Z'
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

func (h *Handler) toolSearch(ctx context.Context, args map[string]any) (any, error) {
	q := stringArg(args, "query", "")
	limit := intArg(args, "limit", 25)

	// Grove's symbol search is ranked (exact name > prefix > substring,
	// v0.6.0) — deliver it directly, matching this tool's contract of
	// searching symbol names rather than re-ranking semantically.
	syms, err := h.Grove.SearchSymbols(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	syms = filterGeneratedPrismContext(syms)
	return map[string]any{"symbols": syms}, nil
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
	return map[string]any{
		"name":        res.Name,
		"count":       len(res.Refs),
		"definitions": res.DefCount,
		"ambiguous":   res.Ambiguous,
		"byFile":      byFile,
	}, nil
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
	// any substring of it, as shown by prism_resolve) to pick the right one.
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
	return map[string]any{"symbol": nil}, nil
}

func (h *Handler) toolIndex(_ context.Context, args map[string]any) (any, error) {
	dir := stringArg(args, "dir", h.Root)
	// Indexing large codebases can take several minutes; use a fresh context
	// with an extended deadline instead of the 60-second Invoke-level one.
	idxCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := h.Grove.Index(idxCtx, dir)
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

// EvidencePacket is the G: sub-agent evidence response.
// An array of these replaces a prose sub-agent summary in the parent context.
type EvidencePacket struct {
	Claim      string `json:"claim"`
	File       string `json:"file,omitempty"`
	LineStart  int    `json:"lineStart,omitempty"`
	LineEnd    int    `json:"lineEnd,omitempty"`
	SymbolName string `json:"symbolName,omitempty"`
	SHA        string `json:"sha,omitempty"`        // content SHA of the file at delivery time
	LookupHint string `json:"lookupHint,omitempty"` // prism_lookup key if symbol is known
}

// toolEvidence compiles a typed evidence packet from an array of caller-supplied
// claim objects. For each claim that references a file, it resolves the content
// SHA from the session tracker (if available) so the parent can verify staleness.
func (h *Handler) toolEvidence(_ context.Context, args map[string]any) (any, error) {
	rawClaims, ok := args["claims"]
	if !ok {
		return nil, errors.New("claims is required")
	}
	buf, _ := json.Marshal(rawClaims)
	var claims []map[string]any
	if err := json.Unmarshal(buf, &claims); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}

	packets := make([]EvidencePacket, 0, len(claims))
	originalTokens := 0
	for _, c := range claims {
		// Estimate tokens the prose claim would have cost if passed verbatim.
		rawJSON, _ := json.Marshal(c)
		originalTokens += ranking.EstimateTokens(string(rawJSON))

		p := EvidencePacket{
			Claim:      stringArg(c, "claim", ""),
			File:       normalizePath(stringArg(c, "file", "")),
			LineStart:  intArg(c, "lineStart", 0),
			LineEnd:    intArg(c, "lineEnd", 0),
			SymbolName: stringArg(c, "symbolName", ""),
		}
		// Resolve SHA from session tracker so the parent can detect if the
		// file changed since the sub-agent read it.
		if p.File != "" {
			if entry, seen, _ := h.Session.Lookup(p.File, ""); seen && entry.ContentHash != "" {
				short := entry.ContentHash
				if len(short) > 8 {
					short = short[:8]
				}
				p.SHA = short
			}
		}
		if p.SymbolName != "" {
			p.LookupHint = p.SymbolName
		}
		if p.Claim != "" {
			packets = append(packets, p)
		}
	}

	// Measure delivered tokens (the typed packet JSON).
	deliveredBuf, _ := json.Marshal(packets)
	deliveredTokens := ranking.EstimateTokens(string(deliveredBuf))
	h.Ledger.Record("prism_evidence", originalTokens, deliveredTokens)

	savings := 0.0
	if originalTokens > 0 {
		savings = (1.0 - float64(deliveredTokens)/float64(originalTokens)) * 100.0
	}
	return map[string]any{
		"evidence":        packets,
		"claimCount":      len(packets),
		"originalTokens":  originalTokens,
		"deliveredTokens": deliveredTokens,
		"savingsPercent":  savings,
	}, nil
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
		return nil, fmt.Errorf("change-impact: %w", err)
	}
	h.Ledger.RecordCall("prism_change_impact")
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
		"query":        r.Query,
		"declarations": compact(r.Declarations),
		"supers":       compact(r.Supers),
		"family":       compact(r.Family),
		"callers":      compact(r.Callers),
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
	return out, nil
}

func (h *Handler) toolMissingImplementations(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	r, err := h.Grove.MissingImplementations(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("missing-implementations: %w", err)
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

func (h *Handler) toolUntestedSurface(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	r, err := h.Grove.UntestedSurface(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("untested-surface: %w", err)
	}
	h.Ledger.RecordCall("prism_untested_surface")
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
	untested := make([]map[string]any, 0, len(r.Untested))
	for _, s := range r.Untested {
		untested = append(untested, site(s))
	}
	covered := make([]map[string]any, 0, len(r.Covered))
	for _, c := range r.Covered {
		entry := site(c.Symbol)
		entry["testCount"] = c.TestCount
		tests := make([]map[string]any, 0, len(c.Tests))
		for _, t := range c.Tests {
			tests = append(tests, site(t))
		}
		entry["tests"] = tests
		covered = append(covered, entry)
	}
	out := map[string]any{
		"query":      r.Query,
		"totalSites": r.TotalSites,
		"untested":   untested,
		"covered":    covered,
		"note": "covered = a test reaches the site within 3 resolved caller hops; " +
			"untested = no such test. Dynamic dispatch the graph cannot resolve " +
			"(reflection, framework executors) is not seen — untested is a " +
			"write-tests-here work list, not proof of zero coverage",
	}
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
	}
	return out, nil
}

// toolAffected maps a set of CHANGED FILES to the tests that cover them —
// the file-diff form of test selection for CI ("run only the affected tests").
func (h *Handler) toolAffected(ctx context.Context, args map[string]any) (any, error) {
	var files []string
	if raw, ok := args["files"].([]any); ok {
		for _, x := range raw {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				files = append(files, strings.TrimSpace(s))
			}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("files is required (repo-relative paths, e.g. from `git diff --name-only`)")
	}
	tests, err := h.Grove.AffectedTests(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("affected: %w", err)
	}
	h.Ledger.RecordCall("prism_affected")
	byFile := map[string][]map[string]any{}
	order := []string{}
	for _, s := range tests {
		if _, seen := byFile[s.FilePath]; !seen {
			order = append(order, s.FilePath)
		}
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		byFile[s.FilePath] = append(byFile[s.FilePath], map[string]any{
			"name": s.Name, "qualifiedName": qn, "line": s.Span.Start, "kind": s.Kind,
		})
	}
	testFiles := make([]map[string]any, 0, len(order))
	for _, f := range order {
		testFiles = append(testFiles, map[string]any{"filePath": f, "tests": byFile[f]})
	}
	return map[string]any{
		"changedFiles":  files,
		"testFiles":     testFiles, // grouped by test file — the CI unit
		"testCount":     len(tests),
		"testFileCount": len(order),
	}, nil
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
		return nil, fmt.Errorf("rename-plan: %w", err)
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

// isTestWritingTask reports whether the task description signals the agent
// is about to write or add tests, so we can surface more test context.
func isTestWritingTask(task string) bool {
	lower := strings.ToLower(task)
	return strings.Contains(lower, "write test") ||
		strings.Contains(lower, "add test") ||
		strings.Contains(lower, "test for") ||
		strings.Contains(lower, "tests for") ||
		strings.Contains(lower, "coverage for") ||
		strings.Contains(lower, "need to test")
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
		// Resolve symlinks in caller-supplied absolute paths too.
		if resolved, e := filepath.EvalSymlinks(candidate); e == nil {
			candidate = resolved
		}
	} else {
		candidate = filepath.Clean(filepath.Join(rootAbs, p))
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

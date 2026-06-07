package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/embeddings"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// Handler holds the shared backend state used by all 8 prism_* tools.
type Handler struct {
	Cfg     *config.Config
	Root    string
	Grove   *grove.Client
	Session *session.Tracker
	Ledger  *session.Ledger
	Signals *ranking.SignalComputer
	Weights *ranking.LearnedWeights // A: per-repo outcome-conditioned weights

	embMu  sync.Mutex
	emb    embeddings.Backend
	corpus []grove.SymbolRecord
	dirty  bool

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
		Cfg:     cfg,
		Root:    root,
		Grove:   client,
		Session: tr,
		Ledger:  ledger,
		dirty:   true,
		Weights: ranking.LoadLearnedWeights(root), // A: load per-repo learned weights
	}
	h.Signals = ranking.NewSignalComputer(root, semanticAdapter{h: h})
	return h
}

// SaveSessionCache flushes the LRU tracker to disk. Called by the MCP server
// on shutdown so the next session opens warm.
func (h *Handler) SaveSessionCache() {
	session.SaveCache(h.Session, h.Root, 500)
}

// MarkCorpusStale forces a rebuild of the embedding index on next use.
func (h *Handler) MarkCorpusStale() {
	h.embMu.Lock()
	h.dirty = true
	h.embMu.Unlock()
}

func (h *Handler) ensureEmbeddings(ctx context.Context) error {
	h.embMu.Lock()
	defer h.embMu.Unlock()
	if !h.dirty && h.emb != nil {
		return nil
	}
	// Pull a representative sample of symbols from Grove for the corpus.
	syms, err := h.Grove.SearchSymbols(ctx, "", 5000)
	if err != nil {
		return fmt.Errorf("warm embeddings: %w", err)
	}
	tf := embeddings.NewTFIDF()
	tf.Index(syms)
	h.emb = tf
	h.corpus = syms
	h.dirty = false
	return nil
}

// semanticAdapter is a tiny shim so the ranker can use whatever Backend the
// handler currently has loaded.
type semanticAdapter struct{ h *Handler }

func (a semanticAdapter) Similarity(task string, sym grove.SymbolRecord) float64 {
	a.h.embMu.Lock()
	b := a.h.emb
	a.h.embMu.Unlock()
	if b == nil {
		return 0
	}
	return b.Similarity(task, sym)
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
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// ToolSchemas returns the schema list for tools/list.
func ToolSchemas() []map[string]any {
	names := []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_index", "prism_compact", "prism_savings", "prism_feedback",
		"prism_evidence",
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
	"type": "string",
	"description": "The model ID you are currently running on (e.g. \"claude-sonnet-4-6\", " +
		"\"claude-opus-4-7\", \"gpt-4o\"). Optional but recommended — Prism uses this to size " +
		"context budgets to your actual window. If omitted, Prism falls back to configured/default model.",
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
					"description": "Natural-language description of what you are trying to do.",
				},
				"terms": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Grep-style search terms to seed retrieval (e.g. [\"AccessCount\",\"sha-pointer\"]). When provided, Prism searches these terms directly instead of TF-IDF guessing — same precision as your own grep, plus graph expansion.",
				},
				"include": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": []string{"graph", "tests", "docs"}},
					"description": "Which result categories to return. \"graph\" = code + callers/callees, \"tests\" = test files, \"docs\" = markdown/docs. Default: [\"graph\",\"tests\"].",
				},
				"graph_depth": map[string]any{
					"type":        "integer",
					"description": "BFS depth for call-graph expansion via Impact(). 1 = immediate callers only, 2 = two hops (default), 3+ = wider blast radius.",
				},
				"model":   modelProp,
				"dir":     map[string]any{"type": "string", "description": "Project root directory (optional, defaults to workspace root)."},
				"limit":   map[string]any{"type": "integer", "description": "Max symbols to return (default 50)."},
				"profile": map[string]any{"type": "string", "description": "Ranking profile: default | implement_feature | fix_bug | code_review"},
				"budget":  map[string]any{"type": "integer", "description": "Token budget (default 8000). Increase for large refactors or module-wide exploration."},
			},
		}
	case "prism_read":
		return map[string]any{
			"type":     "object",
			"required": []string{"file"},
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "File path relative to the project root.",
				},
				"model": modelProp,
				"task":  map[string]any{"type": "string", "description": "Current task description, used for relevance ranking within the file."},
				"dir":   map[string]any{"type": "string", "description": "Project root directory (optional)."},
			},
		}
	case "prism_evidence":
		return map[string]any{
			"type":     "object",
			"required": []string{"claims"},
			"properties": map[string]any{
				"claims": map[string]any{
					"type":        "array",
					"description": "Array of evidence claims. Each item must have 'claim' (string) and 'file' (path). Optional: 'lineStart', 'lineEnd' (int), 'symbolName' (string for sha lookup).",
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
	default:
		return open
	}
}

func toolDescription(name string) string {
	switch name {
	case "prism_query":
		return "Call AFTER grep locates your anchor. " +
			"Pass the same terms you used in grep via terms=[...] — Prism searches those terms " +
			"directly and then expands through the call graph to return callers, callees, and " +
			"tests the agent would not find by reading alone. " +
			"Default include=[\"graph\",\"tests\"]. " +
			"Use include=[\"docs\"] for documentation search — returns ranked filenames only (~10 tokens each), not content. " +
			"graph_depth controls BFS hops: 1=immediate callers, 2=two hops (default), 3+=blast radius."
	case "prism_read":
		return "Read a whole file with session-aware compression: " +
			"full content on first read, SHA-pointer (~10 tokens) on second read, " +
			"signatures on third read if content has scrolled past attention. " +
			"Use for whole files. " +
			"For a single function body, use prism_lookup instead — ~5× cheaper."
	case "prism_search":
		return "Substring search over indexed symbol names, signatures, and docstrings. " +
			"Use when you know a symbol's name but not which file it lives in. " +
			"Does NOT search source code text — for that, use grep."
	case "prism_lookup":
		return "Retrieve the complete source of one symbol by qualified name " +
			"(e.g. 'ranking.Select' or 'mcp.Handler'). " +
			"Use this instead of prism_read when you want one function body — " +
			"costs ~5× fewer tokens than reading the whole file."
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
	case "prism_feedback":
		return "Record a 0–5 quality rating for the last prism_query result. " +
			"0 = completely wrong context, 5 = perfect. Optional notes field."
	case "prism_evidence":
		return "Convert a sub-agent's prose summary into a typed evidence packet: " +
			"an array of {claim, file, lineStart, lineEnd, sha} citations. " +
			"Pass this packet to the parent agent instead of full prose to save 75–95% tokens. " +
			"Each claim is dereferenceable via prism_lookup."
	}
	return "Prism tool: " + name
}

// --- Tool implementations -----------------------------------------------

type queryResult struct {
	Task             string           `json:"task"`
	Profile          string           `json:"profile"`
	Phase            string           `json:"phase,omitempty"`
	BudgetUsed       int              `json:"budgetUsed"`
	BudgetTotal      int              `json:"budgetTotal"`
	Symbols          []rankedSymbol   `json:"symbols"`
	TimingMs         map[string]int64 `json:"timingMs"`
	ExcludedManifest []string         `json:"excludedManifest,omitempty"`
}

type rankedSymbol struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	QualifiedName string         `json:"qualifiedName"`
	FilePath      string         `json:"filePath"`
	Kind          string         `json:"kind"`
	Score         float64        `json:"score"`
	Category      string         `json:"category"`
	Disclosure    string         `json:"disclosure"`
	TokenCost     int            `json:"tokenCost"`
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

	t0 := time.Now()
	if err := h.ensureEmbeddings(ctx); err != nil {
		return nil, err
	}
	tEmb := time.Since(t0)

	t0 = time.Now()
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
			for _, m := range matches {
				if !seenTermSeeds[m.ID] {
					seenTermSeeds[m.ID] = true
					seeds = append(seeds, m)
				}
			}
		}
		seeds = filterGeneratedPrismContext(seeds)
	} else {
		// TF-IDF fallback when no terms provided.
		var err error
		seeds, err = h.Grove.QueryByIntent(ctx, task, limit)
		if err != nil {
			return nil, fmt.Errorf("grove query: %w", err)
		}
		seeds = filterGeneratedPrismContext(seeds)
	}
	tGrove := time.Since(t0)

	// Build candidates: treat first 5 as seeds (distance 0), remainder as candidates.
	seedCount := minInt(5, len(seeds))
	seedSyms := seeds[:seedCount]
	candidateSyms := seeds[seedCount:]

	profile := ranking.SelectProfile(profileName)
	profile = h.Weights.Apply(profile)

	graphDist := make(map[string]int)
	hasTestEdgeID := make(map[string]bool)
	testFilePaths := make(map[string]bool)

	seenIDs := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seenIDs[s.ID] = true
	}
	var graphExtra []grove.SymbolRecord

	for _, seed := range seedSyms {
		if includeSet["graph"] {
			if impacted, err := h.Grove.Impact(ctx, seed.Name, graphDepth); err == nil {
				for _, imp := range impacted {
					if _, exists := graphDist[imp.ID]; !exists {
						graphDist[imp.ID] = 1
					}
					if !seenIDs[imp.ID] {
						seenIDs[imp.ID] = true
						graphExtra = append(graphExtra, imp)
					}
				}
			}
		}
		if includeSet["tests"] {
			if tests, err := h.Grove.Tests(ctx, seed.Name); err == nil {
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
			// Not reached by BFS: fall back to TF-IDF position as distance proxy
			// so semantically adjacent symbols still score above unrelated ones.
			dist = 3 + (i / 10)
		}
		sv := h.Signals.Compute(ctx, task, sym, dist, hasTestEdgeID[sym.ID], testFilePaths[sym.FilePath])
		score := ranking.Score(sv, profile)
		cat := categorize(sym)
		sessionPath := normalizePath(sym.FilePath)
		entry, seen, _ := h.Session.Lookup(sessionPath, "")
		conf := session.Low
		if seen {
			tokensSince := h.Ledger.TotalDeliveredTokens() - entry.TokenDistanceAtSend
			if tokensSince < 0 {
				tokensSince = 0
			}
			conf = session.EstimateConfidence(tokensSince, callCfg.ContextWindow())
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
	// so the ceiling here is a safety cap, not a fill target. Callers that
	// genuinely need more context can pass budget explicitly via the args.
	const defaultTaskBudget = 8000
	callerBudget := intArg(args, "budget", 0)
	budget := defaultTaskBudget
	if callerBudget > 0 {
		budget = callerBudget
	}
	if budget < 4000 {
		budget = 4000
	}
	// B: apply phase-derived budget multiplier (e.g. 0.60 for code_review).
	if phaseBudgetMult > 0 && phaseBudgetMult != 1.0 {
		shaped := int(float64(budget) * phaseBudgetMult)
		if shaped < 4000 {
			shaped = 4000
		}
		budget = shaped
	}
	picked := ranking.Select(seedSyms, candidates, budget)

	// Build response.
	used := 0
	out := queryResult{
		Task:        task,
		Profile:     profile.Name,
		Phase:       string(phase),
		BudgetTotal: budget,
		Symbols:     make([]rankedSymbol, 0, len(picked)),
		TimingMs: map[string]int64{
			"embeddings": tEmb.Milliseconds(),
			"grove":      tGrove.Milliseconds(),
		},
	}
	for _, p := range picked {
		used += p.TokenCost
		out.Symbols = append(out.Symbols, rankedSymbol{
			ID:            p.Symbol.ID,
			Name:          p.Symbol.Name,
			QualifiedName: p.Symbol.QualifiedName,
			FilePath:      p.Symbol.FilePath,
			Kind:          p.Symbol.Kind,
			Score:         p.Score,
			Category:      string(p.Category),
			Disclosure:    string(p.Disclosure),
			TokenCost:     p.TokenCost,
			Content:       ranking.Render(p.Symbol, p.Disclosure),
			Span:          p.Symbol.Span,
		})
	}
	out.BudgetUsed = used

	// Anti-context manifest: collect low-scoring candidates the ranker
	// rejected. Emit them as // [prism:excluded] sentinel lines so the agent
	// knows not to speculatively read those paths.
	out.ExcludedManifest = buildAntiContextManifest(candidates, picked)

	h.Ledger.Record("prism_query", used*3 /* approximate "what raw would cost" */, used)
	return out, nil
}

// excludeScoreThreshold is the maximum score a candidate can have and still
// appear in the anti-context manifest. Candidates below this threshold are
// considered irrelevant enough to suppress.
const excludeScoreThreshold = 0.10

// buildAntiContextManifest builds the list of [prism:excluded] sentinel lines
// from candidates that were not selected AND scored below the exclusion
// threshold. Entries are grouped by directory to keep the manifest compact.
func buildAntiContextManifest(candidates []ranking.Candidate, picked []ranking.BudgetedSymbol) []string {
	pickedIDs := make(map[string]bool, len(picked))
	for _, p := range picked {
		pickedIDs[p.Symbol.ID] = true
	}

	excluded := make(map[string]float64) // dir → lowest score seen
	for _, c := range candidates {
		if pickedIDs[c.Symbol.ID] {
			continue
		}
		if c.Score >= excludeScoreThreshold {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(c.Symbol.FilePath))
		if dir == "." {
			dir = filepath.ToSlash(c.Symbol.FilePath)
		}
		if prev, ok := excluded[dir]; !ok || c.Score < prev {
			excluded[dir] = c.Score
		}
	}

	if len(excluded) == 0 {
		return nil
	}

	// Sort for deterministic output.
	dirs := make([]string, 0, len(excluded))
	for d := range excluded {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	manifest := make([]string, 0, len(dirs))
	for _, d := range dirs {
		manifest = append(manifest,
			fmt.Sprintf("// [prism:excluded] %s/* — score %.2f, not relevant to this task", d, excluded[d]))
	}
	return manifest
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
	// Look up file's symbols via Grove (by file basename).
	syms, err := h.Grove.SearchSymbols(ctx, baseName(sessionPath), 200)
	if err != nil {
		return nil, fmt.Errorf("grove symbols: %w", err)
	}
	// Filter to symbols actually in this file path.
	fileSyms := make([]grove.SymbolRecord, 0, len(syms))
	needle := normalizePath(sessionPath)
	for _, s := range syms {
		sp := normalizePath(s.FilePath)
		if sp == needle || strings.HasSuffix(sp, "/"+needle) {
			fileSyms = append(fileSyms, s)
		}
	}
	if err := h.ensureEmbeddings(ctx); err != nil {
		// non-fatal — fall back to no semantic
	}
	readCfg := h.Cfg.WithModel(stringArg(args, "model", ""))
	confidence := session.Low
	if entry, seen, _ := h.Session.Lookup(sessionPath, ""); seen {
		tokensSince := h.Ledger.TotalDeliveredTokens() - entry.TokenDistanceAtSend
		if tokensSince < 0 {
			tokensSince = 0
		}
		confidence = session.EstimateConfidence(tokensSince, readCfg.ContextWindow())
	}
	res := compression.CompressFileRead(sessionPath, string(data), compression.Options{
		Task:            task,
		Symbols:         fileSyms,
		Session:         h.Session,
		Ledger:          h.Ledger,
		TokenLedgerName: "prism_read",
		Confidence:      confidence,
		Embeddings:      semanticAdapter{h: h},
	})
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

	// Attempt semantic re-ranking via TF-IDF if the corpus is ready.
	// Falls back to Grove substring results if embeddings are unavailable.
	if q != "" {
		if err := h.ensureEmbeddings(ctx); err == nil {
			h.embMu.Lock()
			tf, corpus := h.emb, h.corpus
			h.embMu.Unlock()
			if tf != nil && len(corpus) > 0 {
				if tfidf, ok := tf.(*embeddings.TFIDF); ok {
					hits := tfidf.Query(q, corpus, limit)
					out := make([]grove.SymbolRecord, 0, len(hits))
					for _, h := range hits {
						if !isGeneratedPrismContext(h.Symbol) {
							out = append(out, h.Symbol)
						}
					}
					if len(out) > 0 {
						return map[string]any{"symbols": out}, nil
					}
				}
			}
		}
	}

	// Fallback: Grove substring match.
	syms, err := h.Grove.SearchSymbols(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	syms = filterGeneratedPrismContext(syms)
	return map[string]any{"symbols": syms}, nil
}

func (h *Handler) toolLookup(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", stringArg(args, "qualifiedName", ""))
	if name == "" {
		return nil, errors.New("name is required")
	}
	syms, err := h.Grove.SearchSymbols(ctx, name, 25)
	if err != nil {
		return nil, err
	}
	syms = filterGeneratedPrismContext(syms)
	// Prefer exact qualified-name match.
	for _, s := range syms {
		if s.QualifiedName == name || s.Name == name {
			return map[string]any{"symbol": s, "content": s.RawText}, nil
		}
	}
	if len(syms) > 0 {
		return map[string]any{"symbol": syms[0], "content": syms[0].RawText}, nil
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
	h.MarkCorpusStale()
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
	return ranking.CategoryDependency
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
	rootAbs = filepath.Clean(rootAbs)

	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
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

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sortSymbolsByName is a stable helper used by tests.
func sortSymbolsByName(syms []grove.SymbolRecord) {
	sort.SliceStable(syms, func(i, j int) bool { return syms[i].Name < syms[j].Name })
}

var _ = sortSymbolsByName // keep helper for tests if added later

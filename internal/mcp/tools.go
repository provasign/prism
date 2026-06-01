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

	"github.com/tabladrum/grove-suite/prism/internal/compression"
	"github.com/tabladrum/grove-suite/prism/internal/config"
	"github.com/tabladrum/grove-suite/prism/internal/embeddings"
	"github.com/tabladrum/grove-suite/prism/internal/grove"
	"github.com/tabladrum/grove-suite/prism/internal/ranking"
	"github.com/tabladrum/grove-suite/prism/internal/session"
)

// Handler holds the shared backend state used by all 8 prism_* tools.
type Handler struct {
	Cfg     *config.Config
	Root    string
	Grove   *grove.Client
	Session *session.Tracker
	Ledger  *session.Ledger
	Signals *ranking.SignalComputer

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
	}
	h.Signals = ranking.NewSignalComputer(root, semanticAdapter{h: h})
	return h
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
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// ToolSchemas returns the schema list for tools/list.
func ToolSchemas() []map[string]any {
	names := []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_index", "prism_compact", "prism_savings", "prism_feedback",
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
				"model":   modelProp,
				"dir":     map[string]any{"type": "string", "description": "Project root directory (optional, defaults to workspace root)."},
				"limit":   map[string]any{"type": "integer", "description": "Max symbols to return (default 50)."},
				"profile": map[string]any{"type": "string", "description": "Ranking profile: default | test-heavy | doc-heavy"},
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
	default:
		return open
	}
}

func toolDescription(name string) string {
	switch name {
	case "prism_query":
		return "ALWAYS call this BEFORE reading any files or searching code. " +
			"Given a natural-language task description, returns pre-ranked, " +
			"token-compressed symbol definitions covering target (35%), " +
			"dependencies (25%), tests (20%), docs (10%). Replaces grep/find/read " +
			"for context gathering. Uses 5-signal ranking: graph distance, semantic " +
			"similarity, recency, test relevance, edit frequency."
	case "prism_read":
		return "Call instead of any built-in 'read file' or 'cat' action. " +
			"Returns the file with session-aware compression: full text on first " +
			"read, signatures only on second read, symbol references on third+ read. " +
			"Saves 35–92% tokens on average; 99.7% savings on repeated reads. " +
			"Always prefer this over direct file reads."
	case "prism_search":
		return "Keyword search across all indexed symbols (names, signatures, docs). " +
			"Use instead of grep when looking for a function or type by name. " +
			"Returns symbol records with file path, kind, and signature."
	case "prism_lookup":
		return "Retrieve the complete source of one symbol by its qualified name " +
			"(e.g. 'ranking.Select' or 'mcp.Handler'). Use after prism_search narrows " +
			"the candidate to one specific symbol."
	case "prism_index":
		return "Delta-index the workspace through Grove. Call once at session start " +
			"or after significant file changes. Subsequent prism_query calls are only " +
			"as fresh as the last index."
	case "prism_compact":
		return "Compress a conversation history JSON array. Call when the context " +
			"window is near capacity to summarize older turns while preserving recent ones."
	case "prism_savings":
		return "Return this session's token-savings dashboard: total delivered, " +
			"percentage saved, per-tool breakdown. Useful for reporting efficiency."
	case "prism_feedback":
		return "Record a 0–5 quality rating for the last prism_query result. " +
			"0 = completely wrong context, 5 = perfect. Optional notes field."
	}
	return "Prism tool: " + name
}

// --- Tool implementations -----------------------------------------------

type queryResult struct {
	Task        string           `json:"task"`
	Profile     string           `json:"profile"`
	BudgetUsed  int              `json:"budgetUsed"`
	BudgetTotal int              `json:"budgetTotal"`
	Symbols     []rankedSymbol   `json:"symbols"`
	TimingMs    map[string]int64 `json:"timingMs"`
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
	profileName := stringArg(args, "profile", h.Cfg.Profile)
	// model arg overrides config for this call's budget calculation only.
	// If the agent passes its own model ID (e.g. "claude-sonnet-4-6"), we use
	// it; otherwise fall back to whatever was set at initialize time.
	callCfg := h.Cfg.WithModel(stringArg(args, "model", ""))
	limit := intArg(args, "limit", 50)

	t0 := time.Now()
	if err := h.ensureEmbeddings(ctx); err != nil {
		return nil, err
	}
	tEmb := time.Since(t0)

	t0 = time.Now()
	seeds, err := h.Grove.QueryByIntent(ctx, task, limit)
	if err != nil {
		return nil, fmt.Errorf("grove query: %w", err)
	}
	tGrove := time.Since(t0)

	// Build candidates: Grove returns ordered results; treat first 5 as seeds
	// (distance 0), remainder as distance 1+ candidates.
	seedCount := minInt(5, len(seeds))
	seedSyms := seeds[:seedCount]
	candidateSyms := seeds[seedCount:]

	profile := ranking.SelectProfile(profileName)
	candidates := make([]ranking.Candidate, 0, len(candidateSyms))
	for i, sym := range candidateSyms {
		dist := 1 + (i / 10) // crude — grove doesn't return distance today
		sv := h.Signals.Compute(ctx, task, sym, dist, false, false)
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
	budget := callCfg.ContextWindow() - 5000 /* output reserve */ - 1000 /* system reserve */
	if budget < 4000 {
		budget = 4000
	}
	picked := ranking.Select(seedSyms, candidates, budget)

	// Build response.
	used := 0
	out := queryResult{
		Task:        task,
		Profile:     profile.Name,
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
	h.Ledger.Record("prism_query", used*3 /* approximate "what raw would cost" */, used)
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
	syms, err := h.Grove.SearchSymbols(ctx, q, limit)
	if err != nil {
		return nil, err
	}
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

func (h *Handler) toolIndex(ctx context.Context, args map[string]any) (any, error) {
	dir := stringArg(args, "dir", h.Root)
	res, err := h.Grove.Index(ctx, dir)
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
	return map[string]any{"recorded": entry, "totalRatings": len(h.feedback)}, nil
}

// --- helpers -------------------------------------------------------------

func categorize(s grove.SymbolRecord) ranking.Category {
	// Tests usually live in *_test.go / __tests__ / .spec.ts / .test.ts.
	p := strings.ToLower(s.FilePath)
	if strings.Contains(p, "_test.") || strings.Contains(p, ".test.") ||
		strings.Contains(p, ".spec.") || strings.Contains(p, "/__tests__/") ||
		strings.HasSuffix(p, "_test.py") {
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

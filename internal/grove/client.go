// Package grove is Prism's adapter to the in-process Grove engine.
//
// Historically this package was an HTTP client that spoke to a long-running
// `grove serve` daemon. The embedded-Grove architecture removes the daemon:
// Prism links against `grove/pkg/grove` directly and opens the on-disk index
// in the same process. The public surface of Client (NewClient, EnsureRunning,
// Index, Query…) is preserved so the rest of Prism (ranking, MCP, CLI) is
// unchanged.
package grove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// Client wraps an embedded Grove engine. baseURL/groveBin are ignored in the
// embedded model and retained only so existing call sites keep compiling.
type Client struct {
	root string

	mu  sync.Mutex
	eng *groveeng.Engine
}

// NewClient returns a Client. baseURL and groveBin are accepted for API
// compatibility but unused — Grove is now embedded in-process.
func NewClient(_, _ string) *Client {
	return &Client{}
}

// WithTokenFromDir records the repository root so EnsureRunning can open the
// engine at <root>/.grove/grove.db. The name is kept for compatibility; no
// shared-secret token is read or sent (none exists in the embedded model).
func (c *Client) WithTokenFromDir(root string) *Client {
	if abs, err := filepath.Abs(root); err == nil {
		c.root = abs
	} else {
		c.root = root
	}
	return c
}

// BaseURL returns an embedded-mode marker so legacy log lines remain readable.
func (c *Client) BaseURL() string { return "embedded://grove" }

// Health returns nil once the engine is open.
func (c *Client) Health(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng == nil {
		return errors.New("grove engine not open; call EnsureRunning first")
	}
	return nil
}

// EnsureRunning opens the embedded Grove engine if it has not been opened yet.
// Replaces the old HTTP probe + auto-spawn of `grove serve`.
func (c *Client) EnsureRunning(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng != nil {
		return nil
	}
	if c.root == "" {
		return errors.New("grove: WithTokenFromDir(root) must be called before EnsureRunning")
	}
	eng, err := groveeng.Open(ctx, groveeng.Config{RepoRoot: c.root})
	if err != nil {
		return fmt.Errorf("grove open: %w", err)
	}
	c.eng = eng
	return nil
}

// Shutdown closes the engine.
func (c *Client) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng != nil {
		_ = c.eng.Close()
		c.eng = nil
	}
}

func (c *Client) engine() *groveeng.Engine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eng
}

func (c *Client) requireEngine() (*groveeng.Engine, error) {
	if e := c.engine(); e != nil {
		return e, nil
	}
	return nil, errors.New("grove engine not open; call EnsureRunning first")
}

// FileSymbols returns the symbols currently indexed for one repo-relative
// file path. Used by file reads and working-set drift checks.
func (c *Client) FileSymbols(ctx context.Context, relPath string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	return convertSymbols(e.FileSymbols(ctx, relPath)), nil
}

// DiffFile diffs the symbols delivered earlier (before) against the file's
// current indexed symbols using Grove's GraphDiff, so renames are paired and
// breaking changes classified instead of appearing as remove+add churn.
func (c *Client) DiffFile(ctx context.Context, before []SymbolRecord, relPath string) (*FileGraphDiff, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	beforeEng := make([]groveeng.Symbol, 0, len(before))
	for _, s := range before {
		es, err := toEngineSymbol(s)
		if err != nil {
			return nil, fmt.Errorf("convert symbol %s: %w", s.ID, err)
		}
		beforeEng = append(beforeEng, es)
	}
	d := groveeng.Diff(beforeEng, e.FileSymbols(ctx, relPath))
	return &FileGraphDiff{
		Added:    convertSymbols(d.Added),
		Removed:  convertSymbols(d.Removed),
		Changed:  convertChanges(d.Changed),
		Renamed:  convertChanges(d.Renamed),
		Breaking: convertChanges(d.BreakingChanges),
	}, nil
}

func convertChanges(in []groveeng.SymbolChange) []SymbolChange {
	out := make([]SymbolChange, 0, len(in))
	for _, c := range in {
		sc := SymbolChange{SignatureChanged: c.SignatureChanged, BodyChanged: c.BodyChanged}
		if c.Before != nil {
			b := convertSymbol(*c.Before)
			sc.Before = &b
		}
		if c.After != nil {
			a := convertSymbol(*c.After)
			sc.After = &a
		}
		out = append(out, sc)
	}
	return out
}

// toEngineSymbol converts Prism's wire-format record back into the engine
// shape for GraphDiff via the shared JSON tags. The nested field types
// (SymbolKind, LineRange) live in grove/internal/core and are not
// re-exported, so a JSON round-trip is the supported conversion path; both
// structs mirror the same wire format by construction.
func toEngineSymbol(s SymbolRecord) (groveeng.Symbol, error) {
	var out groveeng.Symbol
	b, err := json.Marshal(s)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Status returns the persisted index summary.
func (c *Client) Status(ctx context.Context) (*StatusResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	st, err := e.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &StatusResult{
		FilesIndexed: st.FilesIndexed,
		SymbolCount:  st.SymbolCount,
		EdgeCount:    st.EdgeCount,
	}, nil
}

// Index indexes dir (defaults to the project root) and returns a result
// summary in the wire-format shape Prism's callers expect.
func (c *Client) Index(ctx context.Context, dir string) (*IndexResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	res, err := e.Index(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &IndexResult{
		Root:         res.Root,
		FilesSeen:    res.FilesSeen,
		FilesUpdated: res.FilesUpdated,
		FilesSkipped: res.FilesSkipped,
		FilesPruned:  res.FilesPruned,
		SymbolCount:  res.SymbolCount,
		EdgeCount:    res.EdgeCount,
	}, nil
}

// QueryByIntent resolves an intent string into ranked symbols.
func (c *Client) QueryByIntent(ctx context.Context, intent string, limit int) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.Query(ctx, intent, limit)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// SearchSymbols returns symbols matching query (substring).
func (c *Client) SearchSymbols(ctx context.Context, query string, limit int) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.Symbols(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// Deps returns dependency edges for file.
func (c *Client) Deps(ctx context.Context, file string) ([]Edge, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	edges, err := e.Deps(ctx, file)
	if err != nil {
		return nil, err
	}
	out := make([]Edge, 0, len(edges))
	for _, ed := range edges {
		out = append(out, Edge{From: ed.From, To: ed.To, Type: string(ed.Type), Confidence: ed.Confidence})
	}
	return out, nil
}

// Impact returns the blast radius for a symbol/file query.
func (c *Client) Impact(ctx context.Context, query string, maxDepth int) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.Impact(ctx, query, maxDepth)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// CallNeighbors returns a symbol's direct call neighbors — callees (out) and
// callers (in) — as wire symbols, excluding test doubles. Edge types other than
// `calls` (e.g. uses-type) are deliberately dropped: this is the precise
// call-chain neighborhood for prism_query's graph include, not the flat,
// type-erased Impact blast radius.
func (c *Client) CallNeighbors(ctx context.Context, query string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	ns, err := e.Neighbors(ctx, query, "both", groveeng.EdgeCalls)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolRecord, 0, len(ns))
	for _, n := range ns {
		if isCallNeighborTestDouble(n.Symbol.FilePath) {
			continue
		}
		out = append(out, convertSymbol(n.Symbol))
	}
	return out, nil
}

// isCallNeighborTestDouble drops mock/fake/stub/test files from call neighbors so
// the chain shows real implementations, not test doubles that share a name.
func isCallNeighborTestDouble(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, "_test.go") ||
		strings.Contains(p, "mock") || strings.Contains(p, "fake") ||
		strings.Contains(p, "stub") || strings.Contains(p, "/testdata/")
}

// References returns code occurrences of a symbol name — the reference layer
// ("where is X used"), near-complete for types/classes the call graph misses.
func (c *Client) References(ctx context.Context, name string) (groveeng.ReferenceResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return groveeng.ReferenceResult{}, err
	}
	return e.References(ctx, name)
}

// Semantic returns TF-IDF-ranked symbols with cosine-similarity scores.
func (c *Client) Semantic(ctx context.Context, query string, limit int) ([]SemanticResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	scored, err := e.Semantic(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SemanticResult, 0, len(scored))
	for _, sc := range scored {
		if sc.Symbol == nil {
			continue
		}
		out = append(out, SemanticResult{Score: sc.Score, Symbol: convertSymbol(*sc.Symbol)})
	}
	return out, nil
}

// Tests returns the tests covering query.
func (c *Client) Tests(ctx context.Context, query string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.Tests(ctx, query)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// convertSymbols maps grove engine symbols to Prism's wire-format type.
func convertSymbols(in []groveeng.Symbol) []SymbolRecord {
	out := make([]SymbolRecord, 0, len(in))
	for _, s := range in {
		out = append(out, convertSymbol(s))
	}
	return out
}

func convertSymbol(s groveeng.Symbol) SymbolRecord {
	calls := make([]CallSite, 0, len(s.CallSites))
	for _, cs := range s.CallSites {
		calls = append(calls, CallSite{Callee: cs.Callee, Line: cs.Line})
	}
	return SymbolRecord{
		ID:             s.ID,
		FilePath:       s.FilePath,
		BlobSha:        s.BlobSHA,
		Language:       s.Language,
		Kind:           string(s.Kind),
		Name:           s.Name,
		QualifiedName:  s.QualifiedName,
		Signature:      s.Signature,
		Docstring:      s.Docstring,
		Span:           SpanInfo{Start: s.Span.Start, End: s.Span.End},
		ParentSymbol:   s.ParentSymbol,
		Imports:        s.Imports,
		Exports:        s.Exports,
		RawText:        s.RawText,
		Modifiers:      s.Modifiers,
		TypeParameters: s.TypeParameters,
		Annotations:    s.Annotations,
		CallSites:      calls,
	}
}

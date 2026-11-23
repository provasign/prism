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
	"github.com/provasign/prism/internal/trust"
	"os"
)

// Client wraps an embedded Grove engine. baseURL/groveBin are ignored in the
// embedded model and retained only so existing call sites keep compiling.
type Client struct {
	root    string
	oneShot bool
	force   bool // Force: every Index re-extracts and re-runs analyzers
	// linked: other repositories linked to root (absolute paths). The
	// engine never indexes them; a compiler pass may read a dependency's
	// declarations from their sources.
	linked []string

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

// OneShot marks a client that indexes once and exits (a CLI command): the
// engine keeps no resident analyzer workers or background warm-up, and an
// index run with changes diffs against the stored baseline. Long-lived
// callers (the MCP server, watch) must not set it. Call before EnsureRunning.
func (c *Client) OneShot() *Client {
	c.oneShot = true
	return c
}

// Force makes every Index call re-extract every file and re-run every
// analyzer, ignoring the unchanged-file shortcut and analyzer retry waits.
// Call before EnsureRunning.
func (c *Client) Force() *Client {
	c.force = true
	return c
}

// RemoveIndex deletes root's index files under the index lock so the next
// index builds from scratch. Call it with no engine open on root.
func RemoveIndex(ctx context.Context, root string) ([]string, error) {
	return groveeng.RemoveIndex(ctx, root)
}

// WithLinkedRoots names the repositories linked to this one. Call before
// EnsureRunning.
func (c *Client) WithLinkedRoots(roots []string) *Client {
	c.linked = roots
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
// Replaces the old HTTP probe + auto-spawn of `grove serve`. It must stay
// cheap — no indexing: the MCP handshake gates on it, and `prism index`
// callers run their own index right after (query paths that need a populated
// graph call AutoIndexIfEmpty).
func (c *Client) EnsureRunning(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng != nil {
		return nil
	}
	if c.root == "" {
		return errors.New("grove: WithTokenFromDir(root) must be called before EnsureRunning")
	}
	// SEC-01: the user's trust decision for this root, never the repo's.
	eng, err := groveeng.Open(ctx, groveeng.Config{RepoRoot: c.root, OneShot: c.oneShot, ForceIndex: c.force, LinkedRoots: c.linked,
		Untrusted: trust.For(c.root).Untrusted})
	if err != nil {
		return fmt.Errorf("grove open: %w", err)
	}
	c.eng = eng
	return nil
}

// IndexNeedsRefresh reports whether the stored graph needs an explicit build
// or version refresh. It never indexes source files.
func (c *Client) IndexNeedsRefresh(ctx context.Context) (bool, error) {
	e, err := c.requireEngine()
	if err != nil {
		return false, err
	}
	return e.IndexNeedsRefresh(ctx)
}

// AutoIndexIfEmpty builds the index for a never-indexed repo or refreshes one
// whose extractor/resolver version is old (or whose edge write was interrupted).
// Query paths call this after EnsureRunning; index paths index explicitly.
func (c *Client) AutoIndexIfEmpty(ctx context.Context) error {
	e, err := c.requireEngine()
	if err != nil {
		return err
	}
	stale, err := e.IndexNeedsRefresh(ctx)
	if err != nil {
		return fmt.Errorf("grove index version: %w", err)
	}
	if stale {
		// Re-checked under the index lock: of several parallel first
		// commands only one builds the index; the rest wait and read it.
		res, err := e.IndexIfStale(ctx, c.root, func(st groveeng.Status) {
			if note := trust.NoticeOnce(c.root); note != "" {
				fmt.Fprintln(os.Stderr, note)
			}
			if st.SymbolCount == 0 {
				fmt.Fprintln(os.Stderr, "prism: repo not indexed yet — building the index (one-time)")
			} else {
				fmt.Fprintln(os.Stderr, "prism: index is incomplete or from an older engine — rebuilding it (one-time)")
			}
		})
		if err != nil {
			return fmt.Errorf("index refresh failed: %w", err)
		}
		// An index that came back EMPTY is the real problem, and saying so
		// here saves the caller from a downstream "no type named X in the
		// graph" — which reads like a wrong query when the truth is there is
		// nothing indexed under this root (measured: a truncated corpus
		// checkout cost 10 minutes of misdiagnosis).
		if res.SymbolCount == 0 {
			fmt.Fprintf(os.Stderr,
				"prism: WARNING index is empty — no supported source files found under %s. "+
					"Every lookup will come back empty; check the path and that the "+
					"repository actually contains code prism can parse.\n", c.root)
		}
	}
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
	syms, err := e.FileSymbols(ctx, relPath)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// DiffFileContent diffs the symbols delivered earlier (before) against the
// symbols parsed from content, the file's bytes as the caller just read them.
// Use it when the caller already knows the file changed: the index's
// size+mtime freshness check can miss a same-size edit inside one timestamp
// tick (coarse-mtime filesystems, e.g. a Linux bind mount), and DiffFile would
// then diff against the stale indexed symbols and report nothing.
func (c *Client) DiffFileContent(before []SymbolRecord, relPath string, content []byte) (*FileGraphDiff, error) {
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
	d, err := e.DiffAgainstFileContent(beforeEng, relPath, content)
	if err != nil {
		return nil, err
	}
	return &FileGraphDiff{
		Added:    convertSymbols(d.Added),
		Removed:  convertSymbols(d.Removed),
		Changed:  convertChanges(d.Changed),
		Renamed:  convertChanges(d.Renamed),
		Breaking: convertChanges(d.BreakingChanges),
	}, nil
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
	after, err := e.FileSymbols(ctx, relPath)
	if err != nil {
		return nil, err
	}
	d := groveeng.Diff(beforeEng, after)
	return &FileGraphDiff{
		Added:    convertSymbols(d.Added),
		Removed:  convertSymbols(d.Removed),
		Changed:  convertChanges(d.Changed),
		Renamed:  convertChanges(d.Renamed),
		Breaking: convertChanges(d.BreakingChanges),
	}, nil
}

// Callers returns the full symbol records of everything with a calls edge
// INTO name — the required-update set for a bare function's signature
// change. Unlike CallNeighbors, test callers are NOT filtered: a test that
// calls a changed function must be updated too.
func (c *Client) Callers(ctx context.Context, name string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	ns, err := e.Neighbors(ctx, name, "in", groveeng.EdgeCalls)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolRecord, 0, len(ns))
	for _, n := range ns {
		out = append(out, convertSymbol(n.Symbol))
	}
	return out, nil
}

// PreviewFileSymbols parses in-memory content as if it lived at relPath and
// returns the symbols Grove would index for it — the "before" side for
// diffing a git base version that is not on disk (verify, merge drivers).
func (c *Client) PreviewFileSymbols(relPath string, content []byte) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.PreviewFileSymbols(relPath, content)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
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
		Native:       append([]string(nil), st.Native...),
		Readiness:    Readiness(c.root, st.Native),
	}, nil
}

// QuickStatus reports the persisted index summary without booting the full
// engine. Engine open rehydrates every stored symbol and edge into the
// in-memory graph — work status never reads (it is three live COUNT(*)
// queries). Store-only open returns byte-identical counts in ~5ms instead of
// ~1.3s on a 500k-edge index (measured, netty). Counts only — no freshness
// claim, same as Status.
func (c *Client) QuickStatus(ctx context.Context) (*StatusResult, error) {
	st, err := groveeng.QuickStatus(ctx, c.root)
	if err != nil {
		return nil, err
	}
	return &StatusResult{
		FilesIndexed: st.FilesIndexed,
		SymbolCount:  st.SymbolCount,
		EdgeCount:    st.EdgeCount,
		Native:       append([]string(nil), st.Native...),
		Readiness:    Readiness(c.root, st.Native),
	}, nil
}

// Index indexes dir (defaults to the project root) and returns a result
// summary in the wire-format shape Prism's callers expect.
func (c *Client) Index(ctx context.Context, dir string) (*IndexResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	if note := trust.NoticeOnce(c.root); note != "" {
		fmt.Fprintln(os.Stderr, note)
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
		Errors:       append([]string(nil), res.Errors...),
		Native:       append([]string(nil), res.Native...),
		Readiness:    Readiness(res.Root, res.Native),
	}, nil
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

// SearchSymbolsScoped applies repository-relative path and glob filters inside
// Grove before ranking and limiting. Filtering a global prefix afterward is
// both incomplete and prohibitively slow for broad terms on large indexes.
func (c *Client) SearchSymbolsScoped(ctx context.Context, query string, limit int, paths, globs []string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	syms, err := e.SymbolsScoped(ctx, query, limit, paths, globs)
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

// InboundCallers returns the direct `calls` callers of query, in BOTH
// production and test files, unfiltered by isCallNeighborTestDouble — the
// caller decides what to keep. CallNeighbors deliberately drops every
// _test.go/mock/fake/stub file so the call CHAIN shows real implementations;
// this is the opposite use case, "who tests this", which needs exactly the
// callers CallNeighbors throws away. Nothing new is indexed for this: a
// test calling the code it exercises is an ordinary `calls` edge already
// captured for every symbol (confirmed live, 2026-09-02: change_impact's
// caller list already includes test callers, unfiltered, via a different
// path -- this method gives prism_query the same access CallNeighbors
// structurally denies it).
func (c *Client) InboundCallers(ctx context.Context, query string) ([]SymbolRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	ns, err := e.Neighbors(ctx, query, "in", groveeng.EdgeCalls)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolRecord, 0, len(ns))
	for _, n := range ns {
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

func dedupeResolved(in []ResolvedSymbol) []ResolvedSymbol {
	seen := map[string]bool{}
	out := in[:0]
	for _, r := range in {
		key := fmt.Sprintf("%s\x00%s:%d", r.Name, r.File, r.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// testLabel names what isCallNeighborTestDouble matched: "test double"
// for mock/fake/stub code, "test" for other test files. TestDouble covers
// both, which is right for ranking but labelled ordinary test callers
// "[test double]" (BUG-21).
func testLabel(path, name string) string {
	if !isCallNeighborTestDouble(path) {
		return ""
	}
	p, n := strings.ToLower(path), strings.ToLower(name)
	for _, marker := range []string{"mock", "fake", "stub"} {
		if strings.Contains(p, marker) || strings.Contains(n, marker) {
			return "test double"
		}
	}
	return "test"
}

// EdgeRecord is one typed graph edge from a resolved seed to a neighbor symbol.
type EdgeRecord struct {
	Name       string  `json:"name"`     // neighbor's qualified name
	File       string  `json:"file"`     // neighbor's file path
	Line       int     `json:"line"`     // neighbor's start line
	Kind       string  `json:"kind"`     // symbol kind of the neighbor
	EdgeType   string  `json:"edgeType"` // calls, uses-type, implements…
	Direction  string  `json:"direction"`
	Confidence float64 `json:"confidence"`
	TestDouble bool    `json:"testDouble,omitempty"`
	TestLabel  string  `json:"testLabel,omitempty"` // "test double" or "test"
}

// ResolvedSymbol is a candidate the agent can anchor a graph query on.
type ResolvedSymbol struct {
	Name       string `json:"name"` // qualified name
	Kind       string `json:"kind"`
	Language   string `json:"language,omitempty"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	TestDouble bool   `json:"testDouble,omitempty"`
	TestLabel  string `json:"testLabel,omitempty"` // "test double" or "test"
}

// edgeKindByName maps the public edge-kind strings (the schema the agent is told)
// to Grove's edge types.
var edgeKindByName = map[string]groveeng.EdgeType{
	"calls":      groveeng.EdgeCalls,
	"uses-type":  groveeng.EdgeUsesType,
	"implements": groveeng.EdgeImplements,
	"extends":    groveeng.EdgeExtends,
	"overrides":  groveeng.EdgeOverrides,
	"contains":   groveeng.EdgeContains,
	"defines":    groveeng.EdgeDefines,
	"imports":    groveeng.EdgeImports,
}

// Edges returns a seed symbol's direct typed graph neighbors. direction is
// "out", "in", or "both"; kinds filters by edge-kind name (empty = calls).
// This is the primitive the agent drives: "what does X call" is
// (direction=out, kinds=[calls]); "who calls X" is (in, [calls]). There is no
// "tests" kind (Grove removed the heuristic test-coverage edges): "what tests
// X" is answered by the (in, [calls]) neighbors that live in test files,
// which the TestDouble/test-path tagging on each record identifies.
func (c *Client) Edges(ctx context.Context, name, direction string, kinds []string) ([]EdgeRecord, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	if len(kinds) == 0 {
		kinds = []string{"calls"}
	}
	var types []groveeng.EdgeType
	for _, k := range kinds {
		t, ok := edgeKindByName[strings.ToLower(k)]
		if !ok {
			// An unknown kind must FAIL, not fall through: with every
			// requested kind unknown, the empty type set would make grove's
			// Neighbors return EVERY edge kind — the agent asked for a
			// filter and would silently get the whole neighborhood
			// (observed with the removed "tests" kind).
			return nil, fmt.Errorf("unknown edge kind %q (valid: calls, uses-type, implements, extends, overrides, contains, defines, imports)", k)
		}
		types = append(types, t)
	}
	ns, err := e.Neighbors(ctx, name, direction, types...)
	if err != nil {
		return nil, err
	}
	out := make([]EdgeRecord, 0, len(ns))
	for _, n := range ns {
		qn := n.Symbol.QualifiedName
		if qn == "" {
			qn = n.Symbol.Name
		}
		out = append(out, EdgeRecord{
			Name:       qn,
			File:       n.Symbol.FilePath,
			Line:       n.Symbol.Span.Start,
			Kind:       string(n.Symbol.Kind),
			EdgeType:   string(n.EdgeType),
			Direction:  n.Direction,
			Confidence: n.Confidence,
			TestDouble: isCallNeighborTestDouble(n.Symbol.FilePath),
			TestLabel:  testLabel(n.Symbol.FilePath, n.Symbol.Name),
		})
	}
	return out, nil
}

// Resolve returns the symbols a name could anchor on — exact name or qualified
// (including Type.Method) matches — each tagged with kind, location, and whether
// it's a test double, so the agent can pick the right seed before asking Edges.
func (c *Client) Resolve(ctx context.Context, name string) ([]ResolvedSymbol, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	leaf := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i+1 < len(name) {
		leaf = name[i+1:]
	}
	syms, err := e.Symbols(ctx, leaf, 50)
	if err != nil {
		return nil, err
	}
	qualified := name != leaf // input carried a Type./pkg. qualifier
	if qualified {
		// A bare-leaf search caps before the intended Type.Method; also search the
		// qualified form (searchRank matches qualified_name exactly) so the exact
		// symbol is in the pool. Prepend so it leads.
		if extra, qerr := e.Symbols(ctx, name, 25); qerr == nil {
			syms = append(extra, syms...)
		}
	}
	var exact, real, doubles []ResolvedSymbol
	for _, s := range syms {
		isExact := qualified && s.QualifiedName == name
		match := isExact || s.Name == leaf || s.QualifiedName == leaf ||
			strings.HasSuffix(s.QualifiedName, "."+leaf)
		if !match {
			continue
		}
		rs := ResolvedSymbol{Name: s.QualifiedName, Kind: string(s.Kind), Language: s.Language, File: s.FilePath, Line: s.Span.Start}
		if rs.Name == "" {
			rs.Name = s.Name
		}
		switch {
		case isExact:
			exact = append(exact, rs)
		case isCallNeighborTestDouble(s.FilePath):
			rs.TestDouble = true
			rs.TestLabel = testLabel(s.FilePath, s.Name)
			doubles = append(doubles, rs)
		default:
			real = append(real, rs)
		}
	}
	// A qualified input names one symbol — return just the exact match(es). For a
	// bare name, return all candidates: real implementations first, doubles last.
	if qualified && len(exact) > 0 {
		// The qualified and the leaf search both return the exact symbol.
		return dedupeResolved(exact), nil
	}
	return append(append(exact, real...), doubles...), nil
}

// ChangeImpact resolves a "Type.method" or "Type.method(ParamType, ...)"
// query to the exact change-set: declaration(s), override/implementation
// family in the subtype closure, super-declarations, and callers.
func (c *Client) ChangeImpact(ctx context.Context, query string) (*ChangeImpactResult, error) {
	return c.ChangeImpactScoped(ctx, query, "")
}

// ChangeImpactScoped is ChangeImpact with an optional declaring-file filter
// for same-named types in distinct packages.
func (c *Client) ChangeImpactScoped(ctx context.Context, query, file string) (*ChangeImpactResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	r, err := e.ChangeImpactScoped(ctx, query, file)
	if err != nil {
		return nil, err
	}
	return convertChangeImpact(r), nil
}

func convertChangeImpact(r groveeng.ChangeImpactResult) *ChangeImpactResult {
	return &ChangeImpactResult{
		Query:               r.Query,
		Declarations:        convertSymbols(r.Declarations),
		Supers:              convertSymbols(r.Supers),
		Family:              convertSymbols(r.Family),
		Callers:             convertSymbols(r.Callers),
		DeclaringTypes:      convertSymbols(r.DeclaringTypes),
		ExternalSupers:      r.ExternalSupers,
		OverridesExternal:   r.OverridesExternal,
		Completeness:        r.Completeness,
		HasHeuristicRefs:    r.HasHeuristicRefs,
		NameMatchedCallers:  r.NameMatchedCallers,
		CallerLines:         r.CallerLines,
		ResolvedCallerLines: r.ResolvedCallerLines,
		MemberKind:          r.MemberKind,
		Accesses:            convertMemberAccesses(r.Accesses),
		AmbiguousAccesses:   convertMemberAccesses(r.AmbiguousAccesses),
		ExcludedAccesses:    r.ExcludedAccesses,
		AccessCoverage:      r.AccessCoverage,
		AccessNote:          r.AccessNote,
		Related:             convertRelatedSites(r.Related),
		ReExports:           convertMemberAccesses(r.ReExports),
	}
}

func convertRelatedSites(in []groveeng.RelatedSite) []RelatedSite {
	if len(in) == 0 {
		return nil
	}
	out := make([]RelatedSite, 0, len(in))
	for _, s := range in {
		out = append(out, RelatedSite{Symbol: convertSymbol(s.Symbol), Relation: s.Relation, Via: s.Via, Detail: s.Detail})
	}
	return out
}

func convertMemberAccesses(in []groveeng.MemberAccess) []MemberAccess {
	if len(in) == 0 {
		return nil
	}
	out := make([]MemberAccess, 0, len(in))
	for _, a := range in {
		qn := a.Enclosing.QualifiedName
		if qn == "" {
			qn = a.Enclosing.Name
		}
		out = append(out, MemberAccess{
			FilePath: a.FilePath, Line: a.Line, Enclosing: qn, EnclosingKind: string(a.Enclosing.Kind),
			Access: a.Access, Evidence: a.Evidence, Text: a.Text,
		})
	}
	return out
}

// MissingImplementations resolves a "Type.method" query to every type in the
// subtype closure that fails to implement the member.
func (c *Client) MissingImplementations(ctx context.Context, query string) (*MissingImplementationsResult, error) {
	return c.MissingImplementationsScoped(ctx, query, "")
}

// MissingImplementationsScoped is MissingImplementations with an optional
// declaring-file fragment for same-named types in different packages.
func (c *Client) MissingImplementationsScoped(ctx context.Context, query, file string) (*MissingImplementationsResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	r, err := e.MissingImplementationsScoped(ctx, query, file)
	if err != nil {
		return nil, err
	}
	return &MissingImplementationsResult{
		Query:             r.Query,
		Contract:          convertSymbols(r.Contract),
		Missing:           convertSymbols(r.Missing),
		AbstractMissing:   convertSymbols(r.AbstractMissing),
		Unverifiable:      convertSymbols(r.Unverifiable),
		ImplementedCount:  r.ImplementedCount,
		DefaultProvided:   r.DefaultProvided,
		ExternalSupers:    r.ExternalSupers,
		OverridesExternal: r.OverridesExternal,
		Completeness:      r.Completeness,
	}, nil
}

// RenamePlan converts the change-impact set for query into concrete line
// edits renaming the member to newName.
func (c *Client) RenamePlan(ctx context.Context, query, newName string) (*RenamePlanResult, error) {
	return c.RenamePlanScoped(ctx, query, newName, "")
}

// RenamePlanScoped is RenamePlan with an optional declaring-file fragment
// for same-named types in different packages.
func (c *Client) RenamePlanScoped(ctx context.Context, query, newName, file string) (*RenamePlanResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	r, err := e.RenamePlanScoped(ctx, query, newName, file)
	if err != nil {
		return nil, err
	}
	conv := func(in []groveeng.RenameEdit) []RenameEdit {
		out := make([]RenameEdit, 0, len(in))
		for _, e := range in {
			out = append(out, RenameEdit{
				FilePath: e.FilePath, Line: e.Line,
				Before: e.Before, After: e.After, Site: e.Site, Reason: e.Reason,
			})
		}
		return out
	}
	return &RenamePlanResult{
		Query:             r.Query,
		NewName:           r.NewName,
		Edits:             conv(r.Edits),
		Ambiguous:         conv(r.Ambiguous),
		Unresolved:        r.Unresolved,
		SitesTotal:        r.SitesTotal,
		ExternalSupers:    r.ExternalSupers,
		OverridesExternal: r.OverridesExternal,
		Completeness:      r.Completeness,
	}, nil
}

// DeadCode reports production functions/methods unreachable from every entry
// point.
func (c *Client) DeadCode(ctx context.Context, extraRoots []string) (*DeadCodeResult, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, err
	}
	r, err := e.DeadCode(ctx, extraRoots)
	if err != nil {
		return nil, err
	}
	return &DeadCodeResult{
		RootCount:            r.RootCount,
		ReachableCount:       r.ReachableCount,
		Considered:           r.Considered,
		Dead:                 convertSymbols(r.Dead),
		ExportedUnreferenced: convertSymbols(r.ExportedUnreferenced),
		Caveats:              r.Caveats,
	}, nil
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

// SnapshotGraph returns every symbol and edge in the current graph. This is
// the bulk export the view layer builds component projections from; edges
// carry their evidence source so induced results can report the tier of
// their constituent evidence.
func (c *Client) SnapshotGraph(ctx context.Context) ([]SymbolRecord, []Edge, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, nil, err
	}
	syms, edges, err := e.SnapshotGraph(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Edge, 0, len(edges))
	for _, ed := range edges {
		out = append(out, Edge{
			From: ed.From, To: ed.To, Type: string(ed.Type),
			Confidence: ed.Confidence, Source: string(ed.Source), Lines: ed.Lines,
		})
	}
	return convertSymbols(syms), out, nil
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
		calls = append(calls, CallSite{Callee: cs.Callee, Line: cs.Line, Col: cs.Col, Argc: cs.Argc})
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

// ReadSymbolsNamed answers from another repository's existing index without
// opening an engine there: nothing is indexed, built, or written under root.
// It fails when root has no index.
func ReadSymbolsNamed(ctx context.Context, root string, names []string) ([]SymbolRecord, error) {
	r, err := groveeng.OpenReader(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	syms, err := r.SymbolsNamed(ctx, names)
	if err != nil {
		return nil, err
	}
	return convertSymbols(syms), nil
}

// ExternalRef is a call site in a repository whose compiler-resolved target
// another module declares.
type ExternalRef = groveeng.ExternalRef

// SymbolKey is the key other repositories record when they name s,
// declared under root: Go functions, methods and types, and JavaScript or
// TypeScript functions, methods and classes.
func SymbolKey(root string, s SymbolRecord) (string, bool) {
	return groveeng.KeyFor(root, s.Language, s.Kind, s.FilePath, s.QualifiedName)
}

// ReadExternalRefs lists, from another repository's existing index, its
// call sites whose target is one of keys. Nothing is written under root.
func ReadExternalRefs(ctx context.Context, root string, keys []string) ([]ExternalRef, error) {
	r, err := groveeng.OpenReader(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.ExternalRefs(ctx, keys)
}

// ManifestRequires returns what the repository at root declares it depends
// on, from its build manifests.
func ManifestRequires(root string) []groveeng.ManifestRequire {
	return groveeng.ScanManifests(root).Requires
}

// IndexState says which tree state another repository's index describes.
type IndexState = groveeng.IndexState

// ReadIndexState reads, without writing, which commit and branch the index
// under root was built from and when. It fails when root has no index.
func ReadIndexState(ctx context.Context, root string) (IndexState, error) {
	r, err := groveeng.OpenReader(root)
	if err != nil {
		return IndexState{}, err
	}
	defer r.Close()
	return r.State(ctx), nil
}

// RefreshIndex brings the existing index under root up to date with the
// files on disk: the ordinary incremental run, bounded by ctx.
func RefreshIndex(ctx context.Context, root string, linked ...string) error {
	c := NewClient("", "").WithTokenFromDir(root).WithLinkedRoots(linked).OneShot()
	if err := c.EnsureRunning(ctx); err != nil {
		return err
	}
	defer c.Shutdown()
	_, err := c.Index(ctx, root)
	return err
}

// KeyFor is SymbolKey for a symbol given by its fields, for one that is no
// longer in the index. An empty language is inferred from the file
// extension; an empty kind is taken to be a function.
func KeyFor(root, language, kind, filePath, qualifiedName string) (string, bool) {
	return groveeng.KeyFor(root, language, kind, filePath, qualifiedName)
}

// ManifestPackages returns what the repository at root declares, from its
// build manifests.
func ManifestPackages(root string) []groveeng.ManifestPackage {
	return groveeng.ScanManifests(root).Packages
}

// SymbolKeys returns every key other repositories may have recorded for s.
// Most languages have one; Python has one per package above the defining
// module, since a consumer records the module it imported the name from.
func SymbolKeys(root string, s SymbolRecord) []string {
	return groveeng.KeysFor(root, s.Language, s.Kind, s.FilePath, s.QualifiedName)
}

// KeysFor is SymbolKeys for a symbol given by its fields.
func KeysFor(root, language, kind, filePath, qualifiedName string) []string {
	return groveeng.KeysFor(root, language, kind, filePath, qualifiedName)
}

// ResolverVersion is the resolver version this build writes into an index.
const ResolverVersion = groveeng.ResolverVersion

// LinkedDigest identifies a set of linked repositories, for comparing with
// IndexState.Linked.
func LinkedDigest(roots []string) string { return groveeng.LinkedDigest(roots) }

// ExternalImpl is a method in another repository that implements a method
// of an interface this module declares.
type ExternalImpl = groveeng.ExternalImpl

// ReadExternalImplementers lists, from another repository's existing index,
// its methods that fulfil any of ifaces ("import/path.Interface").
func ReadExternalImplementers(ctx context.Context, root string, ifaces []string) ([]ExternalImpl, error) {
	r, err := groveeng.OpenReader(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.ExternalImplementers(ctx, ifaces)
}

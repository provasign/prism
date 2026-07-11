// Package kit exposes prism's engine as an embeddable library for
// downstream agents (e.g. provasign/mason).
//
// It is a thin facade over the same internals the CLI uses: one Kit is one
// prism session against one repo root, with the grove client, session cache,
// and persistent ledger wired exactly as `prism <op>` CLI invocations wire
// them. Nothing in this package changes existing prism behavior; it only
// re-exposes it across the module boundary.
package kit

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/session"
)

// Kit is an open prism session against one repository root.
type Kit struct {
	root       string
	client     *grove.Client
	handler    *mcp.Handler
	ledgerFile string
}

// Savings is the session token ledger as plain data, safe to consume from
// other modules (the underlying ledger type is prism-internal).
type Savings struct {
	OriginalTokens  int64
	DeliveredTokens int64
	SavedPercent    float64
}

// Open starts (or connects to) the grove engine for dir, auto-indexes a
// never-indexed repo, and attaches the persistent per-root ledger.
func Open(dir string) (*Kit, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("kit: %s is not a directory", root)
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	client := grove.NewClient(cfg.GroveURL, cfg.GroveBinary).WithTokenFromDir(root)
	if err := client.EnsureRunning(context.Background()); err != nil {
		return nil, fmt.Errorf("grove: %w", err)
	}
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		client.Shutdown()
		return nil, err
	}
	ledgerFile := ledgerPath(root)
	ledger, err := session.LoadLedger(ledgerFile)
	if err != nil {
		ledger = session.NewLedger(time.Now().Format("20060102-150405"))
	}
	return &Kit{
		root:       root,
		client:     client,
		handler:    mcp.NewHandlerWithLedger(cfg, root, client, ledger),
		ledgerFile: ledgerFile,
	}, nil
}

// Invoke runs one prism tool by its MCP name (e.g. "prism_change_impact",
// "prism_read") and returns the raw result. Same dispatch as the MCP server
// and the CLI query commands.
func (k *Kit) Invoke(tool string, args map[string]any) (any, error) {
	return k.handler.Invoke(tool, args)
}

// Root returns the absolute repository root this Kit is bound to.
func (k *Kit) Root() string { return k.root }

// Savings reports the persistent ledger totals for this root.
func (k *Kit) Savings() Savings {
	l := k.handler.Ledger
	return Savings{
		OriginalTokens:  l.TotalOriginal,
		DeliveredTokens: l.TotalDelivered,
		SavedPercent:    l.SavingsPercent(),
	}
}

// Close persists the session cache and ledger and shuts down the client.
func (k *Kit) Close() error {
	k.handler.SaveSessionCache()
	err := k.handler.Ledger.Save(k.ledgerFile)
	k.client.Shutdown()
	return err
}

// ledgerPath mirrors the CLI's per-root persistent ledger location so Kit
// sessions and CLI invocations share one ledger.
func ledgerPath(root string) string {
	sum := sha1.Sum([]byte(root))
	key := hex.EncodeToString(sum[:])
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "prism", "ledger", key+".json")
}

// FileSymbol is one indexed symbol in a file, as plain data.
type FileSymbol struct {
	Name          string
	QualifiedName string
	Kind          string // "function", "method", "class", ...
	Line          int
}

// FileSymbols returns the engine's indexed symbols for one repo-relative
// file — the hook downstream agents use to ask "what did this task create"
// and then interrogate the graph about each symbol.
func (k *Kit) FileSymbols(ctx context.Context, relPath string) ([]FileSymbol, error) {
	syms, err := k.client.FileSymbols(ctx, relPath)
	if err != nil {
		return nil, err
	}
	out := make([]FileSymbol, 0, len(syms))
	for _, s := range syms {
		out = append(out, FileSymbol{
			Name:          s.Name,
			QualifiedName: s.QualifiedName,
			Kind:          s.Kind,
			Line:          s.Span.Start,
		})
	}
	return out, nil
}

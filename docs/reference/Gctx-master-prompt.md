# Master Prompt: gctx (InfiniContext) — Token-Optimized Context Delivery (Go Edition)

## Instructions for Agent

You are building **gctx** — a token-optimization system for AI coding agents. It delivers relevant code context while using 35–92% fewer tokens per file read and 99.7% savings on re-reads through session deduplication.

This is a **Go** project. Build it module by module. The architecture is proven in production — preserve the algorithms exactly.

**Language:** Go 1.22+ (modules, generics where appropriate) **Runtime:** Single static binary (cross-platform: Linux, macOS, Windows) **Storage:** SQLite (mattn/go-sqlite3 with CGO, or modernc.org/sqlite for pure-Go) with FTS5 full-text search **Parsing:** Tree-sitter (smacker/go-tree-sitter with native bindings) **Embeddings:** ONNX Runtime (yalue/onnxruntime_go) with all-MiniLM-L6-v2 (384-dim) **Token counting:** tiktoken-go (pkoukk/tiktoken-go) with cl100k_base + o200k_base encodings **Distribution:** Single binary (CLI + MCP server) via `go install` + goreleaser **VS Code Extension:** Remains TypeScript (VS Code extensions require JS/TS runtime)

-----

## What You Are Building

A system where:

1. **Indexing** — Tree-sitter parses source files into symbols (functions, classes, types)
1. **Storage** — Symbols stored in SQLite with FTS5 search, delta-indexed by git blob SHA
1. **Graph** — Code Knowledge Graph with 8 edge types and BFS traversal
1. **Ranking** — 5-signal composite scoring selects the most relevant symbols
1. **Budget** — Token budget calculated from model context window, allocated per category
1. **Compression** — Progressive disclosure (full → signature → reference) based on relevance
1. **Session** — O(1) LRU cache deduplicates content across conversation turns
1. **Delivery** — MCP server exposes tools that AI agents call automatically

The key insight: agents waste most of their context window on irrelevant code. gctx delivers only what matters, at the right fidelity level, without re-delivering what’s already been seen.

-----

## Core Design Decisions

### Progressive Disclosure

Every symbol has a disclosure level:

- **full** — complete source code (for highly relevant symbols)
- **signature** — type signature + docstring (~15% of full tokens)
- **reference** — one-line pointer: `function qualifiedName (path:line)` (~3% of full tokens)

Decision logic:

```
if score >= RELEVANCE_THRESHOLD (0.15) → full
if previously_seen + high_confidence → reference
if previously_seen + medium_confidence → signature
otherwise → signature
```

### Budget-Aware Selection

Token budget = contextWindow - maxOutput - systemPromptReserve

Budget allocated per category:

- **target** (35%) — symbols directly relevant to the task
- **dependency** (25%) — imports, callees, type definitions
- **test** (20%) — test files covering the targets
- **doc** (10%) — documentation/comments
- **summary** (10%) — structural overviews

Greedy selection: highest-scored symbols first, degrading disclosure level as budget fills.

### Session Deduplication

O(1) LRU cache using `container/list` + `sync.Map` (or a custom concurrent LRU):

- Promote on re-access: move to front
- Evict oldest: remove from back
- Max 50K files (configurable via `GCTX_MAX_CACHE_FILES`)
- Content hashes detect unchanged file re-reads

Confidence model (token-distance based):

```
tokensSince = cumulativeTokensDelivered − tokenDistanceAtDelivery
ratio = tokensSince / contextWindowSize
if ratio < 0.3 → 'high'    (content likely still visible to model)
if ratio < 0.7 → 'medium'  (possibly pushed out of context)
else → 'low'               (almost certainly gone)
```

### Delta Indexing

- Compute git blob SHA for each file: `git hash-object <file>`
- Compare against stored SHA in `file_index` table
- Only re-parse files where SHA differs
- Cold start: full parse (~683ms for 957 symbols, ~5.4s for 5K files)
- Warm start: only changed files (~50–200ms typical)

-----

## Architecture

```
ENTRY POINTS
MCP Server (stdio/HTTP+SSE) | CLI (11 commands)
        |               |
        ▼               ▼
        CORE ENGINE
  ┌──────────┬──────────┬──────────┬──────────┐
  | Ranker   | Session  |Compressor| Assembler|
  | 5-signal | O(1) LRU | disclosure| PCX proofs|
  └────┬─────┴────┬─────┴────┬─────┴──────────┘
       |          |          |
       ▼          ▼          ▼
  | Graph   | Ledger   | Tokens  |
  | BFS/edges| accounting| tiktoken|
       |
       ▼
  | Store (SQLite + FTS5) | Embeddings (ONNX) | Git Signals |
       |
       ▼
  PARSERS (Tree-sitter native via CGO)
  TypeScript | JavaScript | Python | Go | Java | TSX
```

-----

## Data Types

```go
package core

// LineRange represents a 1-indexed inclusive source range
type LineRange struct {
    Start int `json:"start"` // 1-indexed inclusive
    End   int `json:"end"`   // 1-indexed inclusive
}

// SymbolKind enumerates recognized symbol types
type SymbolKind string

const (
    SymbolFunction    SymbolKind = "function"
    SymbolMethod      SymbolKind = "method"
    SymbolClass       SymbolKind = "class"
    SymbolInterface   SymbolKind = "interface"
    SymbolType        SymbolKind = "type"
    SymbolConst       SymbolKind = "const"
    SymbolConstant    SymbolKind = "constant"
    SymbolEnum        SymbolKind = "enum"
    SymbolModule      SymbolKind = "module"
    SymbolVariable    SymbolKind = "variable"
    SymbolConstructor SymbolKind = "constructor"
    SymbolField       SymbolKind = "field"
    SymbolDecorator   SymbolKind = "decorator"
    SymbolNamespace   SymbolKind = "namespace"
    SymbolAnnotation  SymbolKind = "annotation"
)

// SymbolRecord is the core indexed symbol representation
type SymbolRecord struct {
    ID            string     `json:"id"`            // "{filePath}::{qualifiedName}@{blobSHA}"
    FilePath      string     `json:"filePath"`
    BlobSHA       string     `json:"blobSHA"`
    Language      string     `json:"language"`
    Kind          SymbolKind `json:"kind"`
    Name          string     `json:"name"`
    QualifiedName string     `json:"qualifiedName"`
    Signature     string     `json:"signature"`
    Docstring     string     `json:"docstring,omitempty"`
    Span          LineRange  `json:"span"`
    Imports       []string   `json:"imports"`
    Exports       bool       `json:"exports"`
    RawText       string     `json:"rawText"`
    ParentSymbol  string     `json:"parentSymbol,omitempty"`
    TokenEstimate int        `json:"tokenEstimate"`
    Annotations   []string   `json:"annotations,omitempty"`
    Modifiers     []string   `json:"modifiers,omitempty"` // public/private/static/abstract/async
    TypeParameters []string  `json:"typeParameters,omitempty"` // generics
    CallSites     []string   `json:"callSites,omitempty"` // AST-extracted call site names
}

// EdgeType enumerates graph edge types
type EdgeType string

const (
    EdgeCalls      EdgeType = "calls"
    EdgeImports    EdgeType = "imports"
    EdgeDefines    EdgeType = "defines"
    EdgeTests      EdgeType = "tests"
    EdgeContains   EdgeType = "contains"
    EdgeExtends    EdgeType = "extends"
    EdgeImplements EdgeType = "implements"
    EdgeUsesType   EdgeType = "uses-type"
)

// TKGEdge represents a typed edge in the Code Knowledge Graph
type TKGEdge struct {
    From       string   `json:"from"`
    To         string   `json:"to"`
    Type       EdgeType `json:"type"`
    Confidence float64  `json:"confidence"` // 0.0–1.0
}

// DisclosureLevel controls how much of a symbol to show
type DisclosureLevel string

const (
    DisclosureFull      DisclosureLevel = "full"
    DisclosureSignature DisclosureLevel = "signature"
    DisclosureReference DisclosureLevel = "reference"
)

// BudgetCategory determines budget allocation bucket
type BudgetCategory string

const (
    CategoryTarget     BudgetCategory = "target"
    CategoryDependency BudgetCategory = "dependency"
    CategoryTest       BudgetCategory = "test"
    CategoryDoc        BudgetCategory = "doc"
    CategorySummary    BudgetCategory = "summary"
)

// BudgetedSymbol is a symbol selected for delivery with budget metadata
type BudgetedSymbol struct {
    Symbol         SymbolRecord    `json:"symbol"`
    Score          float64         `json:"score"`
    Category       BudgetCategory  `json:"category"`
    DisclosureLevel DisclosureLevel `json:"disclosureLevel"`
    TokenCost      int             `json:"tokenCost"`
}

// SignalValues holds the 5 ranking signals for a symbol
type SignalValues struct {
    GraphDistance     float64 `json:"graphDistance"`
    SemanticSimilarity float64 `json:"semanticSimilarity"`
    Recency           float64 `json:"recency"`
    TestRelevance     float64 `json:"testRelevance"`
    EditFrequency     float64 `json:"editFrequency"`
}

// RankingProfile defines signal weights for a ranking strategy
type RankingProfile struct {
    GraphDistance      float64 `json:"graph_distance"`
    SemanticSimilarity float64 `json:"semantic_similarity"`
    Recency            float64 `json:"recency"`
    TestRelevance      float64 `json:"test_relevance"`
    EditFrequency      float64 `json:"edit_frequency"`
}

// Predefined ranking profiles
var RankingProfiles = map[string]RankingProfile{
    "implement_feature": {GraphDistance: 0.3, SemanticSimilarity: 0.25, Recency: 0.15, TestRelevance: 0.15, EditFrequency: 0.15},
    "fix_bug":           {GraphDistance: 0.2, SemanticSimilarity: 0.1,  Recency: 0.25, TestRelevance: 0.25, EditFrequency: 0.2},
    "code_review":       {GraphDistance: 0.2, SemanticSimilarity: 0.2,  Recency: 0.15, TestRelevance: 0.2,  EditFrequency: 0.2},
}

// BudgetAllocation defines per-category token budget percentages
var BudgetAllocation = map[BudgetCategory]float64{
    CategoryTarget:     0.35,
    CategoryDependency: 0.25,
    CategoryTest:       0.20,
    CategoryDoc:        0.10,
    CategorySummary:    0.10,
}

const RelevanceThreshold = 0.15
```

-----

## Code Knowledge Graph

### Data Structures

```go
package graph

import "sync"

// CodeGraph holds the complete code knowledge graph with concurrent-safe lookups
type CodeGraph struct {
    mu sync.RWMutex

    symbols   []SymbolRecord
    edges     []TKGEdge
    adjacency map[string][]AdjEntry      // nodeID → neighbors
    outByType map[string]map[EdgeType][]string // nodeID → edgeType → target IDs
    inByType  map[string]map[EdgeType][]string // nodeID → edgeType → source IDs
    byFile    map[string][]SymbolRecord   // filePath → symbols
    byName    map[string][]SymbolRecord   // name → symbols
    byID      map[string]*SymbolRecord    // ID → symbol
    importsByFile map[string]map[string]bool // filePath → imported file paths
}

type AdjEntry struct {
    ID   string
    Type EdgeType
}
```

### Edge Building (6 types, built in order)

1. **DEFINES** (confidence: 1.0) — For every symbol: file → symbol edge.
1. **IMPORTS** (confidence: 0.9) — Resolve each symbol’s import list to actual files. Resolution order: relative → directory index → module aliases → bare module basename. Populates `importsByFile` map.
1. **INHERITANCE** (confidence: 0.85) — Parse “extends X” / “implements Y” from signature text. Resolve to known class/interface symbols.
1. **USES-TYPE** (confidence: 0.5) — Scan signatures for uppercase identifiers (type names). Skip primitives. Scope to same-file + imported files only.
1. **CALLS** (confidence: 0.85 AST / 0.6 regex) — If callSites populated (AST): use directly. Else: strip comments/strings, regex match “word(” patterns. Scope to same-file + imported files only.
1. **TESTS** (confidence: 0.8) — Match test file patterns: `*_test.go`, `test_*.py`, `*.test.ts`, `*.spec.ts`, `*Test.java`. Score by directory proximity.

### BFS Algorithms

```go
// GetNeighbors performs single-source BFS with optional edge type filtering
func (g *CodeGraph) GetNeighbors(nodeID string, depth int, edgeTypes []EdgeType) map[string]bool

// GetDistance returns shortest path length (-1 if unreachable)
func (g *CodeGraph) GetDistance(fromID, toID string, maxDepth int) int

// GetDistancesFromSeeds performs multi-source BFS (batch distance computation)
func (g *CodeGraph) GetDistancesFromSeeds(seedIDs []string, maxDepth int) map[string]int

// GetTestsFor returns symbols with inbound 'tests' edges
func (g *CodeGraph) GetTestsFor(symbolID string) []*SymbolRecord

// GetCallees returns symbols with outbound 'calls' edges
func (g *CodeGraph) GetCallees(symbolID string) []*SymbolRecord

// GetCallers returns symbols with inbound 'calls' edges
func (g *CodeGraph) GetCallers(symbolID string) []*SymbolRecord
```

### Key Implementation Rules

1. **Call edge scoping is critical.** Without scoping to same-file + imported files, false positives explode. This reduced false edges by ~80%.
1. **AST-based call extraction >> regex.** Use callSites when available (0.85), regex fallback (0.6).
1. **Strip comments and strings before regex matching.** Otherwise comments create false edges.
1. **Test linking uses directory proximity.** Basename matching + longest common directory prefix.
1. **Import resolution must handle aliases.** Go module paths, Python relative imports, tsconfig paths.

-----

## Ranking Engine

### Algorithm

```
rankAndSelect(seeds, allSymbols, graph, profile, modelId):
  1. Multi-source BFS from all seeds (depth 5)
  2. For each non-seed symbol, compute 5 signals:
     - graphDistance: 1 / (1 + BFS_distance). 0 if unreachable.
     - semanticSimilarity: cosine similarity of embeddings (or TF-IDF fallback)
     - recency: from git log (days since last edit, normalized)
     - testRelevance: 1.0 if symbol tests a seed, else 0
     - editFrequency: from git log (commit count, normalized)
  3. Composite score = weighted sum of signals (weights from profile)
  4. Sort candidates descending by score
  5. Budget-aware greedy selection with progressive disclosure
```

### Budget Selection

```
selectWithBudget(seeds, candidates, totalBudget):
  1. Seeds always included at full disclosure
  2. Allocate per-category budgets (target 35%, deps 25%, tests 20%, etc.)
  3. For each candidate (highest score first):
     a. Determine disclosure level:
        - score >= RELEVANCE_THRESHOLD (0.15) → full
        - else → signature
     b. Estimate token cost for chosen disclosure level
     c. If cost exceeds remaining category budget:
        - Try signature level (fewer tokens)
        - If still exceeds, skip
     d. Add to selection, deduct from category budget
  4. Return selected symbols with disclosure levels
```

-----

## Result Compression

### File Read Compression

```
compressFileRead(filePath, content, task, symbols, graph):
  1. Compute content hash (SHA-256)
  2. Check session: is this a re-read of unchanged content?
     - If re-read + high confidence → return minimal reference (~3% tokens)
     - If re-read + medium confidence → return at signature level
     - If over-compressed (read 3+ times) → escalate to full
  3. For fresh reads with indexed symbols:
     - Score each symbol's relevance to current task
     - Full disclosure for relevant symbols (score >= 0.15)
     - Signature for irrelevant symbols (structural awareness)
     - Reference for previously-seen symbols
  4. Safety valve: cap at 50K tokens per file (for huge generated files)
  5. Record delivery in session tracker
  6. Return compressed output + metadata (savings %, strategy used)
```

### Disclosure Content

```go
func GetDisclosedContent(symbol *SymbolRecord, level DisclosureLevel) string {
    switch level {
    case DisclosureFull:
        return symbol.RawText
    case DisclosureSignature:
        parts := []string{}
        if symbol.Docstring != "" {
            parts = append(parts, symbol.Docstring)
        }
        parts = append(parts, symbol.Signature)
        return strings.Join(parts, "\n")
    case DisclosureReference:
        return fmt.Sprintf("%s %s %s (%s:%d)", symbol.Kind, symbol.QualifiedName,
            symbol.FilePath, symbol.Span.Start)
    default:
        return symbol.RawText
    }
}
```

-----

## Storage (SQLite)

### Schema (V5)

```sql
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE file_index (
    file_path  TEXT PRIMARY KEY,
    blob_sha   TEXT NOT NULL,
    language   TEXT NOT NULL,
    indexed_at TEXT NOT NULL
);

CREATE TABLE symbols (
    id              TEXT PRIMARY KEY,
    file_path       TEXT NOT NULL,
    blob_sha        TEXT NOT NULL,
    language        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    name            TEXT NOT NULL,
    qualified_name  TEXT NOT NULL,
    signature       TEXT NOT NULL,
    docstring       TEXT,
    span_start      INTEGER NOT NULL,
    span_end        INTEGER NOT NULL,
    imports         TEXT NOT NULL DEFAULT '[]',
    exports         INTEGER NOT NULL DEFAULT 1,
    raw_text        TEXT NOT NULL,
    parent_symbol   TEXT,
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    annotations     TEXT,
    modifiers       TEXT,
    type_parameters TEXT
);

CREATE INDEX idx_symbols_file ON symbols(file_path);
CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_kind ON symbols(kind);

-- FTS5 for full-text search
CREATE VIRTUAL TABLE symbols_fts USING fts5(
    name, qualified_name, signature, docstring,
    content=symbols, content_rowid=rowid
);
```

### Operations

- **Upsert:** Delete stale symbols by file_path, insert new batch (use transactions)
- **Delta detection:** Compare file_index blob_sha against current git blob SHA
- **Search:** FTS5 MATCH query + fallback to LIKE for short terms
- **Pragmas:** WAL mode, synchronous=NORMAL (safe + fast)
- **Connection pool:** Use a single `*sql.DB` with appropriate `SetMaxOpenConns`

-----

## MCP Server (8 Tools)

|Tool           |Description                                                              |
|---------------|-------------------------------------------------------------------------|
|`gctx_query`   |Ranked context for a task (semantic discovery + graph + budget selection)|
|`gctx_read`    |Code-aware file read with compression (35–92% savings)                   |
|`gctx_search`  |Search symbols by keyword (FTS5 + name matching)                         |
|`gctx_lookup`  |Full source of a specific symbol by name                                 |
|`gctx_index`   |Force re-index (delta: only changed files)                               |
|`gctx_compact` |Compress conversation history (turn classification + pruning)            |
|`gctx_savings` |Token savings dashboard (session stats)                                  |
|`gctx_feedback`|Rate context quality (feeds calibration engine)                          |

### MCP Implementation

- Use JSON-RPC 2.0 over stdio (standard MCP transport)
- Implement `initialize`, `tools/list`, `tools/call` handlers
- HTTP+SSE transport as secondary option (for remote usage)
- Use `encoding/json` for marshaling (no external JSON libs needed)
- Goroutine-per-request with shared state protected by `sync.RWMutex`

### Server State (shared across session)

- Cached symbols + graph (rebuilt on 60s staleness TTL)
- Session tracker (LRU cache, content hashes, token ledger)
- Embeddings cache (symbol + file level, ONNX or TF-IDF fallback)
- Background refresh (goroutine-based non-blocking re-index on TTL expiry)
- Calibration state (EMA-tuned ranking weights from feedback)

-----

## CLI Commands (11)

|Command                 |Description                                       |
|------------------------|--------------------------------------------------|
|`gctx init`             |Initialize .gctx workspace                        |
|`gctx index [dir]`      |Delta-index codebase to SQLite                    |
|`gctx pack [target]`    |Assemble context pack (ranked + budgeted)         |
|`gctx search <query>`   |Search indexed symbols (FTS5)                     |
|`gctx serve`            |Start MCP server (stdio or –port for HTTP+SSE)    |
|`gctx verify [proof]`   |Verify PCX proof against current repo state       |
|`gctx proofs [dir]`     |List generated proofs                             |
|`gctx scan [dir]`       |Index + print stats                               |
|`gctx stale [dir]`      |Check for stale (changed) files                   |
|`gctx models`           |List supported models with context windows        |
|`gctx calibration [dir]`|Show calibration state (weights, feedback history)|

### CLI Framework

- Use `cobra` (spf13/cobra) for CLI — standard Go CLI framework
- Use `viper` for config loading (optional, can also use plain env vars)

-----

## Embeddings & Semantic Search

### Pipeline

1. Model: `all-MiniLM-L6-v2` (384-dim, ONNX, CPU-only, ~30MB)
1. Lazy load: model downloaded on first use, cached in `~/.gctx/models/`
1. Batch embed: symbols and files embedded in chunks (goroutine workers)
1. Similarity: cosine similarity between task embedding and symbol embeddings
1. Fallback: TF-IDF when ONNX runtime unavailable (still effective for keyword matching)

### Embedding Text Construction

- Symbols: `{kind} {qualifiedName}: {signature} | {docstring_first_line}`
- Files: `{path} | {exported_symbol_names} | {import_paths}`

### ONNX Integration

- Use `yalue/onnxruntime_go` for inference
- Tokenization: implement WordPiece tokenizer in Go (or use a Go port)
- Alternative: shell out to a bundled Python script for embedding (simpler, slower)
- Alternative: use `go-skynet/go-bert` or similar pure-Go transformer inference

-----

## Calibration (EMA-Based Auto-Tuning)

On feedback (positive/negative):

1. Load calibration state from `.gctx/calibration.json`
1. Identify which signal contributed most to the top-ranked results
1. Adjust weight via Exponential Moving Average:
   
   ```
   new_weight = alpha * feedback_weight + (1 - alpha) * old_weight
   alpha = 0.1 (slow adaptation to prevent oscillation)
   ```
1. Normalize weights to sum to 1.0
1. Save state

On re-read (Gap C detection):

- If file read 3+ times → signal that compression is too aggressive
- Reduce disclosure aggressiveness for that file

-----

## PCX Proofs (Reproducibility)

```go
type PCXProof struct {
    ID         string       `json:"id"`
    ManifestID string       `json:"manifestId"`
    TaskID     string       `json:"taskId"`
    CreatedAt  string       `json:"createdAt"`
    BaseCommit string       `json:"baseCommit"` // git commit SHA at time of retrieval
    Citations  []PCXCitation `json:"citations"` // what was delivered, anchored to blob SHAs
    Retrieval  struct {
        SeedSymbols         []string `json:"seedSymbols"`
        GraphDepth          int      `json:"graphDepth"`
        BudgetTokens        int      `json:"budgetTokens"`
        CandidatesConsidered int     `json:"candidatesConsidered"`
        CandidatesIncluded  int      `json:"candidatesIncluded"`
    } `json:"retrieval"`
    ContentHash string `json:"contentHash"` // SHA-256 of assembled pack
}

type PCXCitation struct {
    Path           string          `json:"path"`
    BlobSHA        string          `json:"blobSHA"`
    Ranges         []LineRange     `json:"ranges"`
    SymbolName     string          `json:"symbolName,omitempty"`
    DisclosureLevel DisclosureLevel `json:"disclosureLevel"`
    TokenCount     int             `json:"tokenCount"`
}
```

Verification: recompute blob SHAs for cited files → compare against proof. Any mismatch = stale proof.

-----

## Model Registry (40+ models)

Budget calculation: `contextWindow − maxOutput − systemPromptReserve`

Resolution order:

1. Exact match in registry
1. Longest prefix match (e.g., “claude-3.5-sonnet-20241022” → “claude-3.5-sonnet”)
1. Family heuristic (regex: `claude`, `gpt`, etc.)
1. Safe default: 128K - 16K - 2K = 109,616 tokens

Auto-detection from environment: `ANTHROPIC_MODEL`, `OPENAI_MODEL`, `COPILOT_MODEL_ID`, `VSCODE_LM_MODEL`, `LLM_MODEL`.

-----

## VS Code Extension (Remains TypeScript)

The VS Code extension stays in TypeScript — extensions require the JS/TS runtime. It communicates with the Go binary via subprocess/MCP stdio.

- **File-save watcher:** Incremental index on save (shells out to `gctx index`)
- **LM Tools:** Registered with VS Code Copilot Agent mode (`vscode.lm.registerTool`)
- **Commands:** `gctx.index`, `gctx.query`, `gctx.savings`
- **Bundled:** esbuild single-file bundle (no node_modules in VSIX)
- **Integration:** Spawns `gctx serve` as MCP subprocess, communicates via stdio JSON-RPC
- **No core logic in TS:** The extension is a thin UI/watcher layer; all indexing, ranking, and compression happens in the Go binary

-----

## Project Structure

```
gctx/
├── cmd/
│   └── gctx/
│       └── main.go              ← Entry point
├── internal/
│   └── core/
│       ├── types.go             ← Type definitions + model registry + budget calc
│       ├── store.go             ← SQLite persistent storage + FTS5
│       ├── graph.go             ← Code Knowledge Graph (adjacency, edges, BFS)
│       ├── ranker.go            ← 5-signal ranking + budget-aware selection
│       ├── session.go           ← O(1) LRU session tracker + dedup
│       ├── compressor.go        ← Result compression engine
│       ├── assembler.go         ← Pack assembler + PCX proof generator
│       ├── ledger.go            ← Token accounting
│       ├── tokens.go            ← Tiktoken-based token counting
│       ├── embeddings.go        ← ONNX semantic embeddings
│       ├── tfidf.go             ← TF-IDF fallback
│       ├── git_signals.go       ← Git-based recency/frequency signals
│       ├── calibration.go       ← EMA-based weight auto-tuning
│       ├── staleness.go         ← Delta indexing (SHA detection)
│       ├── ignore.go            ← .gitignore/.gctx-ignore support
│       ├── verifier.go          ← PCX proof verification
│       └── quality_metrics.go   ← Query outcome tracking
│   └── parsers/
│       ├── base.go              ← Tree-sitter infrastructure
│       ├── typescript.go        ← TypeScript/JavaScript parser
│       ├── python.go            ← Python parser
│       ├── golang.go            ← Go parser
│       ├── java.go              ← Java parser
│       ├── tsx.go               ← TSX parser
│       ├── javascript.go        ← JavaScript parser
│       └── registry.go          ← Parser registration + language detection
│   └── cli/
│       ├── root.go              ← Root cobra command
│       ├── index.go             ← index command
│       ├── pack.go              ← pack command
│       ├── search.go            ← search command
│       ├── serve.go             ← serve command (MCP server)
│       ├── verify.go            ← verify command
│       ├── proofs.go            ← proofs command
│       ├── scan.go              ← scan command
│       ├── stale.go             ← stale command
│       ├── models.go            ← models command
│       ├── calibration.go       ← calibration command
│       └── init.go              ← init command
│   └── mcp/
│       ├── server.go            ← MCP JSON-RPC server (stdio + HTTP+SSE)
│       ├── handlers.go          ← Tool handler implementations
│       └── transport.go         ← Transport layer (stdio, HTTP+SSE)
├── pkg/
│   └── mcp/
│       └── protocol.go          ← MCP protocol types (reusable)
├── test/
│   ├── core/                    ← Unit tests for core/
│   ├── parsers/                 ← Unit tests for parsers/
│   ├── mcp/                     ← Integration tests for MCP server
│   └── testdata/                ← Test fixtures
├── vscode/                      ← VS Code extension (TypeScript, separate build)
│   └── src/
│       ├── extension.ts         ← Extension entry point
│       ├── commands.ts          ← VS Code commands
│       ├── lm-tools.ts          ← LM tools for Copilot Agent mode
│       └── indexer.ts           ← File-save watcher (spawns gctx binary)
│   ├── package.json
│   ├── tsconfig.json
│   └── esbuild.config.js
├── go.mod
├── go.sum
├── Makefile
├── goreleaser.yml               ← Cross-platform release builds
└── README.md
```

-----

## Key Dependencies

|Package                                       |Purpose                                                                                       |
|----------------------------------------------|----------------------------------------------------------------------------------------------|
|`github.com/mattn/go-sqlite3`                 |SQLite with CGO (FTS5 enabled) OR `modernc.org/sqlite` (Pure-Go, no CGO, easier cross-compile)|
|`github.com/smacker/go-tree-sitter`           |Tree-sitter Go bindings                                                                       |
|`github.com/smacker/go-tree-sitter/typescript`|TypeScript grammar                                                                            |
|`github.com/smacker/go-tree-sitter/python`    |Python grammar                                                                                |
|`github.com/smacker/go-tree-sitter/golang`    |Go grammar                                                                                    |
|`github.com/smacker/go-tree-sitter/java`      |Java grammar                                                                                  |
|`github.com/yalue/onnxruntime_go`             |ONNX Runtime inference                                                                        |
|`github.com/pkoukk/tiktoken-go`               |Token counting (cl100k_base, o200k_base)                                                      |
|`github.com/spf13/cobra`                      |CLI framework                                                                                 |
|`github.com/hashicorp/golang-lru/v2`          |LRU cache (or implement custom)                                                               |

-----

## Go-Specific Design Choices

### Concurrency

- Use goroutines for parallel file parsing during indexing
- `sync.WaitGroup` for batch operations
- Worker pool pattern: N parser goroutines reading from a channel of file paths
- `context.Context` throughout for cancellation and timeouts
- `sync.RWMutex` on shared state (graph, session cache)

### Error Handling

- Return `error` from all fallible operations (standard Go idiom)
- Use `fmt.Errorf("operation: %w", err)` for wrapping
- Define sentinel errors in each package: `var ErrNotFound = errors.New("not found")`

### Testing

- Standard `testing` package + `testify/assert` for convenience
- Table-driven tests for parser and ranking logic
- `testdata/` directories for fixture files
- Benchmarks with `testing.B` for performance-critical paths

### Build Tags

- `//go:build cgo` for sqlite3 variant (default)
- `//go:build !cgo` for modernc/sqlite variant (pure-Go fallback)
- Allow building with or without ONNX: `//go:build onnx`

### Performance Advantages of Go

- No JIT warmup (unlike Node.js) — instant cold starts
- Lower memory footprint (no V8 heap overhead)
- Native concurrency (goroutines vs single-threaded event loop)
- Static binary — no `node_modules`, no npm install
- Tree-sitter native bindings (not WASM) — faster parsing

-----

## Performance Targets

|Metric                   |Target    |Notes                          |
|-------------------------|----------|-------------------------------|
|Index 957 symbols (cold) |< 500ms   |Go should beat Node.js (~683ms)|
|Index 5,000 files        |< 4s      |Parallel goroutine parsing     |
|Throughput               |< 1ms/file|Native tree-sitter + goroutines|
|BFS depth-5              |< 10ms    |Pure in-memory graph traversal |
|Token savings (file read)|35–92%    |Same algorithm                 |
|Token savings (re-read)  |> 95%     |Same algorithm (99.7% expected)|
|Memory                   |< 150MB   |No V8 overhead                 |
|Session cache            |50K+ files|O(1) ops                       |
|Binary size              |< 50MB    |Static binary with all parsers |
|Startup time             |< 10ms    |No module resolution           |

-----

## Constraints & Rules

1. **Never deliver more tokens than the budget.** Safety valve at 50K per file.
1. **Delta indexing always.** Never re-parse unchanged files. Git blob SHA is the staleness signal.
1. **Session state survives process boundaries.** Save/restore within TTL (10min MCP, 24h CLI).
1. **Embeddings are optional.** TF-IDF fallback when ONNX runtime unavailable.
1. **Parsers are lazy-loaded.** Tree-sitter grammars initialized on first parse, not at startup.
1. **Content hashes for dedup.** SHA-256 of file content, not path (handles renames).
1. **Call edge scoping mandatory.** Without same-file + imported-file scoping, graph is unusable.
1. **PCX proofs are verifiable.** Any proof can be checked against current repo state.
1. **Calibration is conservative.** EMA alpha=0.1 prevents oscillation from noisy feedback.
1. **Over-compression detection.** File read 3+ times → reduce compression for that file.
1. **No external services required.** Everything runs locally (SQLite, ONNX, tree-sitter).
1. **Single binary distribution.** No runtime dependencies — `go install` or download binary.

-----

## Build & Test

```bash
go build ./cmd/gctx              # Build binary
go test ./...                    # Run all tests
go test -bench=. ./internal/...  # Run benchmarks
go test -race ./...              # Race condition detection
goreleaser release --snapshot    # Cross-platform builds (linux/mac/win, amd64/arm64)
```

### Makefile Targets

```makefile
.PHONY: build test bench lint release

build:
    go build -ldflags="-s -w" -o bin/gctx ./cmd/gctx

test:
    go test -v ./...

bench:
    go test -bench=. -benchmem ./internal/...

lint:
    golangci-lint run ./...

release:
    goreleaser release --clean
```

-----

## Begin Implementation

Start with these packages in order (each depends on the previous):

1. `internal/core/types.go` — Type definitions, model registry, budget calculation
1. `internal/core/tokens.go` — Token counting with tiktoken-go
1. `internal/core/store.go` — SQLite storage + FTS5 + delta detection
1. `internal/parsers/base.go` — Tree-sitter infrastructure
1. `internal/parsers/typescript.go` — First language parser (most common)
1. `internal/core/graph.go` — Code Knowledge Graph (edges, BFS, queries)
1. `internal/core/embeddings.go` — ONNX semantic embeddings
1. `internal/core/ranker.go` — 5-signal ranking + budget selection
1. `internal/core/session.go` — O(1) LRU session tracker
1. `internal/core/compressor.go` — Result compression engine
1. `internal/core/assembler.go` — Pack assembly + PCX proofs
1. `internal/mcp/server.go` — MCP server (8 tools, stdio + HTTP+SSE)
1. `internal/cli/root.go` — CLI commands (cobra)
1. **Remaining parsers** — Python, Go, Java, TSX
1. `cmd/gctx/main.go` — Entry point wiring

For each package: implement with comprehensive unit tests and benchmarks. Validate against performance targets before proceeding. Use `go test -race` to catch concurrency bugs early.

-----

## Migration Notes (TypeScript → Go)

|TypeScript                   |Go Equivalent                                        |
|-----------------------------|-----------------------------------------------------|
|`better-sqlite3` (sync)      |`mattn/go-sqlite3` or `modernc.org/sqlite`           |
|`web-tree-sitter` (WASM)     |`smacker/go-tree-sitter` (native CGO)                |
|`@xenova/transformers` (ONNX)|`yalue/onnxruntime_go`                               |
|`js-tiktoken`                |`pkoukk/tiktoken-go`                                 |
|`commander`                  |`spf13/cobra`                                        |
|`zod`                        |Struct tags + custom validation                      |
|`@modelcontextprotocol/sdk`  |Custom JSON-RPC implementation                       |
|`Map` (LRU)                  |`hashicorp/golang-lru/v2`                            |
|`Promise.all`                |`sync.WaitGroup` + goroutines                        |
|`EventEmitter`               |Channels                                             |
|`Jest`                       |`testing` + `testify`                                |
|`interface`                  |Go interface (implicit satisfaction)                 |
|`enum` (string union)        |`type X string` + const block                        |
|`async/await`                |Goroutines + channels (or just synchronous)          |
|`npm package`                |`go install` / goreleaser binary                     |
|VS Code extension            |Keep in TypeScript (VS Code extensions require JS/TS)|
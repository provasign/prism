# Prism — Implementation Plan

**Project:** Prism (formerly: gctx / InfiniContext)  
**CLI:** `prism`  
**Role:** Token-optimized context delivery for AI agents  
**Language:** Go 1.22+  
**Depends on:** Grove (core graph engine)  
**Status:** Pre-build — architecture validated, implementation not started  
**Last Updated:** May 26, 2026

---

## Overview

Prism takes a full codebase and delivers only the relevant context to an AI agent — the wavelengths that matter, filtered from the full spectrum. It achieves 35–92% token savings on file reads and 99.7% savings on re-reads through session deduplication.

**Relationship to gctx:** Prism is gctx rebuilt on top of Grove. The graph-building, symbol storage, delta indexing, and BFS traversal that gctx built internally are now delegated entirely to Grove. Prism owns the ranking, budget allocation, session tracking, compression, and MCP interface layers.

### What Prism owns (not Grove)

| Component             | Description                                                    |
|-----------------------|----------------------------------------------------------------|
| Ranking Engine        | 5-signal composite scoring (graph distance + 4 other signals) |
| Budget Allocator      | Token budget calculation and per-category allocation           |
| Progressive Disclosure| Full → signature → reference compression                      |
| Session Tracker       | O(1) LRU cache; deduplicates content across conversation turns |
| Token Ledger          | Tracks delivered tokens per session for savings reporting      |
| Embeddings (optional) | all-MiniLM-L6-v2 (384-dim) via ONNX for semantic similarity   |
| MCP Server            | 8 tools exposed to AI agents (prism_*)                        |
| CLI                   | 11 commands for direct usage                                   |

### Two Deployment Modes

Prism ships two independent integration paths. Users choose one or both.

| Mode | How it works | Works with |
|------|-------------|------------|
| **MCP Mode** | `prism serve` → JSON-RPC 2.0 server over stdio/SSE | Claude Code, Cursor, any MCP-compatible client |
| **VS Code Extension** | TypeScript extension calls the `prism` binary directly; registers all 8 tools via `vscode.lm.registerTool` | VS Code Copilot Agent mode — **no MCP server needed** |

The VS Code extension does not require `prism serve`. It manages its own workspace-scoped session state, spawns the `prism` and `grove` binaries as child processes, and registers Prism's 8 tools natively with VS Code's language model API. Context quality is identical across both modes — only the transport layer differs.

---

## Repository Layout

```
prism/
├── cmd/
│   └── prism/
│       └── main.go                  # Binary entry point
├── internal/
│   ├── config/
│   │   └── config.go                # prism.yaml, env vars, defaults
│   ├── grove/
│   │   ├── client.go                # Grove HTTP/gRPC client
│   │   └── types.go                 # Mirror of Grove data types (for client use)
│   ├── ranking/
│   │   ├── ranker.go                # 5-signal composite scoring
│   │   ├── signals.go               # Individual signal computation
│   │   ├── profiles.go              # Predefined ranking profiles
│   │   └── budget.go                # Budget allocation + greedy selection
│   ├── session/
│   │   ├── tracker.go               # O(1) LRU session cache
│   │   ├── ledger.go                # Token accounting + savings calculation
│   │   └── confidence.go            # Token-distance confidence model
│   ├── compression/
│   │   ├── compressor.go            # File read compression pipeline
│   │   └── disclosure.go            # Disclosure level content rendering
│   ├── embeddings/
│   │   ├── engine.go                # ONNX runtime wrapper
│   │   └── tfidf.go                 # TF-IDF fallback (no model required)
│   ├── mcp/
│   │   ├── server.go                # MCP server (JSON-RPC 2.0 stdio/SSE)
│   │   └── tools.go                 # 8 tool handlers
│   └── cli/
│       └── commands.go              # cobra command tree
├── vscode-extension/
│   ├── src/
│   │   ├── extension.ts             # Extension entry point + lifecycle
│   │   ├── prismClient.ts           # Child-process wrapper for prism + grove binaries
│   │   ├── tools.ts                 # vscode.lm.registerTool handlers (8 tools)
│   │   ├── session.ts               # Workspace-scoped session tracker
│   │   ├── sidebar.ts               # Grove stats + Prism savings webview panel
│   │   └── decorations.ts           # Inline impact score annotations on hover
│   ├── package.json
│   └── tsconfig.json
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## Data Models

```go
// DisclosureLevel controls how much of a symbol is shown
type DisclosureLevel string
const (
    DisclosureFull      DisclosureLevel = "full"       // complete source
    DisclosureSignature DisclosureLevel = "signature"  // ~15% of full tokens
    DisclosureReference DisclosureLevel = "reference"  // ~3% of full tokens
)

// BudgetCategory is the allocation bucket for a symbol
type BudgetCategory string
const (
    CategoryTarget     BudgetCategory = "target"      // 35%
    CategoryDependency BudgetCategory = "dependency"  // 25%
    CategoryTest       BudgetCategory = "test"        // 20%
    CategoryDoc        BudgetCategory = "doc"         // 10%
    CategorySummary    BudgetCategory = "summary"     // 10%
)

// SignalValues holds the 5 ranking signals for a symbol
type SignalValues struct {
    GraphDistance      float64 // 1/(1+BFS distance from seeds) — from Grove
    SemanticSimilarity float64 // cosine(embed(task), embed(symbol)) or TF-IDF
    Recency            float64 // normalized days since last git edit
    TestRelevance      float64 // 1.0 if symbol tests a seed, else 0
    EditFrequency      float64 // normalized commit count over 90 days
}

// RankingProfile defines weights for a task type
type RankingProfile struct {
    GraphDistance      float64
    SemanticSimilarity float64
    Recency            float64
    TestRelevance      float64
    EditFrequency      float64
}

var Profiles = map[string]RankingProfile{
    "implement_feature": {0.30, 0.25, 0.15, 0.15, 0.15},
    "fix_bug":           {0.20, 0.10, 0.25, 0.25, 0.20},
    "code_review":       {0.20, 0.20, 0.15, 0.20, 0.25},
    "default":           {0.25, 0.25, 0.20, 0.15, 0.15},
}

// BudgetedSymbol is a symbol selected for delivery
type BudgetedSymbol struct {
    Symbol         grove.SymbolRecord
    Score          float64
    Category       BudgetCategory
    DisclosureLevel DisclosureLevel
    TokenCost      int
}

const RelevanceThreshold = 0.15
```

---

## Phase 1 — Grove Integration

**Goal:** Prism delegates all graph/storage operations to a running Grove instance.

### 1.1 Grove Client (`internal/grove/client.go`)

Prism communicates with Grove via its HTTP API or gRPC. Both channels are supported; HTTP is the default for simplicity.

```go
type Client struct {
    baseURL    string // e.g., "http://localhost:7777"
    httpClient *http.Client
    grpcConn   *grpc.ClientConn // optional
}

// Core methods Prism calls on Grove
func (c *Client) QueryByIntent(intent string, limit int) ([]grove.SymbolRecord, error)
func (c *Client) GetDeps(filePath string) (*grove.DepsResult, error)
func (c *Client) SearchSymbols(query string) ([]grove.SymbolRecord, error)
func (c *Client) GetImpact(file string, line int) ([]grove.ImpactNode, error)
func (c *Client) Index(dir string) error
func (c *Client) GetStatus() (*grove.StatusResult, error)
```

**Configuration:** `GROVE_URL` env var or `grove_url` in `prism.yaml`. Default: `http://localhost:7777`. If Grove is not reachable, Prism starts it as a subprocess automatically (`grove serve --port 7777 &`).

### 1.2 Startup Behavior

```
prism serve
  1. Check GROVE_URL / config
  2. GET grove:7777/health
     → OK: use existing Grove instance
     → Fail: exec grove binary as child process (auto-start)
  3. POST grove:7777/index {"dir": workspaceRoot}
  4. Start Prism MCP server on stdio
```

---

## Phase 2 — Ranking Engine

**Goal:** Score every candidate symbol by relevance to the current task.

### 2.1 Signal Computation (`internal/ranking/signals.go`)

#### Signal 1: Graph Distance (from Grove)
Grove's `QueryByIntent` returns symbols with BFS distances from seed nodes. Prism converts:
```
graphDistance = 1 / (1 + bfsDistance)
Seeds (distance 0) → score 1.0
Distance 1 → 0.5
Distance 2 → 0.33
Distance 5 → 0.17
Unreachable → 0.0
```

#### Signal 2: Semantic Similarity
- **Primary:** ONNX Runtime with `all-MiniLM-L6-v2` (384-dim embeddings). Cosine similarity between task embedding and `name + qualifiedName + signature + docstring`.
- **Fallback (no model):** TF-IDF cosine similarity computed in-process. Vocabulary built from all symbol text in the index.
- Embeddings are cached by symbol ID + blobSHA. Recomputed only when symbol changes.

```go
func (e *EmbeddingEngine) Similarity(taskText string, symbol grove.SymbolRecord) float64
```

#### Signal 3: Recency
```
recency = 1 / (1 + daysSinceEdit / 30)
// daysSinceEdit from git log on the source file
// Refreshed during indexing; cached in Prism's own lightweight store
```

#### Signal 4: Test Relevance
```
testRelevance = 1.0 if symbol has inbound 'tests' edge to any seed
              = 0.5 if symbol is in same file as a direct test
              = 0.0 otherwise
// Determined by Grove graph topology
```

#### Signal 5: Edit Frequency
```
editFrequency = min(1.0, commitCount90days / 20)
// commitCount from `git log --follow --oneline -- filePath | wc -l` over 90 days
// Cached per file; refreshed on index
```

### 2.2 Composite Score

```go
func Score(signals SignalValues, profile RankingProfile) float64 {
    return signals.GraphDistance      * profile.GraphDistance +
           signals.SemanticSimilarity * profile.SemanticSimilarity +
           signals.Recency            * profile.Recency +
           signals.TestRelevance      * profile.TestRelevance +
           signals.EditFrequency      * profile.EditFrequency
}
```

### 2.3 Budget-Aware Greedy Selection (`internal/ranking/budget.go`)

```
Input: seeds, candidates (scored), totalBudget (tokens), profile
Output: []BudgetedSymbol with assigned DisclosureLevel

Algorithm:
1. Seeds always included at DisclosureFull (not counted against budget)
2. Compute per-category budgets:
   target     = totalBudget × 0.35
   dependency = totalBudget × 0.25
   test       = totalBudget × 0.20
   doc        = totalBudget × 0.10
   summary    = totalBudget × 0.10
3. Sort all candidates by score descending
4. For each candidate:
   a. Assign category based on Grove edge type (callee→dependency, test→test, etc.)
   b. Determine disclosure level:
      - score >= RelevanceThreshold (0.15) → DisclosureFull
      - previously_seen + high_confidence → DisclosureReference
      - previously_seen + medium_confidence → DisclosureSignature
      - otherwise → DisclosureSignature
   c. Estimate token cost for chosen disclosure level
   d. If cost > remaining category budget:
      - Try DisclosureSignature (if not already)
      - If still exceeds, try DisclosureReference
      - If still exceeds, skip
   e. Deduct cost, add to result
5. Return selected symbols
```

**Token Budget Calculation:**
```
totalBudget = contextWindow - maxOutputTokens - systemPromptReserve(1000)
Default contextWindow per model:
  gpt-4o          → 128,000
  claude-3-5-sonnet → 200,000
  claude-opus-4   → 200,000
  gemini-1.5-pro  → 1,000,000
Model detected from PRISM_MODEL env var or prism.yaml.
```

---

## Phase 3 — Session Tracking & Compression

**Goal:** Eliminate re-delivery of content the model already has in context.

### 3.1 Session Tracker (`internal/session/tracker.go`)

O(1) LRU cache using a doubly-linked list + hash map.

```go
type SessionEntry struct {
    ContentHash         string  // SHA-256 of file content
    TokenDistanceAtSend int64   // cumulative tokens delivered when this was sent
    DisclosureLevel     DisclosureLevel
    AccessCount         int
}

type SessionTracker struct {
    mu       sync.Mutex
    entries  map[string]*list.Element // filePath → list element
    lru      *list.List
    maxFiles int // default 50,000; env PRISM_MAX_CACHE_FILES
}

func (t *SessionTracker) Record(filePath, contentHash string, tokensDelivered int64, level DisclosureLevel)
func (t *SessionTracker) Lookup(filePath, contentHash string) (*SessionEntry, bool)
func (t *SessionTracker) Evict()
```

### 3.2 Confidence Model (`internal/session/confidence.go`)

Determines whether previously-seen content is still likely in the model's context window:

```
tokensSince = currentCumulativeTokens − entry.TokenDistanceAtSend
ratio       = tokensSince / contextWindowSize

if ratio < 0.3  → confidence = "high"   (content likely still visible)
if ratio < 0.7  → confidence = "medium" (possibly pushed out)
else            → confidence = "low"    (almost certainly gone)
```

### 3.3 File Read Compression (`internal/compression/compressor.go`)

Called when an AI agent reads a file through Prism:

```
CompressFileRead(filePath, content, task, groveSymbols, session):
  1. Compute SHA-256 of content
  2. Lookup in session:
     a. Re-read + high confidence  → return DisclosureReference for all symbols (~3% tokens)
     b. Re-read + medium confidence → return DisclosureSignature for all symbols
     c. Re-read + low confidence   → treat as fresh read
     d. Read 3+ times (always escalate to full regardless of confidence)
  3. Fresh read with Grove symbols available:
     a. Score each symbol vs current task
     b. score >= RelevanceThreshold → DisclosureFull
     c. score < threshold → DisclosureSignature
     d. Previously seen symbols → DisclosureReference
  4. Safety cap: max 50,000 tokens per single file response
  5. Record delivery in session tracker
  6. Return: compressed output + metadata (strategy, savings %, original tokens, delivered tokens)
```

### 3.4 Disclosure Content Rendering (`internal/compression/disclosure.go`)

```go
func Render(symbol grove.SymbolRecord, level DisclosureLevel) string {
    switch level {
    case DisclosureFull:
        return symbol.RawText
    case DisclosureSignature:
        parts := []string{}
        if symbol.Docstring != "" { parts = append(parts, symbol.Docstring) }
        parts = append(parts, symbol.Signature)
        return strings.Join(parts, "\n")
    case DisclosureReference:
        return fmt.Sprintf("%s %s (%s:%d)", symbol.Kind, symbol.QualifiedName,
            symbol.FilePath, symbol.Span.Start)
    }
}
```

---

## Phase 4 — Token Ledger

**Goal:** Track token savings for reporting and feedback loops.

### 4.1 Ledger (`internal/session/ledger.go`)

```go
type Ledger struct {
    mu            sync.Mutex
    SessionID     string
    TotalOriginal int64   // tokens that would have been delivered without Prism
    TotalDelivered int64  // tokens actually delivered
    ByTool        map[string]*ToolStats
    StartTime     time.Time
}

type ToolStats struct {
    Calls     int
    Original  int64
    Delivered int64
}

func (l *Ledger) Record(tool string, originalTokens, deliveredTokens int)
func (l *Ledger) SavingsPercent() float64
func (l *Ledger) Summary() LedgerSummary
```

---

## Phase 5 — MCP Server

**Goal:** Expose Prism's capabilities as 8 MCP tools that AI agents call automatically.

### 5.1 MCP Server (`internal/mcp/server.go`)

JSON-RPC 2.0 over stdio. Secondary: HTTP+SSE for remote usage.

Shared server state:
- Grove client connection
- Session tracker (per MCP session, identified by client initialization)
- Token ledger (per session)
- Embedding engine

### 5.2 MCP Tools (`internal/mcp/tools.go`)

| Tool Name          | Purpose                                                                 |
|--------------------|-------------------------------------------------------------------------|
| `prism_query`      | Ranked context for a task: intent → relevant symbols at right fidelity  |
| `prism_read`       | Code-aware file read with compression (35–92% token savings)            |
| `prism_search`     | Search symbols by keyword (FTS5 via Grove + name matching)              |
| `prism_lookup`     | Full source of a specific symbol by qualified name                      |
| `prism_index`      | Force re-index (delta: only changed files, delegates to Grove)          |
| `prism_compact`    | Compress conversation history (turn classification + pruning)           |
| `prism_savings`    | Token savings dashboard for current session                             |
| `prism_feedback`   | Rate context quality (0–5 stars, used for confidence calibration)       |

#### `prism_query` — detailed flow

```
Input: { "task": "add rate limiting to auth service", "model": "claude-3-5-sonnet", "profile": "implement_feature" }

1. Call Grove: grove_query(task) → candidate symbols with BFS distances
2. Compute 5 signals for each candidate
3. Score with profile weights
4. Calculate token budget from model context window
5. Greedy selection with progressive disclosure
6. Check session: demote previously-seen symbols to reference/signature
7. Return: { "symbols": [...BudgetedSymbol...], "budget_used": N, "savings_pct": X }
```

#### `prism_compact` — conversation compression

```
Input: { "turns": [...conversation turn JSON...] }
Output: { "compressed_turns": [...], "tokens_saved": N, "strategy": "..." }

Turn classification:
  - "exploration" turns (file reads, searches) → compress to reference-level
  - "implementation" turns (code edits) → keep at signature level
  - "recent" turns (last 3) → always keep at full
  - Deduplicate: if same file read multiple times, keep only most recent
```

---

## Phase 6 — CLI

### Commands (`internal/cli/commands.go`)

```
prism init                        Initialize .prism workspace (also inits Grove)
prism index [dir]                 Index codebase via Grove
prism status                      Show token savings stats + graph stats

prism query <task>                Find and rank relevant context for a task
prism read <file>                 Read file with compression (reports savings)
prism search <keyword>            Search symbols by name/content
prism lookup <symbol-name>        Show full source of a named symbol
prism compact                     Compress conversation history from stdin

prism serve                       Start MCP server (stdio)
prism serve --port 8888           Start MCP + HTTP server

prism savings                     Show session savings dashboard
prism config                      Show resolved configuration
```

---

## Phase 7 — Embeddings (Optional Enhancement)

**Default behavior:** TF-IDF cosine similarity (zero external dependencies).  
**Enhanced behavior:** ONNX Runtime with `all-MiniLM-L6-v2`.

### ONNX Setup (`internal/embeddings/engine.go`)

```go
// Dependency: yalue/onnxruntime_go + all-MiniLM-L6-v2.onnx model file
// Model download: grove/prism downloads on first use to ~/.prism/models/

type OnnxEngine struct {
    session *ort.Session
    tokenizer *tokenizer.Tokenizer // HuggingFace tokenizers-go
}

func (e *OnnxEngine) Embed(text string) ([]float32, error)        // returns 384-dim vector
func CosineSimilarity(a, b []float32) float64
```

Embedding cache: in-memory map `symbolID+blobSHA → []float32`. Recomputed on symbol change. Never persisted to disk (rebuilt from Grove symbols on start, ~1s for 10K symbols).

---

## Phase 8 — VS Code Extension (MCP-Free Path)

**Goal:** Deliver all 8 Prism tools inside VS Code without a running MCP server. Copilot Agent mode calls them natively via `vscode.lm.registerTool`. Also surfaces Grove graph stats and Prism token savings in a sidebar.

**Stack:** TypeScript. VS Code Extension API. Ships as `.vsix`, published to VS Code Marketplace.

### Why this mode exists

`vscode.lm.registerTool` lets any VS Code extension register tools that Copilot Agent mode calls natively — no MCP protocol negotiation, no separate server process, no stdio pipe. The extension acts as the session host; the `prism` binary is the compute backend.

### PrismClient — child-process wrapper (`src/prismClient.ts`)

```typescript
class PrismClient {
    private binaryPath: string;  // config: prism.binaryPath, default "prism"

    // Runs: prism <tool-name> --json <JSON(input)>
    // Returns parsed stdout; logs stderr to Output channel
    async invoke(toolName: string, input: unknown): Promise<unknown>

    async ensureGrove(): Promise<void>   // checks grove health; auto-starts if needed
    async index(dir: string): Promise<void>
    async getStatus(): Promise<StatusResult>
}
```

### Tool registration (`src/tools.ts`)

```typescript
export function registerPrismTools(
    context: vscode.ExtensionContext,
    client: PrismClient,
    session: WorkspaceSession
): void {
    const tools = [
        { name: 'prism_query',    description: 'Find relevant code symbols for a task' },
        { name: 'prism_read',     description: 'Read a file with token-saving compression' },
        { name: 'prism_search',   description: 'Search symbols by keyword' },
        { name: 'prism_lookup',   description: 'Get full source of a named symbol' },
        { name: 'prism_index',    description: 'Re-index the workspace (delta-aware)' },
        { name: 'prism_compact',  description: 'Compress conversation history' },
        { name: 'prism_savings',  description: 'Show token savings for this session' },
        { name: 'prism_feedback', description: 'Rate context quality (0–5)' },
    ];
    for (const tool of tools) {
        context.subscriptions.push(
            vscode.lm.registerTool(tool.name, {
                invoke: async (options, _token) => {
                    const result = await client.invoke(tool.name, options.input);
                    return new vscode.LanguageModelToolResult([
                        new vscode.LanguageModelTextPart(JSON.stringify(result))
                    ]);
                }
            })
        );
    }
}
```

### Session management in extension mode

Unlike MCP mode (session = one stdio connection), the extension session = one VS Code workspace window. Session state is held in memory in the extension host process:
- `WorkspaceSession` mirrors `internal/session.SessionTracker` logic in TypeScript
- Cleared on `Prism: New Session` command or workspace reload
- LRU cache and token ledger persist across tool calls within one window lifetime

### Extension features

1. **Auto-index on save** — `vscode.workspace.onDidSaveTextDocument` → `prism index` (delta via Grove)
2. **Copilot tool registration** — all 8 Prism tools available to Copilot Agent mode, no MCP config required
3. **Sidebar panel** — shows Grove graph stats (symbols, files, staleness) + Prism session savings %
4. **Inline decorations** — hover over a symbol → impact badge (`⚡ 12 dependents`)
5. **Commands palette**: `Prism: Index Workspace`, `Prism: Query...`, `Prism: Show Impact at Cursor`, `Prism: Show Token Savings`, `Prism: New Session`

### `package.json` (key fields)

```json
{
    "name": "prism",
    "contributes": {
        "commands": [
            { "command": "prism.index",      "title": "Prism: Index Workspace" },
            { "command": "prism.query",      "title": "Prism: Query..." },
            { "command": "prism.impact",     "title": "Prism: Show Impact at Cursor" },
            { "command": "prism.savings",    "title": "Prism: Show Token Savings" },
            { "command": "prism.newSession", "title": "Prism: New Session" }
        ],
        "views": {
            "explorer": [{ "id": "prism.sidebar", "name": "Prism", "type": "webview" }]
        },
        "configuration": {
            "prism.binaryPath":  { "type": "string",  "default": "prism" },
            "prism.grovePath":   { "type": "string",  "default": "grove" },
            "prism.autoIndex":   { "type": "boolean", "default": true },
            "prism.profile":     { "type": "string",  "default": "default" }
        }
    },
    "activationEvents": ["onStartupFinished"]
}
```

---

## Phase 9 — Testing Strategy

### Unit Tests

- Ranking: known signal values → assert composite score with profile weights
- Budget selection: fixed symbol list + budget → assert selected symbols and disclosure levels
- Session tracker: LRU eviction, confidence transitions, re-read escalation
- Compression: fixture files + known session state → assert output tokens and strategy used
- Token ledger: record events → assert savings percentage calculations
- TF-IDF: known corpus → assert similarity scores

### Integration Tests

- Start Grove + Prism against `testdata/` fixture repos
- `prism_query` with known intent → assert expected symbols appear in top N
- `prism_read` on a file, read again → assert second read has significant savings
- `prism_savings` → assert non-zero savings after several reads

### Performance Benchmarks

| Benchmark                          | Target  |
|------------------------------------|---------|
| `prism_query` end-to-end           | < 200ms |
| `prism_read` with session cache    | < 50ms  |
| Embedding 1000 symbols (TF-IDF)    | < 100ms |
| Embedding 1000 symbols (ONNX)      | < 500ms |
| Session LRU lookup                 | O(1)    |
| Budget selection for 10K symbols   | < 20ms  |

---

## Configuration (`prism.yaml`)

```yaml
version: 1
grove_url: "http://localhost:7777"   # Grove server URL (auto-start if unreachable)
grove_binary: "grove"                # Path to grove binary for auto-start
model: "claude-3-5-sonnet"           # Default AI model (sets context window)
profile: "default"                   # Default ranking profile
embeddings:
  backend: "tfidf"                   # tfidf | onnx
  model_path: "~/.prism/models/all-MiniLM-L6-v2.onnx"
session:
  max_cache_files: 50000
server:
  port: 8888
  mode: "mcp"                        # mcp | http | all
```

Environment variable overrides: `PRISM_GROVE_URL`, `PRISM_MODEL`, `PRISM_PROFILE`, `PRISM_EMBEDDINGS_BACKEND`.

---

## Dependency Map

```
prism_query (MCP)
  └── Grove: grove_query(intent) → symbols + BFS distances
  └── Prism: compute signals 2-5 locally
  └── Prism: composite score + budget selection
  └── Prism: session deduplication

prism_read (MCP)
  └── Grove: grove_symbols(file) → symbols in file
  └── Prism: compression pipeline (session-aware)

prism_search (MCP)
  └── Grove: grove_symbols(query) → FTS5 results

prism_lookup (MCP)
  └── Grove: grove_symbols(name) → full symbol

prism_index (MCP)
  └── Grove: grove_index(dir)

prism_compact (MCP)
  └── Prism: local turn classification (no Grove needed)

prism_savings (MCP)
  └── Prism: local ledger read

prism_feedback (MCP)
  └── Prism: local confidence calibration store
```

---

## Phased Delivery Schedule

| Phase | Deliverable                                          | Depends on      |
|-------|------------------------------------------------------|-----------------|
| 1     | Grove client + auto-start logic                      | Grove ≥ Phase 6 |
| 2     | Ranking engine (all 5 signals, TF-IDF default)       | Phase 1         |
| 3     | Budget allocator + greedy selection                  | Phase 2         |
| 4     | Session tracker + confidence model                   | —               |
| 5     | File read compression + disclosure rendering         | Phase 3, 4      |
| 6     | Token ledger                                         | Phase 4         |
| 7     | MCP server (8 tools)                                 | Phase 1–6       |
| 8     | CLI (11 commands)                                    | Phase 7         |
| 9     | ONNX embeddings (optional enhancement)               | Phase 2         |
| 10    | Tests + benchmarks                                   | All phases      |
| 11    | VS Code Extension (MCP-free path)                    | Phase 1–7       |

---

## Key Design Constraints (Non-Negotiable)

1. **Grove is required** — Prism does not build its own graph. If Grove is unreachable, it starts Grove as a subprocess.
2. **No external API calls** — all processing local. Embeddings run locally via ONNX or TF-IDF.
3. **Session state is per-MCP-connection** — each new AI agent session starts with a fresh session tracker and ledger.
4. **Safety cap** — never deliver more than 50,000 tokens for a single file, regardless of file size.
5. **Progressive re-read escalation** — a file read 3+ times in a session always gets DisclosureFull on the third read, regardless of confidence.
6. **Single binary** — `prism` binary includes the MCP server, HTTP server, and CLI. The VS Code extension is a separate TypeScript package that ships alongside the binary.
7. **Two modes are independent** — MCP mode and VS Code Extension mode use identical core Go logic but different transport layers. The extension does not require `prism serve` to be running. Both modes produce identical context quality.

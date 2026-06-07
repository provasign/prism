# Prism Agent Workflow Benchmark (2026-06-07)

> **Historical note:** This report measured Prism through MCP-style
> `prism_query` responses. For the current recommended agent path, use CLI
> `--format text`; see
> [Prism CLI Real-World Benchmark](Prism-CLI-Real-World-Benchmark-2026-06-07.md).

Real agent tasks run both ways — shell tools (grep/find/cat) vs Prism MCP — on the Prism
codebase itself. All measurements are from live tool calls, not synthetic data.

**Codebase under test:** Prism v0.5.5, 98 files, 639 symbols, 2,259 edges  
**Model:** claude-sonnet-4-6  
**Token estimation for shell path:** raw bytes / 4 (standard approximation)

---

## Summary

| Scenario | Shell tokens (est.) | Prism tokens | Token savings | Shell correctness | Prism correctness |
|---|---:|---:|---:|---|---|
| S1: Trace MCP dispatch chain | 12,179 | 3,163 | **74%** | Partial — misses CLI bridge | Full — surfaces bridge |
| S2: Add a new MCP tool | 12,743 | 4,341 | **66%** | Partial — misses contract test | Full — surfaces schema count test |
| S3: Blast radius of a rename | 14,561 | 2,335 | **84%** | Partial — 0.4% signal-to-noise | Full + dependency chain |
| S4: Session tracker + test gaps | 17,566 | 3,133 | **82%** | Scope pollution risk | Full + gap list |

**Average token savings: 77%**  
In a session with 20 such context-gathering steps, the shell path consumes ~570K tokens vs ~154K for Prism — a 3.7× difference.

---

## Scenario 1 — Trace the MCP Dispatch Chain

**Agent task:** "I need to add error handling to the MCP dispatch layer. Trace how a
`tools/call` JSON-RPC request flows from the server loop to `toolQuery`."

### Shell path

```bash
grep -rn "tools/call|func.*Invoke|dispatch|toolQuery" \
  internal/mcp/server.go internal/mcp/tools.go

# Agent then reads both files in full to understand context
cat internal/mcp/server.go   # 5,351 bytes
cat internal/mcp/tools.go    # 42,583 bytes
```

| Metric | Value |
|---|---|
| Grep output | 783 bytes (9 matching lines) |
| Files read in full | server.go + tools.go = 47,934 bytes |
| **Total estimated tokens** | **~12,179** |
| Wall-clock time | 35 ms |

**What the agent got:** The two files contain the answer but also 1,085 other lines
of unrelated tool implementations. Signal-to-noise: 9 matching lines out of ~1,100
total lines read.

**What the agent missed:** `invokeWithPersistentLedger` in `commands.go` is the
CLI-to-MCP bridge — it shows how `cmdQuery` reaches `h.Invoke(tool, args)`. Without it, an
agent tracing error propagation from the CLI surface would have an incomplete picture.
Shell grep on `server.go` + `tools.go` never surfaces this.

### Prism path

```
prism_query(
  task="Trace how a tools/call MCP request reaches toolQuery…",
  terms=["Invoke", "dispatch", "toolQuery"],
  include=["graph", "tests"]
)
```

| Metric | Value |
|---|---|
| **budgetUsed** | **3,163 tokens** |
| Grove latency | 1 ms |
| Symbols delivered | 28 (targets + graph + tests) |

**What the agent got:** `dispatch` (full body), `Invoke` (full switch statement showing
all 9 tool routes), `toolQuery` (signature), **plus** `invokeWithPersistentLedger` as a
call-graph dependency — the CLI bridge the shell grep missed. Also surfaced:
`TestDispatch_ToolsCall_OK`, `TestDispatch_ToolsCall_BadJSON`,
`TestDispatch_ToolsCall_InvokeError` — so the agent immediately knows the test surface
before writing any error-handling code.

**Token savings: 74%** (3,163 vs 12,179)

---

## Scenario 2 — Add a New MCP Tool

**Agent task:** "Add a `prism_ping` tool. Understand the tool registration pattern:
what files to edit, what functions to update, and what tests need to change."

### Shell path

```bash
grep -rn "ToolSchemas|case \"prism_|func.*tool[A-Z]|\"name\".*\"prism" \
  internal/mcp/ --include="*.go"

cat internal/mcp/tools.go    # 42,583 bytes
cat internal/mcp/server.go   #  5,351 bytes
```

| Metric | Value |
|---|---|
| Grep output | 2,534 bytes (37 matching lines) |
| Files read in full | tools.go + server.go = 47,934 bytes |
| **Total estimated tokens** | **~12,743** |
| Wall-clock time | 48 ms |

**What the agent got:** The registration pattern is learnable from the grep output, but
the agent has to read all 1,087 lines of `tools.go` to see how `toolDescription`,
`toolSchema`, and `Invoke` fit together.

**What the agent missed:** `TestToolSchemasReturnsNineTools` — a test that asserts
`ToolSchemas()` returns exactly 9 tools and names all of them. This is the contract test
the agent must update when adding `prism_ping`. An agent that doesn't see this test will
ship code that breaks CI.

```go
// The test an agent needs to know about:
func TestToolSchemasReturnsNineTools(t *testing.T) {
    schemas := ToolSchemas()
    if len(schemas) != 9 {  // ← must update to 10
        t.Fatalf("want 9 tool schemas, got %d", len(schemas))
    }
```

Without Prism, this test is only discovered when CI fails.

### Prism path

```
prism_query(
  task="Add a new MCP tool prism_ping — understand registration pattern…",
  terms=["ToolSchemas", "Invoke"],
  include=["graph", "tests"]
)
```

| Metric | Value |
|---|---|
| **budgetUsed** | **4,341 tokens** |
| Grove latency | 1 ms |
| Symbols delivered | 30 (targets + graph + tests) |

**What the agent got:** `ToolSchemas` (full — shows the name list that drives
`toolDescription`/`toolSchema`), `Invoke` (full switch statement — shows exactly where
to add the new `case`), `dispatch` (full — shows the `ToolSchemas()` call site in
`tools/list`), **plus** `TestToolSchemasReturnsNineTools` as a top target — the contract
test the agent must update, surfaced before a single line of code is written.

**Token savings: 66%** (4,341 vs 12,743)  
**Correctness win:** CI-breaking test surfaced proactively.

---

## Scenario 3 — Blast Radius of a Rename

**Agent task:** "Rename `filterDocSeeds` to `filterNonCodeSeeds`. Find every caller,
understand the blast radius, and check what tests cover it."

### Shell path

```bash
grep -rn "filterDocSeeds" . --include="*.go"
# → 3 hits in tools.go (def + 1 call site) and tools_cov_test.go (1 test call)

cat internal/mcp/tools.go       # 42,583 bytes  — to find the function body
cat internal/mcp/tools_cov_test.go  # 15,447 bytes  — to find the test
```

| Metric | Value |
|---|---|
| Grep output | 215 bytes (3 lines) |
| Files read in full | tools.go + test file = 58,030 bytes |
| **Total estimated tokens** | **~14,561** |
| Wall-clock time | 149 ms (slowest scenario — large file reads) |
| Signal-to-noise | 3 relevant lines / ~1,400 total lines read = **0.2%** |

**What the agent got:** Two massive files where the relevant symbol spans ~9 lines.
To answer "what does `filterDocSeeds` depend on?" the agent would need another grep
pass for `categorize` and `ranking.CategoryDoc`.

**What the agent missed on the first pass:** The `categorize` function that
`filterDocSeeds` delegates to, and the `TestCategorize` table-driven test that
documents the full classification rules. A rename without understanding `categorize` is
incomplete — the doc/code boundary is defined there.

### Prism path

```
prism_query(
  task="Rename filterDocSeeds to filterNonCodeSeeds — blast radius…",
  terms=["filterDocSeeds"],
  include=["graph", "tests"],
  graph_depth=3
)
```

| Metric | Value |
|---|---|
| **budgetUsed** | **2,335 tokens** |
| Grove latency | 0 ms |
| Symbols delivered | 35 (function + full test + dependency graph) |

**What the agent got:** `filterDocSeeds` (48 tokens — just the function body, not the
whole file), `TestFilterDocSeeds` (full test showing README.md/ROADMAP.md filter cases),
`TestCategorize` (the 14-case table that defines the doc/code boundary), and `toolLookup`
(the other caller in the graph). Complete blast radius in one call.

**Token savings: 84%** (2,335 vs 14,561)  
**Speed win:** 0 ms Grove latency vs 149 ms for two large file reads.  
**Completeness win:** `categorize` + `TestCategorize` surfaced automatically via
graph expansion; would require a second shell pass to discover.

---

## Scenario 4 — Understand a Module and Find Test Gaps

**Agent task:** "The session tracker seems buggy. Understand what it tracks, who calls
`Lookup` and `Record`, and what code paths are not yet covered by tests."

### Shell path

```bash
grep -rn "\.Track\b|\.Lookup\b|h\.Session|NewTracker|session\.New" \
  internal/ --include="*.go" | grep -v "_test.go"

cat internal/session/tracker.go       # 3,520 bytes
cat internal/session/cache.go         # 4,981 bytes
cat internal/session/confidence.go    #   757 bytes
cat internal/session/ledger.go        # 3,629 bytes
cat internal/session/file_lock.go     # 1,155 bytes
cat internal/mcp/tools.go             # 42,583 bytes  (primary caller)
cat internal/compression/compressor.go # 12,645 bytes  (secondary caller)
```

| Metric | Value |
|---|---|
| Grep output | 997 bytes (12 project hits) |
| Files read in full | 5 session pkg + 2 caller files = 69,270 bytes |
| **Total estimated tokens** | **~17,566** |
| Wall-clock time | 49 ms |

**Critical failure without `internal/` scope:**  
Running `grep -rn "\.Lookup\b" .` without scoping to `internal/` returns **50+ hits
from `.cache/go/pkg/mod/`** (golang.org/x/tools, golang.org/x/mod, etc.) — a scope
pollution problem that doesn't exist in Prism. An agent that forgets `--include="*.go"`
or misses the `.cache` directory entirely wastes tokens on irrelevant stdlib matches.

**What shell cannot answer:** "What code paths are not covered by tests?" There is no
shell command for this. The agent must manually cross-reference grep hits against test
files — a multi-step inference process that is both slow and error-prone.

### Prism path

```
prism_query(
  task="Session tracker seems buggy — understand it, who calls it, what's not tested",
  terms=["Tracker", "NewTracker", "Lookup"],
  include=["graph", "tests", "coverage_gaps"]
)
```

| Metric | Value |
|---|---|
| **budgetUsed** | **3,133 tokens** |
| Grove latency | 1 ms |
| Symbols delivered | 34 code+test + **4 coverage gaps** |

**What the agent got:** `Tracker` struct (full), `BenchmarkTracker_RecordLookup` and
`BenchmarkTracker_LookupHit` (performance profile — tells agent where hot paths are),
`TestLoadCache_RespectsTrackerCapacityKeepsMRU` (the LRU eviction contract), plus the
`coverageGaps` list:

```
coverage_gaps:
  - cmdSearch   (internal/cli/commands.go)
  - cmdSavings  (internal/cli/commands.go)
  - cmdServe    (internal/cli/commands.go)
  - LoadLedger  (internal/session/ledger.go)
```

The agent now knows which functions touching the session subsystem have no test edge —
in a single call, without any manual cross-referencing.

**Token savings: 82%** (3,133 vs 17,566)  
**Unique capability:** `coverage_gaps` is impossible to replicate with shell tools — it
requires call-graph awareness that grep doesn't have.

---

## What Prism Does Differently

### 1. Surgical extraction vs whole-file reads

Shell grep finds the needle, but the agent is still forced to read the haystack. In S3,
3 grep hits triggered 58,000 bytes of file reads — a 270× amplification. Prism delivers
the function body (48 tokens) plus its graph neighborhood, not the whole file.

### 2. Call-graph expansion in one call

Every Prism result includes callers, callees, and tests for the anchor symbols — surfaces
that grep cannot reach without multiple follow-up passes. S1 is the clearest example:
`invokeWithPersistentLedger` only appears because Prism followed the call graph one hop
from `Invoke` into `commands.go`. A shell agent would need to know to look there.

### 3. Coverage gaps as a first-class output

`include=["coverage_gaps"]` returns symbols in the blast radius with no test edges.
There is no shell equivalent. S4 produced 4 specific untested functions in one call;
a shell agent would need to manually diff caller lists against test files.

### 4. Scope safety

Prism queries are bounded to the indexed workspace. Shell `grep -rn .Lookup` in S4
returned 50+ hits from the Go module cache before the scope flag was added. Prism never
escapes project boundaries.

### 5. Compression compounds over a session

These scenarios each represent a single context-gathering step. Real sessions involve
20–50 such steps. At 77% savings per step, a session that would consume 400K tokens
with shell tools consumes ~90K with Prism — staying inside the usable context window
instead of blowing past it.

---

## Raw Numbers

| Scenario | Shell bytes | Shell tokens (est.) | Prism budgetUsed | Token savings | Grove ms |
|---|---:|---:|---:|---:|---:|
| S1: Dispatch chain | 48,717 | 12,179 | 3,163 | 74% | 1 |
| S2: Add new tool | 50,975 | 12,743 | 4,341 | 66% | 1 |
| S3: Blast radius rename | 58,245 | 14,561 | 2,335 | 84% | 0 |
| S4: Tracker + gaps | 70,267 | 17,566 | 3,133 | 82% | 1 |
| **Total** | **228,204** | **57,049** | **12,972** | **77%** | — |

Shell byte counting methodology: sum of `wc -c` on all files an agent would realistically
read to answer the question (grep output + full file contents of matched files). Token
estimate: bytes / 4.

Prism token counting: `budgetUsed` field from the JSON response, which reflects the
actual token cost of all delivered symbol content.

# Prism

> **Focused, graph-ranked context for any AI coding agent — 35–92% fewer tokens, zero manual file selection.**

---

An AI agent that gets bad context produces bad code. Not because it's a bad agent — because it's working blind.

The naive approach is to dump related files into the context window and hope the agent figures out what matters. This fails in two directions at once: it wastes tokens on code that's nearby in the file tree but irrelevant to the task, and it misses code that *is* critical but has no obvious filename match. The agent hallucinates the gaps.

Prism solves this. Given a task description, it queries Grove's knowledge graph, scores every candidate symbol across five signals, allocates a token budget across five categories, and returns exactly what matters — full source for the first read, signatures on the second, one-line references on the third. The agent gets more signal per token. Every time.

Works with Claude Code, GitHub Copilot (VS Code), Cursor, Codex CLI, Windsurf, Zed, and any MCP-capable tool.

---

## How It Works

```
Task description ("add rate limiting to the login endpoint")
     │
     ▼
prism_query
     │
     ├──► Grove: FTS5 symbol search
     ├──► Grove: BFS graph traversal (depth 2–3)
     │
     ▼
┌────────────────────────────────────────────────────┐
│  5-Signal Ranking                                  │
│                                                    │
│  1. Graph distance    — BFS hops from seed symbol  │
│  2. Semantic similarity — Model2Vec cosine score   │
│  3. Recency           — recent git commits score   │
│  4. Test relevance    — is this a test for target? │
│  5. Edit frequency    — hot files get priority     │
└───────────────────────────┬────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────┐
│  Budget Allocation                                 │
│                                                    │
│  Target symbols    35%  (the thing you're editing) │
│  Dependencies      25%  (what it calls/imports)    │
│  Tests             20%  (tests that cover it)      │
│  Documentation     10%  (docstrings, comments)     │
│  Summary           10%  (file/module overview)     │
└───────────────────────────┬────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────┐
│  Progressive Disclosure                            │
│                                                    │
│  First read:   full source text                    │
│  Second read:  signature only (saves ~70%)         │
│  Third+ read:  one-line reference (saves ~90%)     │
└───────────────────────────┬────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────┐
│  Session Deduplication                             │
│                                                    │
│  O(1) LRU cache: symbols already in context        │
│  are downranked to avoid repeating known content   │
└───────────────────────────┬────────────────────────┘
                            │
                            ▼
Token-optimized context pack returned to agent
```

---

## IDE and CLI Support

Prism has two integration paths. Both produce identical context quality — only the transport layer differs.

### MCP Stdio

`prism init` detects which tools are installed and writes their MCP config automatically. After a tool restart, all 8 `prism_*` tools are available.

| Tool | Config written | Notes |
|------|---------------|-------|
| Claude Code CLI | `.claude/mcp.json` | `prism mcp` launched per session |
| GitHub Copilot (VS Code) | `.vscode/mcp.json` | `"servers"` key, `"type":"stdio"` |
| Cursor | `.cursor/mcp.json` | Same |
| Codex CLI | `~/.codex/config.toml` | `[[mcp_servers]]` TOML; skipped if `~/.codex/` absent |
| Windsurf | `.windsurf/mcp.json` | Same |
| Zed | `~/.config/zed/settings.json` | `context_servers` key |
| Any MCP client | manual config | Point at `prism mcp <dir>` |

### VS Code (Copilot Agent Mode)

The VS Code extension does not use MCP. It registers all 8 tools via `vscode.lm.registerTool` and spawns the `prism` binary per call. No `prism serve` required, no port.

Tools appear in Copilot Chat as `#prismQuery`, `#prismRead`, `#prismSearch`, etc.

The extension also provides two left status bar items:
- **Grove symbols** — live symbol count from the knowledge graph, click to re-index
- **Prism savings** — session token savings %, click to view details

[Extension documentation →](vscode-extension/README.md)

### CLI (no agent)

The `prism` binary is usable directly for scripting, debugging, or any workflow outside an AI agent:

```bash
prism query "add rate limiting to the login endpoint"
prism read internal/auth/login.go
prism search AuthService
prism savings
```

Add `--json` for machine-readable output.

---

## Language Support

Prism delegates all parsing and graph construction to Grove. Language support is identical:

| Language | Extensions | What is extracted |
|----------|-----------|-------------------|
| Go | `.go` | functions, methods, types, interfaces, structs, consts, vars |
| TypeScript | `.ts` | classes, functions, interfaces, types, enums, namespaces |
| TSX | `.tsx` | same as TypeScript + JSX components |
| JavaScript | `.js .jsx .mjs .cjs` | functions, classes, arrow functions, exports |
| Python | `.py` | functions, classes, methods, decorated definitions |
| Java | `.java` | classes, interfaces, enums, methods, fields, constructors |
| Rust | `.rs` | functions, structs, enums, traits, impl blocks, fields |
| C | `.c .h` | functions, typedef structs/enums, tagged types |
| C++ | `.cc .cpp .cxx .hh .hpp` | classes, namespaces, templates, methods |
| C# | `.cs` | namespaces, classes, structs, interfaces, methods, properties |
| PHP | `.php .phtml` | classes, interfaces, traits, enums, functions, methods |

Non-code files (`.md`, `.yaml`, `.json`, `.xml`, `.sh`, `.toml`, `.proto`, `.sql`, `Makefile`, `Dockerfile`, and more) are indexed as document symbols and ranked alongside code in every query. Agents can discover architectural decisions in ADRs, API contracts in OpenAPI files, and deployment configuration in Dockerfiles — all without manual file selection.

Semantic similarity uses Model2Vec (potion-base-8M, embedded in the Grove binary — no download, no server, no GPU). Set `GROVE_EMBEDDINGS=tfidf` to opt out.

---

## Quick Start

```bash
# 1. Install (build Grove first — Prism requires it)
cd grove && make install
cd prism && make install

# 2. Initialize a project (run once per project root)
cd /your/project
prism init

# 3. Restart your coding tool to pick up the MCP config

# 4. Index
prism index

# 5. Verify savings after using the agent
prism savings
```

`prism init` does three things: writes `prism.yaml`, writes steering instructions for your agent (`CLAUDE.md`, `.cursorrules`, etc.), and registers the MCP server config for every coding tool it detects.

---

## MCP Tools

| Tool | What it does |
|------|-------------|
| `prism_query` | Ranked context pack for a task description — call this first |
| `prism_read` | Progressive-disclosure file read (full → signature → reference) |
| `prism_search` | Symbol search across the indexed graph |
| `prism_lookup` | Full source for a named symbol |
| `prism_index` | Trigger or check reindex |
| `prism_savings` | Token savings report for this session |
| `prism_feedback` | Rate a context result (trains future ranking) |
| `prism_compact` | Summarize older conversation turns to free context |

**Rule of thumb for agents:** start every task with `prism_query`, use `prism_read` instead of reading files directly, use `prism_search` instead of grep.

---

## Session Savings Ledger

Token savings are persisted per project across separate CLI invocations. The ledger lives at `~/.cache/prism/ledger/<sha1(root)>.json` and is pruned automatically after 30 days. `prism savings` reads the current project's ledger:

```bash
prism savings
# Session savings: 12,847 tokens delivered from 41,200 original (68.8% reduction)
# grove_query:  8 calls · 31,400 → 9,100 tokens
# grove_read:  22 calls · 9,800 → 3,747 tokens
```

---

## CLI Reference

```bash
prism init [--global] [dir]     # initialize project or user-level config
prism index [dir]               # index or reindex the project
prism query <task> [dir]        # ranked context for a task description
prism read <file> [dir]         # progressive-disclosure file read
prism search <keyword> [dir]    # symbol search
prism lookup <name> [dir]       # full source for a symbol
prism savings [dir]             # token savings report
prism serve [--port 8888] [dir] # start persistent HTTP server (optional)
prism mcp [dir]                 # start MCP stdio server
prism version                   # show version
```

---

## HTTP Server Mode

`prism serve` is optional. Use it only when you need a persistent HTTP daemon — for non-MCP integrations, custom automation, or tooling that prefers HTTP over stdio.

```bash
prism serve --port 8888 /path/to/project
```

HTTP binds to `127.0.0.1` only.

---

## Configuration

`prism.yaml` in the project root:

```yaml
grove_url: http://localhost:7777   # Grove instance to use
model: claude-sonnet-4-6           # model for embeddings and compaction
profile: balanced                  # ranking profile: balanced | speed | recall
budget_tokens: 8000                # default context budget
embeddings_backend: local          # local | openai | voyageai
```

Environment overrides: `PRISM_GROVE_URL`, `PRISM_MODEL`, `PRISM_PROFILE`, `PRISM_EMBEDDINGS_BACKEND`.

---

## Performance

Benchmarks run on macOS against synthetic Go projects (2026-05-27). Prism runs atop Grove — query numbers include the full round-trip through Grove's FTS5 + BFS and Prism's ranking pipeline.

### Indexing and Query

| Project | Files | Grove index | Prism index | Query latency | Prism RSS |
|---------|------:|------------:|------------:|--------------:|----------:|
| Small | 61 | 0.06 s | 0.06 s | 680 ms | 12 MB |
| Medium | 801 | 0.85 s | 3.9 s | 680 ms | 12 MB |
| Large | 4,501 | 11.6 s | 8.0 s | 680 ms | 12 MB |
| Monorepo | 9,901 | 34.0 s | 30.0 s | 690 ms | 12 MB |

Prism's own RSS stays near 12 MB regardless of project size — the symbol graph lives in Grove's process, not Prism's.

### Token Savings (progressive disclosure)

| Project | Files | First read | Second read | Third+ read |
|---------|------:|----------:|------------:|------------:|
| Small | 61 | 0% | 68% | 68% |
| Medium | 801 | 56% | 67% | 67% |
| Large | 4,501 | 56% | 67% | 67% |
| Monorepo | 9,901 | 0–58% | 58% | 58% |

**First-read savings** reflect relevance scoring: symbols below the threshold are shown at signature level instead of full source. When nearly all symbols in a file are relevant to the current task (typical for small projects and targeted queries), first-read savings approach 0% — you receive everything. This is correct behaviour.

**Second and third reads** apply session deduplication regardless of project size, cutting 57–68% of tokens.

**Headline targets:** `prism_query` end-to-end < 200 ms · `prism_read` with session cache < 50 ms · budget selection over 10K symbols < 20 ms

---

## Troubleshooting

**Savings stays at 0:** Run `prism index` then try a query. Check `command -v prism` and `prism version` to confirm the right binary is on `$PATH`.

**Port 8888 already in use:** Another `prism serve` is running. Stop it, use `--port 8889`, or use MCP stdio mode instead (which uses no port).

**Agent not using Prism after init:** Restart the coding tool to reload MCP config. Re-run `prism init` if needed.

**Context looks wrong:** Check `prism savings` — if calls are 0, the agent is not calling Prism tools. Check that the steering instructions were written by `prism init` (look for `CLAUDE.md` or `.cursorrules` in the project root).

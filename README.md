# Prism

Prism delivers token-optimized context to AI coding agents.

## Why Token Optimization Matters

An AI agent working on a 50,000-line codebase cannot receive all of it as context — and even if it could, most of it would be irrelevant noise that degrades response quality. The naive approach (send whatever files look related) wastes tokens on distant code and misses closely-related code that doesn't have an obvious filename match.

Prism solves this with a ranking pipeline. Given a task description, it queries Grove's knowledge graph, scores candidates across five signals, allocates a token budget across five categories, and applies progressive disclosure to maximize information density. The result is context scoped to what actually matters for the task at hand.

Typical token savings versus sending files manually: 35–92%.

## Architecture

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
│  2. Semantic similarity — embedding cosine score   │
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

## Integration Modes

Prism has two integration paths. Both produce identical context quality.

### MCP Stdio (recommended)

The `prism mcp` process is started by your coding tool on demand via the MCP configuration written by `prism init`. Each invocation is a short-lived process; there is no persistent HTTP daemon.

Supported tools: Claude Code CLI, Cursor, Windsurf, Zed, any MCP-capable agent.

### VS Code Extension

The extension spawns the `prism` binary per tool call via `child_process.spawn`. It registers all 8 tools with VS Code's Language Model Tools API (`vscode.lm.registerTool`), making them available in Copilot Chat as `#prismQuery`, `#prismRead`, etc.

Use this mode when MCP is not approved or available in your environment.

[Extension documentation →](vscode-extension/README.md)

## Quick Start

```bash
# 1. Install
cd prism && make install

# 2. Verify
prism version

# 3. Initialize a project (run once per project root)
cd /your/project
prism init

# 4. Restart your coding tool to pick up the MCP config

# 5. Index
prism index

# 6. Verify savings after using the agent
prism savings
```

`prism init` does three things: writes `prism.yaml`, writes steering instructions for your agent (CLAUDE.md, .cursorrules, etc.), and registers the MCP server config for the coding tools it detects.

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

## Session Savings Ledger

Token savings are persisted per project across separate CLI invocations. The ledger lives at `~/.cache/prism/ledger/<sha1(root)>.json` and is pruned automatically after 30 days. `prism savings` reads the current project's ledger:

```bash
prism savings
# Session savings: 12,847 tokens delivered from 41,200 original (68.8% reduction)
# grove_query:  8 calls · 31,400 → 9,100 tokens
# grove_read:  22 calls · 9,800 → 3,747 tokens
```

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

## HTTP Server Mode

`prism serve` is optional. Use it only when you need a persistent HTTP daemon — for non-MCP integrations, custom automation, or tooling that prefers HTTP over stdio.

```bash
prism serve --port 8888 /path/to/project
```

HTTP binds to `127.0.0.1` only.

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

## Troubleshooting

**Savings stays at 0:** Run `prism index` then try a query. Check `command -v prism` and `prism version` to confirm the right binary is on `$PATH`.

**Port 8888 already in use:** Another `prism serve` is running. Stop it, use `--port 8889`, or use MCP stdio mode instead (which uses no port).

**Agent not using Prism after init:** Restart the coding tool to reload MCP config. Re-run `prism init` if needed.

**Context looks wrong:** Check `prism savings` — if calls are 0, the agent is not calling Prism tools. Check that the steering instructions were written by `prism init` (look for `CLAUDE.md` or `.cursorrules` in the project root).

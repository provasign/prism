# Prism — VS Code Extension

The VS Code extension delivers ranked, compressed code context to AI agents without requiring `prism serve`. It spawns the `prism` binary per tool call and registers all 8 tools with VS Code's Language Model Tools API, making them available in GitHub Copilot Chat as `#prismQuery`, `#prismRead`, and so on.

## When to Use This

Use the VS Code extension when:
- MCP is not approved or not available in your environment
- You work primarily in VS Code with GitHub Copilot

For Claude Code CLI, Cursor, Windsurf, and other MCP-capable tools, use [MCP stdio mode](../README.md) instead — it requires no VS Code and no extension.

## How It Works

```
Copilot Chat prompt with #prismQuery
     │ vscode.lm.registerTool invocation
     ▼
Prism VS Code Extension (TypeScript)
     │ child_process.spawn("prism query ...")
     ▼
prism binary (Go)
     │ POST grove:7777/query  (auto-starts grove if unreachable)
     ▼
Grove (Go) — SQLite FTS5 + BFS graph traversal
     │
     ▼
5-signal ranking → budget allocation → progressive disclosure
     │
     ▼
Token-optimized context returned to Copilot Chat
```

Each tool call spawns a fresh `prism` process. There is no persistent extension host daemon.

## Requirements

- `prism` binary on `$PATH` (or configure `prism.binaryPath`)
- `grove` binary on `$PATH` (Prism auto-starts it on first call)

Install both:

```bash
cd grove && make install
cd prism && make install
```

## Setup

Install the extension once. Then, for each project you want to enable:

```bash
cd /your/project
prism init
```

`prism init` writes steering instructions and registers tool availability for the project. You do not need to run it again unless you move the project or want to update the instructions.

## Tools

All 8 tools are registered with `vscode.lm.registerTool`:

| Chat reference | Tool | What it does |
|----------------|------|-------------|
| `#prismQuery` | `prism_query` | Ranked context pack for a task — call this first |
| `#prismRead` | `prism_read` | Progressive-disclosure file read |
| `#prismSearch` | `prism_search` | Symbol search across the indexed graph |
| `#prismLookup` | `prism_lookup` | Full source for a named symbol |
| `#prismIndex` | `prism_index` | Trigger or check reindex |
| `#prismSavings` | `prism_savings` | Token savings report |
| `#prismFeedback` | `prism_feedback` | Rate a context result |
| `#prismCompact` | `prism_compact` | Summarize older conversation turns |

## Commands

Access via Command Palette (`Cmd+Shift+P`):

- **Prism: Setup Workspace** — run `prism init` for the current project
- **Prism: Index Workspace** — trigger a manual reindex
- **Prism: Query for Context** — interactive task input, opens ranked result
- **Prism: Show Session Savings** — token savings dashboard
- **Prism: Reset Session** — clear session state

## Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `prism.binaryPath` | `prism` | Path to the prism binary |
| `prism.grovePath` | `grove` | Path to the grove binary |
| `prism.autoIndex` | `true` | Reindex on file save |
| `prism.profile` | `default` | Ranking profile: `default`, `implement_feature`, `fix_bug`, `code_review` |

## Status Bar

Two items appear in the **left** status bar:

| Item | Click action | Shows |
|------|-------------|-------|
| `$(database) Grove N syms` | Re-index workspace | Live symbol count from the knowledge graph |
| `$(graph) Prism X.X%` | Show session savings | Session token savings percentage |

Both items refresh automatically every 15 seconds and after each file save.

## Building from Source

```bash
cd prism/vscode-extension
npm install
npm run compile
npx vsce package --no-dependencies   # produces prism-vscode-X.Y.Z.vsix
```

Install the `.vsix` via Extensions → Install from VSIX.

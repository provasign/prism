# Prism Roadmap

Prism delivers token-optimized code context to AI agents. MIT licensed. Requires Grove.

---

## v0.1.0 — Core Engine ✅ shipped

- [x] Grove client with auto-start logic (`GROVE_URL`, health check, subprocess fallback)
- [x] 5-signal ranking engine: graph distance, semantic similarity (TF-IDF default), recency, test relevance, edit frequency
- [x] Budget allocator: greedy selection across 5 categories (target 35%, deps 25%, tests 20%, doc 10%, summary 10%)
- [x] Progressive disclosure: full source → signature (~15% tokens) → reference (~3% tokens)
- [x] O(1) LRU session tracker (50K file cap)
- [x] File read compression pipeline
- [x] Token ledger: per-session savings tracking persisted to `~/.cache/prism/ledger/`

---

## v0.2.0 — MCP Server & CLI ✅ shipped

- [x] MCP server: 8 tools (`prism_query`, `prism_read`, `prism_search`, `prism_lookup`, `prism_index`, `prism_compact`, `prism_savings`, `prism_feedback`) over JSON-RPC 2.0 stdio + HTTP/SSE
- [x] CLI: `init`, `index`, `query`, `read`, `search`, `lookup`, `savings`, `serve`, `mcp`, `version`
- [x] `prism init` auto-detects installed tools and writes MCP config for Claude Code, GitHub Copilot / VS Code (.vscode/mcp.json), Cursor, Codex CLI (~/.codex/config.toml TOML), Windsurf, Zed
- [x] `prism init` writes steering instructions (CLAUDE.md, .cursorrules, .windsurfrules, .github/copilot-instructions.md, AGENTS.md, GEMINI.md)
- [x] HTTP server mode (`prism serve --port 8888`) binds to `127.0.0.1`
- [x] Compatible with Claude Code, Cursor, Windsurf, any MCP-compliant client

---

## v0.3.0 — VS Code Extension ✅ shipped

- [x] TypeScript extension: spawns `prism` binary as child process, no MCP server required
- [x] `vscode.lm.registerTool` for all 8 Prism tools — visible to Copilot Agent mode as `#prismQuery`, `#prismRead`, etc.
- [x] Grove status in left status bar: symbol count, click to re-index
- [x] Prism savings in left status bar: session token savings %, click to view details
- [x] Auto-index on save (`onDidSaveTextDocument`, configurable)
- [x] Commands: `Prism: Setup Workspace`, `Prism: Index Workspace`, `Prism: Query for Context`, `Prism: Show Session Savings`, `Prism: Reset Session`

---

## v0.4.0 — ONNX Embeddings ✅ shipped

- [x] ONNX Runtime backend: `all-MiniLM-L6-v2` (384-dim) opt-in via `embeddings_backend: onnx` in `prism.yaml`
- [x] Embedding cache: `symbolID+blobSHA → []float32`, in-memory, zero persistence
- [x] Benchmark validated: `prism_query` end-to-end < 200 ms · `prism_read` with session cache < 50 ms

---

## v1.0.0 — Production Hardening

- [x] Token savings validated: 35–92% reduction vs raw file delivery across all project sizes
- [x] Ranking profiles: `default`, `implement_feature`, `fix_bug`, `code_review`
- [ ] VS Code extension published to Marketplace
- [ ] Homebrew tap: `brew install prism`
- [ ] `curl | sh` installer for Linux
- [ ] Published Go module: `github.com/tabladrum/grove-suite/prism`

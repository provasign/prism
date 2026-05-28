# Prism Roadmap

Prism delivers token-optimized code context to AI agents. Requires Grove.

## v0.1.0 — Core Engine
_Target: Phase 1–6 of Implementation Plan_

- [ ] Grove client with auto-start logic (`GROVE_URL`, health check, subprocess fallback)
- [ ] 5-signal ranking engine: graph distance, semantic similarity (TF-IDF default), recency, test relevance, edit frequency
- [ ] Budget allocator: greedy selection with 5-category allocation (target 35%, dep 25%, test 20%, doc 10%, summary 10%)
- [ ] Progressive disclosure: Full / Signature (~15% tokens) / Reference (~3% tokens)
- [ ] O(1) LRU session tracker (50K file cap) with confidence model
- [ ] File read compression pipeline
- [ ] Token ledger: per-session savings tracking

## v0.2.0 — MCP Server & CLI
_Target: Phase 7–8 of Implementation Plan_

- [ ] MCP server: 8 tools (`prism_query`, `prism_read`, `prism_search`, `prism_lookup`, `prism_index`, `prism_compact`, `prism_savings`, `prism_feedback`) over JSON-RPC 2.0 stdio + HTTP+SSE
- [ ] CLI: 11 commands (`init`, `index`, `status`, `query`, `read`, `search`, `lookup`, `compact`, `serve`, `savings`, `config`)
- [ ] Compatible with: Claude Code, Cursor, Windsurf, any MCP-compliant client

## v0.3.0 — VS Code Extension (MCP-Free Path)
_Target: Phase 11 of Implementation Plan_

- [ ] TypeScript extension: spawns `prism` binary as child process, no MCP server required
- [ ] `vscode.lm.registerTool` for all 8 Prism tools — visible to Copilot Agent mode natively
- [ ] Auto-index on save (`onDidSaveTextDocument`)
- [ ] Sidebar panel: Grove graph stats + Prism session token savings
- [ ] Inline decorations: impact score badge on hover
- [ ] Published to VS Code Marketplace

## v0.4.0 — ONNX Embeddings
_Target: Phase 9 of Implementation Plan_

- [ ] ONNX Runtime backend: `all-MiniLM-L6-v2` (384-dim) replaces TF-IDF as primary
- [ ] Embedding cache: `symbolID+blobSHA → []float32`, in-memory, zero persistence
- [ ] Benchmark: `prism_query` < 200 ms end-to-end; `prism_read` with session cache < 50 ms

## v1.0.0 — Production Ready

- [ ] Token savings benchmark: ≥ 35% reduction on standard workloads vs raw file delivery
- [ ] Ranking profiles validated: `implement_feature`, `fix_bug`, `code_review`, `default`
- [ ] Session confidence model calibrated against real usage data
- [ ] Single binary distribution: `brew install prism`, GitHub Releases

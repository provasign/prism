# Prism Roadmap

Prism delivers graph-ranked, token-optimized code context to AI agents. MIT
licensed. Grove is embedded in the Prism binary in current releases.

---

## v0.1.0 — Core Engine ✅ shipped

- [x] Grove-backed symbol, dependency, call, and test graph
- [x] 5-signal ranking engine: graph distance, semantic similarity (TF-IDF default), recency, test relevance, edit frequency
- [x] Budget allocator: greedy selection across 5 categories (target 35%, deps 25%, tests 20%, doc 10%, summary 10%)
- [x] Progressive disclosure: full source → signature (~15% tokens) → reference (~3% tokens)
- [x] O(1) LRU session tracker (50K file cap)
- [x] File read compression pipeline
- [x] Token ledger: per-session savings tracking persisted to `~/.cache/prism/ledger/`

---

## v0.2.0 — Agent CLI ✅ shipped

- [x] CLI: `init`, `index`, `query`, `read`, `search`, `lookup`, `status`, `savings`, `compact`, `feedback`, `version`
- [x] CLI output modes: `text` for agents, `lean` for compact automation, `json` for full metadata
- [x] `prism init` writes steering instructions (CLAUDE.md, .cursorrules, .windsurfrules, .github/copilot-instructions.md, AGENTS.md, GEMINI.md)
- [x] MCP and HTTP surfaces retired in v0.5.x after CLI text mode proved simpler and lower overhead

---

## v0.3.0 — VS Code Extension retired in v0.5.x

- [x] Extension prototype validated native Copilot tools
- [x] Removed as a product surface after CLI text mode became the simpler,
  cross-agent default
- [x] VS Code users can use Prism through generated CLI steering

---

## v0.4.0 — ONNX Embeddings ✅ shipped

- [x] ONNX Runtime backend: `all-MiniLM-L6-v2` (384-dim) opt-in via `embeddings_backend: onnx` in `prism.yaml`
- [x] Embedding cache: `symbolID+blobSHA → []float32`, in-memory, zero persistence
- [x] Benchmark validated: `prism query` end-to-end < 200 ms · repeated CLI reads use session cache

---

## v0.5.x — CLI Agent Mode ✅ shipped

- [x] Embedded Grove indexing; no separate Grove daemon or `grove_url` setup
- [x] CLI text mode installed by `prism init .`
- [x] `prism.yaml` reduced to project profile and optional model settings
- [x] Agent steering now says shell search first, Prism graph expansion second
- [x] `coverage_gaps` reports direct package-local test coverage for exported functions and methods
- [x] Real-world Prism-repo CLI benchmark validated 29.6% average context reduction vs shell-only workflows

---

## v1.0.0 — Production Hardening

- [x] Token savings validated: CLI text mode reduces real relational workflow context
- [x] Ranking profiles: `default`, `implement_feature`, `fix_bug`, `code_review`
- [x] New context-control features shipped: semantic delta encoding, anti-context manifests, trivial-body elision, learned weights, warm cache, phase-aware budget shaping, typed evidence packets
- [ ] Homebrew tap: `brew install prism`
- [ ] `curl | sh` installer for Linux
- [ ] Published Go module: `github.com/provasign/prism`

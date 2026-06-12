# Prism end-to-end review vs Grove v0.6.2 — 2026-06-12

> **Status:** every numbered finding below was fixed the same day (see the
> commit introducing this file). Only the proxy-roadmap items (ICR,
> CertifyDiff) remain deliberately deferred.

Start-to-end review of Prism (~6k LOC) against the Grove v0.6.2 engine it
embeds. Prism is on the latest Grove but consumes only 9 of 19 engine
methods; the highest-impact findings are all cases where Prism still
hand-rolls something Grove now does better. Goal of the follow-up work:
better delivered context *and* lower token/latency overhead.

**Architecture note (design intent, confirmed 2026-06-12):** Prism is the
single MCP surface and *proxies* Grove — Grove is never run as a second MCP
server. Engine capabilities Prism does not yet expose (ICR, CertifyDiff) are
future proxy candidates, not rejected; they stay out only until there is an
agent workflow that needs them through Prism.

## What works well (keep)

- Compression pipeline (`internal/compression/compressor.go`): full-fidelity
  first reads; savings only on safe re-read paths (SHA-pointer, lossless
  semantic delta); refuses to guess when spans can't be trusted.
- MCP server follows the family token rules: compact JSON, small advertised
  schema set, stale-context warnings only on context-bearing calls.
- `prism init` steering installer and "both" mode (MCP primary, CLI fallback
  for subagents).

## High-impact gaps — Grove capabilities Prism ignores

1. **Semantic ranking duplicated.** `ensureEmbeddings`
   (internal/mcp/tools.go) pulls 5,000 symbols and builds Prism's own
   TF-IDF — rebuilt after every reindex and, in CLI mode, on *every process
   invocation*. Grove's `Engine.Semantic` uses model2vec embeddings cached
   by symbol ID (only changed symbols re-embed). `Client.Semantic` exists
   and is dead code. → Delegate; delete `internal/embeddings`.

2. **Drift detection reintroduces solved problems.** `fileDrift`
   (internal/mcp/drift.go) compares name-keyed SHA maps: renames are
   reported as removed+added (Grove v0.6.0 GraphDiff detects renames), no
   breaking-change classification, and bare-`Name` keying collides for
   same-named members in one file. → Use Grove `Diff` /
   `DiffAgainstFileContent`; surface `renamed`/`breaking`; key fallback by
   QualifiedName. Same Name-collision applies to the semantic-delta SHA map
   in the compressor (can produce a lossy delta — correctness bug).

3. **Coverage gaps: perf bug + benchmark overfit.** `hasDirectTestCoverage`
   falls back to a 5,000-symbol pull *per blast-radius symbol*;
   `domainStem` hardcodes "Payments"/"Payment" (payflow artifact). Grove
   v0.6.0 made `Tests()` trustworthy (import-graph-scoped edges,
   call-site evidence, Rust/JUnit/xUnit conventions). → Trust the edges,
   delete the heuristic ladder.

4. **`toolRead` finds file symbols by basename substring search**
   (`SearchSymbols(baseName, 200)` + path filter) — misses symbols when
   >200 matches share a basename. Grove v0.6.1 added `Engine.FileSymbols`
   for exactly this; the client wrapper is already wired. → One-line swap.

## Token-economy issues

5. **Fabricated savings baseline.** `Ledger.Record("prism_query", used*3,
   used)` — the dashboard's prism_query baseline is an assumed 3×
   multiplier, not a measurement. → Use a defensible baseline (token cost
   of the unique files containing returned symbols — what an agent would
   have read) or report delivered-only. Repeat-read SHA-pointer savings are
   real and measured; lead with those.

6. **Confidence model sees only Prism-delivered tokens.** Everything else
   in the agent's context (shell output, edits, other servers) is
   invisible, so freshness is systematically overestimated and SHA-pointers
   can reference scrolled-out content. → Accept an optional `context_used`
   hint; make the 4th-read `escalated-full` confidence-aware.

7. **Wire trims.** Drop the Prism-internal `disclosure` field from
   prism_query symbols. Honor explicit caller budgets (currently a
   `budget: 2000` request is silently raised to 4,000 and still
   phase-multiplied).

## Workflow/consistency issues

8. **Steering recommends unadvertised tools.** `ToolSchemas` lists 6 tools;
   `Invoke` dispatches 10; the installed MCP steering block tells agents to
   use `prism_compact`/`prism_evidence`, which aren't in `tools/list`. →
   Align (keep them dispatchable for CLI/HTTP; remove from MCP steering).

9. **Open schemas on half the tools.** `prism_search`, `prism_lookup`,
   `prism_index`, `prism_drift` publish `additionalProperties: true` with
   no parameter docs. Grove v0.6.0 set the family rule (real schemas,
   terse descriptions). → Match it.

10. **Silent wrong-symbol fallback in `prism_lookup`.** No exact match →
    first search hit returned with no indication. Wrong context is worse
    than no context. → `matched: false` + candidate names on fallback.

11. **Dead code and parity gaps.** `Client.Deps`,
    `buildAntiContextManifest`, `sortSymbolsByName` unused; httpapi routes
    omit `prism_drift`; `ranking.SignalComputer` shells out to `git log`
    (incl. `--follow`) twice per candidate file — batch the 90-day stats
    into one pass.

## Deliberately deferred (proxy roadmap)

- `Engine.ICR` and `Engine.CertifyDiff`: expose through Prism (e.g. a
  future `prism_certify` / pre-commit gate) when an agent workflow needs
  them — consistent with the Prism-proxies-Grove design. Grove's own MCP
  is not part of the recommended setup.

## Order of attack

1. Coverage-gap fix (live perf bug + benchmark residue)
2. `toolRead` → `FileSymbols`
3. Semantic delegation to Grove
4. GraphDiff-based drift (+ semantic-delta collision fix)
5. Honest savings accounting
6. Steering/schema alignment, lookup fallback flag, budget honoring
7. Confidence hardening
8. Cleanups (dead code, httpapi drift route, batched git signals)

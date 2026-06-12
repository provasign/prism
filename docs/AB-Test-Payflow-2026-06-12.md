# Prism A/B Agent Benchmark — Payflow, post-review-fixes
**Date:** 2026-06-12
**Prism version:** dev build containing every fix from
[prism-review-2026-06-12.md](prism-review-2026-06-12.md) (Grove v0.6.2 embedded)
**Subagent model:** claude-sonnet-4-6, one run per arm per task
**Prior run for comparison:** AB-Test-Payflow-2026-06-07 (v0.5.5; recoverable
via `git show 9f89552:docs/AB-Test-Payflow-2026-06-07.md`)

## Purpose

Re-run the 2026-06-07 controlled A/B after the review-fix pass, on the same
synthetic payflow project with the same pre-established ground truth, plus
new deterministic scenarios for capabilities that did not exist in June 7's
build (GraphDiff drift, context-aware re-read escalation, measured savings).

The two "honest findings" of the old run are the direct targets:
1. **prism_read path-resolution failures** (6 failed calls across T1/T2).
2. **coverage_gaps false positives** (`RevokeToken`, `RefundPayment`
   reported untested when both are tested).

## Test project

Reconstructed 17-file Go payment service at `/tmp/prism-ab-bench/payflow`
(`github.com/example/payflow`: api / auth / model / payment / storage,
82 symbols, 508 edges). Compiles; its own test suite passes. Coverage gaps
are designed in and verified at the graph level before the benchmark:
every designed gap has zero `tests` edges; everything else has direct
test-edge evidence.

**Ground truth — untested exported functions (12):** the five `Handler.*`
methods, `Middleware.RequireScope` (api), `Register` (api),
`Service.CompletePayment` + `Service.ListPayments` + `ValidateCurrency`
(payment), `Service.RequireScope` (auth), `MemoryStore.UpdatePayment`
(storage). Deliberately tested (must NOT be flagged): `Service.RevokeToken`,
`Service.RefundPayment`, `Service.ProcessPayment`, the storage CRUD except
Update, `RequireAuth`, `TokenFromContext`, `TokenStore.*`, validators except
ValidateCurrency, `Token.IsExpired/HasScope`.
(`Register` was not enumerated in the 2026-06-07 table but is genuinely
uncalled by any test; both arms found it, so it counts as a true gap.)

**Tasks (identical to 2026-06-07):**
- T1 trace the `CreatePayment` call chain + tests
- T2 files to change for a new `StatusCancelled`
- T3 list every untested exported function
- T4 find every call site of `auth.ValidateToken`

**Arms:** baseline = shell only (rg/grep/cat + Read). Prism = `prism` CLI in
`--format text/lean` (the documented subagent workflow), rg allowed only for
anchoring. Arms enforced by prompt; each agent self-reports a tool log.

---

## Result 1 — tool-level coverage precision (the headline before/after)

Union of `coverage_gaps` over 12 focused queries (1 term each), scored
against ground truth, no agent in the loop:

| | 2026-06-07 (name heuristics) | 2026-06-12 (Grove test edges) |
|---|---|---|
| Designed gaps found | all | **11/11** |
| False positives | **2** (`RevokeToken`, `RefundPayment`) | **0** |

The false positives are structurally gone: coverage is now read off Grove's
import-scoped, call-evidenced `tests` edges instead of name patterns.

Caveat discovered while scoring: one query audits only its seeds (max 5) +
blast radius, so packing five `--terms` into one call narrows the audit —
4 broad queries surfaced just 3 gaps, 12 focused queries surfaced all 11.
Audit-style tasks need one or two terms per query (now reflected in the
guidance below).

## Result 2 — A/B agent runs

| Task | Arm | Subagent tokens | Tool calls | Context bytes | Correct? |
|------|-----|---------------:|-----------:|--------------:|----------|
| T1 chain | baseline | 24,133 | 17 | ~17,900 | ✓ |
| T1 chain | prism | 26,385 | 13 | ~17,500 | ✓ |
| T2 impact | baseline | 22,680 | 19 | ~13,000 | ✓ (6 files) |
| T2 impact | prism | 22,111 | 18 | ~13,900 | ✓ (6 files, identical) |
| T3 coverage | baseline | 31,379 | 35 | ~21,600 | **9/12** — missed `UpdatePayment`, `auth.RequireScope`, `ValidateCurrency`; flagged the two `Error()` methods (disputed) |
| T3 coverage | prism | 33,520 | 12 | ~37,400 | **10/12, 0 false positives** — missed the two `ListPayments` (term-breadth, not tool error) |
| T4 call sites | baseline | 17,939 | 13 | ~4,300 | ✓ all 5 sites |
| T4 call sites | prism | 16,072 | **4** | ~5,100 | ✓ all 5 sites |
| **Total** | baseline | **96,131** | **84** | ~56,800 | |
| **Total** | prism | **98,088** | **47** | ~73,900 | |

### Against the 2026-06-07 run

| Metric | 2026-06-07 | 2026-06-12 |
|---|---|---|
| Prism total-token overhead vs baseline | +27% … +147% per task | **+2% overall** (wins T2 and T4 outright) |
| Failed prism tool calls | 6 (path-resolution bug) | **0** |
| T3 correctness | baseline ✓ / prism 2 false positives | **prism 10/12 + 0 FP / baseline 9/12 with 3 designed gaps missed** |
| T4 tool calls | prism 3× fewer | prism **3.25× fewer** (4 vs 13) |

The most consequential reversal is T3: the baseline agent — reading every
file and cross-referencing by hand across 35 tool calls — *silently missed
three of the deliberately designed gaps*, exactly the failure mode
coverage_gaps exists to prevent. The prism agent trusted the tool (per the
steering rule the old run motivated) and produced zero false positives; its
two misses were query-breadth, recoverable by more focused term selection.

Honest negatives: prism's T3 raw context was ~73% larger than baseline's
(graph payloads in lean JSON across 7 broad queries), and T1 stayed a draw
with slightly higher total tokens. Prism's structural win is fewer tool
calls (47 vs 84) and the correctness profile, not raw byte counts on a
16-file project — graph leverage grows with repo size, and a single-turn
benchmark cannot show the session-scale repeat-read savings below.

## Result 3 — new-capability scenarios (deterministic, scripted MCP session)

Measured on the wire (full JSON-RPC response line), fresh index:

| Scenario | Result |
|---|---|
| First read of `payment/service.go` | full-fresh, 598 tokens (2,993 wire bytes) |
| Second read, same session | **sha-pointer, 29 tokens (366 wire bytes) — 95.2% saved** |
| 3rd/4th read at high confidence (`context_used` hint) | stays sha-pointer — the old build force-resent the full file on every 4th read |
| Rename `RefundPayment`→`ReversePayment` on disk, then `prism_drift` | one `renamed` entry with `renamedTo`, new signature, **`breaking: true`** — 518 wire bytes (~130 tokens) to learn the ground shifted; old build reported an unrelated removed+added pair |
| Next context-bearing call after the rename | carries the stale-context warning automatically (mid-task delivery) |
| `prism_lookup RefundPay` (no exact match) | `matched: false` + 3 candidates — the old build silently returned the closest hit |
| `prism_query --budget 2000` | budgetUsed 525 ≤ 2000; explicit budgets are no longer silently raised to 4,000 |
| Warm cross-session cache | a file delivered via CLI earlier in the day came back as a sha-pointer on the *first* MCP-session read (29 tokens) |

Savings accounting behind these numbers is now measured (containing-file
sizes on disk), not the previous `×3` assumption.

## Guidance updates fed back from this run

- Coverage audits: one or two `--terms` per query; union the results.
  Five-term queries starve the audit set.
- The `context_used` hint is worth passing on re-reads: it is what keeps
  high-confidence repeats at ~29 tokens instead of escalating.

## Reproduction

The fixture is fully specified by this document (package layout, designed
gaps, test placement); it lives outside the repo to keep Prism's own index
clean. Rebuild: create the 17 files per the layout above, `go test ./...`,
`prism index`, then verify the `tests` edges with
`sqlite3 .grove/grove.db "SELECT from_node,to_node FROM edges WHERE edge_type='tests'"`
before running tasks. Subagent prompts and the scripted-session harness are
in the session that produced this report; scenario scripts:
`/tmp/prism-ab-bench/mcp_scenarios.py` (ephemeral).

## Result 4 — grafana scale (18,979 files / 98,545 symbols / 1.16M edges)

Same dev build against the Grove-validation grafana clone
(`/tmp/fuse-corpus/grafana`, 1.5 GB index). This is where the "graph
leverage grows with repo size" claim gets tested.

**Caller tracing at scale.** Task: "who calls `SecretsKVStoreSQL.Get`, and
which tests cover it" (58 call edges across the monorepo).

| Approach | Cost | Completeness |
|---|---|---|
| Thorough shell baseline (read every caller file) | 29 files, 242,827 bytes ≈ **60,700 tokens** | complete |
| Naive grep (`rg "\.Get\(ctx"` in the package) | cheap | finds **11 of 58** call sites — `Get` is too generic to grep across packages |
| Prism, one query | 28,399 bytes ≈ **7,100 tokens** (budgetUsed 6,180) | target struct, its methods, and its real integration test ranked first |

**88% less context than the thorough read, against a grep alternative that
misses 81% of the call sites.** On payflow this same task was a draw; the
curve bends exactly as predicted.

**Latency.** CLI mode pays engine open + graph rehydration per process:
~5.8–11.7 s per call on this repo (fine on payflow, painful here) — on
large repos the MCP session is the right surface. In-session: reads are
instant, the first query costs 6.8 s (one-time semantic corpus load,
cached by symbol ID), subsequent queries **0.75 s**.

**Session economics.** Repeat read of a 2,042-token file: **31 tokens
(98.5% saved)**. One-file edit then `prism_drift`: ~29 s wall (delta
re-index dominates; consistent with Grove's published 18.7 s) for a
607-byte symbol-level report.

**Findings fed back from this run (all addressed same day):**
1. *Fixed in Prism:* prism_query expanded the graph by bare seed name; on
   98k symbols, names like `Get` collide across packages and dragged
   unrelated callers/tests in. It now expands by qualified name.
2. *Fixed in Grove (partially) — test-edge transitivity:* `TestsFor` no
   longer traverses low-confidence fallback edges (cross-file bare-name
   call guesses at 0.6, type-use at 0.5). Verified remaining limitation:
   cross-subsystem noise can still ride high-confidence edges when a bare
   callee name resolves to ≤16 candidates (each gets a 0.95 edge) — on
   this query the test tail stayed noisy. Ranking keeps the right tests
   first and the budget caps the cost; the durable fix is type-aware
   callee resolution in Grove's edge builder.
3. *Fixed in Grove — rename pairing:* two compounding causes. (a) body
   normalization used a substring `ReplaceAll`, so short names ("Get"
   inside "GetKeys") normalized asymmetrically — now whole-identifier
   only; (b) a mechanical rename leaves the old name in the doc comment
   ("// Get an item…"), breaking single-name normalization — a bounded
   pairwise pass now blanks both names on both sides. Re-verified on
   grafana: the `Get`→`Fetch` rename reports as one `renamed` entry with
   `breaking: true` and the new signature.

## Limitations

- One run per arm per task (same as 2026-06-07); model variance applies.
- Context bytes are self-reported by agents from their tool logs (±10%).
- Arm discipline enforced by prompt; both arms complied per their logs.
- 16-file project: graph-expansion leverage and repeat-read savings both
  understate what larger repos see.

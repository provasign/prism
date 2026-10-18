# Prism cost reduction: verified findings and implementation plan

Date: 2026-10-17  
Status: revised after checking the review against result files, code, experiment notes, and Claude Code documentation.

**Later October 17 review:** the issue bed is now complete and the corrected Codex mixed run is stopped. See the [cross-agent improvement plan](prism-cross-agent-improvement-plan-2026-10-17.md) for updated results, newly identified instruction/answer contradictions, and the current implementation priorities. The findings below retain their original experiment scope.

## Scope and constraints

**Prism answers questions only: it never edits source files, emits commands for the agent to execute, or runs builds/tests; schema changes require a paid call-shape check.** The user selects the model. This plan does not introduce model switching or cheaper subagents. Host configuration and benchmark execution belong to the coding host or research harness.

Paid experiments require an explicit go-ahead for each run. The October 14 bug-list closure is a prerequisite. Its own final-state entry records all items closed that night and releases on October 15, so it would be inaccurate to describe that original list as still open. Before any new run, verify closure against subsequent bug reports and current correctness checks; this document authorizes no paid run.

## Findings

**The cost diagnosis holds; the original implementation roadmap overstated the remaining Prism-specific opportunities.** Fewer model responses and fewer total tokens can produce almost no dollar saving because expensive cache writes offset cheaper cache reads.

The original audit reconstructed **146 individual agent sessions across two selected result files**, not either complete bench29/bench30 campaign. Every reconstructed cost matched its recorded Claude CLI estimate within $0.000001. These are list-price estimates, not invoices.

| Audited subset | Model responses, native → Prism¹ | Token reduction | Estimated cost, native → Prism | Recorded solves |
|---|---:|---:|---:|---:|
| bench29 issue42: 41 complete cost pairs | 256 → 226 | 5.3% | $4.4965 → $4.3856: **2.5% cheaper** | 23 → 26 |
| bench30 wide rep2: 32 pairs | 218 → 170 | 12.3% | $3.9498 → $3.9405: **0.24% cheaper** | 32 → 31 |

¹ Deduplicated assistant messages; distinct from the harness's reported “turns.”

**Recomputation method:** complete cost pairs only, Claude list-price estimates, and transcript usage deduplicated by message ID. Output was inferred from the saved aggregate token count minus reconstructed input categories; this was a reconciliation, not an independent per-request output measurement.

The issue file contains 42 tasks. Prism's usage is missing for `apache__dubbo__pr16325` after `ScorerRanNoTests`. Its unmatched native cost is $0.1948326. Including that native cell while omitting unknown Prism spend produces the file's approximately 0.935x known-cost ratio; complete pairs give **0.975x**. The apparent token saving similarly falls from about 10% to 5.3%. Missing spend is unknown, never zero.

The review's **0.95x bench29 and 1.02x bench30 are whole-campaign summaries**, covering two wide repetitions, issue42, and 40 long-session tickets: 146 task/ticket comparisons per campaign. Re-summing their saved records reproduces:

| Whole campaign, known recorded spend | Native | Prism | Prism/native | Recorded solves, native → Prism |
|---|---:|---:|---:|---:|
| bench29 | $17.4478 | $16.5150 | 0.9465x | 120 → 125 |
| bench30 | $16.3343 | $16.6772 | 1.0210x | 123 → 122 |

Each campaign has one Prism cell with missing cost, so these are reproduced recorded totals, not complete expenditure estimates or a valid substitute for a complete-pair comparison. The subset table does not contradict the campaign table. **23 → 26 is present in the actual bench29 issue records.** The earlier October 11 issue run recorded a 25 → 25 tie; it is a different run. Neither a single subset win nor a campaign swing establishes a general quality or cost advantage.

Sources: [audited issue results](../../research/harness/results/bench29-issue42-2026-10-16/results.json), [audited wide results](../../research/harness/results/bench30-wide-rep2-2026-10-16/results.json), [bench29 campaign definition](../../research/harness/results/bench29.sh), [bench30 campaign definition](../../research/harness/results/bench30.sh), and the durable [session-level cost audit](../../research/harness/results/prism-cost-audit-2026-10-17.json).

## Claude pricing and cost attribution

Sonnet 5.5 standard API prices used by this audit, in dollars per million tokens:

| Model | Uncached input | 5-minute cache write | 1-hour cache write | Cache read | Output |
|---|---:|---:|---:|---:|---:|
| Sonnet 5.5 | $2 | $2.50 | $4 | $0.10 | $10 |

Source: [Anthropic pricing](https://platform.claude.com/docs/en/about-claude/pricing). The rates also reconcile with the saved CLI estimates and the October 16 attribution note.

```text
Cost = (2 × uncached_input
      + 2.5 × cache_write_5m
      + 4 × cache_write_1h
      + 0.1 × cache_read
      + 10 × output) / 1,000,000
```

One output token costs as much as **100 cache-read tokens**; a one-hour cache-write token costs as much as **40 cache-read tokens**. Token totals alone therefore hide the main cost differences.

| bench30 wide rep2 cost component | Native | Prism | Effect |
|---|---:|---:|---:|
| One-hour cache writes | $2.6106 | $2.7925 | +$0.1819 |
| Cache reads | $0.5987 | $0.5125 | −$0.0862 |
| Output | $0.7397 | $0.6349 | −$0.1048 |
| Uncached input | $0.0009 | $0.0007 | −$0.0002 |
| **Total** | **$3.9498** | **$3.9405** | **−$0.0093** |

Cache writes account for approximately **71% of Prism's estimated cost** here. About **92% of its additional cache-write tokens occur on the first request**. This identifies a cost component, not an untried startup-trimming opportunity: previous trims and schema experiments already tested that direction, and cache reuse can affect first-request attribution.

## Tried before

The table records prior work so these levers are not presented as new. Historical results are transcribed from the linked experiment notes; they were not all independently rerun or re-scored for this revision. **Pre-September 20 benchmark conclusions were explicitly retired by the project** because of harness defects. Those rows document implementation history only; later evidence supports the current decisions.

| Lever | Date | Recorded result and current decision |
|---|---|---|
| Steering/surface reduction | 2026-08-15, v0.53.0 | Steering **11,834 → 1,847 characters**, not tokens; agent tools 14 → 6. Already implemented; no new trim project. |
| Startup payload trimming | 2026-09-30–10-02, PR #21 | Paid trim27: 19/27 solves for both Prism builds, cost **0.996x**; no measurable saving. Extra first-request context remained about 2.9k tokens in that experiment. This is an observed baseline, not a proven universal floor. |
| Tool-schema trimming | 2026-10-01 | Full schema 19/27 solves; two short-schema builds 17/27 and 16/27. Nested-args calls increased; schema trim reverted. Do not reopen without a distinct hypothesis and paid call-shape gate. |
| Deferred/resident tools | 2026-08-29–09-01 | Deferral, hybrid residency, and later adoption checks were already tried. Current `TestToolSchemas_NoResidency` guards against residency; the code records later evidence beyond August 29. Retain the current policy. |[Current guard](../internal/mcp/searchmulti_test.go). |
| Duplicate evidence and re-delivery | 2026-08-29; 2026-09-30 | Historical overlap estimates: query ~4% of bytes, exact read re-delivery ~3.3%. Later forced30 study found re-reads of Prism-delivered code under 5% of reads. No broad dedup project. Existing delivery-cache code still exists; “dead lever” does not mean the implementation was removed. |
| Intent-sized answers / local discovery composition | 2026-10-02; 2026-10-11 | Absorbable search→read follow-ups ~0.6 turns/session. Batched impact candidate: calls 0.865x initially, then 1.13x and tokens 1.30x on replication; cost 1.20x on replication. Drop the proposed P1 composition work. |
| Search body policy and result budgets | 2026-10-05–11 | Body-selection study and 80-session comparison already performed; targeted correctness fixes shipped, blanket body suppression dropped. Impact rendering already has a 36,000-byte string budget. Historical tool-output estimate was ~2 characters/token, not 4; neither estimate is a billing meter. No specific untested budget lever was established, so remove the generic budget project. |[budget code](../internal/mcp/impactbudget.go). |
| Mechanical edits and edit commands | 2026-09-28 | Explicitly rejected: Prism answers only. Existing assist code does not authorize expanding the product across this boundary. Removed from the plan. |
| Build/test orchestration | Standing answer-only boundary | Belongs to the host/harness. No new Prism execution capability is proposed. |
| Compaction-aware dedup | 2026-10-12 long-session study | Study records zero compactions across 80 cells. No observed cost opportunity supports lifecycle-aware dedup work here; this does not prove compaction never matters in other workloads. |
| Accounting reconstruction | 2026-09-14; 2026-09-29; 2026-10-16 | Prior audits already verified token arithmetic; September 29 matched 60/60 runs. October 16 attribution already split cache categories. Reuse this work and persist it, rather than rebuild accounting. |
| Model choice | 2026-10-12 | Haiku+Prism **34/40, $2.40** versus Sonnet native **37/40, $4.38** (~55% cost). Sonnet+Prism **35/40, $4.08**. The note calls Prism neutral for Sonnet; these counts do not demonstrate quality equivalence. User-selected models remain unchanged; no routing/subagent proposal. |
| Batch discounts | Current Claude Code workflow | No Batch API execution path is established in these interactive CLI runners. Remove Batch as an implementation action for this plan. |
| Source-line evidence, distinct from writing edits | v0.87.27 record, three repetitions | Later `editSites`/line-partition experiment records 60/60 solves in both arms, 0.91x cost and 0.83x tokens; already shipped. This answer-only work is separate from the failed batched-discovery candidate and shows why “all richer answers fail” would be too broad. Treat its dated memory entry as historical reporting, not a new independently verified savings claim. |

## Five implementation items

**Correctness follow-up from this session:** a cached pointer can outlive the source in the agent's active context. Live calls reproduced two compact-gateway defects: `force:true` was discarded for single reads and rejected with batched ranges. The local repair forwards `force`, permits it with ranges, and gives cached range pointers an explicit recovery hint. Gateway regression tests cover repeated forced reads. This is a correctness repair, separate from the deferred dedup cost project; it does not add automatic host-compaction detection. Zero compactions in earlier benchmarks does not rule out this failure in other sessions.

### 1. DONE — Isolated cells and the startup/task split (the standard method)

**Found while running the TTL experiment (2026-10-17):** every benchmark cell shared the Claude Code static prefix (~12.8k tokens native, ~14.6k prism) through the API prompt cache, across arms, within the TTL. All past cost figures are therefore warm-regime task costs with the startup write silently excluded; resolve counts are unaffected. Every cell must be isolated and the cold startup cost reported apart from the native-vs-prism comparison.

**Standard method from now on** (`research/harness/runners/run_ttl.py --no-cache`): `DISABLE_PROMPT_CACHING=1` per cell so no cell can read another's cache; cost reconstructed per request at the 5m and 1h cache rates (matches the CLI bill within 0.02% on 67 cached cells); split into **startup** (first-request cache write) and **task** (everything after). The headline comparison is task-only. Startup is reported per arm beside it and is **not a target**. A nonce MCP tool cannot bust the prefix (deferred tools land after its cache breakpoint); timed gaps (`--cold-gap`) work but cost 6 minutes per cell.

**Result, seeded32 wide bed, Sonnet 5.5, v0.87.30, 32 pairs, 64/64 solved:** task cost prism/native **0.889** at the 5m rate ($1.78 vs $2.00; 95% bootstrap CI 0.79–1.00), 0.912 at 1h; prism cheaper in 19/32; 172 vs 216 model requests. Startup per session: native $0.061, prism $0.068 at 5m. Records: `research/harness/results/nocache32-2026-10-17/SUMMARY.md`.

The TTL counterfactual itself is settled by arithmetic (the 5m rate reprices the same traces for both arms) and is a Claude Code deployment setting outside this plan.

### 2. DONE — Persist per-request usage in results.json before scoring

Reuse the existing accounting and October 16 attribution method. The gap is durable request-level data and failure preservation, not evidence that all token accounting is wrong.

Persist request/message identity, model, uncached input, cache reads, cache writes split by TTL, output when available, and the authoritative aggregate cost. Preserve missing fields as unknown. Keep transcript references and a measurement-completeness marker; distinguish model requests from tool calls and reported turns.

Write the agent result before invoking the scorer. In [run_overnight.py](../../research/harness/runners/run_overnight.py), the broad exception handler at lines 98–99 returns only `resolved` and `error`, and summary construction at lines 153–166 drops richer usage. Scorer failures must retain completed agent spend and session identity. Preserve known retry spend as well.

**Acceptance:** offline fixtures reproduce existing aggregate totals and TTL splits; an injected scorer failure leaves usage intact. No paid run is needed to validate persistence behavior.

### 3. DONE — Fix arm ordering (and retire the overnight runner for cost)

The reviewed [overnight runner](../../research/harness/runners/run_overnight.py) runs native first and Prism second at lines 141–142. The main e2e path already has interleaving/warm-up support; reuse that approach rather than rebuilding it.

Use a saved seed to balance/randomize native and Prism order, and preserve that order in results. Record warm/cold cache policy, actual TTL, binary/model versions, and warm-up expenditure. Include installed-but-unused Prism sessions because their overhead still counts.

**Acceptance:** offline schedule checks show balanced reproducible ordering; summaries use the same complete pair set for each comparative metric, explicitly report missing values, and retain all known failed-attempt expenditure in operational totals.

### 4. P1 — Apply the three-arm quality-and-cost gate (stop rule now in run_ttl.py, on task-only cost)

For a future substantive Prism candidate, compare **native, current Prism, and candidate Prism** on the same selected model and deployment settings. A TTL-only experiment does not need a fictitious third Prism build: compare the two hosts under both TTL conditions.

Before any paid work: confirm bug closure and required offline correctness checks, prepare the exact manifest/configuration and spend limit, and obtain the user's go-ahead for that run. Every subsequent paid rerun requires its own go-ahead.

Use the documented stop rule:

- Stop on a **resolve loss**: the reference solves a paired task and the tested arm does not.
- Stop on **any cell error**, including scoring/infrastructure errors; retain consumed spend.
- Stop when cumulative matched **cost exceeds 1.25x after at least 10 complete pairs**.
- **Do not stop on token ratios.** Report tokens and request counts diagnostically.

For three-arm runs, predeclare comparisons against both native and current Prism and apply these rules to the candidate. For TTL trials, the reference is the corresponding host's one-hour condition. Investigate a stop before considering a separately approved rerun; do not silently discard the cell.

Report raw spend, resolves, missing measurements, and:

```text
Total expenditure across all attempts and recovery
/ correctly completed tasks
```

Use repeated paired trials when justified by the approved budget; keep issue, wide-change, and long-session results separate before combining them. A cheaper failed run does not satisfy the user's quality constraint.

**Acceptance:** no unresolved quality regression; complete, comparable cost data; gains survive replication within the agreed budget.

### 5. P3 — Pricing maintenance

[pricing.py](../../research/harness/lib/pricing.py) currently has a GPT-5.5 table and no Claude table; add a versioned Claude table only if needed for TTL counterfactuals, retaining Claude CLI cost as the recorded estimate.

## Additional routing improvement — disclose the project boundary

State the configured project root once at MCP initialization, and give actionable guidance in outside-root errors. Keep this separate from the five cost-work items: its primary justification is correct routing and fewer predictable failures; net cost savings are unmeasured.

Suggested initialization text:

> Scope: This server serves `<project-root>` only. File reads and text search can access files without indexed symbols; graph operations use this project's index. For another repository, run the Prism CLI from that repository or use its own MCP connection.

Substitute the actual root. Outside-root errors should name that root and explain the same recovery route. This is routing guidance, not generated edit commands. Do not expand the tool schema or repeat the scope explanation in every successful response.

**Verified in this session:** the Prism-rooted MCP connection rejected `../grove/go.mod`; the Prism CLI successfully read Grove's `go.mod` when run from the Grove repository. Grove having an index does not make it accessible through the Prism-rooted connection. File access boundaries and graph-index coverage are separate concerns.

**Expected cost effect:** probably a small benefit in sessions spanning repositories, with little benefit in ordinary single-project sessions. An avoided failed call can eliminate a model request and associated reasoning/output tokens, though not necessarily a user-visible turn. The added instruction also consumes input tokens. Measure net dollars before claiming savings; no paid experiment is authorized by this addition.

**Acceptance:** initialization identifies the configured root, outside-root errors provide the correct recovery route, and the wording distinguishes readable files from indexed symbols. Validate these behaviors offline; retain the existing tool schema.

## Sequence and conclusion

**Done: usage persistence, arm order, isolated cells, startup/task split, wide-bed result. Next: the issue bed under the same method, on explicit approval.**

There is currently no verified, untried Prism implementation change in this review that justifies promising additional cost savings without quality loss. Existing evidence argues against reopening startup trimming, generic deduplication, or larger composite answers without a genuinely new hypothesis.

This revision changes the plan and preserves its audit artifact. It does not implement harness changes, alter the selected model, change Prism's tool surface, or launch paid benchmarks.

## Evidence notes

The prior-work rows cite the project's session notes by date; they are records of earlier work, not independently reproduced experiments in this revision. The run scripts and JSON were used to verify the campaign identity and totals directly.


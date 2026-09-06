# Accuracy and Token Efficiency: Implementation Checkpoint

Date: 2026-09-06

Status: Product changes are committed on `cand-search-context-clean`; not released.
The earlier milestones below are a chronological record. Their references to
uncommitted work, original branches, and temporary paths describe that stage,
not the current integration state. See [the separation record](accuracy-efficiency-candidate/README.md).
The proposal's product-level accuracy and cost targets have NOT been demonstrated.

Companion: [original proposal](ACCURACY_AND_TOKEN_EFFICIENCY_PROPOSAL.md).

## 1. Deliverables and Isolation

Prism base: `a1c7aa6`. Research base: `b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8`.

- [Prism patch](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/prism.patch): seven source/test files.
- [Research harness patch](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/research.patch): seven source/test files.
- [Impact and completeness checks](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/impact-checks.txt).
- [Validation summary](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/validation.json).
- Prism working copy: `/Users/tapabratapal/Projects/provasign/prism`.
- Harness working copy: `/Users/tapabratapal/Projects/provasign/research`.
- Original isolated copies remain under `/private/tmp/prism-improvement.s40slA/`.
- Candidate binary: `/private/tmp/prism-improvement.s40slA/prism-candidate`.

Candidate binary SHA-256:
`0d6c0e9048b6a07c592bcb4c35d42720354b52965417e7cf57c33c94081cdaa2`.

After the user confirmed Claude was finished, both patches were applied to the
working checkouts without conflicts. Existing instruction/config edits,
`harness/mason_bench.py` changes, benchmark artifacts, and shared corpus checkouts
were preserved. The installed Prism binary was not replaced. No paid agent
benchmark was launched. No commit, tag, or push was made.

The patches preserve the context=2 default and earlier exhaustive-search fixes
already present in the Prism base, and build on the measurement changes already
present in the research base. Those earlier changes are not attributed to this
implementation.

## 2. Search Correctness

Implemented:

- Every search response identifies the actual repository root, once per batch.
- Mixed symbol/text searches retain BOTH passes' truncation state. Text timeouts
  and rejected paths survive even when the text pass returned zero hits.
- Incomplete, failed, rejected-scope, and term-capped searches no longer receive
  the global all-empty completion guidance.
- Omitted query terms are listed explicitly as not searched.
- Complete-empty guidance limits its statement to the requested scope and says
  excluded files and unindexed symbols are not covered.
- Symbol fetch growth stops at the configured work bound without overshooting
  it or overflowing a caller-supplied limit.
- Reaching a scan bound without knowing the remaining in-scope count reports
  that the count is unknown, rather than inventing a lower bound.
- Invalid search scopes return an error. Excessive positive result limits are
  clamped with a note.
- Root and partial-result warnings survive repeated-response note compression.

Why this matters: a short result is only a win when an agent can distinguish
a complete result from a partial one. An authoritative-looking partial result
can lower cost by making the agent stop too early, which is not product success.

Not implemented here: a full index-freshness contract, exhaustive pagination,
a byte/token budget over the whole serialized batch, or complete graph-edge
resolution. The existing 2,000-symbol exhaustive cap still exists and is stated.
This milestone does not make an arbitrary exhaustive request unbounded.

## 3. Context Delivery

The CLI search command now uses the existing MCP merged renderer directly,
avoiding the old CLI's repeated paths and overlapping context blocks.
Explicit `--context 0` is passed through instead of silently falling back to
the new default of two context lines. Invalid context arguments fail clearly.

A deterministic regression fixture preserves the same path, line numbers,
matched text, and surrounding source:

| Renderer | UTF-8 bytes |
| --- | ---: |
| Legacy CLI renderer | 581 |
| Shared merged renderer | 118 |

That is **79.7% fewer output bytes on this fixture**. It is not a measured
79.7% token saving, nor an estimate of whole-session savings. Search root and
completeness metadata intentionally add some bytes on other result shapes.

The mechanism is tested; the behavioral hypothesis still needs a model study:
does immediate, non-repeated source context actually displace later host Reads
and shorten successful tasks?

## 4. Measurement Integrity

### Usage Accounting

- Claude uncached input, cache creation, cache reads, and output are retained
  separately. Request-input totals include all three input categories.
- Missing, negative, non-finite, or invalid counters/costs remain unknown.
- The final CLI result supplies aggregate output usage and CLI-reported cost.
  Its cost is a client-side estimate, not authoritative provider billing.
- Transcript assistant messages are deduplicated by message ID. Later streamed
  usage updates replace earlier counters rather than adding duplicate copies.
- Tool calls/results are attributed by tool-use ID, with UTF-8 byte counting.
- Transcript step output is kept as an observation, not silently substituted for
  authoritative final output. Missing identity or usage is flagged.
- Mode-A cells retain raw CLI results, normalized usage, parsed answers, and
  explicit measurement/provider-error status.

### Read-After-Locate Diagnostic

- Classification is frozen before a read receives its own result.
- Repeated stream events are not counted as additional reads or turns.
- Delivered search context is recognized alongside query/lookup/read windows.
- A partial source window cannot prove that an unbounded whole-file read was
  redundant.
- Edits and compaction invalidate old source coverage. Bash conservatively
  invalidates prior coverage because its mutations are not fully observable.
- File matching uses path-component boundaries and rejects ambiguous matches.
- Legacy transcript fingerprint lookup accepts only a unique match.

This remains a diagnostic based on rendered text, not a provider billing
attribution system. Structured provenance and file-version identities are still
needed to replace heuristics. Conservative invalidation can undercount redundant
reads; that is preferable to manufacturing a token-saving explanation.

### Haiku Release Gate

- MCP config files are process-local rather than shared fixed filenames.
- Each cell attempt archives its pinned corpus into a fresh Git repository and
  indexes that snapshot. It never checks out or indexes the shared corpus.
- Cache identity includes the task/ground truth, resolved corpus commit, model
  argument, CLI version, binary SHA-256, steering, config, and relevant source
  hashes.
- Actual attempts are written to uniquely named, immutable evidence files before
  updating the canonical record. Degenerate attempts are preserved too.
- Candidate retries require a fresh cell. All recorded attempt costs and
  request-input tokens contribute to the comparison.
- Missing usage, failed baselines, missing corpora, or incomplete scored pairs
  produce a harness error instead of PASS.
- The gate explicitly states that candidate-only quality retries are an
  operational rule, not a significance test.

The gate still compares released Prism with candidate Prism, not native tools.
Its final-attempt quality selection, small sample size, model aliases, and
cheapest-first fail-fast ordering make it unsuitable for proving product
superiority. Retaining attempts makes selection visible; it does not remove it.

## 5. Verification Performed

All checks below were run against the isolated changes:

| Check | Result |
| --- | --- |
| Full Go suite, `go test ./... -count=1` | Pass |
| Changed packages, `go test ./internal/mcp ./internal/cli -race -count=1` | Pass |
| Harness `pytest harness/tests` | 44 passed |
| `git diff --check`, both checkouts | Pass |
| `prism verify --base HEAD --format text`, both checkouts | No missed sites; 7 changed files each |
| Candidate CLI vs `rg -F`, exact file/line/text equality | Pass |

The CLI parity probe used the same root and `harness/ab_gate.py` in both tools:

| Query | rg hits | Candidate hits |
| --- | ---: | ---: |
| MEAN_RECALL_DROP | 3 | 3 |
| require-cheaper | 4 | 4 |
| not-broken | 1 | 1 |

This establishes parity for these inputs, not universal search correctness.
Prism verification checks diff coverage, not behavioral correctness; it
supplements, rather than replaces, tests. Python impact lookup returned broad
name-collision sets in some cases, so it was not treated as proof of exact
cross-module resolution.

To rerun the free checks:

```sh
cd /private/tmp/prism-improvement.s40slA/prism
GOFLAGS= GOWORK=off go test ./... -count=1
go test ./internal/mcp ./internal/cli -race -count=1

cd /private/tmp/prism-improvement.s40slA/research
python3 -B -m pytest -p no:asyncio -p no:pytest_asyncio harness/tests -q

python3 -B /private/tmp/prism-improvement.s40slA/search_probe.py
```

## 6. Remaining Work and Acceptance

This is an implemented foundation, not the whole proposal. The next milestones
remain gated:

1. Review and commit the integrated changes. The working-tree integration is
   complete; the product goals below are not. Provasign intent/check tooling
   and its CLI are unavailable in this environment, so no certified admission
   or commit was performed.
2. Extend the response contract with source freshness, version-bound reusable
   evidence, bounded whole-response delivery, and actionable continuation.
3. Add task-specific semantic validation to wide changes. Touching the right
   files/regions or compiling does not establish behavioral correctness.
4. Run a budget-capped, isolated native/released/candidate pilot with the same
   pinned model, task inputs, native-tool access, and comparable prompts.
   Randomize/balance arm order, preserve all trials, and separate infrastructure
   retries from quality outcomes. Disable cross-trial cache reuse for estimation.
5. Analyze per-task verified success, required-site recall, false-complete rate,
   full input/output tokens, reported dollar cost, and follow-up reads.
   Keep easy/hard task strata visible and use paired uncertainty estimates.
6. Expand to a held-out confirmation set before release/product claims.

The proposal's targets remain targets: at least 30% lower aggregate reported
model cost and 25% fewer total tokens, without sacrificing required-site recall,
and better verified completion on hard tasks than a competent native-tools
baseline. Neither a smaller fixture nor a passing Haiku release gate meets
those acceptance criteria.

The larger three-arm study remains pending. The user subsequently authorized
the narrow four-way pilot documented in section 10; its result is local
evidence, not confirmation of the product-wide acceptance targets.

## 7. Working-Tree Integration

Both checkouts were still at the recorded base commits when integration began;
neither patch overlapped existing edits. The complete Go suite and all 44
harness tests were rerun successfully on the working checkouts. Race checks
for the MCP and CLI packages also passed with `GOFLAGS= GOWORK=off`. Both
`git diff --check` runs passed. Prism completeness checks reported no missed
sites across 19 changed Prism files and eight research files; those counts
include pre-existing user changes, not just this implementation.

New source/test files are marked intent-to-add so diff checks include them;
their contents have not been staged. The earlier isolated-checkout evidence
and patch artifacts remain available above.

## 8. Analyzer Review Follow-Up

Claude's review identified two reproduced diagnostic problems and one unproven
compatibility concern. The fixes are applied to the research working tree,
still uncommitted. The updated harness suite has **49 passing tests**;
`git diff --check` and `prism verify` pass. No paid run was needed.

- Session transcripts normally have no CLI stdout `result` event. The analyzer
  now reports `aggregate_status=not_present_in_transcript`,
  `usage_complete=null`, and separate `diagnostics_complete`. It retains unknown
  aggregate output instead of promoting per-step placeholders, without labeling
  valid transcript diagnostics as erroneous. The CLI gate's usage check remains
  a boolean and is unchanged.
- Bash no longer asserts that every file was edited. It invalidates coverage
  conservatively and reports freshness as unknown. Successful Edit/Write
  results establish edits; failed edit attempts do not. An independent
  `located_then_read` metric retains the location signal regardless of freshness.
  Shell scripts may alter paths absent from their command line, and today's
  filesystem mtimes cannot reconstruct historical changes.
- All four aggregate token fields were present in **100 saved raw CLI usage
  records** across three benchmark families. Another 411 inspected records did
  not retain raw usage, so they cannot establish key presence. No zero-cache
  aggregate was found in this sample: omission-at-zero remains unproven, not
  ruled out for every CLI version. A new regression test accepts explicit zero
  cache counters while keeping missing counters unknown; the strict rule was
  not weakened on speculation.

Replay of the two transcripts cited in the review:

| Cell | Located reads, before -> after | Confirmed rereads after | Unknown freshness after |
| --- | ---: | ---: | ---: |
| grove b40b72d94e, v0.71.1 | 4 -> 8 | 2 | 4 |
| prism 98ed1b2b4d, v0.71.1 | 1 -> 10 | 14 | 10 |

Both now have valid diagnostics and no missing-aggregate warning. Input totals
and unique-message counts remain unchanged (65 and 194). None of the uncertain
reads is credited as redundant. See [before/after evidence](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/analyzer-review.json).

The source distinction follows the [Claude SDK usage documentation](https://code.claude.com/docs/en/agent-sdk/cost-tracking):
per-step output counters may be placeholders; final output comes from the
result. That documentation also distinguishes SDK/CLI cost estimates from
provider billing. The benchmark objective is lower reported model cost, not a
claim that these local estimates have been reconciled to an invoice.

## 9. Per-Invocation Accounting

The initial `attempt_total` behavior was cumulative across every stored attempt
under a manifest. That is retained for historical auditing only; it is no
longer the gate's primary cost picture or efficiency-comparison numerator.

Each invocation now has a unique ID and a record in `out/invocations/`.
It records newly incurred cost and tokens, including current-run retries,
after each attempt and on fail-fast/error exits. Cached cells explicitly show
`reused` and zero new spend. Unknown aggregate usage remains unknown, including
an agent timeout that returns no final usage. Repeated visits to the same
attempt do not duplicate spend.

Efficiency comparisons use the attempts belonging to each selected measurement's
originating invocation, labeled separately from newly incurred spend. Thus a
cached baseline is not treated as a zero-cost answer, and an earlier invocation's
attempts do not inflate a fresh candidate measurement. `--fresh` reruns both
arms for a new pair. Legacy measurements without an invocation identity cannot
silently count as known zero-cost measurements.

Validation: **54 harness tests pass**, including separate-invocation resets,
cached zero-new-spend behavior, retry inclusion, historical unknown-cost
isolation, failure-path persistence, and a regression in which a candidate's
$100 historical spend cannot change a new $0.50 vs $1.00 comparison.
`git diff --check` and the research `prism verify` pass. These changes remain
uncommitted and do not alter the four-way experiment's raw records.

## 10. Four-Way Pilot

On one 22-site Jackson impact task, Sonnet native found 18 sites and Sonnet
with current Prism found all 22, using 79.0% fewer reported tokens and 62.4%
less estimated model cost. Both Codex cells found all 22; the properly configured
Prism cell used 68.5% fewer tokens and 59.3% less estimated cost.

One Codex MCP-permission setup failure required a replacement execution.
Its cost is retained: all five attempts total $2.3848 estimated model cost.
Charging that setup failure makes Codex + Prism more expensive in this
experiment, even though its corrected cell was cheaper. This was not a
quality-based retry. Claude auxiliary usage is included in audited token totals.

See the [four-way report](accuracy-efficiency-candidate/four-way-2026-09-06/REPORT.md)
for the per-cell table, cost definitions, raw evidence, cache/host limitations,
and why this does not establish an old/new Prism improvement or broad superiority.

## 11. Three-Task Four-Way Panel

The authorized 24-cell panel completed for $4.0132298 estimated model cost,
including every execution in this invocation and none from the earlier pilot.
Three tasks, two repeats, and four arms were frozen before outcomes. No
candidate changes or selective reruns occurred during the panel.

The result is **inconclusive, not a readiness pass**: one Sonnet treatment
execution never used its available Prism tools, leaving 23 arm-valid cells.
That is treatment non-use, not an MCP setup failure. Its $0.024814 cost and
correct answer remain visible but cannot be credited to Prism delivery.

All 11 valid Prism executions achieved full recall and precision. Native
Sonnet missed one TypeORM site while claiming completeness. TypeORM token
savings across two repeats were 79.2% for Sonnet and 36.1% for Codex. However,
Codex used 68.0% more tokens on Gin and 90.1% more on Django. Its six valid
pairs fail both efficiency thresholds: median paired token saving -54.0%
(target +25%) and summed estimated cost saving 19.2% (target 30%).

The next targets are evidence-backed reduction of repeated verification,
cheaper simple-task delivery, clearer parameter contracts, and an explicit
non-use measurement policy. The experiment supports pursuing those targets,
not declaring the product goal achieved. See the [report and all raw cells](accuracy-efficiency-candidate/panel-2026-09-06/REPORT.md).
All 24 saved answers and usage totals were independently replayed; 11 new
offline audit/analysis tests pass. Existing implementation checks above remain
separate from this model experiment. No product code changed during the panel.

## 12. Evidence Delivery And Batched Reads

A subsequent candidate addresses the panel's repeated reads and simple-task
overhead: impact text retains signatures/test labels and adds bounded matching
call expressions; lookup can fetch up to ten named methods together. Heuristic
receiver uncertainty, missing evidence, oversized batches, and lookup errors
stay explicit. No impact sites are removed, and no later verification call is
blocked. This step does not modify the frozen 24-cell panel.

Free pinned-corpus probes preserve every ordered impact site in Gin, Django,
and TypeORM. Django now supplies all seven caller expressions, and TypeORM all
25 callers have source evidence. Gin's three scalar lookup bodies are reproduced
by one batch. The responses are larger, so these checks establish evidence
preservation and fewer possible tool calls, not lower autonomous-session tokens.

Seven new Go tests and the final full race-enabled suite pass. Model spend for
this step is zero. The paid before/after comparison remains pending approval.
See the [candidate, bounds, raw probes, and measurement protocol](accuracy-efficiency-candidate/evidence-delivery-2026-09-06/README.md).

## 13. Codex Before/After Measurement

The user subsequently authorized the eight-cell comparison of the frozen
panel Prism binary against the evidence-delivery candidate: Gin and Django,
two repeats each, Codex GPT-5.5 at medium effort. All eight executions were
arm-valid and achieved full recall and precision. Every candidate pair used
fewer tokens and cost less, but the preset efficiency bar was not met:
15.3% median paired token saving versus a 25% target, and 27.7% aggregate
estimated cost saving versus 30%. Aggregate token saving was 28.9%; it does
not substitute for the task-balanced median criterion.

Current-invocation spend was $1.206159 estimated, with no setup failures,
timeouts, non-use cells, or retries. The raw answers and usage of all eight
executions replay exactly, and six offline comparison tests pass. No product
code or experimental rules changed during execution.

Batched lookup was adopted in both Gin repeats; Django post-impact lookups
fell to zero in both repeats. Post-impact searches persisted, and the candidate
incurred one unsupported search-parameter error in each Gin repeat. The next
targets are task-aware first-tool selection, honest coverage reconciliation,
and parameter-contract clarity. This small old/new Prism study does not
establish superiority over native tools or validate Sonnet behavior.

See the [complete report and all eight executions](accuracy-efficiency-candidate/evidence-delivery-2026-09-06/comparison/REPORT.md).

## 14. Routing And Follow-up Measurement

The completed implementation/evidence was committed as `ebda175`, and the related
research harness changes as `552aa94`. Follow-up `e855442` added direct known-name
routing, explicit search-label support, stable bounded rollups, honest coverage
wording, and canonical scoped text paths. Nine new product tests and the full Go
race suite passed. Both previously failing Gin search requests replayed through
MCP with identical results with/without the label, at zero model spend.

The next authorized study held that new binary fixed and changed only the routing
paragraph: eight fresh Codex cells, Gin and Django, two repeats. Nine new runner
tests, six existing analyzer tests, and an eight-cell model-free dry run passed
before launch. All eight model executions were valid, with 100% recall/precision
and no false-complete answers. The raw scoring and usage replayed exactly.

Results: 338,682 -> 288,583 tokens (14.8% aggregate saving), $0.595794 -> $0.468060
estimated arm cost (21.4% saving), and 11.1% median paired token saving. Both preset
efficiency targets still fail. This invocation spent $1.063854; no retries, setup
failures, non-use, or unknown usage were excluded. One ordinary lookup argument
error is included. No product source or frozen experimental rule changed mid-run.

Searches after impact fell 4 -> 0 and native rescans 2 -> 0, but post-impact
lookups remained 7 -> 7. Across two repeats, Gin saved 31.1% of tokens; Django used
15.5% more tokens despite costing 13.5% less. Fewer tool calls do not establish
fewer model requests or lower token usage. The next implementation hypothesis is
per-item file-scoped batch lookup, preserving explicit ambiguity and omission,
so identical backend method names can be read together across files.

This is guidance-only evidence, not a native-control comparison. Do not compound
its percentages with the earlier binary comparison or claim broad savings.
See the [new report, protocol, and raw evidence](accuracy-efficiency-candidate/routing-2026-09-06/comparison/REPORT.md).

## 15. Commit And Evidence Separation

The original `ebda175` mixed 11 product files with 487 documentation/evidence
files (63,195 added lines overall). It must not be merged into the product's
main branch. A later deletion would leave that evidence in the merge history.

The replacement branch, `cand-search-context-clean`, starts at `a1c7aa6`:

- `e245668`: product-only equivalent of `ebda175`, 11 files, 851 added lines.
- `7169b4a`: product-only equivalent of `e855442`, 11 files, 321 added lines.
- Reports are a separate documentation-only commit. No experiment runner,
  raw transcript, patch, per-cell JSON, or checksum inventory is in this range.

At the separation checkpoint, all 18 distinct product source/test files were
byte-identical to `e855442`.
The full Go suite and affected-package race tests pass in the clean worktree.
Prism reports no missed sites, with the existing steering-string contract
manually reviewed through its two references and regression tests.
No behavior changed during this separation and no paid model run was launched.

The [research archive](https://github.com/provasign/research/tree/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06)
retains all 620 copied Prism evidence/report files and 18 older research records
cited by the proposal. Its manifest hashes all 638 files; all 594 original
artifact checksum checks pass. The 26 existing offline tests pass after the
move. The relocation-aware verifier reproduces the 40-cell summaries and
rechecks raw answers and usage for the 16 before/after executions.

The source branches and their uncommitted edits remain untouched. These new
branches have not been pushed or merged; publish the research branch before
the product report links need to resolve on GitHub. This is a packaging fix,
not additional evidence of accuracy or token savings.

## 16. Exact File-Scoped Batch Lookup

Implemented as `71a509b` on the clean branch. `prism_lookup` accepts
`name=[{"name":"Type.method","file":"path/to/file"}]`, including mixed batches
with legacy strings. Objects require exact repo-relative file scopes; existing
scalar/string batches retain their soft hints. The exact-file path bypasses the
global lookup candidate cap, rejects invalid scopes, and never replaces a miss
with another receiver/file. Ambiguity and scoped omission identities remain
visible within the existing ten-item / 64 KiB record limits.

Ten new Go tests and the full race-enabled suite pass. The model-free Django MCP
replay delivers the recorded five-call sequence in one batch: eight valid bodies
preserved verbatim, plus two explicit wrong-file/type misses. The whole impact
response and all legacy lookup responses remain identical. Response text falls
14,212 -> 13,519 UTF-8 bytes, while the lookup schema grows 979 -> 1,375 bytes.
Those are byte/capability measurements, not model-token savings or agent accuracy.

Prism completeness verification passes for the four product/test files; three
probe tests pass. Both probe-harness failures are retained in research, and no
product source changed between replays. Model executions and spend were zero.
The original evidence archive remains unchanged and verifies successfully.

See the [scope contract, replay, and remaining proof](accuracy-efficiency-candidate/scoped-lookup-2026-09-06/REPORT.md).
The subsequent frozen before/after comparison is recorded below. Efficiency
targets remain unmet, and native Sonnet/Codex controls are still required.
Freshness/continuations, per-edge provenance, and stronger completion validation
remain outstanding work.

## 17. Scoped Lookup Model Comparison: Efficiency Failure

Eight fresh Codex cells, Gin and Django with two repeats each, fixed model,
effort, task snapshots, scoring and guidance; only the Prism binary changed.
All eight were valid, with no setup failures, retries or exclusions. Both arms
scored 100% exact-site recall and precision, with zero false-complete answers.

- Tokens: 250,078 -> 273,667, **9.4% more** with the candidate.
- Median paired token change: **11.5% more**, not the required 25% saving.
- Estimated cost: $0.494915 -> $0.523492, **5.8% more**, not the required 30% saving.
- Total spend for this invocation: **$1.018407**, fixed-rate API equivalent.
- Post-impact lookups: 5 -> 3; searches: 3 -> 5. Total tool calls: 19 -> 18.

Scoped batching was adopted in both Django candidate cells, with eight identities
per batch and no scoped errors/misses/omissions. One repeat replaced five
lookups with one; the other added body retrieval before a search that sufficed
without it in the before arm. Gin consumed more tokens in both repeats.
Capability and exact scope are proven on these fixtures; autonomous efficiency
is not. Do not pool these percentages with earlier studies or call this a
native-control comparison. Quality here measures site sets, not working patches.

Next priority remains context sufficiency: classify actual evidence gaps before
reducing follow-ups, distinguish enumeration from body-needed work, and add
free fixtures for those modes. Investigate the Gin declarations-only `closed`
impact result versus observed interface-call sites before stronger stopping
guidance. Preserve full recovery and uncertainty reporting; do not hide
required sites or label all post-impact searches redundant.

All eight raw answers and usage totals replay, all 111 artifact checksums pass,
and 27 offline tests pass. The original archive remains unchanged. See the
[full comparison and next gate](accuracy-efficiency-candidate/scoped-lookup-2026-09-06/comparison/REPORT.md).
Broad savings claims and release readiness remain unproven.

## 18. Go Coverage Boundary And Task Depth

Implemented as `cef7dad` on the clean branch. A minimal Go fixture reproduces
the Gin concern: an interface-dispatched call is present in source, while
Grove returns one declaration, no callers, and `closed`. Prism now converts
otherwise-closed Go method/interface impact results to `partial` with a
coverage warning. Returned site arrays are untouched. The warning survives
cached/full rendering, the closed-impact ledger excludes these results, and
wider-anchor hints cannot restore a stronger guarantee.

This is a conservative capability boundary, not an engine-recall fix. The
underlying Go interface/embedded-method edges are still missing; do not claim
they were recovered. Non-closed tiers and non-Go closed results are preserved.

Steering now distinguishes read-only body inspection from affected-site
enumeration: a known name alone does not require impact, and enumeration does
not require body retrieval by default. Edit/verify safeguards remain. This
template was not installed into existing agent configs, and no autonomous run
has measured its efficiency effect.

The free Gin/Django replay preserves all impact inventories and body responses.
Gin's two impact replies change only their tier and warning, adding 166 bytes
each. Django impact is unchanged and already includes all eight gold identities.
Four new tests (including eleven capability cases), updated routing/anchor tests,
and the full race suite pass. All 58 evidence checksum checks pass; a retained
sandbox-cache failure passed on the same-source permission-enabled rerun.
Prism's steering-constant review obligation was checked through its two
references and safeguard tests; no missed sites were reported.

Next: fix the embedded engine's Go interface coverage with negative controls
before restoring `closed`; separately measure task-depth routing under a new
frozen protocol. The latest paid efficiency failure remains the latest result.
See the [coverage reproduction and limits](accuracy-efficiency-candidate/impact-coverage-2026-09-06/REPORT.md).

## 19. Native Go Interface Caller Recovery

Grove `ff9adefb` on `fix-go-interface-impact` now resolves local possible
implementations of interface calls through `go/types` method sets, including
external embedded contracts and promoted local methods. Exact signatures and
source-file identities constrain the targets; invalid types do not establish
compatibility. This is separate from the unchanged heuristic matcher.

A fresh pinned Gin replay recovers `Context.Stream`: CloseNotify callers grow
0 -> 2 and Hijack callers 0 -> 3, with no previously returned sites lost.
Supers, family and declaringTypes remain empty. Prism keeps the `partial`
warning. Replies grow to include the missing evidence; no model run or session
token-saving measurement occurred. The latest paid efficiency failure stands.

Seven new tests, full Grove and dependency-replaced Prism race suites, and
Prism's four-file completeness check pass. The free replay's 117 artifact
hashes verify; exact source copies match the Grove commit. Evidence and all
retained attempts stay in research; Prism carries this report only.

The fix is committed in Grove, not shipped in Prism: its dependency manifest
still pins v0.43.1. Nothing was pushed, tagged, installed or merged into main.
Next: inherited contract/family representation and cross-package dispatch
tests, followed by frozen autonomous task-depth/cost comparisons. Do not
restore `closed` or prohibit recovery searches on the strength of this one
caller-recall repair. See the [engine replay and limits](accuracy-efficiency-candidate/go-interface-dispatch-2026-09-06/REPORT.md).

## 20. Inherited Go Interface Contracts

Grove `184f8a9e` adds exact local implements/overrides edges and synthetic
interface-member call anchors on top of `ff9adefb`. A fresh Gin replay shows
the concrete CloseNotify impact gaining its inherited `ResponseWriter` member
and declaring interface; the interface-rooted query gains that inherited
declaration while preserving its concrete family and two callers. No sites
disappear, and Prism continues to report `partial`.

The full Grove and dependency-replaced Prism race suites pass. The follow-up
tests cover interface roots and supers alongside the existing signature,
promotion, invalid-type and incremental controls. Prism reports no missed sites.
All 25 final replay hashes verify, and six archived source files match the Grove
commit. Evidence is isolated in research commit `030ae78`; Prism stores only
the report.

No model ran, no spend occurred, and no autonomous token/accuracy result is
claimed. Cross-package project-source loading failed its exploratory test and
was not papered over with name matching; it remains the next engine task.
Generic interfaces remain a boundary. The feature branches are published, but
Grove is untagged and Prism still pins v0.43.1. See the
[family replay and limits](accuracy-efficiency-candidate/go-interface-family-2026-09-06/REPORT.md).

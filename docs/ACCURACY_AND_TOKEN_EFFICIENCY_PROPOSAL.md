# Prism Accuracy and Token Efficiency Proposal

Date: 2026-09-06

Status: Proposal; targets below are not achieved results

This is the original proposal, retained as a historical design document.
Implementation and experiment outcomes are recorded in the
[implementation checkpoint](ACCURACY_AND_TOKEN_EFFICIENCY_IMPLEMENTATION.md).
Raw evidence is versioned separately in research; the product keeps reports.

Code reviewed: Prism `0a85d47` (latest functional commit `36e66d7`); research harness `3b3fa18`

Scope: retrieval, context delivery, completeness verification, agent integration, and measurement

## 1. Product Decision

Prism should reduce the cost of completing a correct engineering task. A smaller search response is useful only if it preserves the evidence needed to finish and avoids more expensive follow-up work. A high-recall graph is useful only if its evidence reaches the agent and produces a correct result.

The proposed product objective is:

> Deliver enough trustworthy evidence to finish correctly in fewer model requests, with substantially lower total token consumption and cost than a competent agent using native search and reads.

The primary comparator must be the same agent without Prism, using efficient `rg`, batched searches, contextual results, and ranged reads. Comparing candidate Prism against released Prism answers a different question: whether a release regresses.

Priorities, in order:

1. Make root, freshness, partial results, and unresolved relationships explicit. An incomplete result must never imply completeness.
2. Make common Prism responses sufficient to avoid another exploration turn. Reduce unnecessary whole-file reads and early oversized inventories.
3. Preserve required implementation families and caller evidence while compressing redundant representation.
4. Verify behavioral completion, including effects outside compiled code, without creating a noisy second exploration loop.
5. Establish repeatable gains against native tooling on a held-out workload before claiming superiority.

Keep the existing deterministic, named tools. The retired task compiler demonstrates why a new natural-language router is not the starting point; its retirement and surviving anchored operations are documented in [DESIGN_TASK_COMPILER.md](DESIGN_TASK_COMPILER.md).

## 2. Evidence and Its Limits

### 2.1 The historical cost gap is substantial

Recomputed from eight stored `*.sonnet.*.v070sample.json` pairs in [the wide-run directory](https://github.com/provasign/research/tree/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/legacy-references/harness/runs/wide):

| Measure | Native baseline | Prism v0.70.0 |
| --- | ---: | ---: |
| Paired tasks | 8 | 8 |
| Sum of CLI-reported cost | $15.9769 | $25.7249 |
| Mean cost | $1.9971 | $3.2156 |
| Cost ratio | 1.000 | 1.610 |
| Tasks on which Prism was cheaper | N/A | 0 of 8 |

These are historical observations, not estimates of current main. The associated old file-recall numbers are not proof of patch correctness and must not be mixed with newer site scores.

For scale: reaching 30% below this historical baseline would mean about $1.398 per task. That requires about a 56.5% reduction from the historical Prism mean. Small formatting savings alone cannot close that gap.

[BACKLOG.md, section 21](../BACKLOG.md#21-token-cost-decomposed-v070sample-2026-09-05--the-handle) reports a separate reconstruction over seven transcript pairs: 77.3M versus 120.4M cumulative context tokens, a 43.1M increase. Its reported attribution was:

| Component | Increase | Approximate share of reported increase |
| --- | ---: | ---: |
| Host Read payloads | 23.3M | 54% |
| Prism search payloads | 9.7M | 23% |
| Assistant text | 4.9M | 11% |
| Fixed prompt repeated across requests | 3.3M | 8% |
| Shell output | 2.6M | 6% |
| Loaded tool schemas | 2.0M | 5% |

The table omits smaller positive and negative components. These are the backlog's historical attribution estimates; they were not independently reconstructed from all raw transcripts for this proposal. They are neither unique payload tokens nor billed dollars. Cache effects, streaming duplication, and context compaction require separate accounting.

The same analysis reports 35 of 40 whole-file reads targeted files Prism had already located, and only 2 of 26 search calls requested surrounding context. This supports testing response sufficiency first. It does not prove that all later reads were unnecessary: locating a file is different from delivering its relevant body.

### 2.2 The Grafana failure illustrates the correctness tradeoff

The two saved `grafana-checkhealth-impact` gate answers were rescored offline with current Mode-A scorer v3:

| Measure | Released-Prism cell `06b72411` | Candidate cell `30db37f7` |
| --- | ---: | ---: |
| Required sites found | 41 / 41 | 30 / 41 |
| Recall | 1.0000 | 0.7317 |
| Precision under current scorer | 0.5775 | 0.9677 |
| F1 | 0.7321 | 0.8333 |
| Answer entries, including duplicates/tests | 91 | 39 |
| Unique answer strings | 75 | 39 |
| Claimed complete | Yes | Yes |
| Agent turns reported | 20 | 4 |
| CLI-reported cost | $0.2215 | $0.0583 |

Sources: [task ground truth](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/tasks/grafana-checkhealth-impact.json), [released-Prism record](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/legacy-references/harness/runs/ab-gate/grafana-checkhealth-impact.haiku.06b72411.s2.json), [candidate record](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/legacy-references/harness/runs/ab-gate/grafana-checkhealth-impact.haiku.30db37f7.s2.json). Stored records are v2; rescoring their saved answers reproduces the rounded reported metrics under v3. Run evidence is local and some artifacts may be untracked.

The candidate was cheaper and had higher F1, but omitted 11 required implementations while claiming completeness. That is unacceptable for an exhaustive impact task. Answer length alone is misleading: 39 is about 43% of 91, and neither number is the production-site denominator.

The code confirms that symbol search previously honored `limit=25` while ignoring `exhaustive=true`. This is an independently identifiable contract bug. A gate failure and a retry justify investigation and holding a release; they do not, by themselves, establish a statistical noise threshold or prove which commit caused an agent's behavior. The transcript and exact tool-response replay must connect the defective response to the missing evidence.

### 2.3 Fixes already present

| Commit | Implemented change | Remaining validation |
| --- | --- | --- |
| `32de5b5`, `56b3428` | Merge overlapping search context; render a file path once per context group | Preserve every distinct site; measure follow-up reads and whole-task cost |
| `8a95848` | Render verify missed sites that previously disappeared from text output | Test real tool-to-renderer paths and agent consumption |
| `183ae59` | Honor exhaustive symbol retrieval and flag sampled symbol results | Check all result forms, scopes, and boundary conditions |
| `ce10c92` | Normalize non-positive limits; bound exhaustive symbol delivery at 2,000 | A count bound does not guarantee a byte/token bound |
| `36e66d7` | Grow symbol fetch until scope filtering yields enough results or source exhaustion | Distinguish delivery cap, source scan limit, and unknown scoped totals |

The earlier scoped-fetch review is addressed in current code; this proposal does not treat it as an unchanged defect. The next improvement is a clear result contract and a source-level filtered/paged query, avoiding repeated globally ranked fetches.

### 2.4 Root confusion is both a product risk and an agent mistake

In the preceding session, Prism returned no matches for benchmark identifiers while an explicitly rooted `rg` search found them in the sibling `research/harness` directory. Those were not equivalent searches. One earlier MCP query also identified a temporary directory as its project root. The exact root of every later empty response was not established.

The agent should have checked the effective root before repeating searches or declaring absence. The product should make that check cheap and reliable. A result from one root cannot establish absence in another root, even if its text search is internally correct. Rejected paths and stale connections must remain visible, including after repeated-message compression.

### 2.5 The improved scorers still have limited meaning

Current [score.py](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/score.py) v3 rejects wrong-file matches, limits one answer site to one ground-truth site, and charges weak/pathless evidence against precision. [wide_score.py](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/wide_score.py) adds per-region coverage and false-edit regions. These are valuable improvements, but they do not establish semantic accuracy.

An offline probe against the current wide scorer used:

```diff
 # Gold:
-OldAPI()
+NewAPI()

 # Agent:
-OldAPI()
+WrongAPI()
```

The result was `site_recall=1.0`, `site_precision=1.0`, and `site_recall_exact=0.0`. The current headline measures substitution at the expected location; it cannot determine whether the replacement is right. Even the `exact` level accepts any matching added line, rather than proving that a whole required transformation is complete. Likewise, `files_complete` currently counts a file with any substituted site, not necessarily all its required sites.

Mode-A identities also discard receiver/signature information, and partial path suffix agreement needs ambiguity checks. PR-derived changed files are not automatically the complete set of valid solutions. Treat these metrics as localization/edit-coverage diagnostics, validate task ground truth independently, and use task-specific tests and contract checks for correctness claims.

The three scorer suites run for this document passed: Mode-A 11, wide 10, query oracle 2. Passing those tests is compatible with the limitations above.

## 3. Define "Way Better" Before Running the Study

The following are proposed acceptance targets against a competent native-tool baseline, using the same model and workload. They are intentionally demanding and must be preregistered before examining held-out outcomes.

| Dimension | Proposed product target | Guardrail |
| --- | --- | --- |
| Total reported model cost | Ratio <= 0.70; upper 95% interval <= 0.85 | Include failures and policy-required retries; report each model separately |
| Total input plus output tokens | Ratio <= 0.75; upper 95% interval <= 0.90 | Include cached input; avoid double-counting detail fields |
| Verified task success | Positive overall effect; lower interval for difference >= -2 percentage points | No confirmed deterministic correctness regression |
| Difficult multi-site tasks | At least +10 percentage points verified success, or >=25% relative failure reduction | Choose the applicable criterion before running; establish a positive effect |
| Exhaustive closed-world impact fixtures | 100% of independently labeled required sites | Zero false "complete" results caused by cap, root, or freshness errors |
| Strict site precision | No material decline; <=2 percentage-point non-inferiority margin | Examine ambiguous identities and valid alternative edits |
| Cost per verified success | Ratio <= 0.70 | Numerator includes spending on unsuccessful attempts |
| Simple lookups and latency | Median no worse; investigate >10% p95 regression | No universal extra exploration or verification turn |

Intervals should respect task/repository clustering. If the study cannot establish these claims, report it as inconclusive or release a narrower claim for the demonstrated workload. High-baseline-success slices have limited room for a ten-point improvement; use the preselected failure-reduction criterion there.

A release smoke test can enforce looser regression thresholds than the product study. Every individual release need not be cheaper than the immediately preceding binary; a necessary correctness fix may add evidence and cost. The product still needs to beat the native baseline under the joint accuracy/economics criteria.

## 4. Measure the Right Cost

### 4.1 Separate four quantities

1. **Delivered tokens:** newly emitted tool, instruction, and assistant content.
2. **Request input tokens:** all context presented to the model on each request, including cached input.
3. **Model-reported cost:** price-weighted input, cache, output, and other reported charges.
4. **Total task cost:** model cost plus separately reported indexing/compute, latency, and any measured review or recovery cost.

Report these separately. A provider cache hit lowers price without eliminating context tokens. A Prism cache pointer changes the payload; it does not establish that the model still has the earlier content. A local compression ratio is not a measured end-to-end saving.

For a normalized provider record with mutually exclusive token categories:

```text
request_input = uncached_input + cache_write_input + cache_read_input
request_cost  = uncached_input * input_rate
              + cache_write_input * write_rate
              + cache_read_input * read_rate
              + output * output_rate
task_cost     = sum(request_cost) + separately_accounted_charges
```

Use the actual model, API version, cache tier/TTL, and price basis. Where cache writes have multiple rates, preserve their subcategories. Do not add reasoning tokens twice if already included in output.

Claude exposes uncached input, cache creation, and cache read as separate input categories; their sum gives total input. Its cache categories have different pricing. [Claude prompt-caching documentation](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)

OpenAI input totals include cache detail categories. Normalize using the actual returned schema; do not add cached tokens on top of an inclusive input total. Stable request prefixes and cache diagnostics matter, but a Prism-only MCP server does not control the host's full prompt. [OpenAI prompt-caching documentation](https://developers.openai.com/api/docs/guides/prompt-caching)

Use final CLI/API aggregates as the primary recorded cost and output totals, with explicit provenance. Claude's documentation warns that shared message IDs can repeat usage and per-step output values can be placeholders. Deduplicate request/message usage and reconcile with final results. Label `total_cost_usd` as CLI-reported cost with its pricing basis, not an independently verified invoice. [Claude cost-tracking documentation](https://code.claude.com/docs/en/agent-sdk/cost-tracking)

### 4.2 Measure why each response costs money

For payload `j`, record its size `b_j` and whether it remains in request `t`:

```text
context_exposure(j) = sum_t b_j * present(j, t)
```

This is a diagnostic approximation unless the host supplies exact tokenization and retained-context boundaries. It must account for compaction and evictions. Do not multiply every historical payload by every later turn unconditionally, or call the result dollars.

An early 2,000-token inventory retained for 40 requests contributes about 80,000 input-token appearances under that simple model. Compressing it to 400 tokens reduces that exposure by 64,000. Actual savings depend on cache pricing and whether the compact inventory causes extra requests.

Evaluate each delivery change by:

```text
expected benefit = avoided follow-up context and requests
                 + avoided recovery work
                 - added evidence and metadata
                 - extra expansion requests
```

The largest opportunity is removing avoidable requests and broad rereads while retaining required evidence. Reducing punctuation and repeated notes is useful secondary work.

### 4.3 Fix diagnostic accounting before using it as a target

[usage_account.py](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/usage_account.py) preserves important categories. However, `ab_agentic_mcp.py` still defines its compatibility `tokens_in` as uncached plus cache-read tokens, and the gate prints a token delta from that incomplete field. Use the normalized categories for all new comparisons.

[read_after_locate.py](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/read_after_locate.py) is a useful starting point, but currently:

- A later read of a located file can be necessary; the file path alone does not establish redundant evidence.
- Its byte aggregation associates reads by filename after identifying a qualifying read, which can attribute other reads of that filename outside the qualifying window.
- Its counter advances per tool invocation despite describing a window in assistant turns.
- Its path regex requires a slash, missing repository-root files such as `main.go`.

Replace this diagnostic with tool-call IDs, canonical root/file identities, content hashes, delivered line intervals, and unique model-request IDs. Distinguish `located-only -> needed read`, `already-delivered -> duplicate read`, and `changed-file -> necessary reread`. Treat older counts as indicative until reconciled.

Measure actual UTF-8 bytes separately from string length and tokenizer counts. Add usage-parser fixtures for streamed duplicates, missing IDs, partial output usage, subagent totals, cache creation, and failed requests before treating reconstructed telemetry as a release metric.

## 5. Accuracy Architecture: Make Completeness Inspectable

### 5.1 Root and snapshot identity

Bind a server to a canonical root and stable root ID. Emit the root on first use, on errors/empty results, and whenever root identity is uncertain. In multi-repository work, require an explicit registered root selection or a separate server; do not silently widen or switch scope.

Every machine-readable result should carry a compact root/snapshot reference. The underlying snapshot must include index generation and dirty-file state, not just a Git commit. Reject a mismatched requested root before asserting anything about the repository. If a client cannot provide an expected root, disclose the actual one instead of inferring the user's intent.

Text, symbol, path, and glob searches must share documented scope semantics. Test relative roots, sibling repositories, worktrees, symlinks, ignored files, generated files, deleted files, and source not yet indexed. A text scan of the working tree and a stale graph search cannot be combined into a silently coherent result.

Implementation areas: [server.go](../internal/mcp/server.go), [tools.go](../internal/mcp/tools.go), [textsearch](../internal/textsearch), client initialization in [commands.go](../internal/cli/commands.go).

### 5.2 Separate result delivery from graph authority

Represent at least these independent facts:

- **Scan status:** did the requested search complete, time out, or reach a work limit?
- **Delivery status:** were all results delivered, or is this a page/sample/group inventory?
- **Graph coverage:** closed, project-local, callers-only, or unresolved, using existing semantics.
- **Freshness:** which index/source snapshot supports the result?

A finished text scan is not proof that every runtime caller was resolved. A complete graph result delivered only in part is not a complete answer in the agent's context.

Proposed response example, not an implemented API:

```text
root=research snapshot=r17 scope=harness/ mode=text
scan=complete delivery=complete returned=9 total=9
harness/ab_gate.py:
  54: MEAN_RECALL_DROP = 0.05
  ...
```

For bounded delivery:

```text
root=grafana snapshot=g42 mode=symbols
scan=complete delivery=partial returned=120 total=238 next=p2
```

If the source scan stops early, use `scan=partial total=unknown` or a proven lower bound. Do not claim "more than 2,000 scoped matches" merely because a global fetch limit was hit. The current hard-limit warning needs this distinction.

Keep warning/status fields at the start of the response and reserve space for them in the output budget. Once-per-session note compression may shorten explanatory prose, but must not remove current partiality, root mismatch, timeout, unresolved work, or stale evidence.

### 5.3 Exhaustive means no silent loss

`exhaustive=true` must preserve access to the complete matching set even when one response cannot contain it. Support a continuation or a lossless partitioned inventory. Every member must belong to exactly one recoverable page/group, with counts and stable ordering.

Prefer pushing path/glob filters and pagination into the graph query. Current repeated ranked fetches are a reasonable correctness repair but can rescan/sort the index repeatedly. A source API should expose scoped results and `has_more` without requiring a full exact count on every ordinary search.

Bound the final serialized response by measured bytes/tokens, including signatures, metadata, text matches, and all terms in a batch. A 2,000-symbol count bound alone is insufficient for long signatures or ten concurrent queries. Validate/clamp large positive limits before arithmetic as well as non-positive values.

Continuation must be bound to root, index generation, query, and filters. If edits invalidate that snapshot, return a refresh requirement. Expired state cannot become an empty successful page. Provide an artifact or resumable representation when the consumer must inspect a very large set; do not add a new model request for each trivial page unnecessarily.

### 5.4 Preserve identity and required relationships

Use canonical site identity internally: root, file, qualified symbol, receiver/type, signature where applicable, and source span. Method name or `file:line` alone can collapse overloads or multiple declarations on one line.

Separate required declarations, implementations, callers, external constraints, and optional context. Attach provenance for each required relationship and make uncertainty visible. Dynamic dispatch, reflection, generated code, and external dependencies must not inherit closed-world confidence without evidence.

Ranking and budgets may prioritize optional bodies. They must not silently prune the required site inventory. Compress common paths and repeated declarations while keeping every distinct required identity reachable.

Trace accuracy loss at each stage:

```text
independent required sites
  -> present in index/graph
  -> retrieved by the query
  -> represented in the rendered response
  -> available in the agent's current context
  -> selected in its answer/diff
  -> verified by task checks
```

This distinguishes an indexing miss from a render omission, stale pointer, routing mistake, premature stop, or wrong implementation. The lost-stage counts are more actionable than an overall recall delta.

Implementation areas: [Grove adapter](../internal/grove/client.go), [selection.go](../internal/mcp/selection.go), [delivery.go](../internal/mcp/delivery.go), [verify.go](../internal/mcp/verify.go). Prefer existing evidence tiers over a new confidence vocabulary.

## 6. Token Reduction: Make the Next Action Possible

### 6.1 Use a small set of predictable response shapes

| User need | Existing operation | Preferred payload |
| --- | --- | --- |
| Find a literal or config key | `prism_search(scope="text")` | Batched matches with requested context and truthful scope/status |
| Locate a symbol | `prism_search` | Qualified identities, spans, concise signatures; no full bodies |
| Read one named function/class | `prism_lookup` | Complete body or explicit continuation |
| Understand/edit a small neighborhood | `prism_query(terms=[...])` | Relevant source windows, required relationships, omitted-evidence inventory |
| Determine signature-change impact | `prism_change_impact` | Required family/caller set with provenance and completeness tier |
| Check a multi-site diff | `prism_verify` | Unresolved obligations and precise supporting windows |

Do not make every search return a broad graph expansion. Do not make every lookup invoke a second model. Preserve a cheap literal-search path close to native `rg` cost.

### 6.2 Optimize the search-to-read transition

Test small contextual windows for narrow matches and complete symbol bodies through lookup. Reuse overlapping source regions within a response. For broad searches, lead with a compact inventory and a bounded set of representative windows; explicitly distinguish representatives from the complete set.

For source returned as edit-ready context, include the facts an agent commonly seeks in its next read: exact signature, enclosing control flow, relevant type/receiver, nearby constraints, and direct caller evidence. Include only what the selected task and symbol justify; an entire class body is not a default substitute for a method.

Measure whether adding context prevents later reads and reduces total request count. An extra 300 tokens can be beneficial if it eliminates a request carrying 20,000 tokens of existing context. Automatically appending 300 tokens to every broad match can have the opposite result.

Start by testing the existing `context`, `lookup`, and `query` controls and response placement. Add an explicit response mode only if those cannot express the successful behavior. Avoid opaque natural-language intent classification in the retrieval path.

### 6.3 Keep inventories complete but compact

Reuse the existing bounded directory inventory and merged context rendering. Deduplicate by canonical evidence identity, not by name or repeated source text. Preserve module boundaries and counts so a compact view still exposes separate API, implementation, test, docs, config, and generated-code responsibilities.

Place a small summary and the required work before optional explanation. Do not compress identifiers into a new notation that the agent must decode, or replace source evidence with unsupported natural-language summaries. Measure the final rendered output with the appropriate tokenizer where available; do not use a universal characters-per-token conversion.

### 6.4 Make cached delivery recoverable

Prism's unchanged-file pointer can save payload, but the server cannot assume the host still retains the original content after compaction, a fork, or a restarted session. The host's provider prompt cache and Prism's delivery cache are separate mechanisms.

Tie delivery references to content hash, root/snapshot, session, and host context epoch where available. Provide an explicit full rehydrate path. If retention is unknown, return the needed compact window or let an explicit reread succeed. Preserve necessary warnings and changed-line evidence even when the rest is cached.

Test both false cache hits and false misses. Content-hash invalidation alone does not address a valid file body that is no longer in the model's context. Existing [graphcache.go](../internal/mcp/graphcache.go), [tracker.go](../internal/session/tracker.go), and context-distance heuristics are the starting point; do not build a parallel cache system.

### 6.5 Reduce routing overhead and extra turns

Keep tool schemas stable and defer infrequently used tools where the host supports it. Measure discovery requests, schema bytes, and routing success together. The backlog's residency and description experiments warn against assuming either always-loaded tools or shorter descriptions are universally better.

Batch known names. Avoid a mandatory preliminary call that exists only to acknowledge Prism. Use the narrow operation that answers the task; after a partial result, make the recovery action explicit enough to prevent speculative re-searching.

Host reads remain available. A Prism arm that wins only because useful native tools were disabled does not establish the intended product advantage. Test minimal, realistic installation guidance; report forced-Prism studies separately as mechanism experiments.

## 7. Verification Must Improve Completion

Run task-specific compilation/tests after changes and use Prism for obligations those checks may miss: compatible-but-stale callers, comments, documentation, config, cross-language consumers, and external-contract risks. The compiler is not a completeness oracle for all of these, and Prism is not a replacement for execution tests.

For removal work, inspect residual references once the removed identities are known, then check the final diff. For multi-site edits, report unsatisfied required obligations with concise evidence and a reason. A file being touched is insufficient to discharge all its obligations.

Avoid unconditional expensive verify loops on a single trivial read or unrelated edit. Cache computation by actual diff/snapshot where valid, aggregate common causes, and reevaluate only changed obligations. Retain full detail through explicit expansion.

Track whether verification catches a true omission, whether the agent corrects it, and how much the correction costs. Count false alarms and repeated already-resolved warnings. Improve cost per verified completion, not merely the number of warnings emitted.

## 8. Benchmark Design That Can Support the Claim

### 8.1 Use three explicit arms

| Arm | Purpose | Configuration |
| --- | --- | --- |
| N: Native baseline | Product comparator | Same agent with native search/reads/edits/tests; no Prism schemas or steering |
| R: Released Prism | Release comparator | Same native tools plus pinned released Prism and its documented integration |
| C: Candidate Prism | Treatment | Same native tools plus pinned candidate and recorded integration |

If prompt guidance, tool residency, or host configuration changes, record that as part of the treatment. Separate engine, rendering, and installation-guidance ablations when attributing gains.

The current [ab_gate.py](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/ab_gate.py) compares two Prism binaries. Its `--require-cheaper` option means cheaper than the selected released binary, not cheaper than N. Its inherited Mode-A tool permissions and explicit impact guidance differ from a general native-plus-Prism workflow. Its `arm_for` also sets `alwaysLoad=true`, so it does not reproduce a deferred-tool installation. Keep that diagnostic, but do not call it the product benchmark.

Although Mode-A records precision, the gate currently thresholds recall and cost. Add explicit precision/false-completeness reporting and the appropriate preselected guardrails rather than assuming a PASS certifies all recorded quality dimensions.

### 8.2 Keep Haiku as a sentinel

The nine-task Haiku gate is useful for quickly exposing broken results, errors, or large behavioral changes. It is a screening test. A reproduced failure merits a hold and investigation; one retry does not establish statistical significance, and the quoted approximately 50% cell variability is not a formal detector guarantee.

Preserve first attempts and retries as separate immutable records. The current gate deletes a failed candidate cache record and stores a retry in its place. Such selection can favor a candidate and hides the original attempt's cost if reused as product evidence. Candidate-only retries must not become the analysis policy for the held-out study.

Fail-fast ordering leaves later tasks unobserved. Report `completed / planned` and stop reason; do not publish an aggregate product claim from the cheaper prefix of a stopped run. Add known failure tasks to a deterministic regression bed regardless of their presence in a smaller CI corpus.

### 8.3 Establish task truth independently

Use a stratified workload: local lookup, small bug fix, cross-module signature change, wide refactor/removal, docs/config changes, dynamic or unresolved references, and multi-root/stale-index recovery. Cover supported Go, Java, Python, and TypeScript behavior where actual corpora and independent oracles exist. Report unsupported cases explicitly.

For impact tasks, preserve receivers/overloads and independently enumerate required sites. For edits, combine task tests, old-contract absence/new-contract checks, valid-reference checks, and review of unintended changes. Audit tasks with alternative valid solutions; a gold diff is evidence, not the only admissible implementation.

Keep localization, rendered-evidence coverage, edit-region coverage, exact transformation checks, and executable success as separate metrics. Report hallucinated sites, false completeness, and harmful extra edits. Do not rename a region-overlap proxy "accuracy."

Mutation fixtures should include wrong-directory same basename, wrong receiver, one of two nearby obligations, TODO replacement, incorrect API replacement, removed-then-reintroduced old code, unrelated additions, and partial file completion. Gold-vs-gold 100% is necessary but does not test false credit.

### 8.4 Preserve experiment identity and isolation

Each cell needs an immutable manifest containing:

- Task ID, task/prompt hash, ground-truth hash, corpus commit, and dirty-state digest.
- Prism and Grove versions/binary hashes, host version, resolved model identifier, effort/sampling settings, and time.
- Exact tool configuration, schema and steering hashes, allowed tools, root IDs, and index generations.
- Scorer identity/hash, usage-parser version, pricing basis, environment/toolchain fingerprint, and cache-state policy.
- Run/attempt/session IDs, raw final usage, tool events, full final answer/diff, validation output, outcome, and stop reason.

The current gate's cache key includes task name, model alias, binary hash, and Mode-A scorer version, but not all of these inputs. Separate acquisition caches from rescored results: score a preserved answer again without rerunning the model when the acquisition inputs are unchanged.

Use separate worktrees, per-run server configuration, index/cache namespaces, and output directories for concurrently active runs. Do not checkout a shared corpus under another agent. The per-binary wide-run configuration change helps; it is not sufficient isolation for all runners. Check checkout/index return codes before admitting a cell, and verify the tool's root handshake.

No malformed/missing usage silently becomes zero cost. Record token/model failures separately from infrastructure failures, retain their incurred costs, and preregister exclusions. Missing costs in only some pairs must make the economic comparison incomplete; the current positive-cost-only accumulation can otherwise use a different subset from recall.

### 8.5 Paired analysis and sample size

Randomize/counterbalance N/R/C order within task/model blocks. Preserve comparable cache conditions and report observed cache usage; do not assume a warm second run is a product improvement. Run repeated trials, record all of them, and freeze the analysis plan before the held-out evaluation.

For equal repetitions on a balanced task set, use total paired cost:

```text
cost_ratio = sum(candidate_cost over eligible paired cells)
           / sum(native_cost over the same paired cells)

cost_per_success(arm) = total_cost_of_all_planned_attempts(arm)
                      / number_of_verified_successes(arm)
```

If the success count is zero, cost per success is undefined/infinite, not zero. Report success rate and total spend beside it. A success-only cost average can hide expensive failures. For different deployment task frequencies, use preregistered workload weights and report both weighted and unweighted results.

Report per-task ratios, median and tail costs, absolute quality differences, and paired confidence intervals. Resample tasks/repositories as clusters rather than treating thousands of sites or repeated trials as independent tasks. Do not let one Grafana outlier determine the general conclusion. Do not pool models to hide a losing supported model.

Start with a development pilot, for example 12 distinct tasks x 2 repetitions x 3 arms = 72 cells on one pinned model. Use the resulting paired variance and failure rates to size a held-out study. A planning example is 48 tasks x 3 repetitions x 2 models x 3 arms = 864 cells, but that is a budget illustration, not a power calculation or an authorization to spend. Set a cost budget after the pilot and proceed only with a design capable of resolving the chosen margins.

Use separate development and held-out repositories or PR families where possible. Avoid tuning response shapes repeatedly against the same nine sentinel tasks and interpreting the final pass as generalization.

## 9. Implementation Sequence

| Stage | Concrete deliverable | Principal files/area | Exit criterion |
| --- | --- | --- | --- |
| 0. Measurement truth | Immutable attempts, normalized usage, manifest hashes, corrected read-after-locate diagnostic, semantic validation tiers | Research `ab_gate.py`, `ab_agentic_mcp.py`, `run_wide.py`, `usage_account.py`, scorers | Identical artifacts rescore reproducibly; malformed usage cannot pass; adversarial scorer fixtures behave as labeled |
| 1. Trustworthy search | Root/snapshot contract, truthful partiality, byte/token budget, scoped continuations, warning preservation | `tools.go`, `searchtext.go`, `textsearch`, Grove adapter, CLI | No unmarked loss in exhaustive/root/filter/serialization boundary fixtures |
| 2. Sufficient context | Tune existing contextual search, lookup, query, and required-site inventory to avoid redundant reads | `selection.go`, `delivery.go`, `textmerge.go`, ranking | Fewer duplicate-evidence reads and requests with no lost required sites; whole-task pilot cost falls |
| 3. Reliable completion | Recoverable cached content, compact unresolved obligations, behavior-aware verification and validation | `graphcache.go`, session tracker, `verify.go`, harness validators | Compaction/edit/restart tests preserve evidence; omissions are repaired with acceptable false alarms |
| 4. Product proof | Frozen N/R/C study, held-out tasks, per-model report, release decision | Research harness and release process | Joint economic/quality targets pass or a narrower claim is explicitly selected |

Treat stages as evidence-driven milestones, not a promise that a fixed calendar or payload reduction will produce the desired agent behavior. Run stages 0 and 1 first. Context tuning can then use reliable evidence instead of compensating for broken search contracts.

For every change, record one falsifiable hypothesis, its deterministic fixture, its agent-level measurement, and its rollback criterion. Modify one major behavior at a time. Keep engine correctness fixes even if extra valid evidence costs more locally; optimize their delivery under the same completeness contract.

## 10. First Experiments

| Experiment | Test | Success signal | Reject or revise when |
| --- | --- | --- | --- |
| Scoped search contract | Native and Prism search the identical root/snapshot/ignore rules; include sibling-root rejection and >2,000 long symbols | Exact site inventory agreement or explicit, recoverable partiality | Empty/partial result implies absence or warning vanishes after rendering |
| Grafana replay | Replay actual failing tool calls against pinned old and fixed binaries; compare all 11 missing required identities | Identify precisely which stage lost/recovered each site | Claim relies only on a new favorable agent sample |
| Context sufficiency | Location-only versus small windows versus named-body delivery, using existing controls | Less duplicated evidence and lower total task cost | Larger first response does not reduce later work or increases misses |
| Inventory representation | Flat complete inventory versus lossless groups/continuations | Lower early context exposure with equal module/site recovery | Agent never expands a required group or cost moves into many page requests |
| Verify usefulness | Same tasks with targeted final verification and removal checks | More verified completions per dollar, fewer residual obligations | False alarms or repeated checks dominate work |
| Routing/schema | Current shipped guidance versus one measured revision | Fewer discovery/misrouting requests at stable quality | Lower schema bytes cause worse tool selection or more exploration |

Build transport-boundary tests from real `Handler.Invoke` results through MCP and CLI rendering, not only hand-built maps. Include capped single and batched terms, non-positive/large limits, hard source limits with zero scoped matches, timeout/cancellation, long signatures, and continuation after edits. Assert the identity set as well as payload size.

For a controlled literal comparison, use fixed-string semantics explicitly:

```bash
rg -n -F -C 2 \
  -e 'MEAN_RECALL_DROP' -e 'require-cheaper' -e 'not-broken' \
  /Users/tapabratapal/Projects/provasign/research/harness
```

Run the corresponding Prism search from a server rooted at `research` with `path="harness"`, identical file inclusion rules, and `scope="text"`. The earlier root-mismatched comparison must not enter a Prism-versus-grep accuracy score. Also separate per-call response bytes from the host conversation cost of obtaining them.

## 11. Approaches to Avoid

- Truncate or rank away required sites to achieve an attractive token number.
- Claim confidence from an empty result without the correct root and a completed scan.
- Force agents to use Prism for every trivial operation, then exclude discovery and schema overhead from cost.
- Restrict the native baseline to inefficient searches or full-file reads.
- Replace readable identities with an opaque compression format before measuring whether agents use it correctly.
- Add an LLM to every retrieval or reranking step without accounting for its cost, latency, and errors.
- Treat repeated identical-call deduplication as the largest lever when the recorded workload shows mostly distinct calls and follow-up reads.
- Reintroduce the retired natural-language task compiler as a shortcut to routing.
- Trade away required-site recall because F1 improved, or claim quality because the diff touched expected files.
- Treat a Haiku smoke pass, a gold-vs-gold scorer check, or a single cheap successful task as product validation.

## 12. Release Evidence and Final Decision

A release claiming that Prism is substantially cheaper and more accurate should ship with a report containing:

1. The frozen workload and N/R/C configuration, versions, independent validation rules, and planned/completed counts.
2. Total input/output categories, CLI-reported cost, cost per verified success, latency, and cache state by model and task class.
3. Verified task success, strict site recall/precision, false-completeness rate, and unintended-change findings.
4. Paired uncertainty intervals, every attempt/retry/failure, and exclusions with reasons.
5. A trace of evidence loss/recovery and request reductions that explains the measured improvement.
6. The exact claims supported, unsupported task classes, and any regressions still present.

The decision is joint: lower cost, lower total token use, preserved correctness, and a demonstrated completeness advantage on difficult tasks. If those do not hold, continue with the responsible stage of the implementation plan or narrow the product's supported claim. Formatting improvements and a repaired smoke gate are progress, but they are not the final evidence.

## Appendix: Evidence Provenance

Local evidence used for this proposal:

- Prism Git commits listed above; current search/selection/delivery/cache/verify code linked in the relevant sections.
- [BACKLOG.md](../BACKLOG.md), especially section 21. Its historical transcript attribution is labeled separately from recomputed run costs.
- Research `3b3fa18`: [Mode-A scorer](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/score.py), [wide scorer](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/wide_score.py), [query oracle](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/query_oracle.py), [gate](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/ab_gate.py), [usage accounting](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/usage_account.py), and [read-after-locate analysis](https://github.com/provasign/research/blob/b6956ef22b3c0eee88eda1ed2ecef594c2d4c1a8/harness/read_after_locate.py).
- Eight stored v070sample pairs: costs recalculated from raw JSON; no new model runs.
- Two saved Grafana Mode-A answers: rescored under current v3; candidate has 11 misses and still claims completeness.
- Current wide scorer: incorrect-API substitution probe returned perfect headline region coverage and precision, establishing the need for behavioral checks.
- Scorer validation performed: 11 Mode-A tests, 10 wide tests, and 2 query-oracle tests passed.
- Official provider documentation linked in section 4, accessed 2026-09-06. Pin provider schema and pricing semantics in each future run rather than assuming they stay unchanged.

No product code, benchmark configuration, shared corpus, release tag, or concurrent benchmark was changed for this document. All new API shapes, thresholds, and study sizes above are proposals.

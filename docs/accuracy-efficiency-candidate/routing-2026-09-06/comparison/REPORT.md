# Guidance Comparison Results

Date: 2026-09-06. Run: `prism-routing-ab-y204tiw1`.

## Verdict

**Valid measurement; efficiency acceptance FAIL.** All eight executions passed
the run audit. All had 100% recall and precision, with no false-complete answers.
The new guidance improved aggregate tokens and estimated cost, but failed both
predeclared efficiency targets. No outcomes were retried or discarded.

Both arms use the same binary from the routing/search reliability follow-up.
Only the appended routing paragraph differs. This is not Prism versus native
Codex, nor a before/after comparison of the search-label or rollup code fixes.

| Metric | Search-first guidance | Direct-routing guidance | Change |
|---|---:|---:|---:|
| Total tokens | 338,682 | 288,583 | 14.8% fewer |
| Input tokens, including cache | 333,834 | 284,262 | |
| Cached input tokens, included above | 270,848 | 240,640 | |
| Output tokens | 4,848 | 4,321 | |
| Estimated arm cost | $0.595794 | $0.468060 | 21.4% less |
| Mean recall | 100% | 100% | no change |
| Mean precision | 100% | 100% | no change |
| False-complete answers | 0 | 0 | no change |

Median paired token savings: **11.1%**, below the 25% target.
Aggregate cost savings: **21.4%**, below the 30% target.
Total spend for this invocation: **$1.063854**. Earlier runs are not included.
Costs are estimated from the fixed rates in the protocol, not invoice totals.

## Pairs

Positive percentages mean savings; negative means increased usage.

| Task | Repeat | Before tokens | After tokens | Token savings | Before cost | After cost | Cost savings |
|---|---:|---:|---:|---:|---:|---:|---:|
| Gin | 1 | 114,733 | 99,160 | 13.6% | $0.180046 | $0.157585 | 12.5% |
| Gin | 2 | 105,438 | 52,494 | 50.2% | $0.174430 | $0.101696 | 41.7% |
| Django | 1 | 66,494 | 89,450 | -34.5% | $0.144590 | $0.134550 | 6.9% |
| Django | 2 | 52,017 | 47,479 | 8.7% | $0.096728 | $0.074229 | 23.3% |

Across its two repeats, Gin saved 31.1% of tokens and 26.9% of cost. Django used
15.5% more tokens, while costing 13.5% less. This mixed task behavior must not
be hidden by the aggregate. Cache usage is included in tokens; cache share rose
from 81.1% to 84.7% of input overall. Caching was observed, not controlled.

## Behavior

| Diagnostic | Before | After |
|---|---:|---:|
| All tool calls, including failures | 32 | 20 |
| Search calls anywhere | 9 | 2 |
| Searches after first successful impact | 4 | 0 |
| Native commands after impact | 2 | 0 |
| Lookup calls anywhere, including failures | 8 | 10 |
| Lookup calls after first successful impact | 7 | 7 |
| Successful batch lookup calls | 3 | 6 |
| Read calls | 5 | 2 |
| Successful impact calls | 8 | 6 |
| Ordinary tool errors | 0 | 1 |

All four old-guidance cells opened with search. Both new Gin cells opened with
lookup, and both new Django cells opened with impact. The new guidance therefore
changed routing in the intended direction on all four observed cells.

The first new Gin lookup failed because it supplied unsupported `model="gpt-5"`.
It then retried successfully. This is an ordinary tool-argument error and its
cost remains in the treatment, not an infrastructure failure or excluded attempt.
No search `task` errors occurred in either arm; both received the code fix.

Django repeat 1 still performed five lookup calls after impact. It requested
signature/body evidence for backend methods with identical qualified names in
different files, batching within some files but making separate calls for others.
It used more tokens despite fewer tool calls. Tool count is not model-request
count, and batching is not by itself evidence of reduced context processing.

## Next Implementation

Keep the direct routes and evidence-gap safeguards, but do not treat additional
prompt tuning as sufficient evidence of the required savings. The next bounded
candidate should address **file-scoped items within a single batch lookup**:

- Permit each requested symbol to carry its own file disambiguator, so methods
  with the same qualified name in different backends can be requested together.
- Preserve per-item ambiguity, missing symbols, scope errors, and omitted bodies;
  do not silently treat the current soft file hint as a hard scope.
- Verify exact scalar/batch content parity and replay the observed Django request
  sequence before another autonomous test.
- Separately investigate per-edge provenance in impact results. A blanket
  uncertainty warning can lead to reading every body, but removing that warning
  without stronger evidence would trade correctness for apparent savings.

These are hypotheses for the next candidate, not implemented or measured gains.
No product source was modified during this comparison.

## Validation And Limits

- Nine new runner tests and six existing comparison tests passed before launch.
- The model-free dry run prepared all eight isolated archives, confirmed no git
  history, checked MCP in both arms, and asserted prompt-prefix and normalized
  command equality. It launched zero models.
- The fixed binary SHA256 is
  `969239d7bb991c3feb64e48d952cc1ff17d1a449a5d58df588e0a005b28440a2`.
  Codex CLI 0.153.4, gpt-5.5, medium effort; two executions per pair with reversed
  submission order in second repeats. Actual thread start order can differ.
- Eight raw answers were re-scored, including independent normalized set
  intersections/differences. Every raw usage total and estimated cost agrees with
  the summary. No source edits, protocol violations, setup failures, non-use,
  unknown usage, or quality retries were observed.
- Raw commands, prompts, transcripts, measurements, input hashes, and replay checks
  are in [results](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/routing-2026-09-06/comparison/results/manifest.json), with a [checksum inventory](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/routing-2026-09-06/comparison/results/SHA256SUMS.json).
- This is two tasks with two repeats, not statistical significance. It does not
  establish savings on held-out tasks or over native Codex/Sonnet. The earlier
  native-control panel remains inconclusive overall and failed Codex efficiency.

The earlier evidence-delivery comparison saved 28.9% aggregate tokens and 27.7%
cost against an older Prism binary, at 100% recall/precision in both arms. This
study saved 14.8% tokens and 21.4% cost against search-first guidance on the same
new binary. These percentages have different controls and must not be compounded
or pooled into an overall product-savings claim.

# Codex Before/After: Evidence Delivery

Date: 2026-09-06. Run: `prism-delivery-ab-dfhlujhg`.

## Verdict

**Valid measurement; improvement observed; acceptance targets not met.**
All eight executions passed the arm-use and measurement audits. Both versions
achieved 100% recall and precision in every execution, with zero false-complete
claims. The candidate used fewer tokens and less estimated cost in all four
pairs, but failed both predeclared efficiency thresholds.

| Metric | Frozen panel Prism | Evidence candidate | Saving | Target |
| --- | ---: | ---: | ---: | ---: |
| Aggregate tokens | 439,385 | 312,341 | 28.9% | Reported separately |
| Median paired token saving | | | **15.3%** | At least 25%: **fail** |
| Summed estimated USD | 0.700038 | 0.506121 | **27.7%** | At least 30%: **fail** |
| Mean recall / precision | 100% / 100% | 100% / 100% | No regression | Pass |

All eight benchmark executions cost **$1.206159 estimated**, within the
approximately $3 launch budget. There were no setup failures, non-use cells,
timeouts, replacements, or quality retries. These totals contain this
invocation only; previous pilot/panel spend is not included.

This is evidence that the bundled candidate changes help this small sample,
not that every individual change caused a gain, that the threshold failure is
statistically significant, or that Prism now beats native tools. There are
only two repositories and two repeats each. No native or Sonnet arm ran here.

## Every Pair

| Task | Repeat | Old tokens | Candidate tokens | Token saving | Cost saving |
| --- | ---: | ---: | ---: | ---: | ---: |
| Gin | 1 | 224,372 | 121,941 | 45.7% | 41.5% |
| Gin | 2 | 99,453 | 87,972 | 11.5% | 9.0% |
| Django | 1 | 52,095 | 51,094 | 1.9% | 29.0% |
| Django | 2 | 63,465 | 51,334 | 19.1% | 17.9% |

Across both repeats, Gin saved 35.2% of tokens and 29.7% of estimated cost;
Django saved 11.4% and 23.9%, respectively. The larger first Gin result drives
much of the aggregate advantage. The task-balanced median does not reach 25%.

## What Changed In Practice

| Diagnostic, four executions per version | Old | Candidate |
| --- | ---: | ---: |
| All tool calls | 30 | 19 |
| Successful impact calls | 6 | 6 |
| Lookup calls, anywhere in the session | 14 | 2 |
| Lookup calls after first successful impact | 10 | 2 |
| Search calls after first successful impact | 4 | 5 |
| Native commands after first successful impact | 1 | 0 |
| Successful batch lookups | 0 | 2 |
| Ordinary tool errors | 0 | 2 |

The candidate used batched lookup in **both Gin repeats**, requesting three
and four names. Those two calls account for all candidate lookup calls.
Batch calls and scalar calls are not equal amounts of source delivery; the
lower call count must not be presented as a proportional reduction in bytes
or model requests.

In Django, candidate lookups after impact fell from five to zero in repeat 1
and from one to zero in repeat 2. However, it still performed exhaustive
searches after impact: one in repeat 1, two in repeat 2. The new evidence
displaced body lookups but did not eliminate rechecking of the site set.

Both candidate Gin executions first supplied unsupported `task` to
`prism_search`, received an explicit error, and retried correctly. The same
ordinary API mistake therefore cost two extra calls. They remain in candidate
tokens, cost, and latency; they are not MCP setup failures and were not excluded.

Gin still used two impact calls in each candidate repeat, even though the
task only asks for two local bug-fix sites. Batch adoption worked, but the
entire small-task path is not yet minimal.

## Next Target

Keep this candidate as an experimentally improved checkpoint, not a release
or product-superiority pass. The remaining overhead is now more specific:

1. **Task-aware first tool.** The unchanged experiment prompt still requires
   `prism_search` or `prism_query` first, even when an exact impact target or
   small set of method names is already supplied. Test direct impact for an
   explicit signature target, batched lookup for known local methods, and
   search for genuinely unknown locations. This is a new steering treatment,
   not a reason to invalidate or relabel the current failure.
2. **Close evidence gaps without another whole search.** Django still rescans
   after the full impact inventory and call expressions. A truthful coverage
   reconciliation and actionable unresolved-edge list may replace that work.
   Do not simply tell the model to trust `closed` or suppress verification.
3. **Remove argument friction.** Clarify or deliberately align search/query
   parameter contracts. Do not silently ignore an unsupported scope/parameter.
   Two paid argument-error round trips remained in this tiny candidate sample.

Freeze the next treatment before testing, rerun complete paired blocks, and
include fresh native controls and a Sonnet regression check before claiming
the product's cost/accuracy goal has been reached. Do not rerun just the weak
Django pair until it happens to pass.

## Every Execution

All rows have full recall/precision, valid arm use, and unchanged corpus source.
Times below are agent wall time, excluding indexing.

| Task / repeat | Version | Tokens | Estimated USD | Seconds | Tools |
| --- | --- | ---: | ---: | ---: | ---: |
| Gin / 1 | [Old](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/a/evidence/gin-4645.r1.codex_prism/audit.json) | 224,372 | .291069 | 70.138 | 10 |
| Gin / 1 | [Candidate](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/b/evidence/gin-4645.r1.codex_prism/audit.json) | 121,941 | .170280 | 45.101 | 6 |
| Gin / 2 | [Old](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/a/evidence/gin-4645.r2.codex_prism/audit.json) | 99,453 | .165116 | 44.113 | 8 |
| Gin / 2 | [Candidate](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/b/evidence/gin-4645.r2.codex_prism/audit.json) | 87,972 | .150191 | 36.103 | 6 |
| Django / 1 | [Old](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/a/evidence/django-connparams.r1.codex_prism/audit.json) | 52,095 | .131261 | 34.072 | 7 |
| Django / 1 | [Candidate](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/b/evidence/django-connparams.r1.codex_prism/audit.json) | 51,094 | .093229 | 32.042 | 3 |
| Django / 2 | [Old](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/a/evidence/django-connparams.r2.codex_prism/audit.json) | 63,465 | .112592 | 41.089 | 5 |
| Django / 2 | [Candidate](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/b/evidence/django-connparams.r2.codex_prism/audit.json) | 51,334 | .092421 | 31.065 | 4 |

## Accounting And Controls

- Model `gpt-5.5`, medium effort; Codex CLI 0.153.4. Both versions reused the
  prior panel's exact task/Prism prompt, scorer, execution helper, and client
  settings. Dynamic tool schemas/descriptions came from each binary and are
  part of the treatment. Provider model identifiers do not pin immutable weights.
- Fresh pinned archive per execution, no original Git history or corpus agent
  customization files. Native tools remained available. Only Prism MCP was
  configured, with explicit per-tool approval. No global configuration edits.
- Two simultaneous executions per pair; submission order reversed in repeat 2.
  Actual start times are retained. This does not guarantee strict order or cold
  provider caches, and configured isolation is not an adversarial security claim.
- Tokens are reported input plus output; cached input is already within input.
  API-equivalent cost uses $5 / $0.50 / $30 per million uncached input / cached
  input / output, following [the model pricing](https://developers.openai.com/api/docs/models/gpt-5.5).
  These are estimates, not subscription charges or reconciled invoices.
- Provider caches were uncontrolled. Aggregate input cache shares were 82.84%
  old and 82.22% candidate. In Django repeat 1 specifically, cache share rose
  from about 66% to 80%, so its 29% dollar reduction cannot be described as a
  29% token reduction or attributed solely to fewer reads.
- Cold indexing added 1.50-1.74 seconds per Gin execution and 29.38-30.90
  seconds per Django execution. It used no model tokens and is recorded
  separately. No invoice or machine-resource cost is inferred from these times.

## Evidence And Replay

[Frozen protocol](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/PROTOCOL.md), [manifest](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/manifest.json), [all measurements](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/summary.json),
[acceptance result](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/analysis.json), [raw-answer/usage replay](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/replay-audit.json),
and [artifact checksums](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/comparison/SHA256SUMS.json) are saved alongside the per-cell
stdout, stderr, answers, commands, prompts, configuration, and index timing.

All eight answers were rescored from raw final text. Exact normalized
path/symbol set comparison agreed without the scorer's tolerant matching.
Raw usage reproduced every token/cost value and the per-invocation spend sum.
Six offline comparison tests pass; runner/analyzer/protocol hashes match the
pre-run manifest. No product code, prompt, scorer, or threshold changed during
execution. No commit, tag, installation, or release was made.

Replay without paid model calls:

```sh
python3 -B docs/accuracy-efficiency-candidate/evidence-delivery-2026-09-06/comparison/analyze.py \
  docs/accuracy-efficiency-candidate/evidence-delivery-2026-09-06/comparison
python3 -B -m unittest discover \
  -s docs/accuracy-efficiency-candidate/evidence-delivery-2026-09-06/comparison -p 'test_*.py'
```

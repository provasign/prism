# Four-Way Validation Panel

Date: 2026-09-06. Run: `prism-panel-b4i4a554`.

## Decision

**Not a readiness pass.** All 24 scheduled executions finished, but only 23
passed the arm-use audit. The frozen panel decision is **inconclusive**.
Independently, Codex's six valid pairs **fail both efficiency targets**.
We have enough diagnostic evidence to prioritize the next optimization work,
not evidence to declare Prism consistently cheaper or ready for release.

No candidate, prompt, scorer, or acceptance threshold changed during execution.
There were no replacement executions, quality retries, or task substitutions.
The earlier Jackson pilot is not pooled into these results.

## Main Results

These are sums across both repeats of each task. Positive savings mean Prism
used less. Tokens include all reported input/cache/output categories; cached
input is not counted twice. Dollar savings are separate from token savings.

| Task | Client | Native tokens | Prism tokens | Token saving | Estimated cost saving |
| --- | --- | ---: | ---: | ---: | ---: |
| Gin | Sonnet | 58,942 | 66,585* | -13.0%* | -2.4%* |
| Gin | Codex | 85,296 | 143,261 | **-68.0%** | 1.4% |
| Django | Sonnet | 114,216 | 61,612 | 46.1% | 49.3% |
| Django | Codex | 89,627 | 170,381 | **-90.1%** | **-15.3%** |
| TypeORM | Sonnet | 1,032,743 | 214,469 | 79.2% | 65.4% |
| TypeORM | Codex | 707,636 | 452,216 | 36.1% | 33.7% |

*The Sonnet/Gin treatment total includes a noncompliant execution that never
used Prism. It is retained as assigned-arm descriptive data and experiment
spend, not a valid two-repeat Prism comparison. In the valid first repeat,
Sonnet + Prism used 108.5% more tokens and cost 41.6% more than native Sonnet.

Quality against the frozen production-site answer sets:

- All 11 valid Prism executions achieved 100% recall and precision, with zero
  false-complete claims. No valid pair lost recall.
- Native Sonnet missed `NestedSetSubjectExecutor.isUniqueRootEntity` in
  TypeORM repeat 2: 36/37 sites, despite claiming completeness. Both Prism
  repeats found all 37. All other native executions found every expected site.
- All 24 executions had 100% scored precision. Independent replay using exact
  normalized path/symbol sets agreed, without suffix-path matching or neutral
  test-site exceptions. This checks scoring of these answers, not every
  possible defect in the answer sets.

### Frozen Acceptance Checks

| Check | Sonnet | Codex |
| --- | --- | --- |
| Six valid pairs | No: 5/6 | Yes: 6/6 |
| No paired recall loss | None in valid pairs | Pass |
| No added false-complete claims | None in valid pairs | Pass |
| At least 95% mean recall and precision | 100% on valid Prism cells; incomplete panel | Pass: 100% / 100% |
| At least 25% median paired token saving | Not eligible for a pass | **Fail: -54.0%** |
| At least 30% summed estimated cost saving | Not eligible for a pass | **Fail: 19.2%** |

Codex's aggregate token saving is 13.2%, whereas its median paired saving is
-54.0%. TypeORM dominates the aggregate: four of six Codex pairs use more
tokens with Prism. The broader task's win must not conceal the two smaller
tasks' regressions. Two repeats across three tasks do not establish statistical
superiority or make the six pairs six independent repository samples.

## Invalid Cell And Spending

`gin-4645.r2.sonnet_prism` initialized with Prism MCP available, but made only
a native `Read(response_writer.go)` and `Bash("echo done")`. There were no MCP
transport, permission, or tool errors. This is **treatment non-use**, not the
MCP setup failure from the earlier pilot, and it is not evidence that Prism
retrieval failed. Its correct answer cannot be credited to Prism. Whether the
cause is steering or model choice requires a separate experiment.

| Current invocation only | Estimated USD |
| --- | ---: |
| 23 arm-valid executions | 3.9884158 |
| Noncompliant Sonnet execution | 0.0248140 |
| Setup-failure executions | 0.0000000 |
| **All 24 executions** | **4.0132298** |

The ~$15 authorization was a launch budget, not a hard Codex billing cap.
There were six four-cell waves, a $4 headroom requirement before each wave,
a $1 Claude cell cap, and a 600-second per-cell timeout. Nothing timed out.
No previous pilot or gate invocation is included in this spend.

Claude costs are its CLI's `total_cost_usd` estimates, including auxiliary Haiku
usage. Codex costs are API-equivalent estimates at $5 / $0.50 / $30 per million
uncached input / cached input / output tokens, not subscription charges or an
invoice. See [the official model pricing](https://developers.openai.com/api/docs/models/gpt-5.5).
The estimate uses standard-context rates; the aggregate CLI counters do not
independently establish every individual request's context-length tariff.

Provider caching remained enabled and uncontrolled. Codex's aggregate cached
share of input was 80.5% native versus 81.7% Prism; this does not mean paired
cache states were equal. The tiny Gin dollar saving despite 68% more tokens
is not a token-efficiency win. Arm submission order reversed between repeats,
but four-way concurrency does not guarantee a strict launch order or cold caches.

Agent wall times below exclude fresh indexing. Each Prism cell also incurred
2.02-2.34 seconds indexing Gin, 49.97-50.64 seconds Django, or 3.31-3.39 seconds
TypeORM. Indexing used no paid model tokens. The model-time gains on Django
Sonnet do not pay back that entire cold-index time on a single task; an already
indexed repository has a different latency profile.

## What To Change Next

1. **Reduce repeated verification with better evidence, not stronger demands
   to trust Prism.** Both Codex/Django repeats searched the same symbol several
   ways and looked up most or all eight production methods after impact had
   already listed them. Repeat 2 used 13 calls versus native's six. Impact
   reported `completeness: closed` alongside a warning about uncertain
   name-derived references. Distinguish resolved coverage from heuristic edges,
   show exact caller evidence and remaining gaps, and give the agent a bounded
   way to verify just those gaps. Do not suppress legitimate verification.
2. **Make the simple-task path cheap.** Gin native Sonnet solved repeat 1 with
   one read; Codex + Prism repeatedly searched, read three methods, and computed
   two impact sets for a two-method bug. Test a compact first response that
   supplies the relevant method bodies together, and reserve impact expansion
   for tasks that require it. Retain small-task regression controls.
3. **Reduce parameter mistakes without silent degradation.** Two Codex cells
   tried unsupported `task` on search or `path` on query, then recovered.
   Those errors stay in operating costs. Clarify the tool schemas/descriptions
   and test client-specific tool use; do not silently ignore unsupported scopes.
4. **Resolve how non-use is measured.** If Prism is optional, report assigned-arm
   results and adoption separately, including non-use. If the study requires
   actual delivery, use a predeclared compliant workflow and retain non-use as
   a protocol failure. Do not selectively rerun only this cheap cell to get a PASS.
5. **Test the next candidate against this frozen implementation.** Keep the
   native arms; add current-Prism versus next-Prism controls for attribution.
   Rerun whole paired blocks with fixed rules, then use held-out tasks and
   end-to-end patch tests. Repair the known Django/Grafana oracle omissions
   before using those harder tasks as acceptance gates.

The TypeORM gains justify pursuing the approach. The small-task/Codex losses
show that reduced response bytes alone are insufficient: number of subsequent
turns, evidence sufficiency, tool overhead, and caching all affect the result.

## All Executions

Each row links to its immutable run evidence copy. `FC` means false-complete.
All precision values are 1.0. Seconds are agent wall time, excluding indexing.

| Task | Repeat | Arm | Found | FC | Tokens | Est. USD | Seconds | Calls | Arm valid |
| --- | ---: | --- | ---: | --- | ---: | ---: | ---: | ---: | --- |
| Gin | 1 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r1.sonnet_baseline/audit.json) | 2/2 | No | 16,055 | .018988 | 5.019 | 1 | Yes |
| Gin | 1 | [Sonnet Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r1.sonnet_prism/audit.json) | 2/2 | No | 33,478 | .026888 | 10.045 | 3 | Yes |
| Gin | 1 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r1.codex_baseline/audit.json) | 2/2 | No | 50,553 | .166340 | 35.080 | 7 | Yes |
| Gin | 1 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r1.codex_prism/audit.json) | 2/2 | No | 87,804 | .154484 | 37.086 | 8 | Yes |
| Gin | 2 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r2.sonnet_baseline/audit.json) | 2/2 | No | 42,887 | .031518 | 13.048 | 4 | Yes |
| Gin | 2 | [Sonnet assigned Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r2.sonnet_prism/audit.json) | 2/2 | No | 33,107 | .024814 | 6.020 | 2 | **No** |
| Gin | 2 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r2.codex_baseline/audit.json) | 2/2 | No | 34,743 | .112646 | 27.079 | 5 | Yes |
| Gin | 2 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/gin-4645.r2.codex_prism/audit.json) | 2/2 | No | 55,457 | .120631 | 30.072 | 7 | Yes |
| Django | 1 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r1.sonnet_baseline/audit.json) | 8/8 | No | 66,487 | .057129 | 27.087 | 10 | Yes |
| Django | 1 | [Sonnet Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r1.sonnet_prism/audit.json) | 8/8 | No | 30,927 | .024979 | 12.029 | 2 | Yes |
| Django | 1 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r1.codex_baseline/audit.json) | 8/8 | No | 61,329 | .188552 | 41.110 | 11 | Yes |
| Django | 1 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r1.codex_prism/audit.json) | 8/8 | No | 90,987 | .176762 | 51.106 | 16 | Yes |
| Django | 2 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r2.sonnet_baseline/audit.json) | 8/8 | No | 47,729 | .038113 | 20.061 | 5 | Yes |
| Django | 2 | [Sonnet Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r2.sonnet_prism/audit.json) | 8/8 | No | 30,685 | .023346 | 10.046 | 2 | Yes |
| Django | 2 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r2.codex_baseline/audit.json) | 8/8 | No | 28,298 | .106069 | 42.121 | 6 | Yes |
| Django | 2 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/django-connparams.r2.codex_prism/audit.json) | 8/8 | No | 79,394 | .162898 | 86.248 | 13 | Yes |
| TypeORM | 1 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r1.sonnet_baseline/audit.json) | 37/37 | No | 591,197 | .330321 | 103.230 | 23 | Yes |
| TypeORM | 1 | [Sonnet Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r1.sonnet_prism/audit.json) | 37/37 | No | 155,637 | .157286 | 67.147 | 17 | Yes |
| TypeORM | 1 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r1.codex_baseline/audit.json) | 37/37 | No | 384,527 | .532691 | 150.412 | 23 | Yes |
| TypeORM | 1 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r1.codex_prism/audit.json) | 37/37 | No | 176,335 | .310488 | 75.181 | 7 | Yes |
| TypeORM | 2 | [Sonnet native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r2.sonnet_baseline/audit.json) | 36/37 | **Yes** | 441,546 | .293214 | 113.352 | 23 | Yes |
| TypeORM | 2 | [Sonnet Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r2.sonnet_prism/audit.json) | 37/37 | No | 58,832 | .058307 | 24.080 | 4 | Yes |
| TypeORM | 2 | [Codex native](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r2.codex_baseline/audit.json) | 37/37 | No | 323,109 | .513462 | 111.322 | 18 | Yes |
| TypeORM | 2 | [Codex Prism](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/evidence/typeorm-driver-escape.r2.codex_prism/audit.json) | 37/37 | No | 275,881 | .383303 | 87.281 | 9 | Yes |

## Reproducibility

- [Frozen protocol](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/protocol.md), [manifest](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/manifest.json), [summary](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/summary.json),
  [machine decision](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/analysis.json), and [24-cell replay audit](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/replay-audit.json).
- Models: `claude-sonnet-5` and `gpt-5.5`, medium effort. CLI versions 2.1.263
  and 0.153.4. Model identifiers do not promise immutable provider weights.
- Fresh pinned archives per cell, without original Git history, other MCPs,
  skills, memory, or delegated agents. Native tools stayed available in both arms.
  Isolation was configured and checked, not claimed as an adversarial security boundary.
- Candidate SHA-256:
  `63f5166f6adf40f768a9cf32701a13344abcf29c51064cc9fe372dbb210a7e7d`.
  Source is `a1c7aa6` plus [the frozen internal-source patch](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/prism-working-tree.patch).
  This comparison has no released-Prism arm, so it cannot attribute gains to
  particular recent commits or prove old/new Prism improvement.
- Raw stdout, stderr, answers, commands, prompts, configuration, indexing times,
  original measurements, and corrected `audit.json` records are retained per cell.
  Use `audited_total_tokens` and `audited_valid`, not the helper's legacy fields.
- [Harness source snapshot](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/harness-snapshot/score.py) and original task bytes are
  included. Manifest task hashes refer to those original bytes; the root task
  copies were JSON-reformatted by the runner and were checked for semantic equality.
- [SHA-256 inventory](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/panel-2026-09-06/SHA256SUMS.json) covers the archived files. Full corpora and
  the binary remain under `/private/tmp/prism-panel-b4i4a554`, outside this archive.
- The independent replay checked every final answer, exact normalized site set,
  raw usage total, and per-run cost sum. Eleven offline audit/analysis tests pass.
  These are enumeration tasks, not patch correctness or runtime behavior tests.

Replay the decision without launching paid models:

```sh
python3 -B docs/accuracy-efficiency-candidate/panel-2026-09-06/analyze_panel.py \
  docs/accuracy-efficiency-candidate/panel-2026-09-06
python3 -B -m unittest discover \
  -s docs/accuracy-efficiency-candidate/panel-2026-09-06 -p 'test_*.py'
```

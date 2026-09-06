# Four-Way Pilot: Jackson SettableBeanProperty.set

Date: 2026-09-06. One task, one usable cell per arm. This is a directional
pilot, not a statistically supported product claim or an old/new Prism A/B.

## Results

| Arm | Required sites | Precision | Total reported tokens | Estimated model cost | Agent time | Tool calls |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Sonnet 5, native tools | 18/22 | 100% | 695,012 | $0.3601 | 105.8 s | 30 |
| GPT-5.5, Codex native tools | 22/22 | 100% | 602,966 | $0.8298 | 164.1 s | 30 |
| Sonnet 5 + Prism | 22/22 | 100% | 145,893 | $0.1353 | 39.0 s | 9 |
| GPT-5.5, Codex + Prism, corrected setup | 22/22 | 100% | 190,121 | $0.3381 | 82.7 s | 10 |

Within Sonnet, Prism reduced reported tokens by **79.0%** and estimated cost
by **62.4%**, while recovering four missed sites. Within Codex, the correctly
configured Prism cell reduced tokens by **68.5%** and estimated cost by
**59.3%**, with the same required-site recall and precision.

These totals include cached input, not just fresh input or response bytes.
Sonnet totals also include the CLI's auxiliary Haiku usage: 1,124 tokens for
native and 1,209 for Prism. The initial runner's top-level Claude usage totals
excluded this helper; the audited totals above use all `modelUsage` entries.
All three complete-answer cells claimed completeness. Native Sonnet also
claimed completeness, but missed four required methods.

## Actual Experiment Spend

There were **five executions**, not four. The first Codex + Prism attempt
could not execute its MCP search because tool approval was required while
the CLI's approval policy was `never`. It fell back to native tools. Its
22/22 answer is not evidence about Prism and is excluded from the four-way
quality comparison, but its **549,358 tokens and $0.7216 estimated cost remain
in the experiment's spend**.

Only that arm was rerun, on a new snapshot, with the six local Prism tools
explicitly approved. No quality-based retries were performed. The replacement
had nine successful Prism calls and one rejected unsupported-parameter call;
that rejected call remains included in the replacement's usage.

- Four valid cells: **$1.6632** estimated model cost.
- All five attempts: **$2.3848**, **2,183,350 tokens**.
- Codex + Prism including the setup failure: **$1.0596**, versus native Codex's
  **$0.8298**. The experiment did not save Codex-side spend after charging the
  setup failure, despite the corrected cell being cheaper.

The original `codex_prism/measurement.json` marked provider completion valid
before tool-use auditing. Its adjacent `audit.json` marks `audited_valid=false`
and states why. Raw records are retained unchanged. The original manifest's
`retries=0` is the planned protocol; `summary.json` records the actual one
infrastructure retry.

## Measurement Basis

Claude cost is the CLI's `total_cost_usd`, including auxiliary model use, not
an invoice. Codex provides token counters rather than a dollar charge; its
cost is an API-equivalent estimate using GPT-5.5's published $5 uncached input,
$0.50 cached input, and $30 output per million tokens. Cached input is a subset
of Codex input and is subtracted before pricing uncached input. These estimates
are not subscription charges. See the [OpenAI model pricing](https://developers.openai.com/api/docs/models/gpt-5.5)
and [Claude usage accounting](https://code.claude.com/docs/en/agent-sdk/cost-tracking).

Agent time excludes local Prism indexing: 10.3 seconds for Sonnet + Prism and
10.0 seconds for corrected Codex + Prism. Indexing used no model tokens.
Provider prompt caching was enabled and not experimentally controlled. The
replacement Codex cell ran later, so cache warming and service timing are
possible confounders. Cross-provider token counts are not tokenizer-equivalent.

## Protocol And Audit

- Task: `jackson-settable-set`, Jackson Databind at
  `0b422144d1785200e44a0b00c973f6ac95adcf5a`, 1,295 tracked files.
- Task class: Mode-A signature-change impact enumeration, not patch generation
  or end-to-end behavioral correctness. The existing scorer's universe is
  22 unique production `(relative path, bare method name)` sites; overloads
  and nested methods with the same path/name are not distinguished.
- Models: `claude-sonnet-5` and requested `gpt-5.5`, medium effort in both CLIs.
  Claude Code 2.1.263; Codex CLI 0.153.4. These are comparison choices, not
  equivalent models or identical host system prompts.
- Prism: built from `a1c7aa6` plus the current working-tree implementation.
  Binary SHA-256:
  `63f5166f6adf40f768a9cf32701a13344abcf29c51064cc9fe372dbb210a7e7d`.
- Each execution used a fresh archive of the same corpus commit and a new
  empty Git repository, with no original history, answers, or harness files
  inside its working directory. No source-file changes were detected.
- Baselines had no MCP servers and made no Prism calls. Prism arms retained
  native tools and received the same task/output contract plus Prism guidance.
  Raw transcripts show no delegation or web-tool use. Claude skills/plugins
  were empty; Codex reported removing its skills catalog under the configured
  budget. This is process/configuration isolation, not a separate-machine
  security boundary against malicious access to other local directories.
- Each Claude cell had a $2 cap. Every execution had a 600-second timeout.
  Codex had no CLI dollar cap. No execution hit a cap or timeout.
- Scorer version 3 was applied unchanged, then reapplied offline to every
  saved final answer. Native Sonnet missed `CreatorProperty.inject`,
  `ExternalTypeHandler._deserializeAndSet`, `InnerClassProperty.deserializeAndSet`,
  and `ObjectIdReferenceProperty.handleResolvedForwardReference`.

## Interpretation

This task supplies useful evidence that current Prism can deliver less model
work with preserved or improved completeness for both clients. It is a
structural impact task that fits Prism's graph particularly well. It does not
establish general superiority, accuracy on unrelated task classes, or that
every recent code change caused the gains. Those require held-out paired
trials and an additional released-Prism control.

Full counters, commands, prompts, scores, and all failed/successful attempts
are in [summary.json](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/four-way-2026-09-06/summary.json) and `evidence/`. The scripts, source patch,
manifest, and hashes are alongside this report. The larger corpus snapshots
and executable remain at `/private/tmp/prism-four-way-3k0qvcsa`.

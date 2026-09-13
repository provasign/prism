# Handover — Prism × coding-agent benchmark, 2026-09-13

Companion to `harness-second-call-findings-2026-09-13.md` (evidence and
reasoning excerpts). This file is the action list. Every item says what the
evidence supports, what would count as success, and what not to do. The
governing rule is at the end; nothing here should be judged without it.

Evidence base: 13 Claude Sonnet cells with reasoning captured, two tasks
(`pallets__click__pr3244`, `urllib3__urllib3__pr3786`), three Prism builds
(v0.74.1, an uncommitted wording rebuild, the uncommitted 2026-09-13
first-result change), plus a 6-vs-6 before/after batch on the Prism arm.
Research repo: `harness/results/prism-first-result-2026-09-13/`.

---

## A. Prism (product)

**A1 + A2 (one change, measured as one): deliver the content the model will read next, in the same call — never by asking.**

Evidence. After a locator the model reads the file next in effectively
every cell (13/13), so the answer to "do you want the content?" is always
yes; there is nothing to ask. Two mechanisms are already falsified by the
data: a *question* costs the round trip it claims to save (the "yes" is a
call), and a *suggestion* is what the footer already is ("use op=lookup for
known symbol bodies…", and on the new build "already read; do not fetch it
again") — ignored in every cell; the new build read MORE, not less.

The floor. Claude Code's `Edit` requires a native `Read` of the file it
edits; Prism content cannot replace that read, only make it a targeted
read of the edit range instead of a whole-file orientation read. For files
the model will not edit (callers, tests, siblings) Prism content replaces
the read outright. So the win is (a) non-edited files and (b) turning
~23 KB whole-file reads into a few KB of region.

The arithmetic. A turn ≈ $0.015–0.02. A proactively delivered ~150-line
region ≈ 2–3K tokens, re-read every later turn ≈ $0.01–0.02 across a
20-turn cell — so "save one round trip" alone is a wash. The real saving
is only realized if the region DISPLACES the model's own 19–28 KB/cell of
reading. That did not happen automatically (batch: bytes read went up).

Change, two parts that ship and measure together:
1. Decide, don't ask. For each `file:line` hit, include the enclosing
   method/class body up to a cap (top 1–2 hits for multi-hit terms; the
   rest stay labeled locators, as the new build already does). Keep the
   scope disclosure exactly as it is.
2. Let the agent declare intent in the same call, and name the pattern in
   `instructions`: an argument such as `context: "enclosing"` /
   `include: "bodies"` on the locating call, with the instruction "if you
   will read after locating, ask for the region in the same call" — plus
   the contract stated plainly: one native Read per file you will edit, at
   the edit site, for the range you will change; every other read via
   `op=read`/`op=lookup` with tight ranges. (`op=query` and `op=lookup`
   already return windows/bodies; the model never reaches them because it
   picks `search` first every time.)

Success, on the full e2e set with a native arm at ≥3 trials: whole-file
`Read` calls (no `limit`) down, KB read per cell down, discovery reads
down, cost per resolved cell not worse. Calls going up while reads stay
flat is the footer story again and means no.

Do not: implement this as a prompt/offer the model must answer; deliver
unbounded content; deliver full inventories for many-hit terms; touch the
scope-disclosure or empty-scope wording.

**A1+A2 — test of the corrected build (2026-09-13 12:03, sha e08efaf5), no cells run yet.**
- `instructions` now state the contract and the same-call pattern
  ("If you will read after locating, set include_bodies=true … In hosts
  that require a native Read before Edit, use one tight native Read at the
  edit site; use Prism read/lookup for other follow-ups"). Correct.
- Mechanism is opt-in (`include_bodies` on `search`). Adoption is the
  first thing to measure: fraction of first calls that set it.
- GAP: `compactSearchBodiesEnclosing` caps each body at 160 lines / 10 KB.
  The motivating case — `sys.stdout =` hits at click `testing.py:338/464`
  — sits inside `CliRunner.isolation()` (278–473, 196 lines), which is
  rejected, and nothing is delivered in its place. Live probe with
  `include_bodies=true`: 2,236 bytes vs 2,268 default, same small class
  body relabeled. On this task the flag is a no-op. Fix: when the
  enclosing symbol exceeds the cap, deliver a bounded window around the
  hit (≈±40–60 lines, same 4,000-token budget), labeled as a window.
  Until then a run can measure the instruction effect and adoption, not a
  body-driven read reduction on click.
- RUN (Tier 1, 12:06, 4 of 6 cells before a harness stop; evidence
  `results/prism-first-result-2026-09-13/tier1-codexfix2-partial/`,
  research commit 349ccb40): `include_bodies=true` on the first search:
  **0 of 4**. Second Prism call: **0 of 4**. An explicit imperative
  instruction was not followed in any cell -- the same result as the
  footer, now in opt-in form. Two valid cells: click r2 21 turns/$0.53/
  resolved; urllib3 r2 13 turns/$0.21/not. The other two timed out at
  300 s under three-way concurrency (harness artifact, see B2), ~$1 each
  unbilled. This is the outcome the opt-in form risked: it depends on
  instruction-following this model does not exhibit here.

**THE ASK (decided 2026-09-13 after the run): default-on delivery for
line hits, opt-out only, then measure.**
Codex's gate doc notes small bodies are already delivered by default when
the first-result rule applies. That is not the gap. The gap is a
`file:line` text hit whose enclosing symbol is over the cap (click
`sys.stdout =` at 338/464 inside `isolation()`, 196 lines; the gate doc's
own cell hit `class CliRunner`, 393 lines): today nothing is delivered
and the model reads the whole file. Change:
1. For each of the top two `file:line` hits, deliver the enclosing body if
   it fits the cap, else a bounded window around the hit (≈±40–60 lines,
   same 4,000-token budget), labeled as a window and with the enclosing
   symbol's span stated so the model knows what it did not get.
2. Do this by DEFAULT. Keep `include_bodies` as an opt-out (`false`), not
   an opt-in -- the model does not set flags on instruction.
3. Keep the instruction text (Edit-contract sentence, read/lookup for
   other follow-ups); it is correct even though it did not change
   behavior alone.
Acceptance, in this order: (a) Tier 1 again on the pilot pair, Prism arm,
3 trials, `--concurrency 1` so timeouts are comparable to the sequential
n=6 baselines (v0.74.1 and the 09:33 build) -- look for whole-file Reads
down, KiB read down, discovery reads down, cost per resolved cell not
worse; (b) only if (a) moves, the full e2e set with the native arm at 3
trials, urllib3 swapped for a ~50%-solvable task. `bench.py index` now
emits every metric named here. Do not run comparisons on an uncommitted
harness; do not compare runs made at different `--concurrency`.

**RESULT of THE ASK (2026-09-13, evening) — built, fixed, measured: neutral at n=6.**
- Codex's 12:33 build delivered windows for single-term queries only. Two
  bugs on batched queries (the model's only query shape, 5/5 term sets):
  slots were counted on visited hits before dedup, so a symbol reached via
  its text hit and its symbols entry burned both; and the first-term
  small-symbol rule replaced the enclosing section instead of adding to it.
  Fixed in `internal/mcp/server.go` (slots count delivered regions; one
  region per term first, then a second, max 4, same 4,000-token budget;
  first-term rule reports its picks and the enclosing pass skips them;
  renderer drops a region rather than shrinking below 20 lines).
  Instruction and `include_bodies` descriptions updated to match default-on.
  Tests: `TestCompactBatchedTermsKeepSecondSlotAfterDuplicateHit` (click
  shape), `DoNotRepeatNestedMethod` now expects the freed slot used, CLI
  header string. `go test ./...` green. Uncommitted.
- Tier 1, pilot pair, Prism arm, 3 trials, `--concurrency 1`, harness
  75629a66 (continues past timeouts). 6/6 valid, 0 timeouts. Evidence:
  research `harness/results/prism-first-result-2026-09-13/batch-window-candidate2/`.

  | | v0.74.1 | 09:33 build | window build |
  |---|---:|---:|---:|
  | resolved | 2/6 | 3/6 | 3/6 |
  | Prism calls / follow-up-op cells | 1.7 / 1 | 2.0 / 2 | 2.2 / 2 |
  | native reads (discovery) | 5.5 (4.5) | 6.5 (5.3) | 5.3 (3.7) |
  | whole-file reads | 0.3 | 0.5 | 0.8 |
  | KiB read | 19.6 | 28.5 | 19.7 |
  | cost / cost per resolved | $0.39 / $1.18 | $0.40 / $0.79 | $0.44 / $0.88 |

  Under §E this is not a change: discovery reads and KiB fall back to the
  v0.74.1 level, whole-file reads and cost do not improve. One cell (click
  r1) is the intended shape — search, lookup, ONE 3 KiB native read,
  resolved — and two click cells read 26–27 KiB with a window delivered.
  Delivery works mechanically now; displacement is still the model's call
  and at n=6 it is a coin flip. Next per §E: full e2e set, native arm,
  ≥3 trials, urllib3 swapped (0/9 across builds). Nothing else on
  windows until that runs.

**A3. Keep the 2026-09-13 first-result change as is.**
It rendered correctly in live cells with no over-claims (scope disclosure,
labeled empty symbol scope, labeled token fallback, inline unique small
body). It was behaviorally neutral at n=6 — that is fine; do not extend it
further chasing these cells.

**A4. Hygiene, low priority.**
The locator footer repeats once per term in one response; emit it once.
A term like `EchoingStdin` returned 11 symbols the model never used.
Result size is 0.8–4 KB and is not the cost driver — these are tidiness,
not savings.

**A5. Do not.**
Do not synthesize negatives ("no references in src/") without having run
that search. Do not shrink results to save tokens — the cost is turns and
the fixed per-turn prefix, not result bytes. Do not tune anything to
`click/testing.py`'s shape.

## B. Harness (research repo)

**B1. Ready to use.** `bench.py run --suite e2e --agents claude --tools native,prism --trials 3`
gives native + Prism arms, thinking captured (`thinking_chars` per cell),
network blocked on both agents and every attempt recorded, `bench.py
rescore` to recover a crashed run, `bench.py index` for cross-run rows.
CodeGraph is a third arm (`--tools native,prism,codegraph`).

**B2. Known gaps.**
- `docs/BENCH.md` does not yet document `--tools`, `--codegraph-binary`,
  thinking capture, or the network policy.
- `--concurrency` only parallelizes arms within a wave; a single-arm run
  is sequential. Fix: one pool across waves.
- The network audit's definite tier is a command list (pip/curl/wget/gh/
  git/uv/npm); `python -c` with urllib is recorded as suspect only.
- The per-cell metrics below are computed by ad-hoc scripts; add them to
  `bench.py index`: Prism calls and op sequence, follow-up-op flag,
  native reads split into discovery vs edit-prerequisite (a Read
  immediately followed by an Edit of the same file), KB read, edits,
  turns, cost, resolved, blocked attempts. (`lib/cell_metrics.py` has
  appeared in the working tree -- fold the above into it.)
- Wall-time budget vs concurrency: 0 timeouts in 13 sequential cells; 2 of
  4 timed out (300 s, exit 143) with three cells running concurrently
  under the new cross-wave pool while the five-hour rate window was at
  77%. Runs at different concurrency are not comparable under a wall-clock
  budget. Either fix concurrency at 1 for comparisons, or budget by turns/
  tokens instead of seconds.
- Timed-out cells carry `cost_usd=None` because cost arrives only with the
  `result` event; their spend is real (2.5M and 1.9M cache-read tokens in
  the two cases). Reconstruct usage from per-message `message.usage` when
  the result event is missing, and mark it as reconstructed.
- Never run a comparison on a modified working tree without recording it:
  the Tier 1 run used uncommitted `bench.py`/`runner_core.py` edits; the
  manifest's runner sha does not correspond to any commit.

**B3. Task-set validity — do this before the next Prism decision.**
- The e2e tasks are merged upstream PRs the model partly remembers. It
  hunts for the fix (changelog, tests, `pip download`) in both arms; the
  block stops the leak but not the turns. Add tasks it cannot have
  memorized (post-cutoff PRs or private repos).
- `urllib3__urllib3__pr3786` is 0/6 across both builds at the 5-minute
  budget; it discriminates nothing. Replace with a task the model
  resolves about half the time.
- Use the full 8-task e2e set, not the 2-task pilot.
- Every batch includes the native arm.

**B4. Contamination note.** `sonnet-prism-cost-isolation-2026-09-08` has
five `pip download` cells counted valid (prism: s3-compact-t3, s3-json-t4,
s4-dedup-off-t1; native: s1-treatment-t2, s3-compact-t2). Uncited, but its
STUDY.md conclusions rest on both arms being contaminated; add a dated
note. Historical measurement.json files are evidence — do not rewrite.

## C. How to run the next comparison

```sh
cd research/harness
python3 bench.py run --suite e2e --phase full --agents claude \
  --tools native,prism --trials 3 --concurrency 3 \
  --prism-binary <build under test> --out /tmp/<name>
python3 bench.py index
```
Two runs (old binary, new binary) if the question is a Prism change; one
run if it is Prism vs native. Count only `audited_valid` cells. Quote the
time and cost before launching: a cell is ~1.5–5 min and ~$0.25–0.55;
single-arm runs are sequential until B2 is fixed.

## D. Cost structure to keep in mind (measured, general)

- Each turn re-reads ~26K tokens of context (mostly the fixed prefix) and
  emits ~550 output tokens (~55% thinking). A turn costs ≈ $0.015–0.02
  whatever it does. Turns are the unit of cost.
- Report `cost_usd` and cost per resolved cell, never raw token totals —
  cache reads are ~10x cheaper than input and raw counts double-count them.
- Single cells swing ~2x run to run. n=1 proves nothing.

## E. The rule

A change counts only if it moves the pre-registered metrics (second-call
rate, discovery reads, KB read, cost per resolved cell) across the full
task set, with a native arm, at ≥3 trials, on tasks the model cannot have
memorized. A change that only moves `click` is not a change.

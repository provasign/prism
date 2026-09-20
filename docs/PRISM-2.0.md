# Prism 2.0 — plan

Date: 2026-09-14
Status: proposal, not started

## Why

Two months of paired runs (through 2026-09-06) found no cost win and a recall
tie for Prism as a context-delivery layer. change_impact was reached in 2 of
190 cells. Discovery through Prism does not beat the shell, and agents route
around the "Prism first on every task" steering. The one failure shape the
graph can catch that the compiler cannot — a wrong-scope edit through
interface dispatch, dynamic-language callers, or a mandated wide fix taken
narrow — is the one agents did not consult the graph for (full38,
2026-08-17: graph consulted 8/198 calls; failures edit the right file with the
wrong fix).

Prism 2.0 narrows the product to that failure shape. The thesis changes from
"deliver context cheaper" to "keep the agent from being confidently wrong
about what its change touches." Token savings are not the goal and are not
expected. The measurable claim is a lower rate of wrong-scope diffs on tasks
where the compiler cannot enumerate the sites.

## Transcript evidence for where Prism is cheaper (2026-09-14)

Two structural guesses about "where Prism is cheaper beyond change_impact"
were checked against real transcripts and did not hold up. Recorded here so
they are not re-guessed.

**change_impact is barely used and the one real use lost.** Across the 38
paired full38 sessions (research/harness/results/swebench-live/full38), the
6-tool legacy surface (lookup/read/search/query/change_impact/verify) was
called with change_impact exactly once, on `a2aproject__a2a-python-443`. That
is the one cell in the set where baseline resolved and Prism did not, despite
Prism using more turns and more cost. n=1, proves nothing about the tool
itself, but it means the plan's item 1–3 investment rests on an evidenced
gap (agents don't call it, full38 2026-08-17: 8/198), not on evidence that
calling it wins.

**Batched multi-term search does not correlate with cost savings.** 25 of 79
search calls in the same set batch multiple terms in one call — the
"replaces N greps" pattern this plan assumed. Correlation between a cell's
baseline grep count and Prism's cost advantage: r = 0.066. Cells where
baseline used more greps averaged a *worse* outcome for Prism (-$0.25 vs
-$0.03). Cost in these sessions is dominated by cache-read tokens compounding
over turn count, not by call count — one cell reached 4.3M cache-read tokens
and $3.57 over 59 turns regardless of using batched search. Do not plan
around search-batching as a savings mechanism.

**The evidenced win: lookup on a known symbol, small exact payload.** From a
separate 66-task/942-cell e2e suite (research/harness/RESULTS-E2E.md), Rich
#3882 is a clean matched-patch case: `lookup(on_validate_error)` returns an
eight-line method at 458 bytes with line numbers in both trials; the agent
makes one edit and runs one test. Native uses Grep then Read, and in one
trial attempts an edit at the wrong path before retrying. Both arms land the
identical patch; the 36-55% cost delta is explained by Prism removing the
discovery steps, including the wrong-path retry, not by anything
graph-shaped. Five other tasks in the same suite (Dark Reader #6747, Clap
#4006, Click #3466/#3228, urllib3 #3786) show smaller or confounded deltas —
real numbers, but scoring stopped early, correctness unscored, or patches
failed on both arms, so they don't reach Rich's standard of evidence.

**The one thing that held up in aggregate: fewer redundant retry loops, not
fewer tokens.** The full38 scoring harness (research/harness/scoring/score_cell.py,
`retry_loops`) flags consecutive near-duplicate Bash commands (≥0.8 string
similarity) as redundant — an agent re-running a grep or test command because
it lost track. Across all 38 cells: Prism arm has fewer redundant loops in
18, baseline fewer in 11, tied in 9. That is a real, non-cherry-picked
pattern and it is the same mechanism Rich #3882 shows at the trace level: a
precise located answer prevents the wrong-path/re-derive loop. It is a
different claim than "cheaper tokens," and total cost/turns in full38 remain
a mixed bag — this pattern did not reliably translate into lower cost there.

The other one clear correctness win in full38, `sooperset__mcp-atlassian-581`,
made zero change_impact calls; it used 17 read/search calls tracing a
runtime-conditional (cloud vs. on-prem OAuth config) scattered across six
files with no shared identifier, where baseline's 4 greps missed it. This is
comprehension of dispersed logic, a case discovery can still win — but it is
one cell, and it argues for read/search staying available, not for mandating
them.

**read: a real repeat-read opportunity, no confirmed savings.** 30 of 111
prism_read calls in full38 (27%) are a repeat of a file already read earlier
in the same session, in 12 of the 38 sessions. That's the opportunity
internal/mcp/graphcache.go's `[prism:cached]` marker exists for. tool_calls
in these transcripts carry no per-call byte size, so whether the marker
actually shrinks the repeat payload is not verified here — plausible,
unmeasured. It is also partly offset in Claude Code specifically: the host
requires a native Read before Edit regardless of a prior prism_read, so some
of this cost is paid twice no matter what Prism does (see "What is not
built" below).

**query and verify: confirmed zero use, not confirmed useless.** Both were
on the menu for all 38 full38 sessions. Neither was called once. That is
real evidence agents don't reach for them under current steering; it is not
evidence the capabilities themselves are worthless, since full38's task mix
may simply not contain a removal or rename-shaped task that would pull
verify in. Cutting both from mandatory steering is supported either way —
either they're not needed, or they're needed rarely enough that mandating
them for everyone is the wrong trade.

**Everything past the 6-tool legacy surface remains unmeasured.**
missing_implementations, rename_plan, dead_code, and removed_symbols have
zero transcript evidence in either dataset checked (full38, and the tasks
sampled from the 66-task e2e suite) — not one call. They stay structural
arguments only until a bed with real interface-implementation, rename, or
dead-code-removal tasks is run through them.

**Net:** the evidenced cheap case is lookup on a symbol whose name the agent
already has, not search, not batching, not change_impact. The plan below
still keeps change_impact as the pre-edit obligation because it targets a
correctness failure the transcripts show agents skipping (full38 8/198), not
because it targets a cost win — the two are different claims and should not
be conflated when reporting results. The metric that actually moved in
aggregate — fewer redundant retry loops, 18/38 vs 11/38 — is a better signal
to track for 2.0 than token cost, since token cost did not move reliably in
this data.

| Tool                     | Status                        | Evidence |
|---------------------------|-------------------------------|----------|
| lookup                    | evidenced cost win             | Rich #3882, matched patch |
| search                    | evidenced win, narrow trigger  | sooperset-581, dispersed logic only — not noisy-grep in general |
| change_impact             | correctness obligation, not cost | full38: 1/38 calls, that cell lost; kept for the 8/198 gap, not for savings |
| read                      | plausible, unmeasured           | 27% repeat-read rate is real; savings and host double-Read offset unverified |
| query                     | confirmed unused                | 0/38 calls, cut from steering |
| verify                    | confirmed unused                | 0/38 calls, cut from mandatory steering, kept for removed_symbols |
| missing_implementations, rename_plan, dead_code, removed_symbols | unmeasured | zero calls in either dataset sampled |

## What stays, what goes

Stays, unmandated: lookup, read, search, query, verify. lookup on a known
symbol is the one call with a clean matched-patch cost win in the transcript
evidence above (Rich #3882) and should be named in steering accordingly, not
folded into "search." Read stays for the repeat-read case (27% of reads in
full38 repeat a file already read that session) even though the saving
itself isn't confirmed. Search stays for the dispersed-logic case
(sooperset-581), not as a general grep replacement. Query and verify stay
available but unmandated — both had zero calls across 38 sessions, so
neither belongs in steering as a default move; verify keeps its place for
removed_symbols specifically (docs/verify-findings-2026-09-12.md).

Goes: the steering and server instructions that make Prism the first action
on every task, and the unconditional full-verify closing step (already argued
in docs/verify-findings-2026-09-12.md, item 2).

Steering after 2.0 is one obligation plus two named cheap cases:

> Before editing an exported, overridden, or dynamic-language symbol, call
> impact on it and relay its sites and gaps. Use the shell for everything
> else. Call prism lookup, not search, when you already know the symbol's
> name — it returns the exact body directly. Use prism search only when the
> literal grep is noisy, empty, or the logic is dispersed across files with
> no shared identifier.

## The product: impact as a decision, delivered at the edit moment

### 1. Reshape the impact payload (rendering only, no engine change)

Today toolChangeImpact (internal/mcp/tools.go) returns declarations, supers,
family, callers with an isTest flag and call evidence, completeness,
callerCoverage, and prose coverage notes. Everything needed is present; it
reads as a list, not a decision.

New shape, three top-level sections:

- `sites` — every affected site with a role tag: declaration, override,
  implementation, caller, declaring-type. File, line, kind, call evidence
  where it exists.
- `tests` — inbound callers in test files, separated out. Empty list is a
  finding, not an omission.
- `gaps` — structured, not prose. One entry per unresolved thing:
  `{kind: dynamic-callers|generic-interface|external-contract|heuristic-refs|
  ambiguous-declaration, detail, suggested-check}`.

Plus `completeness` and `callerCoverage` as today. The existing coverage
notes in internal/mcp/impactcoverage.go become gap entries.

Both JSON and the text renderer (internal/mcp/graphtext.go) change. Tests
that pin the current shape (evidencedelivery_test, impactcoverage_test,
wideranchor_test) are updated, not weakened: every site they assert today
must still appear.

Gate: `go test ./...` and ci_invariants (site recall/precision must not
move). No A/B — rendering only.

### 2. Summary mode (the cheap negative)

Same op, flag `summary: true`. Returns counts per section, completeness,
callerCoverage, and gap kinds. Target: under 100 tokens. Reuses the computed
result; only the emit changes.

Purpose: makes the pre-edit call cheap enough to make on every contract
edit, and lets the agent skip the full payload with evidence ("one caller,
same file, no tests, indexed") instead of on a hunch.

Gate: unit tests. No A/B.

### 3. Diff-based impact (reframe the verify seed walk)

internal/mcp/verify.go already derives seeds from `git diff --unified=0
<base>` plus untracked files, filters to function/method/constructor in
non-test files, and runs a per-seed impact walk. It is framed and priced as a
terminal gate and fires in ~7% of cells, at the end.

New: `op=impact` accepting either `name` (pre-edit, symbol known) or `base`
(post-draft, diff known). The `base` form runs the seed walk and returns the
section-1 payload per seed, with no verdict and no gateFailure. verify keeps
its verdict semantics for the closing-gate use and for removed_symbols.

Two moments, one payload:

- before drafting: `impact(name=X, summary=true)` — decide scope before
  choosing a design. This is the call that catches narrow-fix-where-wide-was-
  mandated; the diff form is too late for that.
- after drafting: `impact(base=HEAD)` — what the actual patch touches.

Cost note: the diff form delta-reindexes first. Measure it on a large repo
before advertising it as mid-loop.

Gate: unit tests; ci_invariants unchanged; one-unit probe on a real task
before any fleet run.

### 4. Delta payloads through the session cache

internal/mcp/graphcache.go already dedupes whole-repo results per session
and the read path emits `[prism:cached]` for delivered files. Extend to
impact: a repeat impact on the same symbol in a session returns site
identities plus a content hash, not bodies or call evidence, unless the
index changed. Cache reads compound per turn, so repeated payloads are the
expensive kind.

Gate: unit tests. Small.

### 5. Delivery at the edit moment

This item decides whether 1–3 get used. Without it the obligation is a
sentence in a file, and 2/190 is what a sentence in a file gets.

Evidence to respect:

- Blocking channel with consequence steered ~100% (v0.50 deny hook).
- Advisory nudge in the same channel failed replication at n=2.
- The substitution hook (deny grep on every call) lost to a plain context
  cap on cost (R1, 2026-08-30). Do not rebuild that.

This hook is different in scope: it fires on Edit/Write only, and acts only
when the enclosing symbol at the edit range is exported, an override, or in a
partial-coverage language, and impact has not been called for that symbol
this session. Then it denies once with the summary-mode payload as the
reason and passes on the retry. Every other edit passes silently.

Per-harness adapters over one shared path (resolve symbol at file:range →
summary → decide):

| Harness     | Mechanism available          | Adapter                                  |
|-------------|------------------------------|------------------------------------------|
| Claude Code | PreToolUse on Edit/Write     | deny-once with summary in reason         |
| Codex       | PostToolUse (no pre-edit)    | post-edit report against diff since last firing |
| Cursor      | afterFileEdit                | same as Codex                            |
| none / CI   | —                            | `prism impact --base` as a check humans see |

Codex agents often edit via apply_patch or shell, so the post-edit adapter
must recover changed ranges from the working tree, not tool args. The diff
seed walk from item 3 already does this; use it for both post-edit adapters.

Prerequisite: a warm path. Hooks run the CLI, and the CLI reopens the index
(4.05s cold vs 0.06s warm on a 1.1GB index). A hook that adds seconds to
every edit gets turned off. Options: a socket to the running MCP process, or
a small daemon the CLI dials before falling back to opening the index.
Build and measure this first; item 5 does not ship without it.

Gate: this is the only item with a behavioral claim. It needs the fail-fast
A/B gate (research/harness/ab_gate.py) before any tag, and the bed must be
mandated-wide-fix and dynamic-dispatch tasks — the only bed where the effect
can exist. Baseline arm carries zero Prism steering. n=2 is not evidence.
Post-edit report adapters need the same gate separately; "fact about your
edit" is not the advisory nudge that failed, but that is a hypothesis.

## What is not built

- Ranking callers by "would change my diff." Needs dataflow. Call-expression
  evidence is the cheap approximation and already exists.
- Making prism read satisfy the host's read-before-edit rule. Host-enforced;
  keep the one tight native Read at the edit site.
- Any discovery-side work (query ranking, search budgets). Not the product.
- A sharper failed-edit-retry metric (pairing Edit/Write tool calls to their
  `is_error` tool_result, distinct from the existing Bash-based `retry_loops`
  proxy). Considered 2026-09-14, rejected: it isn't the gate — item 5's
  success criterion is wrong-scope diffs, and retry-loop count is a secondary
  diagnostic the existing Bash-only metric already gives for free. The
  sharper version needs new capture in two runners (`user`-stream
  `tool_result` blocks aren't recorded anywhere today), can't be backfilled
  onto full38 or the e2e suite, and needs a fresh paid run before it produces
  one number — cost with no decision riding on the answer. Use the existing
  `retry_loops` as the diagnostic; revisit only if it turns out too noisy to
  read.

## Order and effort

1. Items 1 and 2 — rendering + summary flag. About a day. No A/B.
2. Item 3 — impact op with name|base. Reuses verify. Measure diff-form
   latency on a large repo.
3. Item 4 — cache extension. Small.
4. Warm path (socket/daemon). Measure hook latency before writing the hook.
5. Item 5 adapters, Claude Code first, then Codex/Cursor, then CLI.
6. Steering rewrite: cut to the one obligation. Cross-layer sweep
   (CLAUDE.md, serverInstructions in internal/mcp/server.go,
   steeringInstructions in internal/cli/commands.go, HANDOFF.md, harness
   isolation lists).

## Success criterion

On a bed of mandated-wide-fix and dynamic-dispatch tasks, the Prism arm
produces fewer wrong-scope diffs than baseline, with tokens flat or lower.
If it does not, the graph does not pay for agents and the program stops.
That answer is worth as much as the other one.

Track redundant retry loops (research/harness/scoring/score_cell.py,
`retry_loops`) alongside wrong-scope diffs and cost. It's the one aggregate
metric that favored the Prism arm in the transcript evidence above (18/38 vs
11/38) when cost did not, and it's the mechanism the lookup and read wins
both run through — a precise located answer preventing a wrong-path retry or
a re-derive loop.

## Standing rules that apply

- No tag without `go test ./...` and ci_invariants green.
- One unit through the identical pipeline before any paid run.
- Behavioral claims need the fail-fast A/B gate; PASS means "not broken."
- Cross-layer sweep before declaring done.

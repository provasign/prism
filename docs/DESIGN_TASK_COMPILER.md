# The Task Compiler: `prism(task)` — One Tool, Two Moments

Status: v1 in progress (branch task-compiler) · Date: 2026-07-19

## Thesis

Prism's engine wins are measured (change-impact recall 0.997 vs 0.62–0.75 for
grep agents; verify precision 1.0 on 16 real shipped commits), but its surface
loses: the e2e pilot showed agents do not route among 16 tools — they grep.
Even the arm named after the graph called it once and grep twelve times.
Discretionary routing across a steering table is the measured failure mode.

The fix is a product consolidation, not a feature: **one tool, one
instruction, two moments.**

    prism(task="...")                          # before editing: prepare
    prism(task="...", changed_files=[...])     # after editing: verify

The agent supplies the developer's task. Prism compiles it into an
evidence-backed working set: what to read, what must change, what proves it.
After the edit, the same call verifies the diff against both the actual
contract changes it contains and the obligations recorded at prepare time.

The daily instruction shrinks from a 16-row table to two sentences:

> Before working on a codebase task, call `prism` with the complete task.
> After editing, call it again with the changed files and resolve anything
> it reports missing.

## Corrections to the proposed design (what our evidence forced)

The external proposal (GPT draft, 2026-07-19) is adopted with five
corrections, each grounded in something this project already measured or
committed to:

1. **No semantic-retrieval promises.** Anchor discovery is term extraction +
   TF-IDF + agent-supplied terms + graph expansion (the existing
   `selectContext`). Deterministic, same-input-same-output. Determinism is
   the moat; "semantic" retrieval is the crowded market we win by avoiding.

2. **No behavior-path narratives.** "Resolve Behavior" as end-to-end route
   tracing (HTTP route → middleware → handler → persistence) is an L4 flows
   feature, and flows are a research track until the asymmetric claim rule
   is enforced in the schema (DESIGN_LAYERED_INTELLIGENCE.md). v1 delivers
   the one-hop typed call neighborhood per anchor (callers, callees,
   covering tests) — every hop a real edge, no synthesized paths.

3. **Obligation classes are evidence tiers, not editorial guesses.** The
   draft's Required / Conditional / Context / Unknown becomes:
   - `required` — closed change-impact set (type-resolved, authoritative)
   - `required-project-local` — same, but external contracts may exist
   - `callers-only` — bare-function fallback (resolved callers, no family)
   - `unknown` — impact not computable; surfaced, never silently dropped
   "Conditional" (relevant-depending-on-implementation) is a judgment call
   we cannot compute deterministically; presenting it as a class would be
   heuristic authority. The agent gets context symbols ranked as today.

4. **Prepare-time obligations are anticipatory, and verify treats them so.**
   The verify verdict is driven by the *diff's actual contract changes*
   (the shipped `prism_verify` fail-closed pipeline: missed sites →
   incomplete; unverifiable seeds → review). Stored obligations whose sites
   the diff did not touch are reported as `unaddressedObligations` with an
   explicit caveat — the agent may legitimately have chosen an
   implementation that never changed that contract. They inform; they do
   not accuse. This preserves the precision-1.0 property that makes the
   gate installable (a checker that cries wolf gets uninstalled).

5. **The second moment must not depend on discretion.** The unified tool's
   verify mode is a convenience for cooperating agents; the same check
   ships as `prism verify` (CLI, hook, CI gate) and runs whether or not
   anyone calls it. A discretionary verify call is skipped exactly when the
   agent is most confident — which is when it is most dangerous.

## The pipeline

### Prepare — `prism(task)`

1. **Anchors** — `selectContext(task, terms?)`: existing ranked selection.
2. **Delivery** — edit-ready line-numbered source windows + per-anchor
   callers/tests (existing `deliverSource`); phase-aware budget.
3. **Obligations** — for each anchor of kind function/method/constructor
   (top N): `changeImpactFor` (change_impact; bare-function callers
   fallback) → required sites (family + callers + declaringTypes), each
   with file:line, tagged with the completeness tier above.
4. **Tests** — covering tests from selection; coverage gaps (blast-radius
   symbols with no test edges) via existing `buildCoverageGaps`.
5. **Persist** — the task package (task, git base, obligations) is written
   to `.grove/task-package.json` so verify can compare later — including
   from a different process (the CLI, a hook, CI).

### Verify — `prism(task, changed_files=[...])` or dirty-tree auto

1. Run the shipped `prism_verify` pipeline (contract-change detection,
   required-set computation, line-precise missed sites, affected tests,
   arch gating, fail-closed verdicts).
2. Load the stored task package; if its task matches, check every recorded
   obligation site against the diff: touched → satisfied; untouched →
   `unaddressedObligations` (informational, caveated).
3. `changed_files`, when provided, is cross-checked against the actual git
   diff; files claimed but unchanged (or changed but unclaimed) are noted —
   the diff is authoritative, the argument is a hint and a mode trigger.

Verdicts are unchanged from verify: `clean | complete | review |
incomplete`, fail-closed.

## Mode resolution

- `changed_files` present → verify.
- `mode="prepare"` / `mode="verify"` → explicit override.
- Otherwise → prepare. (No dirty-tree guessing: a developer's WIP must not
  silently flip a prepare call into a verify of unrelated changes.)

## What happens to the existing tools

Nothing is removed. `prism_query`, `prism_change_impact`, `prism_verify`,
`prism_affected`, `prism_missing_implementations`, `prism_map`, … remain
registered — they are the engine the unified tool orchestrates, the advanced
surface for agents that know exactly what they want, and the CLI/CI surface.
They move from the front of the steering instructions to an "advanced"
section; the two-call workflow becomes the lead.

## Success gate (unchanged from the external proposal — kept verbatim in spirit)

Pursue "default everyday tool" only if the unified surface demonstrates,
on held-out everyday tasks (fix / implement / explain / refactor / review):

- materially higher completed-task correctness than a native agent,
- fewer missed change obligations,
- lower tokens at equal-or-better correctness,
- fewer turns (third-order).

LLM-in-the-loop runs are expensive and noisy: multiple trials, explicit
budget approval before each campaign (measured rule: single-trial e2e reads
are coin-flips). If the gate fails after a focused effort, Prism repositions
honestly as the strongest deterministic change verifier — the gate that runs
in hooks and CI — and the unified tool remains as its front door.

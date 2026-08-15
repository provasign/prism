# Handoff — prism deployment reset, 2026-08-15

Written at the end of a session that evaluated prism's v0.44–v0.51 arc, found
it did not pay for itself, and reverted it. Read this before touching the
steering, the hook, or `prism init`.

**Do not trust the older memory entries or `research/RESULTS.md` §1–§7 as a
description of current behaviour.** They describe the arc that was just
reverted. §8 and §9 of RESULTS.md are still accurate and load-bearing.

---

## 1. Where the code is

Three unpushed commits on `main`, on top of `origin/main = eb1e48b (v0.51.0)`:

| commit | what |
|---|---|
| `2daf496` | revert: restore the v0.43.0 deployment surface in full |
| `a2a351d` | feat: project-level init by default (+ a scope bug fix) |
| `012ea9c` | chore: drop the grep/rg denial from this repo's own settings |

- Working tree clean. `go build`, `go vet`, `go test ./...` green (12 packages).
- **Not pushed, not tagged.** `origin/main` is still v0.51.0.
- `ci_invariants.py` has **not** been run against this tree. Required before
  any tag, per the standing rule.
- The binary at `~/bin/prism` is built from this tree and reports `prism dev`.

The tree is byte-identical to `v0.43.0` except for the two feature commits and
`.shale/` (session evidence, deliberately preserved — it is the audit record,
not product code).

## 2. Why the arc was reverted

Not on argument — on a paired measurement anyone can re-run.

`research/harness/runs/swebench-live/` (`ab38-f1`, `ab38-t1`, `ab38-t2`,
`haiku38`, `sonnet38`) holds 190 paired cells: same task, prism arm vs
baseline arm, with per-cell tokens, cost, turns and full tool traces.

**Cost was a coin flip.** Median paired delta −$0.054; prism cheaper in
107/190. The sign flips per bed: ab38-t1 −$0.270, ab38-t2 −$0.294, but
ab38-f1 +$0.116 and sonnet38 +$0.101.

**The tool mix explains it:**

| tool | calls | cells |
|---|---:|---:|
| `prism_search` | 637 | 95/190 |
| `prism_read` | 166 | 53/190 |
| `prism_lookup` | 55 | 29/190 |
| `prism_query` | 38 | 35/190 |
| `prism_change_impact` | **5** | **2/190** |
| map, dead_code, rename_plan, missing_implementations, arch_check, node, index | **0** | **0** |

The deployment routed reliably — and routed to `prism_search(scope="text")`,
which is *defined* to return what ripgrep returns. So the call carrying the
measured 9.3× token win (`RESULTS.md` §9.1) was reached in 1% of cells, and
8 of 14 always-loaded tools were never called at all.

**Mechanism worth keeping in mind:** cache reads dominate (700k–3.2M tokens
per cell vs 24–62k fresh). A large tool result is paid once fresh and then on
every later turn. A 5 KB result at turn 3 of 28 is paid ~25 times. **Payload
size matters more than call count.**

No engine change was involved: `git diff v0.43.0..HEAD -- go.mod` was empty
across the entire arc. Grove and astkit are untouched, so the multi-hop
receiver-chain and interface-satisfaction gains are intact. v0.43.0 already
carries the symlink-containment check in the native scanner, so nothing
security-relevant was reopened.

## 3. What is actually true about prism

These survive the revert and should anchor any future work:

- **The graph finds nothing grep misses.** 0 graph-only resolved references
  across 127 symbols, 6 repos, 4 languages (§8.2.2). What it does is *filter*:
  file-level precision 0.51 (grep) → 0.91 (change-impact).
- **The win is concentrated.** Opus on change-impact tasks: grep reaches
  recall 1.000 in 26.8 turns / 836k tokens / $1.66; prism reaches 0.987 in
  4.2 turns / 90k / $0.27 (§9.1). On ordinary bug fixes it is parity (§8.1).
- **e2e resolve rate is the wrong instrument** (§8.2.1): cost moved 46% → 2%
  and turns 31 → 3 while resolve rate moved not at all.
- **Agents decline graph tools when a text tool is present.** Measured twice,
  independently — the 190-cell mix above, and the §8.2 codegraph arm that made
  0 calls in 6 cells.

External work agrees and is worth citing:

- **ContextBench** (arXiv 2602.05892, 1,136 tasks): a plain-bash agent beats
  graph-based scaffolds on file-level context F1, 0.634 vs 0.403. Aggressive
  retrieval "mainly increases token consumption."
- **What Context Does a Coding Agent Actually Need?** (arXiv 2607.09691):
  structural representations null at p=0.75, N=70 — but *compression* wins
  big, 19K vs 94K tokens per resolution.
- **SWE-Explore** (arXiv 2606.07297, 848 instances): context efficiency
  correlates with repair success at r=0.95. Removing 25–50% of core evidence
  collapses resolve rate; adding redundant regions barely matters. **Missing
  evidence costs far more than noise — never let a filter drop silently.**
- **Serena / ManoMano**: the nearest comparable product is ~4× the cost and
  60% slower on simple lookup, mandatory on deep refactors. Same asymmetry.

Full write-up, with the measurements and the seven-change proposal:
https://claude.ai/code/artifact/c9409bf3-90c7-47b3-934c-d132ad0b7384

## 4. Known gaps at this base (accepted, not accidental)

- **No path-scoped search.** `prism_search` has no `path=` / `context=` /
  `files_only=`. The v0.51.0 mining found **511 of 946** real grep invocations
  were path-scoped, so scoped questions now return whole-tree results. This is
  the loss most likely to bite in daily use, and the strongest candidate for
  re-landing on its own.
- **No PreToolUse hook.** `prism hook pretooluse` does not exist at this base.
  Any stale registration pointing at it makes every Bash call shell out to an
  unknown command — two such registrations were removed from this workspace.
- **Steering is the long 11.8k-char block** teaching the ToolSearch
  deferred-tools dance. That is correct here: `alwaysLoad` is gone too, so
  client-side deferral is back and the two agree.
- **Tools are deferred, not resident.** `.mcp.json` has no `alwaysLoad`.
  14 tools, ~18.3k chars of schema, loaded on demand via `ToolSearch`.
- **`prism init` writes deny rules only when asked.** It has no path that
  *removes* them; that is manual. This repo now ships none.

## 5. If the deployment layer is rebuilt

The proposal from the evaluation, in priority order. A+B+C carry almost all
the value; none requires the agent to learn anything new.

1. **Deny only what prism can answer better.** Classify the pattern; if it
   does not resolve to an indexed symbol, let the search run. 511 path-scoped
   + 289 pipe-filter invocations = **85% of real grep usage the graph cannot
   improve**. The v0.51.0 commit message has the full mined breakdown.
2. **Answer inside the denial.** `permissionDecisionReason` is fed back to the
   model, and the hook is a prism process with the index open. Return the
   resolved site list instead of an instruction to go fetch it. Removes the
   retry turn entirely.
3. **Default payloads to locations, not bodies.** See the cache-read
   compounding note in §2.
4. Defer the tools nobody calls; the measured routing-critical set is four:
   `search`, `read`, `lookup`, `query`.
5. Exclude agent-state dirs (`.shale`, `.claude`, `.cursor`) from text search.
   `excludeDirs` is only `[".git", ".grove"]`, so prism reads its own session
   transcripts as source.
6. Do not re-ship the consolidation nudge. Three replications, three ignores.
7. Never let the graph narrow a result silently — state both counts.

**Gate any of this before shipping:** replay the 946 mined grep invocations
through the classifier offline (no LLM, seconds) and report token delta plus
how many get a better answer. Then `ci_invariants.py`, then `go test ./...`.

## 6. Things that will surprise you after a restart

- prism's tools will **not** be in the tool list. Load them with
  `ToolSearch("select:prism_query,prism_search,prism_change_impact")`.
- **grep and rg work.** No denial, no hook, anywhere.
- `CLAUDE.md` and the eight sibling steering files carry the long v0.43 block.
- The parent workspace `/Users/tapabratapal/Projects/provasign/` is **not a
  git repo** — changes there have no undo. Back up before editing.

## 7. Bugs found and NOT fixed (all reverted away, all still real upstream)

Recorded so they are not rediscovered from scratch:

- `permissions.deny` **beats** a PreToolUse hook's `allow`. Verified directly.
  This made v0.51.0's pipe-filter fix inert in the shipped config: the hook
  returned `allow` for `git log | grep x` and the call was blocked anyway.
- The v0.51.0 deny reason's CLI form omitted `--scope text`, a **3.8×**
  overcharge (5,126 vs 1,348 bytes) printed by the product's own
  highest-compliance string.
- ~~`prism search --help` runs a search for the string `--help`.~~ **FIXED in
  v0.52.1**, along with the worse sibling found on 2026-08-15: `prism init
  --help` ran a *full init*, writing `prism.yaml`, all nine steering files and
  every project MCP registration. One mechanism behind both — nothing parsed
  `-h`/`--help` before the command body. `Run()` now answers it before
  dispatch for every subcommand. A bare `help` argument is still a query term,
  so `prism search help` searches.
- `grepCommandPattern` treats `(` as a command separator, so any Bash command
  whose *text* contains `(grep` is denied — it fired on a Python script
  manipulating a deny list. Fails closed, so a nuisance rather than a hazard.

## 8. Loose ends

- `scratchpad/steering-fixes-uncommitted.patch` (1,152 lines) — the abandoned
  intent-routing steering rewrite from earlier in the session. Written against
  the v0.51 base; salvage ideas, not the diff.
- The workspace `CLAUDE.md` has a user-authored `## MCP Tooling` section that
  predates all of this and still says to use `prism_*` for "file reads, symbol
  lookup, search". It is the user's, left alone, but it sits against §3.

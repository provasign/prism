# Ranking audit — search / query / select (2026-09-12)

Scope: `internal/ranking/*`, `internal/mcp/selection.go`, the symbol pass of
`Handler.searchOne` (`internal/mcp/tools.go`), `groupPickedByFile`
(`internal/mcp/delivery.go`).

Status: Historical pre-remediation review; names and line numbers below describe the audited snapshot, not the current code.

Judged against what a ranker for a coding agent should do, not against what the
comments say it does. The standard used:

1. Rank by the symbol's relation to the task's anchors. File-level popularity or
   recency may break ties; it must never outrank a graph edge or gate delivery.
2. Every signal that carries weight must be able to vary. A signal that is
   always zero is a lie in the profile table.
3. `budget=` means the tokens the agent will receive. Not a fraction, not a
   number that excludes the largest component.
4. Same index + same arguments = same result on any machine. No hidden inputs.
5. A sample is the top of the ranking. Post-processing that reorders must run
   before the cap, not after.
6. A result says why it matched. Name, signature, package, body, and test are
   different kinds of evidence and the agent must be able to tell them apart.
7. Dead code paths that shape ranking (or look like they do) are removed, not
   retained.

Line refs are to the tree at HEAD `d416f94` plus the uncommitted working tree;
`tools.go` was changing during the review, so its refs may be ±70 lines.

---

## Bugs

### B1. Recency / edit-frequency outrank the call graph and cut real neighbors

**Should be:** a symbol with a verified call edge to a seed always ranks above a
symbol with no edge. Git signals refine order within a tier.

**Is:** `Recency` and `EditFrequency` are computed per FILE
(`signals.go:61-63`, `gitStats(sym.FilePath)`) and carry combined weight 0.467
in the default profile vs 0.333 for `GraphDistance` (`profiles.go:66-69`).
Worked, default profile:

| candidate | graph | recency | edits | score |
|---|---|---|---|---|
| direct caller of the seed, file untouched 1y | 0.5 | 0.076 | 0 | **0.19** |
| leftover seed, no edge, file edited today ×20 commits | 0.25 | 1.0 | 1.0 | **0.55** |

`ScoreCliffFactor = 0.6` (`budget.go:65`) makes the cutoff 0.33. The seed's own
caller is dropped from the result because an unrelated symbol lives in a hot
file.

**Corollary — the benchmark bed cannot see this.** On a pinned corpus every file
is ≥365 days stale, so recency saturates at 0.076 and edits are 0 for all
candidates (`signals.go:99-106`). Scoring degenerates to graph-only and the
cliff cuts every non-neighbor. The query-oracle bed and a live repo run
different rankings; a PASS on one says nothing about the other.

**Fix direction:** make graph relation a tier, not a weight. Sort by
(has-edge, score) or clamp git signals to a small fraction of the graph signal.
Add an invariant test: a candidate with `graphDist==1` is never cut by the cliff
in favor of one with no edge. Add a live-repo oracle cell (recent commits on
unrelated files) so the bed exercises the hot-file regime.

### B2. `budget=` delivers ~25% of its value

**Should be:** `budget=8000` means up to ~8,000 tokens of delivered context, and
the largest component (seeds) is governed by it.

**Is:** `categorize` (`tools.go:~3228`) returns only `Test`, `Doc`, `Dependency`.
Against `CategoryShares` (`budget.go:32-38`):

| bucket | share | reachable by a candidate |
|---|---|---|
| Target | 35% | never — skipped at `budget.go:135`; seeds are uncharged regardless |
| Dependency | 25% | yes |
| Test | 20% | never — `includeSet` defaults to `{graph}` (`tools.go:~1111`), tests filtered at `selection.go:512` |
| Doc | 10% | only with `include=docs` |
| Summary | 10% | never assigned |

Non-seed code gets 2,000 of 8,000 tokens. Seeds — up to 10, method seeds
unconditionally full (`budget.go:112`) — are outside the budget entirely.
`selection.go:559` calls the explicit budget "a contract: honor it exactly".

**Fix direction:** one pool. Charge seeds; drop the category shares (or keep
only a doc cap); let the cliff and per-file cap do the shaping they already do.
Test: `sum(TokenCost)` of the picked set ≤ budget, and ≥ 0.8×budget when enough
candidates exist.

### B3. `TestRelevance` is always zero for anything scored

**Should be:** a signal with 20-28% weight varies.

**Is:** `hasTestEdgeID` is set only inside the `seedSyms` loop
(`selection.go:428`); seeds bypass `Compute` and get `Score: 1.0`
(`budget.go:123`); candidates are disjoint from seeds. `testFilePaths`
(`selection.go:377`) is declared and read (`:536`) and never written. So every
scored symbol has `TestRelevance == 0`, and `LearnedWeights` is nudging it
(+0.04 persisted for `fix_bug`).

**Fix direction:** either compute it for candidates (`InboundCallers` per
candidate is too expensive; a cheaper proxy is "shares a file with a verified
test caller of any seed") or delete the signal and renormalize the profiles.
Do not leave a weight on a constant.

### B4. `GraphDistance` is binary, not a distance

**Should be:** what `signals.go:41` says — BFS distance from any seed, so a
depth-2 callee ranks below a depth-1 callee and above an unconnected symbol.

**Is:** the only write is `graphDist[nb.ID] = 1` (`selection.go:413`). Values
are 0.5 (neighbor) or ≈0.25 (unconnected, `3 + i/10` at `:534`). The fallback
ties ten candidates at a time and resolves by file path alphabetically.

**Fix direction:** either run the BFS to depth 2-3 over `CallNeighbors` with a
node cap, or rename the signal `HasCallEdge` and make it a tier (see B1). Half
measures — a comment that says BFS over a boolean — are the worst option.

### B5. Learned weights are hidden, unversioned, machine-local ranking state

**Should be:** rule 4. If a reinforcement loop exists it has a real signal,
is inspectable, and is excluded from determinism claims.

**Is:** `~/Library/Caches/prism/weights/` holds 2,330 files on this machine,
1,932 with nonzero adjustments; every query loads and applies them
(`selection.go:369`, `learned_weights.go:46-83`). The only production writer is
`toolFeedback` (`tools.go:~2719`) with sentinel paths: each positive rating adds
+0.02 `GraphDistance` to profile `"default"` regardless of the profile used.
Files also carry adjustments from callers that no longer exist and the removed
`SemanticSimilarity` key. `Apply` does not renormalize, and
`RelevanceThreshold`/cliff are absolute, so nudges change disclosure, not just
order. `feedback` is not a compact op, so on the shipped surface this state is
read-only and permanent.

**Fix direction:** remove `LearnedWeights` and `Apply`. The measured basis for
it (`profiles.go:5-17`) was removed 2026-08-01; what remains has no signal.
If a loop is wanted later, feed it from cited-vs-delivered paths in real diffs,
and version the file.

### B6. Symbol search caps before it demotes test doubles

**Should be:** rule 5. With `limit=25`, the 25 delivered are the 25 best.

**Is:** `searchOne` fetches `symCap` (`tools.go:~1945`), slices (`:~1952`),
then partitions doubles to the end (`:~1967-1974`). Observed: scoped search
`"Rank"` in `internal/ranking`, limit 25 → 12 of 25 slots were `bench_test.go`
doubles; `Select`, `Score`, `Render`, `Compute` — non-test, in scope — were cut.

**Fix direction:** fetch `symCap × k` (or the hard max), partition, then cap.
Test: for a query where non-test matches ≥ limit, the delivered sample contains
no test doubles.

### B7. Symbol-search tiers are not what `tools.go:~1909` claims, and match reasons are invisible

**Should be:** rule 6, and exact > prefix > substring on the NAME before any
signature/package/body match.

**Is (observed, Grove v0.49.0, prism delivers its order untouched):**
- `"rank"`: name matches (2) > signature-text matches (`groupPickedByFile`,
  `symbolWindows` — `ranking.BudgetedSymbol` in the parameter list) > package
  members `ranking.*` > tests.
- `"Select"`: exact `Select` first; prefix `SelectProfile` at #7, below
  substring `selection`, `selectedContentHits`.
- `"session"`: a README document and the `helpText` const outrank every
  `session.*` package symbol.

The agent sees `groupPickedByFile` for `"budget"` with no indication that only a
parameter type matched.

**Fix direction:** prism re-tiers on delivery: `name-exact`, `name-prefix`,
`name-substring`, `qualified`, `signature`, `body`, `doc`, `test` — and labels
each row with its tier. Grove's internal order becomes the within-tier order.
Grove's own rule could not be inspected (module cache is client-only); confirm
against the Grove repo before assuming it will change upstream.

### B8. Ranking regime differs between benchmark and live repos (restated as its own bug)

B1's corollary deserves its own line because it is a measurement bug, not a
ranking bug: `ci_invariants` and the query-oracle bed run in the saturated
regime and can pass while the live-repo regime regresses. Until B1 is fixed,
any ranking change should be gated on both a pinned cell and a cell with fresh
unrelated commits.

---

## Improvements (not bugs — the design is doing what it was told, and it is the wrong thing)

### I1. Seeds are unbounded

Up to 10 seeds (`selection.go:364`), method seeds always full and uncharged
(`budget.go:112`). Ten 200-line methods is 8,000+ tokens before any candidate is
considered. Charge seeds against the pool (B2) and let the per-file cap demote
the tail to signature.

### I2. Delivery groups by file and sorts by best score — the second-best file's best symbol can sit below the first file's worst

`groupPickedByFile` (`delivery.go:392-422`) sorts files by their top score, then
`symbolWindows` sorts within a file by line. Rank order is preserved only at
file granularity. Acceptable if intended; should be stated in the output
("files ordered by best match; windows in file order") so the agent does not
read the second window as the second-best result.

### I3. Text hits are unranked

`renderedTextSearchHits` delivers rg walk order. `rankSourceFirst`
(`textsearch.go:524`) exists; whether the search path applies it was not
verified. Even so, "source before tests" is the only tier. A definition line
(`func foo`, `class Foo`) should rank above a use, and a use in a caller of a
seed above one in an unrelated file. The graph is available; text hits ignore
it.

### I4. Dead phase code

`phase.go` (`DetectPhase`, `ShapeForPhase`, keyword tables) is dead per
`selection.go:65-79` and still compiled, tested, and benchmarked. Remove it.
A reader of the package reasonably assumes the task string shapes the budget.

### I5. Sampling is by count, not by payload

`defaultSearchLimit = 25` and "SAMPLE: showing 25 of 66". 66 hits is small.
Sample when the full set exceeds a token budget, not a fixed count; when it
fits, deliver it complete and drop the warning. (Also raised in the compact
schema brief; belongs here because it is a ranking-delivery decision.)

### I6. Profile weights are unmeasured since the 2026-08-01 renormalization

`profiles.go:50-65` records the pre-removal weights and a proportional
renormalization. With B3 dead and B4 binary, only two signals actually vary in
a live repo and one in a pinned one. Re-derive the weights from the oracle bed
after B1-B4, or collapse to a single profile until there is evidence that
profiles differ in outcome.

---

## Order of work, if this becomes a program

1. B5 (delete learned weights) and I4 (delete phase) — pure removals, no
   behavior to defend, and they remove hidden inputs before anything else is
   measured.
2. B2 + I1 (one budget pool, seeds charged) — largest change to what agents
   receive; gate on the oracle bed.
3. B1 + B4 (graph as a tier) — gate on a pinned cell AND a fresh-commit cell
   (B8).
4. B6, then B7 (search sample and tiers) — independent of query; gate on the
   search-recall cells.
5. B3, I6 — decide whether the test signal lives or dies, then re-derive
   weights.

Every step: `go test ./...`, `ci_invariants`, and the A/B gate before a tag.

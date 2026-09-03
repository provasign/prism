# Backlog — measured, 2026-08-16

Everything here comes from traces of real agent runs, not from ideas about
what agents might want. Each item states what was measured, on how much data,
and what it is expected to buy. Where the expected value is small, that is
said rather than dressed up.

**Source data.** 32 paired SWE-bench-Live cells (`v055-full`, `radius8`,
`inject5` in provasign/research), both arms, full tool traces with arguments.
859 agent actions, 374 file reads, 176 searches. Plus three fresh
change-impact cells on the oracle bed.

**The organising principle.** A capability gap gets prism routed around for
reasons unrelated to its value, permanently and silently — measured: 87% of
the reads that bypassed prism were line-ranged, a shape `prism_read` had no
parameter for. But parity is table stakes, not value: `prism_search` has full
grep parity and still measures flat on repair tasks, and 8 of 14 tools with
full capability got zero calls across 190 cells. **Close gaps so prism is
never rejected on expressiveness; expect value only where the graph answers
something grep cannot.**

---

## Where agent effort actually goes

Decomposed over 32 baseline cells, 859 actions:

| phase | share | median |
|---|---:|---:|
| LOCATE — find a file it will edit | 7.7% | 2 actions |
| COMPREHEND — decide what to write | 32.1% | 8 actions |
| IMPLEMENT — edit, test, iterate | 60.2% | 12 actions |

Retrieval is 7.7% of the work on repair tasks. This bounds every
retrieval-side improvement below, and is why prism measures flat on
SWE-bench while winning 8× on change-impact tasks, where LOCATE *is* the
whole task.

---

## 1. JSON envelope on `prism_search` — DO THIS FIRST

**Measured.** Same query, same hits, MCP response vs plain grep output:

```
query "func "       grep 2242B   prism 2663B   1.19x   (+421B)
query "stringsArg"  grep  718B   prism  937B   1.31x   (+219B)
query "Handler"     grep 2211B   prism 2915B   1.32x   (+704B)
```

~55–175 tokens of pure envelope per call. Source lines are the worst
possible JSON payload — every tab becomes `\t`, every quote `\"`:

```
{"line":1084,"text":"\tqueries := stringsArg(args, \"query\")"}
internal/mcp/tools.go:1084: queries := stringsArg(args, "query")
```

**Why it matters more than the ratio suggests.** `prism_search` was called
637 times across 190 cells — the highest-count tool by far — and every
result sits in the session cache for the remainder of the run. A search at
turn 4 of 25 pays its envelope ~21 more times. Cache reads dominate a cell
(700k–3.2M against 24–62k fresh), so payload on the hottest call is the
worst place to carry scaffolding.

**Fix.** Render MCP `content[0].text` as `path:line: text` lines instead of
serialising the result map. Nothing requires JSON — MCP content is free-form
text; the map is what the HTTP API wants and MCP inherited it by default
rather than by decision. Keep the structured map for HTTP.

**Expected value.** Same hits, fewer tokens, cannot make an answer worse.
The only item on this list with no downside risk.

**Care.** The `symbol`, `warning`, `truncated`/`totalHits` and
`rejectedPaths` fields must survive the rendering. Truncation without a
denominator, or a dropped scope rejection, is the silent-narrowing failure
this codebase keeps re-learning.

---

## 2. `context` on `prism_search` (grep -A/-B/-C)

**Measured.** 72 actions, 23.2% of the capability gap. Agents ask for a
match plus surrounding lines and prism cannot express it:

```
grep -n "class LogRecord" -A 140 opentelemetry-sdk/src/opentelemetry/sdk/_logs/_internal/__init__.py
```

**Fix.** One flag on the backend — rg `-C/-A/-B`, grep the same. The native
scanner needs the equivalent, or it diverges (a scoped search silently
returning different results without ripgrep is a bug this repo already had).

**Expected value — corrected 2026-08-16.** Originally recorded here as "no
measured cost saving," compared only against `grep -A` bypassing prism for
free. That undersold it: the real cost isn't the bypass case, it's the
two-call case. Measured in today's traces — `prism_search` immediately
followed by a ranged `prism_read` (find the match, then pull the
surrounding lines) — **13 occurrences** across the day's cells. Sized
directly:

```
prism_search:              611B
prism_read(ranged):        985B   -- a full second JSON envelope
TWO round trips, 1,596B total, two entries paid on every later turn
```

`context=` collapses this into one call, rendered through the same
plain-text path shipped for item 1 — no second envelope, no second cache
entry. Real, mechanism-backed saving, not just gap-closing. Smaller than
ranged reads' own ~1k tokens/cell, but the two-call pattern is common
enough (13 times in ~54 cells) that this is worth building on its own
merits, not only for parity.

---

## 3. Git history joined to symbols

**Measured.** 42 actions, 13.5% of the gap. Concentrated in COMPREHEND, the
32% phase nothing currently serves:

```
git log --oneline -25
git show 57cfdc7 --stat && git show 57cfdc7 -- dagfactory/telemetry.py
git show 823ca8d:dagfactory/telemetry.py | sed -n 35,70p
```

Agents reconstruct intent and convention one commit at a time, three or four
actions per question.

**Why this one is different.** It is the only gap where prism could answer
BETTER rather than merely equally: it can join history to symbols — "the last
3 changes that touched this function" — which `git log` cannot do without a
path guess. Everything else on this list is parity.

**Cost.** A new data source, not a flag. Largest item here.

**Caution.** 42 actions is 4.3% of the COMPREHEND phase; the phase is mostly
local source reading (43.5%) and symbol search (37.3%). Do not size this off
the dag-factory cell, which was an outlier and which I over-read once
already.

---

## 4. `wc -l` — file size without reading it

**Measured.** 4 actions, 1.3%. Mostly closed already: `totalLines` now comes
back on any ranged `prism_read`. Remaining case is asking for size without
reading anything.

---

## Decided against

### `ls` — directory listing. NOT BUILDING. (decided 2026-08-16)

193 actions, 62.1% of the measured capability gap — **the single biggest
item by count, and it is not being built.** Recorded here so it is not
re-proposed from the raw counts.

Most of it is orientation (`ls && ls dagfactory`) and environment poking
(`ls .venv/bin`, `ls /opt/homebrew/bin`), not code intelligence. Prism knows
only the indexed tree: no permissions, no non-indexed files, no `.venv`,
nothing outside the root, nothing it failed to parse.

A `prism_ls` would therefore be a strictly worse `ls` that can nonetheless
express the request. That is the REVERSE failure mode and it is harder to
detect than a gap: with a gap the agent routes around prism and we see it in
the traces; with a worse answer the agent gets a confident, incomplete
listing and proceeds on it. A missing file reads as "not in the repo".

This is the one place in the whole backlog where the shell tool is genuinely
the right tool, and prism should stay out of its way.

## Not on this list, and why

**More retrieval features for SWE-bench repair.** Three levers were sized and
all came out small: symbol/graph injection into search (built, measured
worse on 3 tasks, reverted in 93d19fe); delivery granularity (the agent
already ranges half its reads, and 9.5%-of-file edits do not mean 90% waste);
re-read dedup (14 genuinely redundant reads across 32 cells, ~1k tokens per
cell). Retrieval is 7.7% of the work here. That is the ceiling.

**Restricting cross-tool reads.** Considered and rejected. When grep was
denied the substitute was equivalent, so routing worked and bought nothing.
Here the substitute would be strictly worse until parity is complete — and
after parity, adoption should be observed before being forced.

---

## Standing constraints

- **Correctness first.** Efficiency at unknown resolve-rate is
  uninterpretable; a cheaper wrong answer loses.
- **Payload compounds.** Anything attached to a frequently-called tool is
  paid on every later turn. This is why item 1 leads.
- **Never narrow silently.** State both counts. Missing evidence costs far
  more than noise (SWE-Explore, r=0.95).
- **Measure the slice before building for it.** The reverted injection was
  aimed at a real pattern — 78% of searches are symbol-shaped — that turned
  out to sit inside a 7.7% slice.

---

# Addendum — wide-bed transcript mining, 2026-09-02

**Status (same day):** items 1-8 SHIPPED — 1-3 in prism 0f32d14, 6-7 in
7ce6098, 4 in grove dce6cd75 (Java package scope spans source roots), 5 in
grove 6a5b4873 + prism 79e0275 (file= disambiguator + ambiguityNote), 8 in
f4f7460 (verify removed_symbols fast path). Item 9's core (breaking sites
incl. tests, keyed by file) is substantially covered by 4 + the isTest
caller labels (v0.66.0); a dedicated delete-mode remains unbuilt and can
wait for demand. Item 10 (residency A/B) RESOLVED
2026-09-03: 8-cell paired A/B (4 tasks x 2 reps, all-alwaysLoad
experiment build vs released deferred v0.67.0) — full residency raises
prism call VOLUME on every engaging task but recall never improves and
cost is higher on every task, including one with zero prism calls (pure
schema overhead + variance). The deferred design + imperative ToolSearch
steering stands; do not reverse the v0.65.0 residency removal without
new evidence of a different shape.

Source: 4-analyst sweep of 30 wide-bed session transcripts (tasks-wide,
sonnet, prism_plus arm; research/harness/runs/wide) after the v0.66.0
fixes landed — hunting friction NOT already fixed (ToolSearch hop wording,
empty-search abandonment, change_impact-after-edit, test noise in
delivery). Every item below is anchored to specific transcript call
numbers; the transcripts live under ~/.claude/projects/*wide-*.

## Tier 1 — bugs (small, no design risk)

1. **prism_verify accepts unknown params silently.** b8mjxuh6 #26: agent
   passed `query="MetadataInfo.ServiceInfo.getMethodParameter"` — schema
   has only `base` — the arg was ignored and the agent read the whole-repo
   verdict as if symbol-scoped. Fix: reject/warn on unknown argument keys
   (all tools, one shared validator).

2. **Cached marker contradicts stale warning and elides the matched
   line.** 6rqii7zt #39: hit rendered as `pkg/grove/grove.go: 260 [cached —
   content already delivered this session]`, matched line text dropped;
   #50 then warned the SAME file was stale and must be re-read. Fix: never
   elide the matched line; suppress the cached marker for stale-flagged
   files.

3. **verify output drowned by per-symbol deletion lines.** 6rqii7zt #88:
   13.9k chars, ~30 lines of `... removed with file
   internal/embeddings/model2vec/*.go`; agent dismissed all of it in one
   sentence. Fix: collapse whole-file/whole-dir deletions to one line
   ("internal/embeddings/ deleted — 34 symbols").

## Tier 2 — quality fixes, byte-measurable before/after

4. **change_impact said "completeness: closed" while missing Java TEST
   call sites.** b8mjxuh6 #8: `MetadataInfo.ServiceInfo.getMethodParameter`
   → callers (2), closed — but MetadataInfoTest.java:100/118 call it, the
   agent spent 4 extra calls (#15-#19) re-deriving them and edited both
   (#23/#24). A wrong completeness claim on the flagship guarantee.
   Engine-level (grove): Java test-caller resolution. HIGHEST PRIORITY of
   this tier on principle.

5. **change_impact merges same-named distinct symbols.** 6rqii7zt #37:
   `Engine.Query` returned declarations(2) merging
   internal/embeddings/model2vec.go:194 with pkg/grove/grove.go:260, and
   13 callers all belonging to the embeddings one; agent distrusted the
   result and re-derived manually (#38/#39). Also b8mjxuh6 #8 (two
   overloads listed as one anchor). Fix: `file=` disambiguator (lookup
   already has one) + split output per declaration when packages/signatures
   differ.

6. **Text search ranks build/doc noise above source.** upai2v1g/b8mjxuh6:
   the Dubbo "triple" cell burned 44.8kB across 11 prism calls for ~1.2kB
   of demonstrated value — pom.xml/README/.licenserc hits ahead of any
   .java source; every call was followed by a manual grep of the same
   term. Fix: rank source files first, collapse non-source to a counted
   group. Measure: bytes-to-first-source-hit on the same transcripts'
   queries.

7. **prism_query spends budget on zero-signal anchors.** upai2v1g #93:
   22.5kB delivery, 3 of 4 anchors had "no resolved callers" AND no
   term-name match (generic `triple`/`metadata` field hits), full
   license-header dump of an untouched file; nothing it returned was used.
   Fix: drop anchors with no callers and no name match from source
   windows; spend the budget on the anchor that resolved.

## Tier 3 — workflow features (the $8-cell turn sinks)

8. **Mid-loop residual-reference checking.** ji5zetwy + pgl2edn8 (~210
   calls each): ~45-48 Bash greps per session (~22% of ALL calls) re-check
   the same 8-12 removed identifiers ~15 times — hand-rolled verify.
   prism_verify ran once, at call 210 of 212. Fix: verify accepts
   `removed_symbols=[...]` and reports remaining references cheaply
   mid-loop; steering surfaces it as the loop check, not the exit gate.

9. **Delete-mode impact.** Same cells: 30 test-file edits discovered one
   compiler error at a time; `impact(symbol, mode=delete)` returning every
   breaking site keyed by file (tests included — see item 4) would have
   fronted the whole list. Overlaps 4; do 4 first.

## Tier 4 — strategic (needs an A/B, reverses a v0.65.0 decision)

10. **The deferral cliff survives the imperative wording.** krjw0y3d +
    jqqw5b3d (161/170 calls, ZERO prism, zero ToolSearch): both received
    the fixed "Before your first tool call..." imperative and ignored it at
    turn 0 — first action was a reflex grep; neither session emitted a
    single thinking block, so no deliberation step existed for the
    instruction to win. Wording is exhausted. Remaining lever is
    structural: all-tools alwaysLoad (the all-resident v0.55.10 cells were
    the highest-engagement of the week: 11-17 calls/cell) at ~2k
    tokens/session schema cost. Needs a paired A/B on the wide bed —
    engagement rate AND net cost — before reversing the residency removal.

Out of scope, noted: cwd-loss churn in Bash (harness, not prism);
README-prose reconciliation (prism indexes symbols, not prose).

## Field report, 2026-09-03 — stale v0.50 deny entries + subagent circumvention

Live session on another repo (confirmed + fixed by hand): a v0.50-era
`permissions.deny` trio (Grep/Bash(grep:*)/Bash(rg:*)) survived years of
upgrades — cleanupLegacyDenyEntries only fires on re-init, which nobody
runs. The model, given the BARE denial (no reason attached, unlike the
v0.50 hook that explained itself and steered ~100%), confabulated intent
("user seems to be blocking grep/rg") and routed around the policy via
the Explore subagent — own tool grants, no prism steering, unauditable
calls. The denial achieved the opposite of its intent.

Two items:
1. Stale-denial detection that does not require re-init — e.g. the MCP
   server, at startup, noticing its own legacy deny trio in the host
   settings and warning in-band (loudness, never silent edits of a
   user-owned file — same rule as cleanupLegacyDenyEntries).
2. Standing design rule, now field-confirmed from the failure side: a
   denial without an in-band reason does not steer — it gets confabulated
   around, including via subagent escape hatches. Any future routing
   enforcement must carry its explanation inside the denial itself.

---

# Addendum 2 — v0.67.0 fleet + residency transcript mining, 2026-09-03

Source: 2-analyst sweep of the fresh v0.67.0 fleet and residency-A/B
transcripts (24 cells, all post-dating every fix above). The frontier moved:
engagement is solved, dead ends are recoverable — the new axis is the
MARGINAL VALUE OF THE NTH CALL.

## 11. Opening-inventory steering (amplify the measured win)

e826's first-ever ceiling break (0.3 -> 0.35, AND 36 turns/$0.72 vs
baseline 61/$0.95) was causally ONE call: prism_search(query="zookeeper",
scope=text, exhaustive=true, files_only=true) — a complete file inventory
whose api/curator4/curator5 SPI triangle no artifact-name grep ever
produced (vi304o8a #2; the agent cat'd exactly those three paths at #6 and
edited the SPI file at #29). The same call REPLACED the iterative
discovery phase (one Read in the whole run). Today's steering never
mentions this opening. Fix: one steering line — wide removal/refactor
task? open with an exhaustive files_only inventory of the concept term.

## 12. Hypothesis ledger + scope note (cap the confabulation loss)

The $3.09/recall-0.0 cell (ms-vfs18, unanswerable "triple parameter"
task): the v0.66.0 retry guidance is INNOCENT — 3 firings, ~3 turns, each
followed by an intelligent pivot, zero broaden-retry loops. 59% of cost
($1.82) came AFTER search ended: the agent confabulated a reading
("triple parameter" = the 3-arg constructor, a pun with zero evidentiary
support) and paid to make the fabricated change build (7 edits, 4 Maven
compiles, cascading call-site fixes). The preventing signal existed in
prism's own session by the halfway mark: 5 same-stem negative searches
(Tuple3/Triple</ImmutableTriple/...) + 5 change_impact results all
"completeness: closed" with <=6 sites — contradicting the task's "the
change is deliberately wide" head-on.

Fix: session-scoped hypothesis ledger. Track (a) consecutive same-stem
empty results, (b) closed-small change_impact results. At >=3 of BOTH,
append a scope note to the next result — additive, never a stop, on top
of the existing retry guidance: "N negatives on stem X and M closed
impact results with <=K sites — nothing here has a wide blast radius
matching your terms; if the task says wide, your READING of the term is
probably wrong: restate the term, don't re-search it. Searched stems so
far: ...". Dual threshold verified against both cells: the benign
19-call cell (ddtb4dv8, one change_impact only) never triggers; the
pathological cell trips at call 16 of 25 — before the confabulation
pivot at the transcript's line 264.

## 13. Redundant-call echo (small)

ddtb4dv8: ~7 of 19 prism calls re-asked answered questions — four
sequential prism_read windows of one 863-line file; a prism_search for a
line a previous read had already displayed; two empty searches of the
same question with different punctuation one turn apart. The "searched
stems so far" line from item 12 covers the search half nearly for free;
windowed-read dedup can wait for demand (~$0.30/cell, the cost lives in
the edit loop, not discovery).

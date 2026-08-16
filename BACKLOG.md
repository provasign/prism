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

**Expected value.** Removes a reason to route around prism. No measured cost
saving attached.

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

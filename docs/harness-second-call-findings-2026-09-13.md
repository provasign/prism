# Second-call adoption findings — 2026-09-13

Harness investigation (research repo, `harness/bench.py`, live Claude Sonnet
cells with reasoning captured), not a code read. One task, one model, single
cells per build — so every number below is n=1 unless it says otherwise, and
the one result that holds across builds is called out as such. Repro paths
and persisted evidence are listed so this can be re-derived before anything
is changed on the strength of it.

Task: `pallets__click__pr3244` (e2e coding suite — `CliRunner` streams lack
`fileno()`, so `subprocess.run(stdout=sys.stderr)` raises). Agent: Claude
Code headless, `claude-sonnet-5`, effort medium, 5-min budget, identical
prompt across arms. Reasoning captured via `--thinking-display summarized`.

Evidence (research repo): `harness/results/prism-first-result-2026-09-13/investigation/<run>/evidence/<cell>/`
— `stdout.jsonl` (full transcript incl. thinking), `measurement.json`,
`agent.diff`, `command.json`. Each run's `manifest.json` records the Prism
version and binary sha256.

---

## 1. FINDING (on the click task, all builds) — one Prism call, then native tools for every follow-up

Across four click cells on three builds, the model called Prism **exactly
once** every time, then served every later need with native `Read`/grep. It
never issued `op=read` or `op=lookup`, and the ~5k characters of captured
reasoning per cell never mention them. (On urllib3 the release build already
made 1–3 calls per cell — the one-call pattern is task-shaped, not universal;
the batch in §8 has both tasks.)

| build | Prism result | Prism calls | native reads | read KB | edits | turns | cost |
|---|---:|---:|---:|---:|---:|---:|---:|
| v0.74.1 (release) | 4.1 KB | 1 | 9 | 24.3 | 3 | 39 | $0.67 |
| wording rebuild (uncommitted, 2026-09-12) | 1.4 KB | 1 | 6 | 22.1 | 4 | 28 | $0.45 |
| wording rebuild + thinking captured | 0.8 KB | 1 | 5 | 11.3 | 3 | 20 | $0.26 |
| first-result change (uncommitted, 2026-09-13) | 3.8 KB | 1 | 1 (whole file) | 22.8 | 1 | 12 | $0.23 |
| *native, no tool (2 cells)* | — | — | 6 / 2 | 12.9 / 4.1 | 3 / 1 | 26 / 14 | $0.38 / $0.24 |

This is the finding to act on. It is a workflow-adoption problem, not
evidence that the first result was too small (see §3).

**Where the reads went, from the reasoning** (build: wording + thinking):

- Prism's line numbers were used precisely — first read was `Read 95:40`,
  right at its `_NamedTextIOWrapper 103–126` hit.
- Each later read had a new, named reason:
  - before `Read 1:100`: *"I should check how stdin capturing handles this —
    perhaps it already uses a real OS-backed pipe or tempfile that I could
    mirror for stdout/stderr."*
  - before `Read 278:140`: *"let me look at the isolation method more closely"*
  - before `Read 440:30` / `469:15`: *"I should add proper cleanup by closing
    the write_fd in the finally block of the isolation() context… so let me
    check the finally section around line 454-465."*
- Zero mentions of `op=read`/`op=lookup` in any cell.

## 2. CONSTRAINT — Claude Code's `Edit` requires a native `Read` of the file first

Claude Code's `Edit` tool contract: *"You must Read the file in this
conversation before editing, or the call will fail."* Prism's `op=read` does
not satisfy it. In every cell a native `Read` of `testing.py` preceded the
first `Edit`, and no `Edit` ever failed.

Consequences:

- At least one native read per edited file is a floor no MCP tool can
  remove in this harness. The realistic target is *discovery* reads, not the
  edit-prerequisite read.
- Any instruction phrased as "use `op=read` instead of `Read`" asks the
  model to do something that will make its next `Edit` fail. The current
  `instructions` string already says *"Known file/range: op=read"* and
  *"Do not re-read unchanged source already included in a Prism result"*;
  the model was sent exactly that text in the first-result-change cell and
  still did a native whole-file read that included the inlined body. The
  contract, not the wording, is the likeliest reason.

## 3. What the first-result change did and did not do (n=1)

The 2026-09-13 change rendered exactly as designed in the live cell, with no
over-claims:

```
// match lists restricted to path=["src/click/testing.py"]; files outside these
// filters, including tests outside them, were not searched for matches
── fileno ──
// no indexed symbol matches in the requested symbol scope
// no matches — search completed (not truncated, not timed out)
…
// Exact source for the unique small symbol match in the first batched term;
// other hits remain locators (already read; do not fetch it again):
```
(full `BytesIOCopy` body, lines 68–88, followed.)

Did: delivered the small body; disclosed scope; labeled the empty symbol
result; the bounded token fallback path is tested (`compact_test.go`) though
this cell's terms did not trigger it.

Did not (this cell): reduce bytes read (22.8 KB, one whole-file `Read`, vs
22.1–24.3 KB in earlier cells) or produce a second Prism call. The model's
own search terms (`class BytesIOCopy`, `fileno`, `EchoingStdin`) never asked
for `isolation()`/`_NamedTextIOWrapper`, so rather than search again it read
everything. The 20 → 12 turn drop coincides with a 1-edit trajectory — the
same shape that produced the 14-turn native cell — so it is not attributable
to the change at n=1. A before/after batch is running (§6).

## 4. OBSERVATION — query choice varies per run and drives result size

Four runs, four different term sets for the same task:
`class _NamedTextIOWrapper, def isolation, sys.stdout` /
`class BytesIOCopy, "isolation stdout stderr", "sys.stdout = ", fileno` /
`class _NamedTextIOWrapper, fileno` / `class BytesIOCopy, fileno, EchoingStdin`.
Result size (0.8–4.1 KB) tracked the query, not the build. Two of the four
included a multi-word phrase that matched nothing — the token fallback now
covers that case; whether it changes behavior is unmeasured.

## 5. OBSERVATION — the model hunts for the upstream fix (both arms, harness-side)

First reasoning line in the Prism cell: *"This is the known click issue…"*;
later: *"I need to verify against click's actual changelog"*, *"check whether
there's a known PR number tied to this repo's fix."* Recall is wrong and
flip-flops; it costs 2–5 turns and, on v0.74.1, produced two `pip download`s
of newer click releases. The native arm does the same. This is a benchmark
validity issue, not a Prism one; the harness now blocks network on both
agents and records attempts. Noted here only because it inflates Prism-arm
turn counts in older cells.

## 6. Options, ranked by evidence

1. **Make the second call the path of least resistance, within the contract.**
   Say the mechanical truth in `instructions`: one native `Read` per file you
   will edit is unavoidable — do it at the edit site, for the range you will
   change; for everything else (other symbols, other files, callers, tests)
   use `op=read`/`op=lookup`. This targets discovery reads without asking the
   model to break `Edit`. Unmeasured; cheapest to try.
2. **Carry the next call's answer in the first result** — the first-result
   change already does this for one small symbol. Extending it to the
   *containing* region of a located line (the model's actual next question
   was "what surrounds line 338/464?") would address the whole-file read
   seen in §3. Bounded by size; scope disclosure must stay as-is.
3. **Do not** synthesize negatives ("no references in src/") without having
   run the text search — already handled correctly by the 2026-09-13 change;
   keep it that way.

## 7. Measurement plan and what is running

Metrics computed per cell from the transcript (no new instrumentation
needed): Prism calls (second-call rate), native reads of Prism-located
ranges split into *discovery* vs *edit-prerequisite* (read immediately
followed by an `Edit` of the same file), bytes read, edits, turns, cost,
resolved. Only audited-valid cells count.

Running now: `bench.py run --suite e2e --phase pilot --agents claude
--tools prism --trials 3` for v0.74.1 and for the 2026-09-13 build — pilot
pair (`pallets__click__pr3244`, `urllib3__urllib3__pr3786`), 12 cells,
reasoning captured, network blocked. Results will be persisted under
`harness/results/prism-first-result-2026-09-13/batch-*/`. If the second-call
rate stays 0 on the new build across 6 valid cells, option 1 is the next
change to measure; if read bytes drop without a second call, option 2 is
doing the work.

Repro of any single cell:
```sh
cd research/harness
python3 bench.py run --suite e2e --tasks pallets__click__pr3244 --agents claude \
  --tools prism --trials 1 --out /tmp/cell --prism-binary <binary>
# then: /tmp/cell/evidence/pallets__click__pr3244.r1.sonnet_prism/{stdout.jsonl,measurement.json}
```

## 8. Batch result (2026-09-13) — the first-result change is neutral at n≈6

Pilot pair, Prism arm, 3 trials per task per build, valid cells only. Run B
lost its 6th cell to a harness crash on the deny event; the crashed cell was
rebuilt from its transcript and the 6th was run afterwards from the identical
binary. Evidence: research repo
`harness/results/prism-first-result-2026-09-13/batch-{old-v0.74.1,codex-fix,codex-fix-r3}/`.

| | v0.74.1 (n=6) | 2026-09-13 build (n=6) |
|---|---:|---:|
| resolved | 2/6 (click 2/3, urllib3 0/3) | 3/6 (click 3/3, urllib3 0/3) |
| cells with a follow-up op (`read`/`lookup`) | 1/6 | 2/6 |
| mean Prism calls | 1.7 | 2.0 |
| mean native reads (discovery / edit-prerequisite) | 5.5 (4.5 / 1.0) | 6.5 (5.3 / 1.2) |
| mean KB read | 19.6 | 28.5 |
| mean edits / turns | 2.7 / 22.8 | 2.3 / 22.5 |
| mean cost | $0.39 | $0.40 |

- Turns and cost did not move. Native reading went UP (6.5 reads / 28.5 KB
  per cell vs 5.5 / 19.6): the inlined body coexisted with native reading
  rather than displacing it.
- Follow-up ops 1/6 → 2/6: right direction, not a signal at this size.
- Resolved 3/6 vs 2/6 is one click cell at n=3.
- urllib3: 0/6 across both builds — at or past the model's ceiling; it
  discriminates nothing here. Any further batch should swap it for a task
  the model resolves ~50% of the time.
- The recall hunt is still present on the new build (click r3 attempted
  `pip download`; the harness refused it; the cell still resolved).

Implication for §6: content added to the first result did not change the
~4.5 discovery reads per cell. Option 1 (state the Edit-contract truth and
route non-edit reads to `op=read`/`op=lookup` in `instructions`) is the
untested lever; measure it the same way, with a mid-difficulty second task.

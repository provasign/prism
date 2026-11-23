# A/B gate: backlog bug fixes, pre-fix build vs candidate (2026-11-22)

The bug fixes from the 2026-11-22 audit change what agents see, so they
needed a paid A/B before release. Topo approved the full 42-task bed with a
$60 cap.

## Arms

| Arm | Build |
|---|---|
| `prism_cli` (baseline) | multi-repo before BUG-01: prism 06464fa, grove 63c042a6 |
| `prism_cli_b` (candidate) | prism 3845bd3, grove c5aba584, frozen for the whole run |

Both arms ran Sonnet 5.5 through the research harness
(`run_cli_product.py`, `prism init --interface cli`). Prompt caching was
disabled and costs are repriced at the 5-minute cache rate. Task cost
excludes startup, which was the same in both arms.

`guard.py` ran one task at a time. Before each task it checked that the data
volume had at least 500 GB free. After each task it deleted the cell
checkouts, logged cache sizes, and applied the stop rules:

- any cell error;
- a resolve loss (baseline solved, candidate did not);
- candidate/baseline cost above 1.25× after 10 pairs;
- a usage limit hit inside a cell.

## History

1. **First attempt** (`results/ab-bugfix-2026-11-22-r1-aborted`), candidate
   at c043b79. Stopped after 3 pairs at 1.5× cost. On gin pr4698 the
   agent's search for `brokenPipe` found nothing under BUG-10's smart case
   (the code says `isBrokenPipe`). Every search also printed a "not
   searched: .claude" line, triggered by prism init's own settings file.
2. **Fixes, before the rerun:**
   - c402d2b: default case is insensitive; the not-searched line appears
     only when the skipped folder has matches.
   - 7df682b: the header says plain "matches".
   - 3845bd3: wide `references` answers print compactly.

   Output size was then checked against the baseline on chi: equal or
   smaller, except `references` on small answers, which gained line text.
3. **Rerun** (`results/ab-bugfix-2026-11-22-r2`):
   - urllib3 pr5020 was skipped; the harness marks it invalid, as it did
     on 2026-10-20.
   - Two stops were caused by the setup, not by prism, and the run was
     resumed after each: the guard misread that skip as a cell error, and
     a usage limit cut short pr6044's baseline cell. That pair was removed
     and rerun.
   - Prism stayed frozen at 3845bd3.

## Result (41 pairs)

| | Baseline | Candidate |
|---|---|---|
| Solved | 24 | 25 |
| Task cost @5m | $3.142 | $2.820 (0.90×) |
| Model requests | 325 | 307 |

- **No resolve loss.** The one difference is zod pr6500, which only the
  candidate solved.
- **Higher-cost pairs were not prism.** I read the transcripts of every pair
  where the candidate cost noticeably more. The extra came from the agent's
  own verification (Maven reruns, javap, scratch tests) or edit retries, not
  from prism output.
- **BUG-01/02 never fired.** The "is ambiguous" error appeared in none of
  the 10 jackson tasks, in either arm.
- **Both-fail tasks match October.** gson pr3059, pr3098, pr3112 and
  dubbo pr16395 also failed in both arms on 2026-10-20.

Spend: $44.27 uncached for the rerun, plus $2.14 for the first attempt and
about $0.6 for the removed limit-hit pair. Disk stayed above 593 GB free;
the Docker image grew from 5 to 8.8 GB.

## Limits

- **One trial per arm.** Per-pair cost swings ±40-70% between runs, so the
  0.90× is indicative, not settled.
- **The fixes were tuned against this bed.** The first attempt's findings
  drove three output commits that were then measured on the same 42 tasks.

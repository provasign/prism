# A/B gate: the rest of the 2026-11-22 backlog, baseline vs candidate (2026-11-23)

After the bug fixes (ab-bugfix-2026-11-22), the backlog's SEC, REL, CON, GAP, ENG-05 and MKT-05 items changed what agents see:
- the CON-01..04 output changes;
- the SEC-01 trust notice;
- withheld lines for secret files;
- `missing_implementations` as an MCP op;
- kind tags on `references`;
- the no-graph-language line.

Topo approved the full 42-task bed with a $60 cap.

## Arms

| Arm | Build |
|---|---|
| `prism_cli` (baseline) | multi-repo before BUG-01: prism 06464fa, grove 63c042a6, the same baseline as ab-bugfix-2026-11-22 |
| `prism_cli_b` (candidate) | prism a44e87d, grove 1f3a75e2, frozen for the whole run |

Both arms ran Sonnet 5.5 through the research harness (`run_cli_product.py`, `prism init --interface cli`). Prompt caching was disabled and costs are repriced at the 5-minute cache rate. Task cost excludes startup.

`guard.py` is the 11-22 guard pointed at the new candidate. It ran one task at a time with a 500 GB free-disk floor and the same stop rules:
- any cell error;
- a resolve loss (baseline solved, candidate did not);
- candidate/baseline cost above 1.25× after 10 pairs;
- a usage limit hit inside a cell.

`AB_PROBE=1` stops after one pair, for the probe-first rule.

## History

1. **Probe.** One pair (gin pr4698) went through the same pipeline: both solved, the candidate in 3 requests to the baseline's 4. The transcript was read before the full run. The candidate's first command shows the SEC-01 trust notice, as designed.
2. **Full run.** It stopped after 36 pairs when the 5-hour usage limit cut off the candidate cell of jackson-databind pr6138. That row was removed (backup kept) and the run resumed after the reset with the same frozen binaries. The rerun of pr6138 solved in both arms.
3. urllib3 pr5020 was skipped; the harness marks it invalid, as on 2026-10-20 and 2026-11-22.

## Result (41 pairs)

| | Baseline | Candidate |
|---|---|---|
| Solved | 24 | 24 |
| Task cost @5m | $3.060 | $2.826 (0.92×) |
| Model requests | 319 | 297 |

- **No resolve loss.** Every task has the same outcome in both arms.
- **The baseline matches 11-22.** No baseline task changed outcome from the 11-22 run. zod pr6500, which only 11-22's candidate solved, failed in both arms here: it flips between runs.
- **The 17 both-fail tasks** include the four that also failed in both arms on 2026-10-20 (gson pr3059, pr3098, pr3112, dubbo pr16395).
- **Higher-cost pairs were not Prism.** I read the two pairs where the candidate cost noticeably more:
  - urllib3 pr5093 ($0.137 vs $0.080): each arm made one Prism search of similar size (3.7 KB vs 2.5 KB). The extra came from the candidate's own test experiments: scratch test files, pytest runs and a mock script.
  - jackson-databind pr6042 ($0.168 vs $0.106): the candidate made fewer Prism calls (2 vs 6) and received less Prism output (about 7 KB vs 23 KB). The extra came from grep/sed exploration, an edit reverted with `git checkout` and redone, and import fixes.

Spend: $43.18 uncached for the 41 pairs, plus the cut-off pr6138 cell and the probe. Disk stayed at 580–583 GB free; the Docker image grew 8.8 → 9.2 GB.

## Limits

- **One trial per arm.** Per-pair cost swings ±40–70% between runs, so the 0.92× is indicative, not settled.
- **The bed is the same 42 tasks as 11-22.** None of this round's fixes was tuned against it: the candidate was frozen before the probe and nothing was changed during the run.
- **Most new features never fired.** No task touched a secret file, a non-graph language or `missing_implementations`. The bed measures the default paths (output shape, trust notice), not the new features.

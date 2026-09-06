# Accuracy And Efficiency Reports

This directory contains reports only. The initial raw transcripts, per-cell
results, patches, protocols, frozen harness snapshots, and experiment scripts
are in the [research archive](https://github.com/provasign/research/tree/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06).
Later stage reports link to their separate research evidence.
Links to evidence are pinned to that commit, not a mutable branch.

## Results

| Study | Result |
| --- | --- |
| [Jackson four-way pilot](four-way-2026-09-06/REPORT.md) | Local savings and full Prism recall; one setup failure retained |
| [24-cell four-way panel](panel-2026-09-06/REPORT.md) | Inconclusive overall; Codex fails both efficiency targets |
| [Evidence-delivery comparison](evidence-delivery-2026-09-06/comparison/REPORT.md) | 100% recall/precision; 15.3% median token saving, 27.7% cost saving; fails targets |
| [Guidance comparison](routing-2026-09-06/comparison/REPORT.md) | 100% recall/precision; 11.1% median token saving, 21.4% cost saving; fails targets |
| [Exact scoped lookup](scoped-lookup-2026-09-06/REPORT.md) | Free replay: five calls -> one batch, eight bodies preserved, two explicit misses |
| [Scoped lookup model comparison](scoped-lookup-2026-09-06/comparison/REPORT.md) | 100% recall/precision; 9.4% more aggregate tokens and 5.8% higher estimated cost; fails targets |
| [Impact coverage and task depth](impact-coverage-2026-09-06/REPORT.md) | Go false-closed case reproduced; partial coverage reported without losing sites; no new model savings measurement |

The comparisons use different controls. Do not compound their savings or
claim product-wide superiority over native tools.

## Branches

Merge candidate: **`cand-search-context-clean`**, based on `a1c7aa6`.
The initial product-only commits are `e245668` and `7169b4a`; reports are separate.
Those source/test bytes match the old `e855442` tree. Scoped lookup follows as
`71a509b`, with Go coverage/task-depth safeguards in `cef7dad`.
Do not merge the superseded `cand-search-context` evidence-heavy
commits into main.

Evidence branch: **`accuracy-efficiency-evidence`** in research, at
`1aa2adcb8be12d7d43d7f41c17575269225f5075`.
It contains the initial archive, historical references, free replay and the
eight-cell scoped-lookup model comparison, including its efficiency failure.
It also retains the Go coverage replay and its sandbox-cache setup failure.
The original source branches and working copies were preserved unchanged.
Neither new branch has been pushed; the pinned GitHub links will resolve after
the research branch is published.

## Verification

- Full Go suite and affected-package race tests (MCP, CLI, text search) pass
  on the clean product worktree. Race fixtures require localhost/cache access;
  the sandbox-denied run was rerun successfully with those permissions.
- Prism completeness verification reports no missed sites. It marks the
  steering string as a manual-review contract: its only references are the
  declaration and `steeringBlock`, covered by the routing/safeguard tests.
- All 34 local report links and 87 pinned research links resolve to files or
  Git objects. Remote links become accessible after the evidence is pushed.
- All 638 copied files match their source hashes; all 594 original artifact
  checksum checks pass. Frozen manifests and scripts were not rewritten.
- All 26 historical offline experiment tests pass after relocation.
- The new eight-cell comparison replays all raw answers and usage totals;
  all 111 archived checksum checks and 27 offline experiment tests pass.
- The Go coverage replay preserves site arrays and bodies; all 58 artifact
  hashes and the full race-enabled product suite pass. Engine recall is not fixed.
- The read-only [archive verifier](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/verify_archive.py)
  reproduces 40 summary cells and rechecks 16 raw answers and usage records
  without the original checkout, temporary binaries, or model calls.

Historical reports retain the original experiment dates, paths, and commit
identities for provenance. Their old "uncommitted" or "next experiment" wording
describes that stage; use the implementation checkpoint and later reports for
the current status. Future model runs need new protocols and explicit paths,
not execution of frozen historical runners inside the archive.

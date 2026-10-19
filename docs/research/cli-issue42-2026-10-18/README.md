# Prism CLI runs, 2026-10-18 (Claude Code 2.1.302, Sonnet 5.5, no cache, repriced)

Scripts: `run_cli_product.py` overlays the research harness in-process (CLI arm set
up by `prism init --interface cli`, optional second binary as `prism_cli_b`, spend
cap); `compare.py` (CLI vs the 10-17 native/MCP runs) and `ab_compare.py` (two CLI
binaries). Results live in `research/harness/results/<run>/`.

| Run | What | Result |
|---|---|---|
| cli-issue42-product | CLI arm, issue bed; stopped at 30/41 | CLI 24 resolved vs MCP 22, native 22/23; task cost 0.93x MCP, ~1.02x native; prism called in 30/30, ~1.1 calls/task |
| cli-ab-generic14 | 52e687e vs 48f7a8e (mention grouping, query cap, multi-file read), 14 wide tasks | 13 vs 14 resolved, task cost 0.946x, no loss |
| cli-steer12 | 48f7a8e vs 4293823 (per-moment steering, shell cap, arg checks), 12 tasks | 12 vs 12, task cost 0.96x; prism calls 1.50 -> 1.67, greps 2.25 -> 2.42 |
| cli-must12 | 48f7a8e vs 96b7c89 (MUST steering); fail-fast stop after 2 pairs | prism calls 1.0 -> 4.0, greps 1.5 -> 0; chi pr1158 lost (agent also did the rename the issue suggests); awaiting adjudication |

The native and MCP numbers in the first row are from the 10-17 runs on an older binary, not run alongside.

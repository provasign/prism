# What CLI agents did with prism, 2026-10-18

59 sessions: 53 Claude (issue bed, generic-name A/B, probe), 6 Codex (stopped release run).
`seq.py` builds a per-session timeline (`timelines/`): every call, the text before it,
and the head of its output. `follow2.py` classifies each grep after the first prism
call (`follow2.tsv`).

- Claude: 52 of 53 sessions made exactly one prism call, as the first call; after it
  1.04 greps, 0.43 shell reads, 2.0 edits, 1.1 test runs per session. Codex: 8.5 prism
  calls, no grep.
- 89 Claude greps after prism: 39 pre-edit for new terms (multi-term regex, receiver
  filters), 10 re-checking a term prism listed, 20 post-edit checks (11 on the rename
  target). `prism verify --removed` was never called.
- 23 of 58 Claude prism calls were piped to `head`; 14 were cut. One 31.7 KB answer
  went past Claude Code's ~30 KB Bash limit and reached the model as a 2 KB preview.
- Codex lost calls to MCP spellings on the CLI (`exhaustive=true`, `'\bmatch\b'`
  without `--regex`, a five-file `prism read`); a zod name lost its `$` to the shell.

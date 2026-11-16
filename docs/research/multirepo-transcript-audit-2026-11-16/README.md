# Cross-repo transcript audit, 2026-11-16

Source: `mr-real-final-2026-11-15` (prism-mr-final, 15 comparable pairs) and
`mr-real-codex-2026-11-16` (prism-mr-codex, 19 pairs), Sonnet 5.5, in
`~/.cache/prism-research/`. One trial per arm; no cost claim is made.

Scripts (run in order; all take paths as arguments):
- `mine.py <run-dir> <out.json>` extracts every tool call and its result from the transcripts.
- `an.py <dir>` counts Prism ops, `| head`, `--repo`, module-cache greps. `an2.py` measures head truncation and `--repo` reruns.
  `an3.py` classifies pre/post-edit discovery and lists miss/error lines. `an4.py` sizes the runs and lists post-edit commands.
  Each expects `c15.json` / `c16.json` (mine.py output) in the directory given as its first argument.

Findings and the fixes made (commit on the multi-repo branch):
1. `change-impact` of a name defined only in a linked repo missed (2 of 10 calls) -> now falls through to that repo (found via lookup).
2. `query` miss printed JSON under `--format text` -> text, and the linked-repo hint is on stdout.
3. Linked-repo search listed test-file lines (40% of lines) -> counted per file like local search; `--exhaustive` lists them.
4. Linked-repo search with a path naming no file said "0 match(es)" -> FILTER MATCHED NO FILES.
5. Batched `lookup` of two names for one symbol printed the body twice -> second says "same symbol as ...".

Not changed (measured, no fix tested): module-cache reads (11 cells), near-zero Prism use after the first edit (13 Prism vs 132 grep).

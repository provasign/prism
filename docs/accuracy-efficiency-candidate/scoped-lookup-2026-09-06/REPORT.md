# Exact File-Scoped Batch Lookup

Date: 2026-09-06. Product commit: `71a509b` on `cand-search-context-clean`.
Implemented and tested; no new autonomous model comparison has run.

## API And Accuracy Contract

`prism_lookup` now accepts exact file-scoped items alongside string names:

```json
{
  "name": [
    {"name": "DatabaseWrapper.get_connection_params", "file": "django/db/backends/postgresql/base.py"},
    {"name": "DatabaseWrapper.get_connection_params", "file": "django/db/backends/mysql/base.py"}
  ],
  "fields": ["signature", "body"]
}
```

- Each object supplies an exact repo-relative file. A missing symbol does not
  fall back to another receiver or file. Invalid file scopes produce per-item
  errors while other valid items remain available.
- Exact scope uses the file's symbol index, bypassing the global lookup's
  25-candidate limit. Traversal outside the root, absolute paths, directories,
  and external symlinks are rejected. Normalized in-root relative paths work.
- Existing scalar/string-array calls keep their soft `file` hint semantics.
  Mixed batches are supported; outer hints apply only to string entries.
- All entries share `fields`. The ten-item and 64 KiB serialized-record limits
  remain. Oversized scoped records are named in `omittedItems` with both name
  and file; bodies are never silently shortened. Envelope/omission metadata is
  outside that existing byte budget.
- Ambiguity, errors, and omitted identities survive rendering. Unknown metadata
  falls back to JSON. Scoped misses do not claim a substitute body was shown.

This still reads indexed evidence, not a new source-freshness guarantee. The
broader freshness/continuation contract and per-edge impact provenance remain
unfinished. No uncertainty warning or required impact site was removed.

## Free Django Replay

The replay retains all ten requests from the recorded five-call follow-up
sequence. The base-backend directory hint is converted to its exact file.
Two requests do not exist in that requested file/type; they remain explicit
misses instead of receiving legacy fallback bodies.

| Metric | Result |
| --- | --- |
| Recorded lookup calls -> scoped batch calls | 5 -> 1 |
| Valid bodies/projections preserved verbatim, once | 8/8 |
| Wrong file/type requests retained as explicit misses | 2/2 |
| Full impact response and legacy lookup outputs | Unchanged |
| Batch versus ten scoped single-item outputs | Exact equality |
| Response UTF-8 bytes | 14,212 -> 13,519 (4.9% fewer) |
| Lookup schema/description UTF-8 bytes | 979 -> 1,375 (+396) |
| Model executions / model spend | 0 / $0 |

This proves delivery capability and scope correctness on this fixture, not
autonomous adoption, session-token savings, or a new task-accuracy percentage.
The smaller response partly reflects removing inappropriate substitute bodies;
the schema is larger. The earlier model-study efficiency failures still stand.

## Verification And Next Gate

Ten new Go tests cover scope, body/projection parity, ambiguity, >25 collisions,
invalid inputs and paths, compatibility, omission identities, and the schema.
Seven functional tests reproduced the prior unsupported-input behavior. All ten
pass with the candidate, as does the full race-enabled Go suite. Prism reports
the four-file product change complete, with no missed sites. Three new offline
probe tests pass.

Raw requests/replies, hashes, protocol, and both retained probe-harness failures
are in [research](https://github.com/provasign/research/tree/2f79a4347be886ce6effe92156cd3fa11604dbb3/harness/runs/prism-scoped-lookup-2026-09-06).
The product repo carries this report only. Corpus files, old evidence, original
working checkouts, installed binaries, and global agent configs were not changed.

Next is a separately frozen, budgeted before/after model comparison with fixed
tasks, guidance, model, and effort. Check actual scoped-item adoption, remaining
lookups, total tokens/cost, recall, and false-complete answers. Retain every
execution and its per-run spend. Then run native Sonnet/Codex controls before a
broader product claim. The targets remain 25% median paired token savings and
30% aggregate estimated cost savings without paired recall loss.

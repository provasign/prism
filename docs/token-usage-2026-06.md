# Token Usage Investigation — Prism, Grove, Fuse MCP (2026-06-12)

Roadmap row 8. Measured on the wire (full JSON-RPC response line, stdio
transport), tokens estimated at ~4 bytes/token. Workspace: the grove repo
(80 files, ~950 symbols) unless noted.

## Where the tokens go

Two cost classes:

1. **Schemas (paid once per session, every session).** `tools/list`:

   | Server | Tools | Wire size | ~Tokens |
   |--------|------:|----------:|--------:|
   | grove  | 9 | 4,368 B | ~1,090 |
   | prism  | 6 | 3,358 B | ~840 |
   | fuse   | 4 | 2,057 B | ~515 |

   Verdict: acceptable, and shrinking in practice — harnesses that defer
   tool loading don't pay this at all until a tool is used. Not worth
   compressing descriptions below usefulness.

2. **Results (paid per call).** This is where the real waste lived:

   | Call | Before | After | Fix |
   |------|-------:|------:|-----|
   | `grove_query` (10 results) | ~10,700 tokens | **~1,080** | Symbol payloads embedded `RawText` (full bodies), imports, call sites — replaced with a slim wire shape (id, path, kind, names, signature, first docstring line, span) |
   | `grove_impact` | unbounded (4,468 nodes measured on grafana) | **~1,640 max** | Capped at 50 minimal refs (path, qualified name, kind, line) + exact count |
   | `grove_symbols` / `grove_tests` | bodies included | slim shape | same |
   | all grove + prism responses | pretty-printed | compact | `MarshalIndent` → `Marshal` (~15–25% of every payload was indentation) |
   | `fuse_merge_check` (clean) | — | ~40 tokens | built token-disciplined from day one |

## What was deliberately NOT changed

- **`prism_query` (~7,300 tokens for 8 results).** Inspected composition:
  ~880 bytes of *delivered context* per symbol plus ~85 bytes of metadata.
  The content is the product — Prism's whole job is selecting and
  budgeting it, the ledger tracks it, and the model-aware budget bounds
  it. No duplication found. Leave it.
- **`prism_read`/`prism_lookup`** already deliver compressed re-reads
  (sha-pointer, semantic delta) via the session tracker.
- **Tool descriptions.** Useful prose beats saved tokens at this scale;
  deferred tool loading is the structural fix and is happening at the
  harness level.

## Guidance going forward (the family rule)

1. Results are summary-first: verdicts and counts always; per-item detail
   capped, with the true count reported.
2. Symbol payloads on the wire never include bodies. Agents that need a
   body read the file or use Prism, which compresses re-reads.
3. Compact JSON everywhere (`json.Marshal`); the MCP text envelope already
   double-encodes, don't pay it twice.
4. A tool that can return an unbounded list must cap and count.
   (`fuse_impact` 50, `grove_impact` 50.)
5. New tools budget their schema: terse description (40–400 chars,
   enforced by test in fuse), few parameters, no boilerplate.

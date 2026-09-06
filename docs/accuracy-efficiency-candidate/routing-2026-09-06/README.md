# Routing And Search Reliability Follow-up

Date: 2026-09-06. Starting Prism commit: `ebda175` on `cand-search-context`.
Related research harness commit: `552aa94` on `main`.

## Decision

Implementation and free checks pass. No new autonomous model runs were made
for this follow-up. The prior eight-cell comparison still fails its efficiency
targets; none of its evidence, scores, or acceptance rules changed.

## Changes

- Generated steering routes known affected-site questions directly to impact,
  and known methods directly to batch lookup. Unknown locations still use search.
  After impact, follow-ups address specific evidence gaps, not reproduction of
  the same site list. Edit-impact and diff-verification safeguards remain.
- Generated steering is 2,511 UTF-8 bytes, with a 3,000-byte regression ceiling.
  This is not a tokenizer or session-cost measurement. Existing project/user
  instruction files were not regenerated or overwritten.
- `prism_search.task` is an optional string label, explicitly declared as having
  no retrieval, scope, or output effect. Missing queries, invalid label types,
  unsupported arguments, and invalid scopes still fail. This does not enable
  silently ignored arbitrary filtering parameters.
- Search descriptions now disclose the actual exhaustive bounds: 100,000 text
  hits, 10,000 per file, and 2,000 symbols, with deadlines and partial warnings.
- Equal-count rollup entries have stable file/line ordering before the ten-group
  delivery cap. Previously, repeated identical searches selected different groups
  from a Go map. The rollup no longer calls capped group names a complete inventory.
- The auxiliary rollup scan now honors its intended 2,000-hit cap. Previously,
  `Exhaustive: true` overrode that cap. Scan caps, deadlines, and rejected scopes
  produce explicit incomplete notes; unnamed groups and unmapped hits remain noted.
  This bounds supplementary evidence, not the primary exhaustive search response.
- External search paths are canonicalized. `path="."` could produce
  `././big.go`, reduced only to `./big.go`, which did not match the graph's
  `big.go`. The new end-to-end fixture reproduced that missing rollup before
  the fix and returns the indexed symbol afterward.

## Validation

- Full suite: `GOFLAGS= GOWORK=off go test ./... -race -count=1` passed.
- Nine new test functions cover label-only parity, rejected parameters, routing
  safeguards, twelve repeated tied rollups, the scan cap, partial scopes, and
  canonical paths across ripgrep, grep, and the native scanner.
- Harness: 54 tests passed with
  `PYTEST_DISABLE_PLUGIN_AUTOLOAD=1 python3 -B -m pytest -q -p no:cacheprovider harness/tests`.
  Disabling third-party plugin autoload avoids an installed pytest/pytest-asyncio
  incompatibility; harness tests themselves do not require that plugin.
- [Free MCP replay](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/routing-2026-09-06/free-replay/results.json): both exact failed Gin requests from
  the prior candidate transcripts now succeed. Fresh-session comparisons with
  and without `task` produce identical complete MCP result objects. Source files
  in the pinned corpus were unchanged. No models or paid APIs were invoked.
- `git diff --check` passed for the follow-up.
- `prism_verify` returns `review`, not `clean`: a string constant's instruction
  contract cannot be verified as a call graph. Its reference list contains only
  the declaration and `steeringBlock`; the emitted block is covered by tests.
  No missed call sites were reported. [Impact/reference review](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/routing-2026-09-06/impact-review.txt)
  preserves the full sets used for this edit.

## Remaining Proof

Removing an error and shortening instructions are mechanistic improvements,
not proof that an autonomous agent uses fewer tokens. Additional schema text,
remaining searches, reasoning, and cache differences can offset savings.

The next proposed experiment uses the same new binary in both arms and changes
only routing guidance, with all costs attributed to that invocation. See
[proposed protocol](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/routing-2026-09-06/NEXT_TEST.md). It must not be pooled with the completed
before/after comparison or presented as Prism-versus-native evidence. Native
controls and a Sonnet regression check are still needed before a broader claim.

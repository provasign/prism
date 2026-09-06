# Evidence Delivery Candidate

Date: 2026-09-06. Branch: `cand-search-context`. Uncommitted, not released.
This follows the [24-cell panel](../panel-2026-09-06/REPORT.md); that panel and
its binary/evidence are unchanged. No paid model run was launched in this step.

## Changes

1. Impact text now includes declaration/family signatures, test-caller labels,
   and bounded source expressions inside callers. The handler already supplied
   signatures and test labels, but the old renderer discarded them.
2. Root-level `tests/` paths are recognized for impact labeling. This does not
   alter shared verification filtering, remove tests, or change membership.
3. Heuristic-edge uncertainty is explicit: `closed` describes indexed scope,
   not independently proven receiver resolution. Evidence is matched by callee
   name within the reported caller; it cannot resolve homonymous receivers.
4. `prism_lookup` accepts one name as before, or an array of up to ten names.
   File disambiguation and field projection apply to the batch. The same
   single-symbol resolution path handles every entry, retaining misses,
   ambiguity, and per-entry failures.
5. Tool descriptions distinguish affected-site enumeration from body retrieval
   and suggest batched method reads for small local tasks. No new tools or
   mandatory extra calls were added. Existing repository steering files were
   not changed in this step.

Example small-task read:

```json
{"name":["responseWriter.Hijack","responseWriter.CloseNotify","responseWriter.Flush"],"file":"response_writer.go"}
```

## Bounds And Honesty

Impact preserves the entire ordered site inventory. Only supplementary source
is bounded: up to two distinct matching lines per caller, 240 characters per
line/signature, and 8 KiB of caller-expression text across the result. Truncation,
omitted lines, and unavailable indexed evidence are labeled. These are not
whole-body reads or an exhaustive raw-reference scan. Existing index refresh
and stale-index warnings remain in force.

Lookup batches retain whole result records within a 64 KiB serialized-record
budget; they never silently cut a method body. Oversized records are named as
not delivered, with single-name or signature-only continuation. The budget
does not include the outer result envelope/omission metadata. Invalid batches
fail before querying. The scalar API and CLI syntax remain compatible; this
new array input is on the MCP/Invoke surface.

No search/lookup is suppressed because the agent previously saw an impact
result. Verification of ambiguous receivers and relevant behavior remains
available and explicitly encouraged.

## Free Evidence

The probe used the frozen panel corpus pins, fresh independent archives,
the old panel binary, and the new binary. Each old/new comparison used the
same unedited archived source snapshot. It compared every ordered
`qualifiedName/filePath/line` entry across declarations, supers, family,
callers, and declaring types. **No site disappeared or moved.**

| Impact query | Engine entries, unchanged | Callers with source evidence | Before text bytes | After text bytes |
| --- | ---: | ---: | ---: | ---: |
| Gin `responseWriter.Hijack` | 1 | 0/0 | 141 | 216 |
| Django `BaseDatabaseWrapper.get_connection_params` | 12 | 7/7 | 1,189 | 2,063 |
| TypeORM `Driver.escape` | 38 | 25/25 | 3,397 | 6,680 |

These engine counts include test/type entries and are not the benchmark's
production-only, normalized answer counts. TypeORM's 25 callers received 35
distinct source lines within the configured per-caller bounds. Django includes
five signatures and all three production caller expressions, plus its four
test callers, now visibly labeled.

For Gin, **one batch delivered the same three complete method bodies as three
scalar lookups**. Combined text grew from 874 to 974 bytes due to per-request
labels. The proposed saving is two avoided tool/model round trips, not smaller
source text. This does not prove that an autonomous agent will choose the batch.

The impact responses also grew. That is deliberate evidence delivery, not a
measured session-token reduction. The candidate succeeds only if the extra
evidence displaces enough later searches/reads without harming accuracy.

## Verification

- Seven new Go tests cover site identity, source lines, evidence limits,
  missing evidence, root-level test labels, warning retention, batch/scalar
  equivalence, projection, misses, invalid input, oversized bodies, per-entry
  errors, and conservative JSON fallback.
- Targeted tests and the full ordinary Go suite passed. The final candidate
  passed `GOFLAGS= GOWORK=off go test ./... -race -count=1`.
- Live stdio MCP probes exercised both binaries, tool schema discovery,
  impact rendering, and array lookup delivery. Model runs: **0**, model spend: **$0**.
- [Probe results](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/probe-results.json), [Django response](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/django-connparams.after.txt),
  [TypeORM response](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/typeorm-driver-escape.after.txt), and
  [Gin batch response](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/gin-batch-lookup.json) are retained here.
- [Pre-edit impact sets](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/impact-before.txt) are preserved without filtering.
  [Candidate patch](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/candidate.patch) includes the prior implementation plus
  this step, against Prism `a1c7aa6`; it is not an incremental patch against
  the already-dirty working tree.
- Applying that patch to a fresh base archive reproduces all eleven combined
  source/test files byte-for-byte. [Validation and source hashes](https://github.com/provasign/research/blob/dc0972177436c191b8b7c8f30cff406f5006915f/harness/runs/prism-accuracy-efficiency-2026-09-06/artifacts/evidence-delivery-2026-09-06/validation.json)
  record the checks. Prism verify reports no missed sites for the tracked diff;
  new untracked files are covered by compilation, tests, and patch replay.

New binary: `/private/tmp/prism-evidence-delivery-candidate`.
SHA-256: `b8819ec951c78823c0df87b9ac39a9916302322c4799760e772efd125e4c39d0`.
The installed Prism and the frozen panel binary were not replaced.

## Next Measurement

The following measurement was subsequently authorized and completed: eight
valid executions, full recall/precision throughout, 15.3% median paired token
saving and 27.7% estimated cost saving. Both efficiency thresholds were missed.
Current-run spend was $1.206159 estimated. See the [full comparison report](comparison/REPORT.md).
The free-check record above remains unchanged; these are additional paid runs.

The predeclared design was eight fresh Codex executions comparing the frozen panel
Prism binary against this candidate on Gin and Django, two repeats each. Keep
the same pinned tasks/model/effort/prompt, native tools, output scorer, and
per-invocation accounting. Alternate order, retain non-use/errors and every
cost, and do not retry according to quality. Proposed launch budget: about $3;
Codex has no hard billing cap.

Primary checks: no paired recall loss or added false-complete claim, at least
25% median paired token saving and 30% summed estimated cost saving, and valid
arm use in every cell. Also measure searches and lookups after impact, actual
batch adoption, tool errors, and per-task regressions. This before/after study
would establish local candidate effects, not broad product superiority; a
subsequent native-tools comparison and Sonnet regression check remain necessary.

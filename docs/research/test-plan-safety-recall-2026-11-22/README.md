# test-plan tier recall on the 42-task issue bed (2026-11-22)

MKT-08 step 5. Does each tier of `prism test-plan` contain the tests that
fail before the fix? No model is called.

## Method

For each task in `research/harness/results/issue42-manifest.json`
(`harness/tasks/e2e/<id>.json`):

1. Shallow-fetch the base commit from the public GitHub repository.
2. Commit the held-out test patch, so the failing tests exist.
3. Apply the gold source patch, uncommitted.
4. `prism index --allow-heuristic`, then `prism test-plan --format json`.
5. Map each FAIL_TO_PASS id to its file: Java `pkg.Class::m` →
   `pkg/Class.java`, Python and TypeScript ids carry the path, and a Go
   `TestX` maps to the `_test.go` file that declares it.
6. A test is selected by a tier when its file is listed in that tier.

Two binaries ran on the same index:

- **pre**: 6594e4a, before the two tiers: focused, transitive and coarse
  fallback, all in one list.
- **new**: 9d74ab8: run first (focused, transitive) plus run for safety
  (package closure).

`measure.py` produced `results.jsonl`; `tally.py` gives per-test numbers.
`per-task.txt` is the per-task table. No coverage files were present in
these checkouts, so the coverage source did not take part.

## Results

All 42 tasks were measured with no errors: 506 failing tests, all mapped to a
file.

| Tier | Tasks with every failing test in the tier | Failing tests in the tier |
|---|---|---|
| focused (both binaries) | 19 / 42 | 317 / 506 (63%) |
| pre: any file target, coarse fallback included | 38 / 42 | 501 / 506 (99%) |
| new: run for safety | **42 / 42** | **506 / 506 (100%)** |

The four tasks the old list missed are now in the safety tier:
jackson-dataformat-xml pr885, zod pr6129, zod pr6144 and hono pr5199.

Sizes (median over tasks):

- **Old any-target list:** 117 files.
- **New run-first tier:** 1 file (it was the same list before).
- **New safety tier:** 139 test files.

The safety tier holds a median of 99% of the repository's test files. It
narrows only where the dependency graph does: zod 65–68%, and hono pr5199
1% (2 files, one of them the failing test). In these repositories almost
every test package depends on the core package a fix touches. So the
tier is safe, and on small, tightly connected codebases it is close to
"run everything".

## What this does not show

- **Focused recall is low.** The focused tier caught every failing test in
  19 of 42 tasks: 15 of the 23 misses are Java, then Python 4, TypeScript
  3, Go 1. Fresh checkouts had no downloaded dependencies and were indexed
  with `--allow-heuristic`. Whether the compiler pass ran was not recorded,
  so the cause of the Java misses is not established.
- **Heuristic edges may widen the safety tier.** The tier counts every
  dependency edge, heuristic ones included. How much smaller it would be
  on compiler edges alone was not measured.
- **One trial, one task bed, mostly Java and Go repositories with a single
  core package.**

Cost: index median 6.7 s (max 50 s), test-plan median 0.4 s (max 3.9 s).

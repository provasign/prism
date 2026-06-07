# Prism CLI Real-World Benchmark

**Date:** 2026-06-07  
**Repository:** `github.com/provasign/prism`  
**Prism mode:** CLI `--format text`  

This benchmark compares realistic context-gathering tasks on Prism itself:

- **Baseline:** shell-only context gathering with `rg` plus targeted `sed`
  reads an agent would plausibly perform.
- **Prism:** one `prism query ... --format text` command using precise anchor
  terms.

Raw outputs were written to `/tmp/prism-real-bench/` during the run. Token
estimates use `bytes / 4`.

---

## Index State

Before running scenarios:

| Metric | Value |
|---|---:|
| Files seen | 106 |
| Symbols | 686 |
| Edges | 3,242 |

Command:

```bash
prism index .
```

---

## Results

| Scenario | Shell bytes | Prism CLI bytes | Shell est. tokens | Prism est. tokens | Context reduction |
|---|---:|---:|---:|---:|---:|
| S1: Init / CLI steering impact | 19,970 | 12,818 | 4,992 | 3,204 | 35.8% |
| S2: `coverage_gaps` precision | 21,226 | 17,145 | 5,306 | 4,286 | 19.2% |
| S3: CLI text/lean/json output formatting | 15,820 | 14,198 | 3,955 | 3,550 | 10.3% |
| S4: Session cache / savings ledger | 33,134 | 19,922 | 8,284 | 4,980 | 39.9% |
| S5: Release/version/install wiring | 21,246 | 12,157 | 5,312 | 3,039 | 42.8% |

**Average context reduction:** 29.6%  
**Tool-call shape:** 5-6 shell commands per scenario vs. 1 Prism command.

---

## Scenario Notes

### S1: Init / CLI Steering Impact

Prism surfaced `cmdInit`, steering injection, and relevant init tests. The shell
baseline also found these, but required several reads across `internal/cli`,
`internal/config`, and generated steering docs.

Nuance: Prism did not include the full `steeringInstructionsCLI` constant body
in the first response. Editing that prose would need one follow-up
`prism lookup` or `prism read`.

### S2: `coverage_gaps` Precision

Prism returned the direct implementation surface:

- `buildCoverageGaps`
- `hasDirectTestCoverage`
- `hasDirectTestCoverageInSymbols`
- `directTestNames`
- `TestBuildCoverageGaps_*`

The shell baseline pulled in broader docs and CLI mentions of `coverage_gaps`
before narrowing to the actual implementation.

### S3: CLI Output Formatting

Prism found `printOutput`, `printTextOutput`, `printLeanOutput`, and focused
output tests. Savings were modest because this is a compact, well-anchored
surface where targeted shell reads are already efficient.

### S4: Session Cache / Savings Ledger

This was one of the strongest Prism wins. Prism grouped cache, ledger, CLI
persistence, and related tests without pulling entire session files.

### S5: Release / Version / Install Wiring

Prism found release workflow ldflags, installer artifacts, and version wiring
with less output than shell-only reads. Because `Version` is a broad term,
better anchors such as `version.Version`, `release.yml`, `ldflags`, and
`checksums` reduce noise.

---

## Interpretation

Prism CLI text mode is strongest when the task is relational:

- blast radius
- caller/callee tracing
- test discovery
- coverage-gap discovery
- release/config wiring across files

Shell tools remain best for exact string and filename location. The right
workflow is:

```bash
rg "<anchor>"
prism query "<task>" --terms <anchor> --include graph,tests --format text
```

Do not compare Prism output to the first `rg` output. Compare it to all the file
reads the agent would perform after `rg`.

---

## Limitations

- This benchmark measures delivered context bytes, not total model billing
  tokens.
- Scenario design matters. Broad anchors can pull noisy but valid graph
  neighbors; precise `--terms` improves results.
- For editing large constants or prose blocks, a follow-up `prism read` may be
  needed after the first query.

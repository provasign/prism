# Adaptive Search Phase 4 Benchmark — 2026-09-09

## Question

Can Prism make modest text searches complete in one call, report exact counts
when the result is larger than the delivery cap, and do so without materially
regressing ordinary search latency?

## Compared Versions

- Baseline: installed `/opt/homebrew/bin/prism`, `prism v0.72.5`.
- Candidate: the staged Phase 4 diff on repository commit `d2ddbc5`, exported
  with `git checkout-index` so unrelated working-tree edits were excluded.

Environment:

- macOS 26.5.1, arm64.
- ripgrep 15.2.0 backend.
- One warm-up invocation followed by 20 measured CLI processes per case.
- Reported latency is the median wall-clock time. Min and max are included to
  expose variance.
- Fixture repositories were already indexed before measurement.

## Fixtures

### Modest result

Two Go files contain exactly 42 matching lines:

- `alpha.go`: 22 matches.
- `beta.go`: 20 matches.

Putting more than 20 matches in one file exercises the baseline per-file cap.
The default search limit is 25.

### Large single-file result

One Go file contains exactly 500 matching lines. This checks whether the
per-file cap can hide truncation when the sampled global result never reaches
the default limit.

### Absent result

The 42-hit fixture is searched for a string known not to exist.

## Results

### Modest 42-hit result

| Case | Calls | Result | Median | Min–max | Payload |
| --- | ---: | --- | ---: | ---: | ---: |
| v0.72.5 default | 1 | 25 of **at least 40**; incomplete | 34.335 ms | 33.375–36.052 ms | 962 B |
| v0.72.5 default + exhaustive | 2 | Complete 42-hit inventory | 65.282 ms | 62.303–68.133 ms | 1,986 B |
| Phase 4 default | 1 | **Complete, exactly 42** | 34.215 ms | 32.820–35.701 ms | 1,265 B |

The Phase 4 candidate provides the complete answer in one call. Relative to
the baseline workflow required to obtain completeness, it uses:

- 50% fewer Prism calls.
- 47.6% less median wall-clock time.
- 36.3% fewer response bytes.

Compared only with the baseline's incomplete first call, median latency is
effectively unchanged (-0.3%) and the response is 303 bytes larger because it
contains all 42 matching lines rather than a sample.

Baseline headline:

```text
// showing 25 of AT LEAST 40 matches across 2 files — this is a SAMPLE
```

Candidate headline:

```text
// COMPLETE — 42 exact matches across 2 files
```

### Large 500-hit single-file result

| Case | Result | Median | Min–max | Payload |
| --- | --- | ---: | ---: | ---: |
| v0.72.5 default | Five displayed lines; **no truncation warning** | 30.404 ms | 28.798–32.162 ms | 262 B |
| Phase 4 default | 25-line sample of **exactly 500** | 39.003 ms | 37.630–40.341 ms | 681 B |

This fixture exposes a correctness problem beyond the original 42-hit report.
The baseline's 20-hit per-file retrieval cap prevents the global 25-hit limit
from being reached, so the engine does not mark the result truncated. The
renderer then displays only five of those lines, with no indication that 495
matching lines are absent.

The candidate adds 8.6 ms median latency in exchange for an exact denominator
and an explicit sample warning:

```text
// showing 25 of 500 matches across 1 file — this is a SAMPLE
```

The payload increase is not treated as a regression: the baseline's smaller
payload is achieved by silently omitting the existence of most results.

### Absent result

| Case | Median | Min–max | Payload |
| --- | ---: | ---: | ---: |
| v0.72.5 | 30.597 ms | 28.652–32.107 ms | 338 B |
| Phase 4 | 30.388 ms | 28.334–31.848 ms | 338 B |

The candidate's count pass becomes the complete absence check, so it avoids a
second line-retrieval process. Latency and payload are effectively unchanged.

## Candidate Behavior

The candidate performs a compact count pass for default searches:

1. Use the active backend with the same literal or regex semantics, ignore
   rules, globs, and path scopes as the line search.
2. Bound the count pass to one quarter of the search deadline, capped at two
   seconds.
3. If the exact count is at most 64, retrieve and deliver every matching line.
4. If the exact count is larger, retain the default 25-line payload cap while
   reporting the exact total and matched-file count.
5. If counting cannot complete, fall back to the existing lower-bound sample
   behavior rather than claiming exactness.

Explicit non-default limits retain their existing bounded behavior. Exhaustive
search remains available for result sets above the adaptive threshold.

## Verification

The candidate includes tests for:

- Exact count parity across ripgrep, grep, and the native scanner.
- Complete delivery of the 42-hit, two-file fixture.
- Exact counting with bounded delivery for a 100-hit single-file fixture.
- MCP text rendering of all 42 lines and the exact completion headline.
- MCP large-result warnings using an exact denominator rather than
  `AT LEAST`.

Commands completed successfully:

```text
GOFLAGS= GOWORK=off GOCACHE=/private/tmp/prism-go-cache go test ./... -count=1
GOFLAGS= GOWORK=off GOCACHE=/private/tmp/prism-go-cache go test ./internal/textsearch ./internal/mcp -race -count=1
```

## Conclusion

Phase 4 fixes both observed failure modes:

- Small result sets no longer require a sample-then-exhaustive workflow.
- Large results concentrated in one file can no longer look silently complete.

The measured tradeoff is favorable: complete modest searches cost no
additional median latency and materially reduce the complete workflow's time
and payload. Large searches pay a small count-pass latency cost to replace an
ambiguous or incorrect denominator with an exact one.

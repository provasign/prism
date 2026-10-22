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

## Recall, Precision, Turns, and Token Cost

These terms need precise definitions for a text-search benchmark:

- **Exact-site recall** is the share of the fixture's true matching file-and-line
  locations that are explicitly visible in the response. A rollup count or
  `+N more` marker does not identify a site and therefore does not count as
  recalled.
- **Lexical precision** is the share of displayed matching lines that actually
  contain the requested literal. This does not measure whether a hit is
  semantically relevant to a coding task.
- **Round trips** are Prism tool calls needed to obtain the requested evidence.
  Depending on the host's orchestration, two tool calls may or may not occupy
  two visible assistant turns; this CLI benchmark does not claim to measure
  conversation turns directly.
- **Estimated tokens** use Prism's production estimator,
  `ranking.EstimateTokens`, which is response bytes divided by four.

### Modest-result quality and cost

The baseline default output explicitly exposes 14 of the 42 matching
file-and-line locations. Ten are rendered as primary hits and four additional
matching lines happen to be visible as surrounding context. The remaining
locations are represented only by summary counts. The exhaustive follow-up
provides all 42 exact locations in its inventory.

| Metric | v0.72.5 default | v0.72.5 complete workflow | Phase 4 default |
| --- | ---: | ---: | ---: |
| Exact-site recall | 14/42 = 33.3% | 42/42 = 100% | 42/42 = 100% |
| Lexical precision | 14/14 = 100% | 42/42 = 100% | 42/42 = 100% |
| Prism round trips | 1, incomplete | 2 | 1 |
| Estimated response tokens | 240 | 496 | 316 |

For the complete-answer workflow, Phase 4 therefore changes the measured
outcomes by:

- Exact-site recall: +66.7 percentage points on the default response.
- Lexical precision: unchanged at 100%.
- Prism round trips: two to one, a 50% reduction.
- Estimated response tokens: 496 to 316, a 36.3% reduction.

An expert who requests `exhaustive=true` on the first v0.72.5 call can obtain a
complete location inventory in one round trip and approximately 256 estimated
tokens. That route requires knowing in advance that the default result will be
incomplete, and it supplies sampled source text plus a compact inventory rather
than all 42 raw matching lines. Phase 4 optimizes the default sample-then-retry
workflow reported by users; it does not claim to beat a perfectly anticipated
exhaustive request on raw token count.

### Large-result quality and cost

The default renderer deliberately remains sampled above the adaptive threshold.
In the 500-hit fixture, both versions display five exact matching lines after
per-file rendering caps, so default exact-site recall remains 1%. All displayed
lines are true literal matches, so lexical precision remains 100%.

| Metric | v0.72.5 default | Phase 4 default |
| --- | ---: | ---: |
| Exact-site recall | 5/500 = 1% | 5/500 = 1% |
| Lexical precision | 5/5 = 100% | 5/5 = 100% |
| Exact total available | No | Yes: 500 |
| Truncation disclosed | No | Yes |
| Estimated response tokens | 65 | 170 |

Phase 4 is therefore not a token or recall win for default delivery of a large
result. It spends about 105 additional estimated tokens to establish the true
denominator and disclose that the response is a sample. A complete site list
still requires `exhaustive=true`. The gain is precision of the completeness
claim and safer routing, not additional delivered sites.

## Low-turn current-only cells

The low-turn slice ran only fresh current-Phase-4 Sonnet cells. Archived
no-Prism and production-Prism values below were reused; neither reference arm
was rerun.

| Task | No Prism | Production Prism | Current Phase 4 | Current vs no Prism |
| --- | ---: | ---: | ---: | ---: |
| Rich `pr3882` | resolved; 6 turns; 46,330 tokens; $0.0315 | resolved; 6 turns; 67,500 tokens; $0.0369 | resolved; 3 turns; 34,184 tokens; $0.0208 | −3 turns, −26.2% tokens, −34.1% cost |
| Click `pr3228` | unresolved; 11 turns; 94,855 tokens; $0.0678 | unresolved; 8 turns; 102,849 tokens; $0.0608 | unresolved; 11 turns; 145,521 tokens; $0.0761 | same turns, +53.4% tokens, +12.2% cost |

Aggregate current: 1/2 correct fixes, 14 turns, 179,705 tokens, and $0.0969.
No Prism was also 1/2 correct (17 turns, 141,185 tokens, $0.0993); production
Prism was 1/2 correct (14 turns, 170,349 tokens, $0.0976). Thus this small
slice shows a 17.6% turn reduction and 2.5% cost reduction versus no Prism,
but 27.3% more tokens; versus production Prism, turns are equal, tokens are
5.5% higher, and cost is 0.8% lower. The benefit is entirely Rich; Click did
not resolve and consumed more tokens than either reference.

## Click and urllib3 Coding Cells

As a separate end-to-end check, the established coding-suite runner executed
one fresh GPT-5.5 Prism cell for each pinned Click and urllib3 task against each
binary. The runner applied held-out fail-to-pass and pass-to-pass tests; these
cells do not produce a meaningful recall/precision score because they are patch
tasks rather than site-enumeration tasks.

| Task | v0.72.5 result | Phase 4 result | Total tokens, old → new | Cost, old → new | Prism actions, old → new |
| --- | --- | --- | ---: | ---: | ---: |
| Click `pr3244` | resolved; F2P/P2P pass | resolved; F2P/P2P pass | 307,701 → 338,346 | $0.8961 → $0.9205 | 5 → 3 |
| urllib3 `pr3786` | unresolved; F2P fail, P2P pass | unresolved; F2P fail, P2P fail | 673,102 → 603,123 | $1.4935 → $1.4028 | 12 → 4 |

Across the two cells per arm, total model tokens changed from 980,803 to
941,469 (−4.0%) and list-price-equivalent cost from $2.3896 to $2.3233
(−2.8%). Both Codex invocations reported one top-level model turn; the runner's
tool-call traces are the more useful interaction measure here. Total tool calls
were 44 → 58 because the candidate urllib3 run spent more calls on local
inspection/tests even while making fewer Prism calls (17 → 7).

This is a one-trial-per-task diagnostic, not a product verdict. Click is a
clean pass/tie. urllib3 is a hard unresolved task in both arms, and the
candidate's lost pass-to-pass test means it should not be described as an
improvement; repeat trials are required to separate model variance from any
retrieval effect.

## Current-only multi-turn coding cells

To avoid rerunning established arms, the next screen ran only the current Phase
4 candidate with fresh Sonnet sessions on three additional tasks. Existing
production-Prism cells are the reference range.

| Task | Current candidate | Archived production-Prism reference |
| --- | --- | --- |
| Rich `pr3938` | unresolved; F2P failed, P2P passed; 34 turns; 931,538 tokens; $0.4479 | unresolved; 32–33 turns; 815,838–929,757 tokens; $0.4076–$0.4574 |
| Click `pr3466` | resolved; F2P/P2P passed; 20 turns; 410,490 tokens; $0.3017 | 17 turns resolved in one cell, 29 turns unresolved in another |
| Werkzeug `pr3006` | unresolved; F2P failed, P2P passed; 8 turns; 176,231 tokens; $0.1185 | unresolved; 14 turns; 287,698 tokens; $0.1755 |

The current-only aggregate was 62 turns, 1,518,259 total tokens, $0.8680,
and one correct fix out of three. These are independent fresh trials, not
paired baseline comparisons; the archived production references should be
retained rather than rerun for every candidate change.

Against the archived no-Prism Sonnet cells for the same tasks:

| Task | No Prism | Current Phase 4 | Change |
| --- | ---: | ---: | ---: |
| Rich `pr3938` | unresolved; 15 turns; 216,649 tokens; $0.1405 | unresolved; 34 turns; 931,538 tokens; $0.4479 | +19 turns, +330.1% tokens, +218.8% cost |
| Click `pr3466` | unresolved; 25 turns; 464,184 tokens; $0.2776 | resolved; 20 turns; 410,490 tokens; $0.3017 | −5 turns, −11.6% tokens, +8.7% cost; correctness improved |
| Werkzeug `pr3006` | unresolved; 14 turns; 224,594 tokens; $0.1485 | unresolved; 8 turns; 176,231 tokens; $0.1185 | −6 turns, −21.5% tokens, −20.2% cost |

Aggregate: no Prism resolved 0/3 with 54 turns, 905,427 tokens, and $0.5666;
current Phase 4 resolved 1/3 with 62 turns, 1,518,259 tokens, and $0.8680.
That is one additional correct fix, but +14.8% turns, +67.7% tokens, and
+53.2% cost. The gain is concentrated entirely in Click; Rich is much more
expensive without a correctness gain, and Werkzeug is cheaper with the same
failure outcome.

### Absent-result quality and cost

Both versions establish absence within the searched scope in one round trip and
approximately 84 estimated response tokens. The candidate changes neither
measured latency nor payload size materially.

### What remains unmeasured

The deterministic benchmark does not measure semantic precision or bug-finding
success. Those require a task suite with relevance labels. The evidence here
supports a narrower conclusion: Phase 4 improves default exact-site recall for
modest results, preserves literal-match precision, and can remove a follow-up
round trip.

## Fresh Agent Cells on Real Repository Searches

Six fresh GPT-5.5 cells compared installed Prism v0.72.5 with the Phase 4
candidate on three genuine terms in a clean archive of Prism commit
`f1921589ab9b8685f39caa7fc257ecf657cf1a0e`. Arm order was counterbalanced.
Each cell began with the same default `prism_search(scope="text")` request and
could issue the minimum follow-up needed to return every case-insensitive
literal `path:line` location. The independent oracle excluded Prism state
directories, matching the product's search scope.

These are localization cells, so recall and precision are lexical location
metrics, not semantic coding correctness. There is one cell per task; treat the
results as a diagnostic snapshot, not a variance estimate.

| Real repository term | Visible sites | v0.72.5 R / P | Phase 4 R / P | Search calls, old → new | Total model tokens, old → new | Cost, old → new |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `invokeTool` | 28 | 1.000 / 1.000 | 1.000 / 1.000 | 2 → 2 | 40,769 → 42,800 | $0.0988 → $0.1009 |
| `renderSearchAsText` | 39 | 1.000 / 1.000 | 1.000 / 1.000 | 2 → 2 | 42,523 → 40,683 | $0.1072 → $0.1075 |
| `ChangeImpactResult` | 35 | 1.000 / 1.000 | 1.000 / 1.000 | 2 → 1 | 41,783 → 28,493 | $0.1040 → $0.0853 |

Aggregate over these three paired cells:

- Recall: unchanged at 1.000.
- Precision: unchanged at 1.000.
- Prism search round trips: 6 to 5, a 16.7% reduction.
- Request tokens: 123,035 to 109,815, a 10.7% reduction. Of these,
  cache-read tokens changed from 81,408 to 71,168.
- Output tokens: 2,040 to 2,161, a 5.9% increase.
- Total model tokens: 125,075 to 111,976, a 10.5% reduction.
- GPT-5.5 list-price-equivalent cost: $0.3100 to $0.2936, a 5.3%
  reduction. This is not Codex subscription billing.
- Agent wall time: 60.909 to 60.049 seconds, a 1.4% reduction.
- Prism result bytes: 27,598 to 30,924, a 12.1% increase because the default
  candidate response delivers more evidence.

The token reduction came entirely from the `ChangeImpactResult` pair, where
Phase 4 removed the exhaustive follow-up. More evidence in the first response
raised output size, but avoiding the next model/tool cycle saved substantially
more request context.

### Agent-cell defect found

The other two candidate cells still made an exhaustive follow-up because their
first response contradicted itself:

```text
// COMPLETE — 26 exact matches ...
// showing 26 of 26 matches ... this is a SAMPLE ... exhaustive=true
```

The cause is a backend-semantics mismatch. Prism's text search is
case-insensitive. The new grep and native count paths are also
case-insensitive, but `runRgCount` does not pass ripgrep's ignore-case flag.
The count pass therefore saw 26 same-case matches while normal retrieval found
28 or 39 matches after including case variants. That left `Truncated` true,
produced the conflicting warning, and induced the extra call.

Phase 4 is therefore not release-ready from these cells. Add case-insensitive
count parity coverage, fix the ripgrep count arguments, and repeat the paired
cells. The expected post-fix result is one search call for all three tasks; it
must be measured rather than assumed.

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

The deterministic fixtures show that the Phase 4 design addresses both
observed failure modes:

- Small result sets no longer require a sample-then-exhaustive workflow.
- Large results concentrated in one file can no longer look silently complete.

The fresh agent cells also show the intended economic mechanism: eliminating
one follow-up reduced total model tokens by 31.8% and list-price-equivalent cost
by 18.0% in that pair. Across all three pairs, however, the current ripgrep
count path removed only one of three follow-ups because its case sensitivity
does not match Prism search. Fix and rerun that defect before accepting the
aggregate token or turn result as the Phase 4 outcome.

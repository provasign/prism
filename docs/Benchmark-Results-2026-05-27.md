# Prism + Grove Benchmark Results (2026-05-27)

> **Historical note:** This is an early indexing, latency, and repeated-read
> benchmark from before embedded Grove and CLI text-mode agent workflows became
> the default story. For current agent context-gathering results, see
> [Prism CLI Real-World Benchmark](Prism-CLI-Real-World-Benchmark-2026-06-07.md)
> and [Payflow A/B Agent Benchmark](AB-Test-Payflow-2026-06-07.md).

## Scope

This report captures end-to-end benchmark results for Grove and Prism across four project sizes:

- small
- medium
- large
- monorepo-scale

Benchmarks include:

- indexing performance
- query latency
- memory usage (RSS)
- Prism token savings for repeated file reads

## Environment

- Date: 2026-05-27
- OS: macOS
- Go: `go version` (local Go toolchain from `/usr/local/go/bin`)
- Binaries used:
  - `/tmp/grove`
  - `/tmp/prism`
- Grove HTTP endpoint: `http://localhost:7777`
- Prism HTTP endpoints per case: `:8801`, `:8802`, `:8803`, `:8804`

## Test Cases

Synthetic Go projects were generated under `/tmp/prism_grove_bench`:

1. small
- Approx. 61 `.go` files
- Low package fanout

2. medium
- Approx. 801 `.go` files
- Moderate package fanout

3. large
- Approx. 4501 `.go` files
- High package fanout

4. monorepo
- Approx. 9901 `.go` files
- Multiple top-level domains/services (monorepo-like layout)

For each case:

1. Run Grove index and capture time + max RSS
2. Run Grove query and capture latency
3. Run Prism index and capture time + max RSS
4. Run Prism query and capture time + max RSS
5. Start Prism server and call `prism_read` on the same file 3 times
6. Record token savings for read #1, #2, #3
7. Capture Prism server RSS

## Results

| project | files | grove_index_sec | grove_index_rss_mb | grove_query_ms | prism_index_sec | prism_index_rss_mb | prism_query_ms | prism_query_rss_mb | read1_savings | read2_savings | read3_savings | prism_server_rss_kb |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| small | 61 | 0.06 | 30.53 | 6.41 | 30.00 | 12.09 | 680.00 | 29.67 | 0 | 67.5 | 67.5 | 26272 |
| medium | 801 | 0.85 | 55.30 | 6.45 | 3.93 | 12.14 | 680.00 | 22.00 | 56.09756097560976 | 67.07317073170731 | 67.07317073170731 | 21856 |
| large | 4501 | 11.62 | 116.98 | 8.70 | 8.00 | 12.19 | 680.00 | 29.92 | 56.09756097560976 | 67.07317073170731 | 67.07317073170731 | 25712 |
| monorepo | 9901 | 33.96 | 196.17 | 60.92 | 30.02 | 12.19 | 690.00 | 29.69 | 0 | 57.971014492753625 | 57.971014492753625 | 26576 |

## Notes

- Grove index time and memory scale with project size as expected.
- Grove query latency remains low for small/medium/large and increases on monorepo-scale.
- Prism repeated reads show strong token savings after the first read in all cases.
- First-read savings can be near zero depending on file shape and disclosure strategy.
- Prism query latency in this run was roughly stable around 680-690 ms.

## Raw Artifact

- Source CSV: `/tmp/bench_results.csv`

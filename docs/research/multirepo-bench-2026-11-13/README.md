# Multi-repo benchmark, 2026-11-13

Five real Go library/consumer pairs, each checked out side by side with a `go.work`:
spf13/pflag → spf13/cobra, go-git/go-billy → go-git/go-git, prometheus/common →
prometheus/client_golang, charmbracelet/bubbletea → charmbracelet/bubbles, provasign grove → prism.

## 1. Engine accuracy (no LLM)

`engine_accuracy.py`: for every library function, method and type the consumer references,
`prism change-impact <pkg.Name>` is run in the library with the consumer linked. The consumer sites
it lists are compared with a go/types oracle (`mroracle.go`, loads the consumer with tests under the
go.work). Sites in files the oracle does not compile (`*_windows.go`, `_examples/`, nested modules)
are counted apart as "other build", not as errors. The baseline is a whole-word grep of the name over
the consumer's `.go` files.

| pair | symbols | sites | recall | precision | exact | grep precision | grep lines | other build |
|---|---|---|---|---|---|---|---|---|
| pflag → cobra | 45 | 402 | 0.998 | 1.000 | 44/45 | 0.549 | 732 | 0 |
| go-billy → go-git | 41 | 1852 | 0.990 | 0.947 | 36/41 | 0.204 | 9071 | 24 |
| common → client_golang | 61 | 268 | 0.970 | 1.000 | 55/61 | 0.154 | 1740 | 17 |
| bubbletea → bubbles | 15 | 204 | 1.000 | 1.000 | 15/15 | 0.745 | 274 | 0 |
| grove → prism | 32 | 53 | 0.981 | 1.000 | 31/32 | 0.069 | 766 | 0 |
| **total** | **194** | **2779** | **0.990** | **0.964** | **181/194** | **0.221** | **12583** | **41** |

The go-billy precision is one symbol: `BoundOS.Root` reports the 101 calls through the `Chroot.Root`
interface it implements (the override family, the same rule single-repo change-impact uses).

The first pass found four bugs, fixed before the agent run (grove bff8c6e8, prism c4e3aa5):

- Grove type-checked a directory as one package. Where the first file was an external test
  (`package push_test`), go/types rejected every library file in it; otherwise the external tests
  lost their types. Single-repo answers were affected too. Fixed: one type-check per package clause.
- `go list -deps -export ./...` omits packages only tests import, so those tests were untyped. Fixed:
  listed on first use.
- `pkg.Name` (`memfs.New`, `model.LabelSet`, `pflag.NewFlagSet`) failed with "no type named pkg"; a
  scoped type query picked another package's same-named type, or the type's same-named method.
- A resolved module path printed JSON instead of text in the CLI and MCP text forms.

Recall before the fixes, same harness: pflag 0.784, go-billy 0.598, common 0.071, bubbletea 0.980,
grove 0.509.

## 2. Agent A/B (paid)

`run_multirepo.py`: Sonnet 5.5, `claude -p`, prompt caching disabled per cell and cost repriced from
per-request tokens (research harness method). Both arms get the same two clones, the same prompt
(the change, plus: dependents are checked out next to this repository and a go.work builds them
together; update them so every module builds and its tests pass). The prism arm adds
`prism init --interface cli --workspace auto` in the library with the multi-repo binary.

Scoring, mechanical: both modules `go vet -atomic ./...` (type-checks every package and test) for
darwin, linux, windows (grove/prism darwin only, cgo); `go doc` shows the new API and not the old; a
hidden behaviour test (VisitAll stop); the library's tests pass.

| task | native | prism |
|---|---|---|
| pflag: rename FlagSet.SetAnnotation | OK $0.055, 7 req | OK $0.051, 7 req |
| pflag: VisitAll callback returns bool (stop early) | OK $0.199, 20 req | OK $0.197, 19 req |
| go-billy: rename interface method Symlink.Readlink (implementers in go-git) | OK $0.089, 8 req | OK $0.059, 7 req |
| go-billy: rename util.TempDir → MkdirTemp | OK $0.073, 9 req | OK $0.035, 5 req |
| common: MetricFamilyToText returns only error | OK $0.088, 9 req | OK $0.104, 9 req |
| common: rename expfmt.NewFormat → FormatOf | OK $0.033, 4 req | OK $0.038, 5 req |
| bubbletea: rename type PasteMsg → PasteEvent | OK $0.032, 5 req | OK $0.056, 8 req |
| grove: Engine.Neighbors kinds variadic → slice | OK $0.049, 6 req | OK $0.065, 8 req |

Cost is task-only at the 5m cache rate (startup reported apart).

- Resolved 8/8 both arms.
- Task cost prism/native 0.979x; with startup 1.004x. Requests 68 vs 68.
- Prism arm: 14 prism calls (change-impact first on every task), grep calls 21 vs 42.
- Raw uncached spend for the run: $8.78.
- The MetricFamilyToText pair was first scored failed in both arms by an over-strict `go doc` check
  (it required `) error`; both agents wrote the valid named result `(err error)`). Re-scored from the
  saved diffs (`--rescore`); recorded in results.json under `rescored`.

Reading: with the consumer's location in the prompt and a compiler to catch misses, a Go agent finds
consumer sites with grep and the build; Prism ties on correctness and cost here. Its measured edge is
precision (0.96 vs 0.22 for grep) and the answer arriving in one call; where the tasks were widest
(go-billy) it was cheaper, where they were small renames it cost a request or two more. Not measured:
consumers the agent is not told about, languages without a compiler safety net, consumers not
checked out (link server).

Files: `results.json`, `run.log`, `tally.txt`, `accuracy-*.json`, `tasks.json`.

## 3. Real cross-repository tasks from open-source history (paid)

`make_real_tasks.py`, `real_tasks.py`, `run_real.py`. Each task is one change that landed as two commits in
two repositories after June 2026: the library commit and the consumer commit that needed it. The cell checks
out both at their parents, side by side under a go.work; the agent works in the consumer and gets the PR's
problem text (or linked issue) plus the public names the tests call. Scoring is SWE-bench style: the gold
commits' test files (and test-helper packages) replace the agent's, and the tests validated to fail at base
and pass at gold must pass. kubo's `test/cli` tests fail at gold on this machine (network/ports) and are not
counted, so three kubo tasks are scored on boxo's tests (and kubo unit tests where they exist).
Same measurement as the release benchmark: Sonnet 5.5, prompt caching off per cell, cost repriced at 5m,
startup apart.

| task | repos | native | prism |
|---|---|---|---|
| empty Bootstrap list still dials backup peers | boxo + kubo | OK $0.176, 13 req | OK $0.089, 9 req |
| IPIP-548 Ipfs-Uri gateway header | boxo + kubo | fail $0.493, 29 req | fail $0.323, 19 req |
| accept ipfs:// and ipns:// URIs | boxo + kubo | fail $0.449, 25 req | fail $0.598, 34 req |
| bound IPNS caching, validate lifetimes | boxo + kubo | fail $0.552, 33 req | fail $0.290, 21 req |
| store-only QuickStatus for prism status | grove + prism | OK $0.194, 12 req | OK $0.110, 9 req |
| doctor shows Grove's capability manifest | grove + prism | fail $0.156, 14 req | fail $0.122, 11 req |
| promhttp offers OpenMetrics 2.0-rc | common + client_golang | fail $0.223, 15 req | OK $0.336, 23 req |

- Resolved: native 2/7, prism 3/7 (prism-only win: OpenMetrics; native made the rc format identical to 2.0.0
  on the wire, the prism agent traced the format through encode/decode and matched the tests).
- Task cost prism/native 0.833x ($1.868 vs $2.243); with startup 0.864x. Requests 126 vs 141, output tokens
  74k vs 87k. Prism cheaper on 5 of 7 tasks.
- 42 prism calls in the prism arm; grep calls 36 vs 84.
- Raw uncached spend $28.29.
- Both-failed tasks are mostly task-inherent: hidden tests call unexported names the issue cannot reveal
  (`ipfsUriHeaderValue`, `CidFromArg`). Scorer fixes after the run (test-helper package `internal/ipnstest`
  copied with the gold tests; agents' own test edits reverted before the gold tests are written) were
  re-scored from the saved diffs and changed no outcome; recorded under `rescored`.
- Dropped at validation: grove/prism heuristic-refs (no test fails at base). Not attempted: kubo CAR
  hardening (needs two more unreleased repos) and MFS (22 files incl. FUSE).

Seven tasks is a small bed; per-pair cost swings ±40% run to run on the release benchmark, so the cost ratio
is indicative, not settled. For comparison the release benchmark (41 single-repo issues, v0.87.37) measured
task cost 0.916x and total 0.976x.

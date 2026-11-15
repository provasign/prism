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

## 4. More real tasks (validated, not yet run)

15 more tasks pass validation, from 9 library/consumer pairs. With the 7 above, the bed is 22. Mining:
`mine_mr.py`; corpus clones in `~/.cache/prism-research/multirepo-corpus/`. No agent has run on them yet.

`real_tasks.py validate` now also runs the app's gold tests against the *base* library (`app_needs_lib`).
An app test listed there fails without the library change, so the task cannot be solved in one repo.

| task | lib → app | lib F2P | app F2P | app tests needing lib |
|---|---|---|---|---|
| crush-gzip-requests | fantasy → crush | 5 | 1 | 1 |
| crush-brotli-requests | fantasy → crush | 5 | 1 | 1 |
| crush-stream-reset | fantasy → crush | 7 | 2 | 0 |
| cosign-user-agent | sigstore-go → cosign | 1 | 1 | 0 |
| cosign-x509-chain | sigstore-go → cosign | 4 | 4 | 4 |
| ice-turn-context | pion/turn → pion/ice | 5 | 2 | 2 |
| turn-unknown-attrs | pion/stun → pion/turn | 1 | 1 | 1 |
| webrtc-sctp-startcontext | pion/sctp → pion/webrtc | 7 | 4 | 4 |
| webtransport-capsule-api | quic-go → webtransport-go | 6 | 11 | 11 |
| ts-per-peer-psk | tailscale/wireguard-go → tailscale | 1 | 1 | 1 |
| lk-webhook-drain | livekit/protocol → livekit | 0* | 3 | 1 |
| lk-h264-profiles | livekit/protocol → livekit | 0 | 3 | 3 |
| lk-h264-baseline-default | livekit/protocol → livekit | 0 | 1 | 1 |
| lk-red-depacketize | mediatransportutil → livekit | 4 | 2 | 2 |
| lk-harden-inputs | mediatransportutil → livekit | 2 | 5 | 1 |

\* the protocol test fails at gold on this machine and is not counted.

Every library and app commit is dated 2026-07-01 or later. Gold non-test patches (both repos) range from
10 lines (h264-baseline) to about 340 lines (x509-chain).

The first 7 were re-validated with the new check: identical F2P sets. Three of them are scored on library tests only (ipfs-uri-header, ipns-lifetime-bounds, quickstatus); the other four have app tests that need the library change.

**Statement change.** Every statement now ends with the names and signatures the hidden tests call, including unexported helpers that the gold change adds (SWE-bench-Pro style, the same text for both arms). The first 7 tasks were rewritten the same way after their run in section 3, so section 3's results used the older statements and are not pooled with a run on this bed.

**Dropped at mining or validation:**

| candidates | reason |
|---|---|
| grove→prism readiness, graph-correctness, test-discovery, map-cycles, stale-delta | gold patch is 1,000+ lines or a bundle of unrelated fixes |
| grove→prism stale-refresh | 1,000-line app commit mixes unrelated work (would need test-file filtering) |
| tailscale multisocketbind, batch-buf | 1,100–1,800-line dataplane refactors |
| tailscale injection-queue | app tests pass with the base library (single-repo work) |
| webrtc nil-interceptor-attrs, webtransport TryWriteAll, kubo IPIP-550 profile | only library tests are scored |
| crush OSC 7501, bubbletea←ultraviolet | needs a third repo (ultraviolet / x/ansi) |
| crush routed-model, refusal banner | tests call many unexported UI names |
| syft←stereoscope, grype←syft | tests need Docker image fixtures |
| tetragon←cilium/ebpf | vendored dependencies (go.work cannot override) |
| gh-cli←go-gh | pinned go-gh commit no longer reachable |
| livekit agent/SIP/telemetry pairs, temporal←api-go | need `.proto` regeneration |
| tailscale roaming fix | dated June 2026 (before cutoff) |

Sources mined with no usable pair: go-git←billy, cobra←pflag, bubbles←bubbletea, caddy←certmagic,
alertmanager/prometheus←common, containerd, nerdctl, nats, conftest←opa, goreleaser←nfpm, buf←protocompile,
ent←atlas, k6←sobek, opentofu←hcl, redpanda connect←benthos, docker/cli←moby, livekit egress/ingress/sip/sdk.

Not checked yet: whether `run_real.py` cells build each new pair (Go toolchains, module downloads). Before a
paid run, a single probe through `run_real.py` would show that.

## 5. Agent A/B on the 22-task bed (paid, 2026-11-13/14)

`run_real.py --out ~/.cache/prism-research/mr-real-probe2-2026-11-13`, Sonnet 5.5 both arms, caching off per
cell, cost repriced at 5m, startup apart. Per-task rows: `tally-22.txt`.

| | native | prism CLI |
|---|---|---|
| resolved | 10/22 | 9/22 |
| task cost (5m) | $7.653 | $6.914 (0.903x) |
| incl. startup | $8.930 | $8.240 (0.923x) |
| requests | 458 | 420 |
| output tokens | 284k | 275k |
| grep calls | 273 | 155 (+153 prism calls) |

- Resolve differences: native-only kubo-bootstrap-empty-list (prism returned `ErrNotEnoughBootstrapPeers`
  where gold returns nil, a reading the statement allows) and crush-brotli-requests (prism failed one subtest of
  the retry/fallback test); prism-only lk-harden-inputs. On the 15 new tasks: 7 vs 7.
- The cost gap is mostly one task: cosign-x509-chain (both failed) cost native $1.696 / 69 requests vs prism
  $0.930 / 50. Without it, task cost is 1.004x ($5.983 vs $5.958). Read the cost result as parity.
- Raw uncached spend $102.86 including the two probes. The cap is checked between cells, so the last cell
  went $2.86 past $100. Two killed partial cells (stop on resolve loss, rate limits) are not in that figure.
- The run stopped three times: once by the resolve-loss rule (resumed on Topo's word), twice by rate limits
  (429, cells with no requests; those cells were cleared and rerun).
- 22 pairs is still a small bed; per-pair cost swings ±40%.

### 5a. Where prism lost or cost more (transcript analysis)

Script: per-call tool outputs of both arms (`~/.cache/prism-research/mr-analysis/calls.py`).

**Prism limitation found: Go compiler-backed analysis did not run on 9 of 22 tasks.** Grove's Go analyzer
runs `go list` with `GOTOOLCHAIN=local` and `GOWORK=off` (grove `internal/native/go.go:321-335`, a deliberate
no-toolchain-download rule). The machine has go 1.26.3; kubo (x4), crush (x3), cosign-user-agent and
ts-per-peer-psk require newer Go, so `go list` failed ("go.mod requires go >= 1.27 (running go 1.26.3;
GOTOOLCHAIN=local)") and prism fell back to name matching. Nothing told the agent except a footnote in
change-impact ("compiler-backed completion is not confirmed"); `prism index` did not report it as a
readiness issue (readiness covers TypeScript and Java diagnostics, not Go). A user whose installed Go is older
than the repo's go directive hits the same thing, while their own `go build` works by auto-downloading.

| group | tasks | resolved native / prism | task cost prism/native |
|---|---|---|---|
| Go analysis ran | 13 | 6 / 7 | 0.833x (0.98x without cosign-x509-chain) |
| Go analysis failed | 9 | 4 / 2 | 1.032x |

Correlation, not proof: neither lost task below was caused by a wrong prism answer.

**The two native-only wins were agent judgment, not prism output.**
- kubo-bootstrap-empty-list: one correct prism search; the agent then read `bootstrap.go` in full and chose to
  return `ErrNotEnoughBootstrapPeers` where gold returns nil. The statement allows either.
- crush-brotli-requests: 15 of 17 subtests pass; the two failures need refused encodings remembered across
  requests (gold added that; the base had none; the statement does not ask for it). Native inferred it.

**Multi-repo CLI friction (prism, fixable):**
- Default `prism search` shows linked-repo hits only as a count ("boxo 2 in 2 file(s), --repo lists them"),
  so the agent reruns with `--repo`: 38 of 153 prism invocations.
- `--path ../boxo/...` is rejected as outside the root, and the hint says to run prism there or use its
  MCP server, instead of `--repo boxo` (3 calls).
- `change-impact` takes one name; agents passed several (2 calls), and the error says "takes one directory".
- The ambiguous-name answer tells a CLI agent to re-run as MCP JSON (`{"op":"change_impact",...}`).
- An interface method (`Engine.SetPeerConfigFunc`) gave 1 site, partial, without tying the call
  `e.SetPeerConfigFunc(...)` in `local.go:622` (that cell had no Go analysis, see above).
- Path-like terms are searched as text (`sigstore-go/` returned ~10k chars of fuzz-script lines).
- 2 unknown-flag errors (`--lifetime`, `--head`) from agents passing grep-style flags.

**Context volume:** tool output is about equal (prism arm 1,164k chars vs native 1,202k). Prism calls average
4.4k chars vs 2.4k for a grep, and 120k of the prism arm's 507k read chars re-read files prism had already
delivered source from (upper bound, matched by file name).

### 5b. Rerun of the Go-degraded tasks with working Go analysis (paid, 2026-11-14)

Both arms on Go 1.27.4 (`MR_GO_BIN`), prism rebuilt with Go 1.27.4 (`MR_PRISM_BIN=bin/prism-mr-go1274`), and
the prism arm's setup runs `go mod download` (GOWORK=off) in both repos before `prism index`
(`MR_GO_MOD_DOWNLOAD=1`). `prism status` confirmed compiler-backed Go analysis in every cell. Out dir
`~/.cache/prism-research/mr-real-go1274-2026-11-14`; rows in `tally-go1274.txt`. Raw spend $25.87.

| same 8 tasks | resolved native / prism | task cost prism/native |
|---|---|---|
| first run (no Go analysis) | 4 / 2 | 1.037x |
| rerun (Go analysis) | 3 / 3 | 0.942x |

- kubo-bootstrap flipped (native fail, prism OK); crush-brotli failed the same way again (refusal memory).
- cosign-user-agent went from 1.39x to 0.84x; ts-per-peer-psk is still dearer (1.28x, 16 prism calls), but
  change-impact is now `closed` with 4 sites and names the interface family, where the first run was a
  1-site partial.
- 21-task picture with Go analysis everywhere (13 unaffected tasks from §5 + these 8): resolved native 9,
  prism 10; task cost 0.867x (0.965x without cosign-x509-chain).
- kubo-ipns-lifetime-bounds is left out: with GOWORK=off kubo at that commit does not type-check on its own
  (`google.golang.org/grpc ... undefined: http2.TrailerPrefix`; it builds only in the workspace), so
  `prism index` exits 3 even after `go mod download`.

**Two more prism limitations found on the way:**
1. A prism built with Go 1.26 panics (grove `internal/native/go_importer.go:167`, pkgbits decoder) when the
   `go` on PATH is 1.27 — `prism init`/`index` crash instead of degrading.
2. GOWORK=off in multi-repo mode: grove type-checks each repo against its own go.mod and the module cache,
   not the linked checkout. Needs `go mod download` first (prism's own fix hint), sees the pinned library
   release rather than the agent's library edits, and fails outright where a repo builds only as a workspace.

### 5c. Rerun with the fixed build on the machine's default Go (paid, 2026-11-14)

Fixes in `~/Projects/provasign-mr/{grove,prism}` (grove 318471fb, 63c042a6; prism a10aea8; unpushed):
- grove runs a cached `golang.org/toolchain` from GOMODCACHE when the installed Go is older than the
  go.mod/go.work line (no download).
- grove recovers the export-data panic and types dependencies from source.
- grove runs workspace `go list -e` and reports packages that did not fully load. It prefers a clean
  standalone listing over a workspace listing with load errors.
- prism readiness reports Go failures ("requires go >=", "go list failed", unreadable export data).
- CLI fixes: change-impact with several names says so, ambiguous-name hints use CLI form, `--path ../lib`
  routes to the linked repo, the search tail shows up to 5 linked hits per repo inline.

Setup: default go1.26.3 on PATH, `MR_PRISM_BIN=bin/prism-mr-fix`, `MR_GO_MOD_DOWNLOAD=1`, all 9 Go-degraded
tasks (kubo-ipns-lifetime-bounds now indexes). Rows in `tally-fix.txt`. Raw spend $34.65 across all
attempts, including discarded and empty cells.

| same 9 tasks | resolved native / prism | task cost prism/native |
|---|---|---|
| fixed build, go1.26.3 | 4 / 2 | 0.976x ($2.497 vs $2.559) |

- The resolve losses are the two known agent-judgment misses: kubo-bootstrap (`ErrNotEnoughBootstrapPeers`
  again) and crush-brotli (refusal memory again). Each has now gone both ways across runs.
- Both OK: cosign-user-agent 0.92x, ts-per-peer-psk 0.81x (was 1.28x in §5b; 10 prism calls, was 16).
- Both NO in every run so far: kubo-ipfs-uri-header, kubo-native-ipfs-uris, kubo-ipns-lifetime-bounds,
  crush-gzip, crush-stream-reset. They do not separate the arms.
- The first crush-brotli attempt produced empty cells in 0.8 s (rate limit); it was rerun.
- Read this as cost parity with no measured resolve gain. On 9 pairs a 2-resolve gap from two coin-flip
  tasks is noise. The fixes remove a silent failure; they have not shown an agent-level win.

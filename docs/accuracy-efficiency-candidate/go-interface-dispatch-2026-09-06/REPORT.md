# Go Interface Caller Recovery

## Result

Grove `ff9adefb7ce84c7124d95b35295f3d29aec111cd` on
`fix-go-interface-impact` repairs a native Go interface-dispatch gap. A fresh
Gin replay at `8d0468f72897652485933b845253386f9147a8bf` shows:

| Impact query | Callers before | Callers after |
| --- | ---: | ---: |
| responseWriter.CloseNotify | 0 | 2 |
| responseWriter.Hijack | 0 | 3 |

The recovered CloseNotify callers are `Context.Stream` and
`TestResponseWriterHijack`. Hijack adds `TestResponseWriterHijack`,
`TestResponseWriterHijackAfterWrite`, and `TestResponseWriterHijackAfterWriteHeaderNow`.
No previously returned site disappears. Family, supers and declaringTypes
remain empty: this repairs caller recall, not the complete interface contract.

Prism's compact responses grow from 372 to 781 bytes and 382 to 960 bytes
respectively because they now contain the missing callers. Both remain
`partial`, with the coverage warning preserved. No model calls ran; spend was
zero. **Session token savings and autonomous accuracy remain unmeasured.**
The latest paid comparison remains the scoped-lookup failure: 9.4% more
aggregate tokens and 5.8% higher estimated cost.

## Implementation And Checks

The existing native `go/types` pass now computes possible local interface-call
targets using complete method sets. Go checks parameter/result identity,
pointer/value receivers, promoted methods, shadowing and ambiguous embedding.
Targets bind to the exact indexed source file; external declarations cannot
be guessed from local names. Invalid signatures and unresolved embedded
contracts are rejected without discarding valid calls elsewhere in the package.
Targets are cached per interface during the package analysis.

Seven new tests cover those positive/negative cases, aliases, method expressions,
and removal of stale dispatch edges after a method stops implementing the
interface. Full and incremental post-edit graph snapshots agree. The full
Grove race suite and the full Prism race suite with the temporary Grove
replacement pass. Prism verifies all four changed Grove files with no missed
sites. Final archived source hashes match the Grove commit exactly.

Raw evidence, protocol, exact sources and the read-only verifier are in
[research at 366d40e](https://github.com/provasign/research/tree/366d40e4d4faba5ce5e8d15e4da1ac3650023906/harness/runs/prism-go-interface-dispatch-2026-09-06).
All 117 artifact hashes and the three passing development replay summaries
verify. Initial and hardened replays are preserved separately; they are not
statistical repeats. The ledger-cache sandbox failure is retained. Raw
`go version -m` output retains its trailing tabs rather than changing evidence
to satisfy a whitespace check.

## Limits And Next Work

This patch discovers implementations checked in the caller's package. It does
not enumerate cross-package implementors or generic instantiations, repair
heuristic-edge precision, or materialize inherited interface declarations and
families. Native analysis and usable type information are required. The Gin
replay is a targeted positive check, not an exhaustive impact oracle.

Next, represent inherited contracts/families and test cross-package dispatch
with negative controls and incremental equivalence. Retain `partial` until
those boundaries are measurable. Then test task-depth routing and total session
cost under a new frozen comparison with native Sonnet/Codex controls; do not
suppress searches needed to recover missing evidence.

The Grove change is committed but not pushed, tagged or released. Prism's
`go.mod` still pins v0.43.1; integration used an external temporary modfile and
test binary only. No installed binary, original checkout or global agent
configuration changed. A reviewed Grove release and explicit Prism dependency
update are still required to ship it.

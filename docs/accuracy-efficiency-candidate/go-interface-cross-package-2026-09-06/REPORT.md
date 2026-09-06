# Imported Go Interface Contracts

Grove `8dad45479ff72b78c7c8e930c6d49cbed18b39a8` extends local interface
family materialization across import-connected project packages. The engine
source-loads only imported project packages known to declare interfaces, then
uses `go/types` method-set identity. It does not infer contracts from matching
names.

## Accuracy Result

The deterministic fixture has three packages:

- `api.Writer` embeds `http.CloseNotifier` and `api.Stream` calls the inherited
  `CloseNotify` member.
- `impl.Writer` implements the exact `<-chan bool` signature and asserts the
  API contract.
- `impl.Wrong` returns `chan bool`, while an unrelated `util` package is also
  imported.

| Arm | `Writer.CloseNotify` scoped to `api/api.go` |
| --- | --- |
| Grove `184f8a9e` | Exit 1: interface “declares no method” |
| Grove `8dad4547` | 1 declaration, 1 declaring type, 1 caller, 1 exact family member |

The candidate recovers `api.Writer.CloseNotify`, `api.Writer`, `api.Stream`,
and `impl.Writer.CloseNotify`. It excludes `impl.Wrong.CloseNotify`. The
unrelated local import does not prevent type checking of the selected API
contract. The result remains `partial`; this is a concrete recall repair, not
a claim of complete Go dispatch coverage.

## Correctness Guards

The tests cover cross-package source loading, embedded external members, exact
return direction, unrelated local imports, full/incremental edge equivalence,
stale contract removal after an incompatible edit, and synthetic endpoint
carry. Existing parameter/result, case, receiver, promotion, shadowing,
ambiguity, invalid-type, and exact-file controls remain green.

The full Grove race suite and full Prism race suite linked through an external
modfile pass. Prism verification reports no missed sites across the tracked
diff; the two added files are directly covered by their package tests.

## Index Cost Guard

Six trials per arm alternated order while fresh-indexing the same archive of
Prism commit `5bdc73df`:

| Metric | Before | After |
| --- | ---: | ---: |
| Median wall time | 0.6012s | 0.5984s |
| Mean wall time | 0.6030s | 0.5992s |
| Symbols | 1,454 | 1,454 |
| Edges | 4,911 | 4,911 |

The median delta is -2.7ms (-0.45%). This small local sample supports “no
material regression,” not “faster indexing.” The selective preload replaced
an earlier exploratory version that loaded every project package and roughly
doubled this snapshot’s index time; that version was not committed.

## Evidence And Limits

The [research archive at `0059bb2`](https://github.com/provasign/research/tree/0059bb2c2359d82b56bac5a323bbd4bc52c68449/harness/runs/prism-go-cross-package-2026-09-06)
retains the protocol, fixture, raw payloads and stderr, all timing samples,
binary metadata, exact source snapshots, and a read-only verifier. It checks
28 artifact hashes.

No model ran and spend was zero. Session token savings and autonomous task
accuracy are unmeasured. Generic interfaces and structurally compatible
implementations in packages with no import connection remain outside this
increment. Prism must retain its partial warning and recovery-search guidance.

The Grove branch is pushed, but no Grove release is tagged and Prism still
pins v0.43.1. This report does not make the current Prism release contain the
repair.

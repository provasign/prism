# Impact Coverage And Task-Depth Routing

Date: 2026-09-06. Product commit: `cef7dad` on `cand-search-context-clean`.
**Coverage gap reproduced; metadata corrected. No new session savings measured.**

## Confirmed Problem

The previous Gin transcript showed declarations-only `closed` impact results
followed by text searches exposing interface calls. A minimal Go fixture now
reproduces the same failure: an interface embeds `http.CloseNotifier`, `Stream`
calls through it, but Grove reports one declaration, zero callers, zero
declaring types, and `closed`. This is not a renderer losing returned sites;
the embedded engine has not modeled the required relationship.

Prism must not use that label to justify telling the agent to stop searching.
The new regression failed before the change and passes now because the tool
reports its coverage limitation, not because the missing edge was recovered.

## Product Change

- Otherwise-closed Go method/interface impact results now say `partial` and
  warn that interface contracts/callers may be missing. This is deliberately
  conservative, including queries whose returned inventory happens to suffice.
  Existing non-closed tiers and non-Go completeness labels are unchanged.
- All declarations, supers, family, callers and declaring types remain intact.
  The warning survives text rendering and cached pointers. Partial results do
  not enter the closed-impact scope counter, and wider-anchor hints cannot
  restore an unjustified closed claim.
- Steering no longer routes a generic "known symbol" to impact. It separates
  read-only body inspection from affected-site enumeration and discourages
  automatic body reads when the needed signatures/call expressions are already
  delivered. Before-edit and final-verification safeguards remain.

No new parser, model call or graph traversal was added. Installed binaries and
existing agent configuration files were not changed; the steering template
takes effect when instructions are regenerated. The underlying Grove engine
and raw adapter results are unchanged. Engine-level caller recall remains open.

## Free Replay And Verification

| Check | Result |
| --- | --- |
| Gin CloseNotify impact bytes / tier | 206 -> 372; closed -> partial |
| Gin Hijack impact bytes / tier | 216 -> 382; closed -> partial |
| Gin required-site arrays and body responses | Identical |
| Django impact and eight-body responses | Identical |
| Django gold identities already present in impact | 8/8 |
| Benchmark model executions / spend | 0 / $0 |

Fresh pinned archives had no HEAD, and their source files remained unchanged.
The 166-byte warning increase per Gin impact is intentional accuracy overhead,
not a token saving. Four new Go tests, including an eleven-case capability
matrix, cover the fixture, inventory/tier preservation, rendering and cached
warnings. Updated wider-anchor/routing tests and the full race-enabled suite
pass. Prism verify found only the steering constant's manual-review obligation:
its declaration and `steeringBlock` references were checked with the safeguard
tests. All nine source hashes and the archived patch match the product commit.

The [research replay](https://github.com/provasign/research/tree/1aa2adcb8be12d7d43d7f41c17575269225f5075/harness/runs/prism-impact-coverage-2026-09-06)
retains both attempts. The first hit a sandbox denial on the normal CLI ledger
cache after successful MCP calls; the same source/probe passed with cache
permission. The [read-only verifier](https://github.com/provasign/research/blob/1aa2adcb8be12d7d43d7f41c17575269225f5075/harness/runs/prism-impact-coverage-2026-09-06/verify.py)
checks 58 artifact hashes and replays the structural/text comparisons. Earlier
archives and the eight-cell answer/usage audit still pass unchanged.

## Next Gate

Repair the embedded engine's Go interface and embedded-method modeling, with
receiver/signature negative controls, before restoring any closed guarantee.
The task-depth routing change needs its own frozen model comparison; do not
attribute the previous binary study's outcomes to guidance it did not receive.
Native Sonnet/Codex controls remain necessary for broad product claims.

The latest paid result remains 100% exact-site recall/precision, **9.4% more
aggregate tokens and 5.8% higher estimated cost** than the prior Prism binary.
Neither efficiency target is met. This patch improves the honesty of coverage
reporting, not demonstrated caller recall, patch success, or session efficiency.

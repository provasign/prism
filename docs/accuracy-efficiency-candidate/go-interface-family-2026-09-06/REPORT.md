# Go Interface Family Materialization

Grove `184f8a9ee57170d41e96029e70d578655f901a34` extends the native caller
repair with local Go contract edges. The engine now connects interface calls
to deterministic synthetic member IDs and emits type-checked implements and
overrides edges for local method sets. Existing change-impact traversal can
therefore surface members inherited from an embedded external interface.

On independent fresh indexes of Gin `8d0468f7`:

| Query | Before | After |
| --- | --- | --- |
| `responseWriter.CloseNotify` | 1 declaration, 2 callers | same, plus `ResponseWriter.CloseNotify` super and declaring interface |
| `ResponseWriter.CloseNotify` | 1 concrete family member, 2 callers | same, plus inherited declaration and declaring interface |

No previously returned site disappears. Both results remain `partial`.
This is a real contract-recall improvement, not a complete Go impact proof.

The full Grove race suite and the full Prism race suite linked through a
temporary external modfile pass. Tests cover exact parameters and results,
case, pointer/value receivers, promotion, shadowing, ambiguity, invalid types,
exact-file binding, inherited interface roots, supers, and full/incremental
edge equivalence. Prism reports no missed sites across the five-file follow-up.

The [research archive at 030ae78](https://github.com/provasign/research/tree/030ae78aa73cc5a3529b29b72c23d27aff88b2f4/harness/runs/prism-go-interface-family-2026-09-06)
retains the protocol, raw responses, exact sources and verifier. The final
replay checks 25 hashes and matches all six committed Grove source files.

No model ran and spend was zero. Session token savings and autonomous accuracy
are unmeasured; the latest paid result remains a 9.4% aggregate token increase
and 5.8% estimated cost increase. Added response content is required evidence,
not a saving.

Cross-package source loading remains the next engine boundary. Grove's current
per-package type checker cannot reliably load another unbuilt package in the
same module, so this change deliberately does not use same-name matching as a
substitute. Generic interfaces also remain out of scope. Keep Prism's partial
warning and recovery-search guidance.

The Grove feature branch is published, but no release is tagged and Prism
still pins Grove v0.43.1. Nothing is merged to main or installed by this work.

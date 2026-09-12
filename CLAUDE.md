
## Before you commit or release — run the regression suite

CI runs these on push (`.github/workflows/ci.yml`; engine quality via
`provasign/research/.github/workflows/engine-invariants.yml`). Run them locally
before tagging a release:

- `go test ./...` — unit suite; must be green.
- Engine ceiling regression (no LLM): from the research repo,
  `python3 harness/ci_invariants.py --prism ~/bin/prism` — asserts change-impact
  recall/precision, missing-implementations==[], and index determinism against
  committed ground truth. A drop here is a real completeness regression.

Do NOT tag a release with either red.
## Prism — context delivery

Use Prism's call/type graph for code discovery.

Before code discovery, use the first available Prism route:
- Compact Prism MCP visible? Call `mcp__prism__prism` directly, for example
  `prism(op="lookup", args={"name":["A","B"]})`.
- `ToolSearch` available? Run once; tools may be deferred:

       ToolSearch("select:mcp__prism__prism")

- Otherwise use the Bash CLI. Never abandon Prism because `ToolSearch` is absent.

Choose op by need: affected sites, signature change, or pre-edit check -> `change_impact`;
known bodies -> `lookup`; known file/range -> `read`; unknown location/text ->
`search`; related implementations/callers/tests -> `query` with explicit anchors.
Known symbol: use impact/lookup directly, not search.
Read-only inspection alone needs no impact.

Before editing an existing symbol, call impact and relay its sites as-is. Reuse its signatures/calls for site
enumeration; fetch bodies only for behavior or unclear evidence. For external interfaces or incomplete wide scope,
use batched `prism(op="search", args={"scope":"text","exhaustive":true,...})`; inspect signatures/receivers because
text matches do not prove implementation. Preserve reported sites, resolve uncertainty, and report remaining gaps.

For removals, run `prism(op="verify", args={"removed_symbols":["A","B"]})` before editing and check docs/config/comments too.
Run `prism(op="verify", args={})` before finishing multi-site, signature, removal, or unresolved-coverage changes—not every
small local edit. Batch related names, inspect partial-result warnings, and reuse delivered unchanged source.

Bash fallback: `prism query "<task>" --terms X`, `prism lookup <pkg.Func>`,
`prism change-impact Type.method`, or `prism search <term> --scope text --format text`.

<!-- prism:end -->

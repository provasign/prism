# Layered Intelligence: Prism's Graph as Facts, Closures, Views, and Flows

Status: Phase 1 shipped (view kernel + prism_map/prism_cycles) · Date: 2026-07-19

## Thesis

Prism today exposes one code graph at two layers: primitives (facts an agent
composes) and task operations (closures Prism computes whole). Both layers
answer **change** questions: what must I touch, is it safe to touch it.

The same substrate supports two more layers — **views** (architecture-level
projections) and **flows** (path queries) — without new extraction work in
Grove. The design constraint that keeps this Prism, rather than a code graph
with a bigger menu, is that every layer obeys the same two invariants:

1. **Provenance.** No derived result without an expansion handle back to the
   primitive edges that induced it. Abstraction is evidence-backed, never
   narrative.
2. **Tier honesty.** Every derived claim carries the capability tier of its
   weakest load-bearing evidence (Grove tiers: precise / measured /
   structural / heuristic / unsupported). Altitude and confidence are
   orthogonal axes: an exact algorithm over measured edges yields a measured
   result, never a precise one.

A corollary worth stating because it unifies Prism's two public stories: a
view *is* compression. The module-level projection of a 30K-symbol repository
is a few hundred tokens, and it is lossless on demand because every induced
edge expands to its constituent sites. **Aggregation is compression with a
receipt.**

## The layer model

```
Substrate   Grove typed graph — 9 edge kinds (defines, imports, calls,
            extends, implements, uses-type, tests, contains, overrides),
            each edge carrying source / resolver reason / confidence.

L1  FACTS      atomic truths           search · lookup · references · edges · resolve
L2  CLOSURES   whole-task fixpoints    change_impact · rename_plan · affected ·
                                       missing_implementations · untested_surface ·
                                       dead_code
L3  VIEWS      quotient graphs         map · cycles · api_surface · arch_check ·
                                       view-level diff intelligence
L4  FLOWS      path queries            call_path · reaches   (existence-only)
```

Layer numbers are internal vocabulary (docs, steering, code comments). Agents
never pick a layer: `prism_query`'s phase-aware routing absorbs the new task
shapes, exactly as it already routes bug-fix tasks to source delivery.

## L3 — Views

### One construction, not five features

A **view** is:

- a **partition** of symbols into components. The authoritative partition is
  structural and deterministic: package / module / directory, derived from
  `contains` edges and file paths. (Semantic naming or clustering may
  *annotate* a view; it never *defines* one — ML clustering as authority is
  how architecture becomes narrative.) Test files are excluded by default —
  the map describes the production shape — and the exclusion is always
  reported in the result (`testFilesExcluded`, `scope`), never silent;
  `include_tests=true` restores them. Decided after the first live run on
  this repository, where a heuristic test-helper edge manufactured a false
  httpapi<->mcp dependency cycle.
- **induced edges**: for components A ≠ B, an edge A→B exists iff at least
  one primitive `calls` / `imports` / `uses-type` / `implements` edge crosses
  the partition from A to B. Induced edges carry:
  - `weight` — count of constituent primitive edges (site count),
  - `kinds` — breakdown by primitive edge kind,
  - `tiers` — distribution of constituent edge tiers (e.g. "87 edges:
    80 precise, 7 heuristic"),
  - `expand` — a handle returning the constituent edges with file:line sites.

Every L3 operation is a computation **on the view**, so provenance and tier
honesty are inherited by construction:

| Op | Definition on the view | Notes |
|---|---|---|
| `prism_map` | Render the view: components, induced edges with weights, entry components (no internal in-edges), leaf components, test-coverage summary per component | The compact architecture picture; token-cheap by construction |
| `prism_cycles` | Tarjan SCC over induced edges, at a chosen granularity (package or directory level) | Exact algorithm; result tier = weakest constituent edge tier |
| `prism_api_surface` | The boundary cut of one component: exported symbols with in-edges from outside the component; plus exported-but-externally-unreferenced symbols | The view-level sibling of `dead_code`; same caveats (reflection, external consumers) |
| `prism_arch_check` | Validate declared layering rules (allowed / forbidden component dependencies) against induced edges; each violation cites its constituent call sites | Rules from a `prism.yaml` stanza or imported from import-linter / dependency-cruiser / ArchUnit configs |
| diff intelligence | Project a diff's L2 `change_impact` up into the view: which induced edges the change creates, removes, or reweights ("this PR adds the first `store → tui` dependency") | Generalizes Mason `/review`; the natural GitHub Action |

### Why this is defensible (oracles)

Each op ships with a scored oracle before it ships with authoritative
language — the same discipline that made change-impact credible:

- **cycles / map edges** — cross-check induced import/dependency edges
  against compiler ground truth per language (`go list` import graph;
  TS project references / ts-morph module graph). Call-edge quality is
  already measured per language by Grove's eval; induced edges inherit it.
- **api_surface** — for Go: `go/types` gives exported identifiers and their
  external referents exactly; that is a free precise oracle. Other languages
  tier down honestly.
- **arch_check** — repositories that declare their architecture
  (import-linter, dependency-cruiser, ArchUnit configurations in the wild)
  are free ground truth: Prism must reproduce the violations their own
  toolchain reports on the same tree.
- **map (whole)** — structural correctness is a function of partition
  (deterministic) plus edge accuracy (measured); spot-check expansion
  handles: every sampled induced edge must expand to real sites.

### What L3 is for

Onboarding ("map this repo I've never seen"), refactor planning (cycles and
api_surface before extraction), review at altitude (diff intelligence in
Mason `/review` and CI), and steering: an agent that has seen the map makes
better anchor choices for `prism_query`.

## L4 — Flows

`call_path(A, B)` — concrete path(s) from A to B over `calls`/`overrides`
edges, k-shortest, each hop a real site. `reaches(source, sink)` — the same
with endpoint *sets* (e.g. HTTP handlers → exec/query sinks).

**The asymmetric claim rule** (the load-bearing safety property):

> A found path is evidence — show every hop. **Absence of a path is only
> assertable when the relevant edge closure is `precise` for every language
> on the frontier**; otherwise the result is "no path found at tier X",
> which is explicitly not "unreachable".

This is what lets flows ship as an honestly-tiered capability instead of
committing the "heuristic authority" failure mode: a security-shaped answer
("user input cannot reach this sink") built on heuristic edges is worse than
no answer. Flows are a research track until the asymmetry is enforced in the
output schema itself (a `claim: exists | not-found-at-tier` field, never a
bare boolean).

## Tier algebra (applies at every layer)

- A derived result's tier is the **minimum** tier over its load-bearing
  evidence. Supporting annotations (weights, names) do not count; edges on
  the cycle / path / boundary do.
- Results report the tier **distribution**, not only the minimum, so a
  consumer can see "closed except for 3 heuristic edges" and decide.
- `closed` retains its L2 meaning: closed under a named graph policy and
  capability set. L3/L4 results never claim `closed`; they claim
  `complete-at-tier`.

## Build plan

**Phase 1 — the view kernel + two exact ops. [SHIPPED]** Partition +
induction + expansion handles as one internal package (`internal/view`);
`prism_map` and `prism_cycles` on top; MCP + CLI surfaces; steering rows in
the generated agent instructions; fixture-level oracle (a real Go module
whose import chain the induced edges must reproduce exactly, including a
poisoned test file the default view must exclude). Corpus-level oracle runs
on the eval repositories remain open — no public accuracy claim for views
until they exist.

**Phase 2 — boundary + rules. [arch_check SHIPPED]** `prism_arch_check` /
`prism arch`: `arch_deny: "<from> -> <to>"` rules in prism.yaml (component
name, prefix, glob, or `*` per side) validated against the induced view;
every violation cites concrete file:line sites; exit 1 = CI gate. Tier-aware
gating shipped after the first live run: an interface-dispatch call
attributed across a boundary (dependency inversion read backwards, exactly
the ranking->mcp semanticAdapter case in this repo) produced a
heuristic-tier violation — those are now `needsReview` (status `review`,
exit 0) rather than build breaks; `--strict` escalates. Engine-ceiling
injection benchmark in-suite: layered Go module, 10 seeded upward
violations — 10/10 detected, 0 false positives, sub-millisecond check.
Still open in Phase 2: `prism_api_surface` (Go precise oracle first),
rule import from import-linter / dependency-cruiser / ArchUnit,
`prism_query` routing for map-shaped tasks; Mason: render views to the
user (payload isolation), teach `graphIntent` the new shapes.

**Phase 3 — diff intelligence. [prism verify SHIPPED]** `prism_verify` /
`prism verify [--base REF]`: detects contract changes in a diff (signature
changes, renames via base-content preview against the live index), computes
each one's required change set (change_impact for methods; resolved-callers
fallback for bare functions, reported as completeness "callers-only"), and
reports every required site the diff did not touch — line-precise where the
AST records the call, span-level otherwise. Also: affected tests,
cross-component dependency CANDIDATES (all evidence inside changed code; no
base-graph comparison, labeled as such), introduced arch violations with
the same tier gating as arch_check. FAIL-CLOSED: a contract change whose
blast radius cannot be computed (interface/struct/class kinds, resolution
failures) yields verdict "review", never a silent pass — this rule was
added after the first live run produced a false "complete" on a
bare-function change. Verdicts: clean | complete | review | incomplete;
CLI exit 1 on incomplete (--strict escalates review). Engine-ceiling
benchmark in-suite: 10 seeded incomplete-agent trials, 32/32 missed sites
caught, 0 false accusations, ~45ms mean; plus a Python fixture proving the
no-compiler case (missed caller passes py_compile, verify reports the
exact line). Mason `/review` integration and L4 flows remain open.

Each phase gates on its oracle, not on feature completeness. No op ships
authoritative language before its oracle runs on at least the existing eval
corpora.

## Non-goals

- **No layer picker.** Agents ask task-shaped questions; routing is Prism's
  job.
- **No ML clustering as authority.** Deterministic partitions only; semantic
  labels are garnish.
- **No absence claims below precise tier.** Anywhere, at any layer.
- **No framework adapters in the core.** Routes / DI / config maps
  (framework-aware entrypoints) are valuable but belong to a separate
  adapter track with their own oracles — folding them into L3 core would
  dilute the quotient-graph contract.
- **No unscored shipping.** An L3 op without an oracle is repackaged
  retrieval — the crowded market Prism wins by avoiding.

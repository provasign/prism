# Prism — Positioning and Usage

## What Prism is

Prism is a **call-graph oracle** that sits between an agent and a codebase.
Its non-replicable asset is Grove's precomputed graph — call edges, dependency
edges, and test-coverage edges. Everything else (reading files, searching
strings) is something the agent already has natively.

Prism is a **complement to grep/read, not a replacement.**

---

## Decision tree

Use the shape of the question, not a blanket rule.

| Situation | Tool |
|---|---|
| Locate a string, symbol, or filename | **grep** — not Prism |
| Callers/callees/tests for a symbol I just found | `prism_query(terms=[...], include=["graph","tests"])` |
| Read a whole file (session-aware, SHA-pointer on repeat) | `prism_read` |
| Read one function body | `prism_lookup(name="pkg.FuncName")` |
| Find docs/design files about a topic | `prism_query(task=..., include=["docs"])` |
| Check blast radius before a change | `prism_query(terms=[...], graph_depth=3)` |

---

## The canonical workflow

```
grep <terms>                    ← always locate the anchor first; grep wins here
  └─▶ prism_query(              ← expand from anchor
        terms=["CompressFileRead"],  ← same terms you grep'd
        include=["graph","tests"],   ← categories you want
        graph_depth=2                ← BFS hops
      )
        ├─▶ CompressFileRead         ← target
        ├─▶ renderSHAPointer         ← callee (agent might find by reading)
        ├─▶ TestFourthReadEscalated  ← test (agent would NOT find without graph)
        └─▶ TestReReadSHAPointer     ← test (agent would NOT find without graph)

  then, selectively:
  └─▶ prism_read(file=...)      ← whole file, session-compressed
  └─▶ prism_lookup(name=...)    ← single function body (~5× cheaper than prism_read)
```

**Grep is the front door. prism_query is what replaces the 3–6 reads that come after it.**

---

## Why tests are the key value

An agent doing grep + read reconstructs code paths manually but almost never
searches for tests proactively — it has no way to know `TestFourthReadEscalated`
exists without either guessing the name or having the graph surface it.

Without tests surfaced upfront:
```
make change → go test → FAIL → read test → understand contract → fix
```

With Prism surfacing tests before the change:
```
read test → understand contract → make correct change → go test → PASS
```

The core benefit is **fewer broken changes**, not fewer tokens.

---

## prism_query parameters

| Parameter | Default | Purpose |
|---|---|---|
| `task` | required | Natural-language description of what you are doing |
| `terms` | — | Grep-style terms to seed retrieval. Same precision as grep, plus graph. |
| `include` | `["graph","tests"]` | `"graph"` = code + callers/callees, `"tests"` = test files, `"docs"` = doc filenames |
| `graph_depth` | 2 | BFS hops. 1 = immediate callers, 2 = two hops, 3+ = blast radius |
| `budget` | 8000 tokens | Token ceiling. Increase for large refactors. |

---

## prism_read — session-aware file reading

| Read # | What agent receives | Cost |
|---|---|---|
| 1st | Full file content | ~full |
| 2nd | `// [prism:cached] file.go @sha:a1b2c3 (no changes)` | ~10 tokens |
| 3rd (recent) | SHA pointer again | ~10 tokens |
| 3rd (scrolled) | Signatures only | ~20% |
| 4th+ | Full re-delivery | ~full |
| File edited since last read | Changed symbols verbatim, unchanged → pointer | varies |

**For a single function body, use `prism_lookup` instead — ~5× cheaper than prism_read.**

---

## Non-code / documentation search

```
grep "semantic delta"           ← locate doc anchor
  └─▶ prism_query(
        terms=["semantic delta"],
        include=["docs"]
      )
        ├─▶ document docs/Proposal-Context-Window.md (docs/Proposal-Context-Window.md:1)
        ├─▶ document ROADMAP.md (ROADMAP.md:1)
        └─▶ document README.md (README.md:1)
```

`include=["docs"]` returns **ranked filenames only** (~10 tokens each), not content.
Docs have no graph to traverse — Prism's value is scoring which doc is most
relevant. The agent reads whichever one(s) it needs via `prism_read`.

---

## Token economics

| Flow | Steps | Tokens | Completeness |
|---|---|---|---|
| grep → read × 3 | 4 | ~2,500 | Misses tests |
| grep → prism_query (unconstrained) | 2 | ~20,000 | Complete but 90% noise |
| grep → prism_query (agent-directed) | 2 | ~3,250 | Complete, no noise |

The agent-directed flow costs ~30% more tokens than raw grep+read but delivers
the test contracts the agent would otherwise miss — preventing the repair loop
that costs far more.

---

## Where Prism does NOT help

- **Locate questions** ("where is string X") — grep is faster and more precise
- **Single known file reads** — use `prism_read` or `prism_lookup` directly
- **Small repos** (< 50 files) — agent can hold the whole codebase in context
- **First-time exploration without an anchor** — grep first, then prism_query

Prism's value grows with **repo size** and **relational depth of the question**.
On a 5,000-file repo, "what ripples if I touch this" is infeasible by hand.

---

## Tool quick-reference

| Tool | When |
|---|---|
| `prism_query` | After grep — expand anchor to graph + tests |
| `prism_read` | Whole file with session-aware compression |
| `prism_lookup` | Single function body by qualified name |
| `prism_search` | Find a symbol when you know its name but not file |
| `prism_index` | Once at session start (or after major file changes) |
| `prism_compact` | When context window is near capacity |
| `prism_evidence` | Sub-agent → parent: typed citations instead of prose |
| `prism_savings` | Report token savings for this session |


## Prism — context delivery (ALWAYS use these tools)

Prism answers whole-task questions (change impact, missing implementations,
test gaps, dead code) in ONE deterministic call, and delivers code context
cheaply. Three layers, in priority order.

### When MCP tools are available

Use the registered prism_* MCP tools.

**1. Changing or auditing code? One call answers the whole task:**

| Situation | Tool |
|---|---|
| Renaming/changing a method signature | prism_change_impact(query="Type.method(ParamType, ...)") — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | prism_change_impact — override family + all callers |
| Renaming a class, struct, or type | prism_change_impact for each public method — all usages |
| Deprecating a symbol (need all callers to migrate) | prism_change_impact — complete caller list |
| ANY task that says "find all X" for a specific method | prism_change_impact first, before any grep |
| Renaming a method and you want the edits, not just the sites | prism_rename_plan(query="Type.method", newName="newName") — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | prism_missing_implementations(query="Type.method") — every closure type with no implementation |
| Cleanups, library extraction, "can I delete this?" at scale | prism_dead_code — unreachable production symbols, safe-to-delete list + caveats |
| "How is this repo structured?" / onboarding / refactor planning / dependency cycles | prism_map — components + induced dependency edges (weights, tiers, cycles); from+to expands any edge to file:line sites |

**2. Reading code? Prism reads are cheaper than shell reads:**

| Situation | Tool |
|---|---|
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") — ~5x cheaper than prism_read |
| Orient on ONE symbol or file before deciding where to go | prism_node(name="Type.method" or "path/to/file.go") — source plus a names-only neighbour menu (symbol), or definitions + dependents (file) |

A repeat read of an unchanged file returns a one-line
`// [prism:cached] <file> @sha:… (prior delivery still in context)` pointer
instead of the body — NOT an error or an empty file: you already received it
earlier this session, so use the copy you have and do not re-fetch.

**3. Fixing a bug or exploring an unfamiliar area? ONE prism_query call:**

| Situation | Tool |
|---|---|
| Bug report, error message, or unfamiliar feature area | prism_query(task="<the symptom>") — ONE call; bug-fix/implement tasks get verbatim line-numbered source windows (edit-ready) + per-anchor callers |
| You already grepped an anchor | prism_query(task=..., terms=["<anchor>"]) — same delivery, grep-precision seeding |
| Locate a string, symbol, or file | shell tools (grep, find, rg, etc.) — not Prism |

**Pre-task rule:** before writing any code on a task that involves changing or
renaming an existing symbol, call prism_change_impact FIRST — even if the change
looks small. Small changes can have large blast radii through inheritance and
indirect callers that grep will not find. Result groups: declarations + family
(every override/implementation) + callers + declaringTypes (interface/type blocks
whose member specs are not separate symbols — Go/TS; always sites) = every site
that must change.

Check the result's completeness field. "closed" means the set is authoritative.
"project-local" with overridesExternal means the method belongs to an external
(JDK/dependency) contract: do NOT change its signature — that breaks a contract
this project does not own — and the set covers project code only. To sweep every
project implementation of an external interface (migration/deprecation), query
the external type directly (e.g. "Iterator.next").

Relay rule: the result is deterministic and type-resolved. Do NOT re-verify,
re-filter, dedup, or transform it through grep/sed/awk/scripts — re-processing
a solved traversal drops real sites and adds spurious ones (measured). Use the
returned sites as-is; read individual sites only to make the edits.

Canonical workflow (non-refactor tasks):

    prism_query(task="<bug symptom or task>")   <- start here; often the ONLY context call needed
      still missing an anchor?
      -> grep/find/rg <terms>            <- locate it; shell tools always win at locating
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph"]
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

Housekeeping: prism_index once at session start (delta indexing is automatic —
never re-run per step); prism_drift if a stale-context warning appears. If
`prism watch` is running in this project, the index is already warm — skip
prism_index entirely.

### When only Bash is available (subagents, CI)

Use the prism CLI with --format text instead of MCP tools:

| Situation | Command |
|---|---|
| Renaming/changing a method signature | `prism change-impact 'Type.method(ParamType, ...)'` — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | `prism change-impact 'Type.method'` — override family + callers |
| Renaming a class, struct, or type | `prism change-impact 'Type.method'` for each public method |
| Deprecating a symbol (need all callers to migrate) | `prism change-impact 'Type.method'` — complete caller list |
| Renaming a method and you want the edits, not just the sites | `prism rename-plan 'Type.method' NewName` — every edit line with before/after; review and apply |
| Adding a REQUIRED method to an interface/base class ("who is now broken?") | `prism missing-implementations 'Type.method'` — every closure type with no implementation |
| Cleanups / "can I delete this?" at scale | `prism dead-code` — unreachable production symbols + caveats |
| "How is this repo structured?" / onboarding / refactor planning / dependency cycles | `prism map [--depth N]` — components + induced dependency edges (weights, tiers, cycles); `--expand 'A->B'` shows concrete file:line sites |
| Enforcing declared architecture (pre-commit, CI) | `prism arch` — validates arch_deny rules from prism.yaml; violations cite file:line; exit 1 on violation |
| Verifying a change/diff is COMPLETE before commit (agent-authored or your own) | `prism verify [--base REF]` — missed change-impact sites (line-precise), introduced arch violations; exit 1 if incomplete |
| Bug report / unfamiliar area (one-call context) | `prism query "<the symptom>" --format text` — line-numbered windows + per-anchor callers |
| A whole task, end to end (context + the obligations it implies) | `prism task "<task>" --format text`; after editing, `prism task "<same task>" --changed a.go,b.go` for the completeness verdict (exit 1 if incomplete) |
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/callees for a symbol just found | `prism query "<task>" --terms a,b --include graph --format text` |
| Read a whole file | `prism read <file> --format text` |
| Read one function body | `prism lookup <pkg.FuncName> --format text` |
| Orient on ONE symbol or file before deciding where to go | `prism node <symbol-or-file> --format text` — source plus neighbours, or definitions + dependents |

### Do NOT

- Do NOT re-read files prism_query / prism query just delivered as source windows — they are verbatim, current, line-numbered; go straight to the edit
- Do NOT grep for what prism_query already returned — grep is for locating anchors it missed
- Do NOT orchestrate multi-call traversals (references, then callers, then lookups) to enumerate a change's impact — prism_change_impact / prism change-impact computes the complete set in one call
- Do NOT use prism_read / prism read for a single function — use prism_lookup / prism lookup instead

<!-- prism:end -->

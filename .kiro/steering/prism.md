
## Prism — context delivery (ALWAYS use these tools)

Prism answers whole-task questions (change impact, missing implementations,
test gaps, dead code) in ONE deterministic call, and delivers code context
cheaply. Three layers, in priority order.

### When MCP tools are available

Use the registered prism MCP tools (their full names are
mcp__prism__prism_query, mcp__prism__prism_search, and so on). The core
tools are marked always-load, so they are ALREADY in your tool list —
call them directly; no ToolSearch step is needed first.

Only if a prism tool is genuinely not in your tool list (a harness that
defers ALL MCP schemas), load it by its FULL registered name before
concluding Prism is unavailable:

    ToolSearch("select:mcp__prism__prism_query,mcp__prism__prism_search,mcp__prism__prism_change_impact")

or search by keyword: ToolSearch("prism"). Do NOT use bare names like
"select:prism_query" — they do not match the registered names, return
nothing, and an empty result here does NOT mean Prism is absent (measured:
that exact misread is why cheap-tier agents fell back to grep on machines
where prism was connected and loaded).

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
| Read a whole file | prism_read — SHA-pointer (~30 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") — ~5x cheaper than prism_read |
| Orient on ONE symbol or file before deciding where to go | prism_node(name="Type.method" or "path/to/file.go") — source plus a names-only neighbour menu (symbol), or definitions + dependents (file) |

A repeat read of an unchanged file returns a one-line
`// [prism:cached] <file> @sha:… (prior delivery still in context)` pointer
instead of the body — NOT an error or an empty file: you already received it
earlier this session, so use the copy you have and do not re-fetch.

**3. Fixing a bug or exploring an unfamiliar area? ONE prism_query call:**

prism_query REQUIRES terms — guess ONE keyword from the task first (a
class/function name fragment, a domain term); there is no task-alone
fallback, a call with no terms errors with this guidance.

| Situation | Tool |
|---|---|
| Bug report, error message, or unfamiliar feature area | prism_query(task="<the symptom>", terms=["<your best guess>"]) — ONE call; bug-fix/implement tasks get verbatim line-numbered source windows (edit-ready) + per-anchor callers |
| You already grepped an anchor | prism_query(task=..., terms=["<anchor>"]) — same delivery, grep-precision seeding |
| No plausible guess at all | grep/prism_search a domain term first, THEN prism_query with that term |
| Locate a string, symbol, or file | prism_search — searches symbol names AND raw source text (a real rg/grep pass). scope="text" for a PURE grep (cheapest, use it exactly as you would grep; regex=true for patterns) |

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

Route discipline — optimize total latency and context, not Prism usage.
Decide what you need, make ONE call, and treat its result as final:

    locate a string/symbol/file         -> prism_search (scope="text" when you would have run grep)
    read one known function             -> prism_lookup
    read one known whole file           -> prism_read
    orient on ONE symbol or file        -> prism_node
    bug/feature with a plausible anchor -> prism_query, ONE call:
      guess ONE keyword from the task (a class/function fragment, a domain term)
        -> prism_query(task="<bug symptom or task>", terms=["<guess>"])   <- often the ONLY context call needed
        wrong guess / still missing an anchor?
        -> prism_search(query=..., scope="text")   <- locate it: real rg/grep inside prism
        -> prism_query(task="...", terms=["same-grep-terms"], include=["graph"])   <- retry with a real anchor
    signature change / rename / deletion / interface evolution
                                        -> the whole-task op (change_impact, rename_plan,
                                           missing_implementations). Its set is terminal —
                                           never rebuild or re-verify it with searches.
    broad review / onboarding / "what do you think of this repo"
                                        -> README + prism_map (depth 1), then at most ~3
                                           prism_lookup/prism_node probes on representative
                                           symbols. Do NOT run broad prism_query sweeps,
                                           prism_dead_code, or prism_arch_check unless the
                                           user asked about those dimensions.

Redundancy rules:
- Do not stack prism_query + prism_node + prism_lookup on the same symbol
  unless the previous result was genuinely insufficient.
- Do not search broad project-name terms ("Prism") — they match everything
  and anchor nothing.
- Exploratory work wants one SMALL result, not one comprehensive one:
  prism_query with budget=3000-4000 and max_files=3 answers most questions.
- Stop gathering context the moment the question is answerable with concrete
  evidence.

Housekeeping: indexing is AUTOMATIC — the MCP server indexes at startup, a
never-indexed repo indexes itself on first query, and every whole-repo graph
op (change_impact, map, dead_code, rename_plan, missing_implementations,
verify, arch) delta-refreshes before it runs. Call prism_index only after a
stale-context warning or an explicit empty-index failure, never routinely. A
stale-context warning names the changed files — re-read them (prism_read
returns the changed content) before relying on them.

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
| Bug report / unfamiliar area (one-call context) | `prism query "<the symptom>" --terms <your best guess> --format text` — ONE call; --terms is REQUIRED, guess a keyword from the task |
| Locate a string, symbol, or file | prism search <term> — symbol names AND raw source text (real rg/grep inside). Pure grep: prism search <term> --scope text [--regex] |
| Callers/callees for a symbol just found | `prism query "<task>" --terms a,b --include graph --format text` |
| Read a whole file | `prism read <file> --format text` |
| Read one function body | `prism lookup <pkg.FuncName> --format text` |
| Orient on ONE symbol or file before deciding where to go | `prism node <symbol-or-file> --format text` — source plus neighbours, or definitions + dependents |

### Do NOT

- Do NOT re-read files prism_query / prism query just delivered as source windows — they are verbatim, current, line-numbered; go straight to the edit
- Do NOT grep for what prism_query already returned — grep is for locating anchors it missed
- Do NOT orchestrate multi-call traversals (references, then callers, then lookups) to enumerate a change's impact — prism_change_impact / prism change-impact computes the complete set in one call
- Do NOT use prism_read / prism read for a single function — use prism_lookup / prism lookup instead
- Do NOT call prism_index "just in case" at session start — indexing is
  automatic (see Housekeeping); a redundant index call adds seconds for nothing
- Do NOT reach for a separate grep/rg tool: prism_search and prism_query run a
  real ripgrep pass internally, so text matches outside any symbol (comments,
  configs, docs, string literals) come back as textMatches/textHits. Pay only
  for what you need: scope="text" is a pure grep, scope="both" (default) merges
  symbol and text results
- A repeat call to a whole-repo graph op (change_impact, map, dead_code,
  rename_plan, missing_implementations) whose freshly recomputed result is
  IDENTICAL to one already delivered this session returns a one-line
  [prism:cached] pointer plus group counts — NOT an error, NOT an empty
  result: use the delivery you already have

<!-- prism:end -->

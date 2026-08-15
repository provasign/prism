
## Before you commit or release — run the regression suite

**Behavior change? Sweep the assumption surfaces first.** Any change to
tool loading, routing, flags, names, or defaults invalidates text that
DESCRIBES the old behavior elsewhere: the steering const in
internal/cli/commands.go (and its nine generated files), README,
AGENT_SETUP_PROMPT.md, the website, research harness arm definitions, and
metric regexes. Grep them for the old behavior and fix or consciously
exempt every hit — this is as mandatory as the tests below (v0.44.0
shipped alwaysLoad while the steering still taught the deferred-tools
ToolSearch dance; that mismatch became a released bug).

CI runs these on push (`.github/workflows/ci.yml`; engine quality via
`provasign/research/.github/workflows/engine-invariants.yml`). Run them locally
before tagging a release:

- `go test ./...` — unit suite; must be green.
- Engine ceiling regression (no LLM): from the research repo,
  `python3 harness/ci_invariants.py --prism ~/bin/prism` — asserts change-impact
  recall/precision, missing-implementations==[], and index determinism against
  committed ground truth. A drop here is a real completeness regression.

Do NOT tag a release with either red.
## Prism — code intelligence (ALWAYS use these tools)

prism_search/query/read/lookup/change_impact are already in your tool list --
call them directly, no lookup step. Locate a string/symbol -> prism_search(scope="text")
(a real ripgrep pass, same cost as grep). Bug/task with an anchor -> prism_query(task=...,
terms=[...]) -- terms is required, guess one keyword. Signature change, rename, or
"who breaks if I change X" -> prism_change_impact -- returns the complete site set in
one call; do not re-verify it with grep, that measurably drops real sites. A repeat
prism_read of an unchanged file returns a short cached-pointer line, not the body --
that is not an error. Three concrete substitutions, not just "prefer prism":
about to grep several candidate names ("a\|b\|c", unsure which is right)? that is
prism_query(terms=[a,b,c]) in one call, not several rounds of narrowing. About to
grep a name followed by "(" to find who constructs/calls it? that is
prism_change_impact or prism_query's caller list -- resolved call sites, not a
paren-guess that also matches definitions and comments. About to grep a def line
then guess how many lines of context to print (-A10, sed -n 'N,Mp')? that is
prism_lookup -- the whole symbol, no line-count guessing, never cut off early.
No MCP session (Bash-only subagent)? Same names as CLI verbs:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->

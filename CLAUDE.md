
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

Reaching for grep/rg/cat/sed? The prism call is the same request, better answered.
The translation is mechanical — all of these are already in your tool list:

    grep -rn "foo"                      ->  prism_search(query="foo", scope="text")
    grep -rn "foo" src/  (or one file)  ->  prism_search(query="foo", scope="text", path="src/")
    grep -rn "a\|b\|c"  (regex)         ->  prism_search(query="a|b|c", scope="text", regex=true)
    grep -C 5 / -A 10 (context lines)   ->  prism_search(..., context=5)
    grep -rl "foo"  (files only)        ->  prism_search(query="foo", scope="text", files=true)
    guessing names: grep a, grep b, ... ->  prism_query(task="...", terms=["a","b","c"]) -- one call,
                                            all candidates, graph expands from whichever lands
    grep "Foo("  (who calls Foo?)       ->  prism_change_impact(query="Foo") -- resolved call sites,
                                            complete set; do not re-verify with grep, that drops sites
    grep -A10 "def foo" / sed -n N,Mp   ->  prism_lookup(name="foo") -- whole body, no line guessing
    cat file / re-reading a file        ->  prism_read(file=...) -- a repeat read returns a one-line
                                            cached pointer, not the body; that is not an error

Piping ANOTHER command's output through grep (git log | grep x, pip list |
grep -i y) is fine and stays allowed — prism replaces searching FILES, not
filtering streams. No MCP session (Bash-only subagent)? Same verbs as CLI:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->

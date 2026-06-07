# Prism Positioning and Usage

## What Prism Is

Prism is a call-graph oracle for AI coding agents. It uses an embedded Grove
index to return the symbols, callers, callees, tests, docs, and coverage gaps
that matter for a task.

Prism is a complement to shell search, not a replacement.

- Use `rg`, `grep`, or `find` to locate exact strings, filenames, and first
  anchor terms.
- Use Prism to expand those anchors into the graph and test surface.

The agent interface is **CLI text mode** because it is simple, works in
subagents, and avoids JSON wrapper overhead:

```bash
prism query "trace refund flow" --terms RefundPayment --include graph,tests --format text
```

---

## Decision Tree

| Situation | Use |
|---|---|
| Locate an exact string, symbol, or filename | `rg` / `grep` / `find` |
| Expand a known symbol to callers, callees, and tests | `prism query "<task>" --terms X --include graph,tests --format text` |
| Check blast radius before a change | `prism query "<task>" --terms X --depth 3 --format text` |
| Find direct coverage gaps near a change | `prism query "<task>" --terms X,Y --include graph,coverage_gaps --format text` |
| Read a whole file after Prism points to it | `prism read <file> --format text` |
| Read one function body | `prism lookup <qualified.Name> --format text` |
| Find docs/design files | `prism query "<task>" --include docs --format text` |

---

## Canonical Workflow

```bash
rg "buildCoverageGaps" internal/

prism query "write focused tests for buildCoverageGaps" \
  --terms buildCoverageGaps \
  --include graph,tests,coverage_gaps \
  --format text

prism lookup github.com/provasign/prism/internal/tools.buildCoverageGaps --format text
```

The important rule is ordering: shell tools find the anchor; Prism expands it.

---

## Why Tests Are the Key Value

Agents using shell-only workflows usually read implementation first and discover
tests after failure:

```text
grep -> read implementation -> edit -> test fails -> read test -> fix
```

Prism can surface the test contract before the edit:

```text
grep -> prism query graph,tests -> read contract -> edit -> test passes
```

The core benefit is fewer broken changes. Token savings matter, but avoiding a
repair loop matters more.

---

## Coverage Gaps

`--include coverage_gaps` reports exported functions and methods in the query
blast radius that lack direct, package-local test coverage.

Use it before changing code:

```bash
prism query "fix payment lifecycle edge cases" \
  --terms CompletePayment,UpdatePayment,RequireScope \
  --include graph,coverage_gaps \
  --format text
```

Treat `coverage_gaps` as structured output. Do not manually cross-reference it
with `prism search` unless you are debugging Prism itself.

---

## Token Economics

Compare Prism to the file reads an agent would otherwise perform, not to the
first `rg` output.

| Flow | Typical behavior |
|---|---|
| Shell-only | Cheap anchor search, then several file reads and manual test discovery |
| Prism CLI text | One graph-ranked text response, usually less total context for relational tasks |

Recent real Prism-repo CLI scenarios showed 10-43% less delivered context than
shell-only baselines, with one Prism command replacing 5-6 shell commands.

---

## Where Prism Does Not Help

- Exact string search.
- Reading a single file you already know is needed.
- Tiny repositories where the whole codebase fits comfortably in context.
- Broad terms such as `Version` without anchors; use precise `--terms`.

Prism value grows with repo size and with relational questions: callers,
callees, tests, blast radius, and coverage gaps.

---

## Quick Reference

```bash
prism init .
prism index .

prism query "<task>" --terms a,b --include graph,tests --depth 2 --format text
prism query "<task>" --terms a,b --include graph,coverage_gaps --format text
prism read <file> --format text
prism lookup <qualified.Name> --format text
prism search <keyword> --format text
prism savings
```

Use `--format lean` or `--format json` only when a script needs structured data.

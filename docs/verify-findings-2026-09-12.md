# prism_verify findings — 2026-09-12

Session investigation, black-box (external throwaway repos + one real
benchmark batch), not a code read alone. Repro commands included so these
don't need to be taken on faith — rerun them before fixing, per the
project's own standard (measure before changing ranking/verify behavior).

---

## 1. BUG — `removed_symbols` false-positives on prefix-sharing identifiers

**Severity:** real, reproducible, precision bug in the mid-loop fast path
(`Handler.verifyRemovedSymbols`, `internal/mcp/verify.go`).

**Repro:**
```sh
mkdir /tmp/rmtest && cd /tmp/rmtest && git init -q
cat > go.mod <<'EOF'
module removedtest

go 1.21
EOF
cat > lib.go <<'EOF'
package main

func ComputeTotal(a, b int) int {
	return a + b
}

func ComputeTotalV2(a, b int) int {
	return a + b + 1
}
EOF
git add -A && git commit -q -m init
prism index .

# remove ComputeTotal, leave ComputeTotalV2 (a DIFFERENT, still-live symbol
# that merely shares ComputeTotal as a prefix) untouched
python3 -c "
s=open('lib.go').read()
s=s.replace('func ComputeTotal(a, b int) int {\n\treturn a + b\n}\n\n','')
open('lib.go','w').write(s)"

prism verify . --removed ComputeTotal --format text
```

**Observed:**
```
verify --removed: residual references remain (0/1 clean)
  ComputeTotal: 5 remaining
    ...
    lib.go:3: func ComputeTotalV2(a, b int) int {
```

`ComputeTotalV2` is flagged as a residual reference to the removed
`ComputeTotal`. It is a different, still-existing function; the removed
name is just its prefix. `grep -rn '\bComputeTotal\b'` on the same tree
correctly excludes it (4 hits, all real) — the text match inside
`verifyRemovedSymbols` has no identifier/word-boundary check, so it's a
raw substring search.

**Effect:** in any removal where the removed name is a prefix of a
surviving symbol (`Foo` removed, `FooV2`/`FooImpl`/`FooOld` still live —
a common naming pattern), the fast path reports a false "still
referenced," adding noise to the one place this tool is supposed to be a
cheap, trustworthy loop check. Recall was correct in this test (all 4 real
references found); this is a precision defect, not a missed-reference one.

**Fix direction:** in `verifyRemovedSymbols` (`internal/mcp/verify.go`,
~line 1188), the call to `textsearch.Search(ctx, h.Root, s, ...)` needs a
word-boundary constraint — either pass a regex mode with `\b<name>\b`, or
post-filter `r.Hits` to drop matches where the character immediately
before/after the identifier is itself an identifier character. Add a test
alongside the existing `TestToolVerify_RemovedSymbolsFastPath` covering a
removed symbol whose name is a prefix of a surviving one.

---

## 2. STEERING — drop the unconditional full-`verify()` mandate

**Not a code bug — a CLAUDE.md change**, backed by two independent lines
of evidence gathered this session:

- **Constructed tests** (Go/TS/Python throwaway repos): the bug class full
  `verify()` targets — stale caller after rename/removal/signature change
  — is caught by `go build`/`tsc --noEmit` in one line, before any LLM
  involvement. Verify reaches the same verdict at ~7-9x the output. Verify
  is also blind (same as the compiler) to same-signature semantic changes
  and to string/reflection-keyed references
  (`reflect.ValueOf(x).FieldByName("OldName")` survives a field rename
  with `go build` AND `prism verify` both reporting clean; only
  `prism search --scope text --exhaustive` or plain grep catch it).
- **Real benchmark data** (157 cells, 2026-09-08 batch, all `runs/*`
  under `research/harness/runs/`): full `verify()` fired in only 11/157
  cells (~7%), 5 returned clean, 1 errored on `git diff` before checking
  anything, 3 raised an unverified-signature caution with no concrete
  site, and only 2 surfaced a real finding — the *same* missed test-call
  site on the *same* task (`pallets__click__pr3244`), replayed across two
  experiment arms, not two independent catches. When it did fire as a
  terminal/closing call, the assistant turn's `cache_read_input_tokens`
  averaged ~46k tokens against a ~309-char (~77 token) response — a
  ~600:1 ratio, because forcing it as a last step manufactures an
  otherwise-unneeded full-context-replay turn.

**Change**, two files:

`/Users/tapabratapal/Projects/provasign/CLAUDE.md`, replace:
> Run `prism(op="verify", args={})` before finishing multi-site, signature, removal, or unresolved-coverage changes—not every small local edit.

with:
> Run `prism(op="verify", args={})` once before finishing only when the diff touches dynamically-typed code, or code not already covered by a build/typecheck+test step in this task — that step catches the same rename/removal/signature-break class more cheaply for statically-typed code. Don't call it as a routine closing step when a build+test gate already ran clean.

`/Users/tapabratapal/Projects/provasign/prism/CLAUDE.md`, replace:
> - verify({removed_symbols:[...]}) before a removal; verify({}) before finishing a multi-site or signature change.

with:
> - verify({removed_symbols:[...]}) before a removal.
> - verify({}) before finishing only for dynamically-typed diffs or diffs not covered by `go test ./...`/a typecheck step already run this task.

**Explicitly not touched:** the `removed_symbols` fast path itself (item 1
fixes it, doesn't remove it), the tool's registration/exposure to agents,
and full `verify()` for dynamically-typed diffs — no evidence gathered
this session argues against any of those.

---

## 3. Candidate (unconfirmed) — extend rename detection to catch string-keyed references

`staleOldNameRefs` (`internal/mcp/verify.go`) already knows a rename's old
and new name and searches for stale references to the old name at the
symbol/reference-graph level. It does not catch the reflection/string-key
case above (old name still present as a bare string literal, e.g. in
`reflect.FieldByName`, a dispatch table, a JSON tag). Extending it to also
run an exhaustive text search for the literal old identifier and surface
hits as an advisory (same shape as `unverifiedSeeds`) would close this gap
for both full `verify()` and `removed_symbols` in ANY language, not just
dynamic ones. Lower priority than item 1; flagged here as a candidate, not
measured/scoped yet — needs its own repro/false-positive-rate check before
committing to it (a rename's old name is often a common word/substring,
so this could reintroduce item 1's problem at the text-search layer if not
also word-boundary-safe).

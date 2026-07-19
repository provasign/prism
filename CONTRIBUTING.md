# Contributing to Prism

Prism welcomes bug reports, benchmark cases, documentation improvements, and
focused code changes. Correctness and explicit uncertainty take priority over
adding broad but unmeasured behavior.

## Before opening a change

1. Search existing issues and describe the user-visible failure or capability.
2. For graph correctness, include a minimal fixture with expected and forbidden
   edges. A recall fix without a negative precision case is incomplete.
3. Keep generated files, local caches, credentials, and proprietary corpora out
   of commits.

## Development

Prism requires the Go version declared in `go.mod`.

```bash
go test ./...
go test ./... -race -count=1
go vet ./...
```

Changes to public operations must document completeness semantics and add tests
for success, degraded, and error paths. Changes to existing symbols should use
`prism change-impact` before editing and `prism affected` after editing.

## Pull requests

Keep each pull request narrowly scoped. Include the problem, intended behavior,
tests run, compatibility impact, benchmark evidence for quality claims, and any
security or data-retention impact.

By contributing, you agree that your contribution is licensed under the Apache
License 2.0.

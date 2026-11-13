# Working across repositories

Prism answers for one repository. Linking other repositories to it lets the same commands cover
them: `lookup` and `read` reach their source, `search` counts and lists their matches, and
`change-impact`, `verify` and `test-plan` report who uses a symbol outside the repository.

Each repository keeps its own index. Nothing is merged; links are followed when a question is asked.

## Three kinds of linked repository

| Kind | What it is | How to link |
|---|---|---|
| Local checkout | A directory on this machine | `prism workspace add ../grove` |
| Cache clone | A repository that is not checked out, fetched read-only at one commit | `prism workspace add https://host/org/repo.git@v1.2.0` |
| Link server | Repositories known only from link data their CI uploaded | `workspace_server: https://...` in `prism.yaml` |

## Linking

```
prism workspace discover          # related checkouts found in manifests, with the evidence
prism workspace discover --link   # link the ones not yet linked
prism workspace add ../grove      # link one checkout (several paths allowed)
prism workspace                   # what is linked: path, branch@commit, index, why
prism workspace remove grove      # unlink; the checkout is not touched
```

Local links are `workspace_repo: <id>=<relative path>` lines in `prism.yaml`, safe to commit.
A `prism.yaml` in a plain folder that holds several checkouts can declare them once:
run `prism workspace add app lib other` in that folder, and each checkout sees the others.

`prism init` lists related checkouts and links them only with `--workspace auto` or an interactive yes.

Discovery reads `go.mod`, `go.work`, `package.json`, `Cargo.toml`, `pyproject.toml`, `pom.xml` and
`composer.json`. It reports a checkout when this repository's build already points at it, when a
sibling directory declares a package this one requires or requires a package this one declares, and
when a repository Prism was set up in elsewhere on the machine does.

Every linked repository needs its own index: run `prism index` in it once. A linked repository with
no index is named in answers as not searched; Prism does not build a first index on its own.

## What the commands do

- `prism lookup Name` — a name this repository does not define is shown from the linked repository
  that does, under a path like `../grove/pkg/x.go`.
- `prism read ../grove/pkg/x.go` — reads a file in a linked repository. Anything else outside the
  root is still refused.
- `prism search term` — ends with one line counting matches in each linked repository.
  `--repo <id>` lists them; `--repo all` appends every linked repository.
- `prism change-impact Name` — after this repository's sites, the sites in other repositories, per
  repository, with the version each requires. Then a `workspaceScope:` line saying what was searched.
- `prism verify` — lists sites in other repositories that reference a contract the diff changed.
  Its verdict covers this repository only, and says so.
- `prism test-plan` — lists tests in other repositories that reference a changed symbol.

## What is recorded, per language

| Language | Recorded | Evidence |
|---|---|---|
| Go | calls, functions used as values, types named in a function or a type declaration | compiler-resolved |
| TypeScript, JavaScript | calls | compiler-resolved; matched by package and name, not owner type |
| Python | calls through imported names | import-scoped; matched by module and name |
| Java, Rust, PHP, C#, C/C++ | nothing yet | — |

A site carries a tag when it is not a full compiler match. A reference in a package-level variable
or constant declaration is not recorded.

## Branches and staleness

An index describes the tree that was on disk when it was written, and records that commit and
branch. Before answering from a linked checkout, Prism compares that with what is checked out now,
including uncommitted files. If they differ it runs the ordinary incremental index there, within
20 seconds (`PRISM_WORKSPACE_REFRESH=<duration>` or `off`). If that cannot finish, the answer names
the repository as `STALE` with both commits.

The workspace is whatever is checked out. Prism never switches a branch. Two branches of one
repository at once means two directories (`git worktree`), each linked and indexed on its own.

A cache clone is one commit and never changes. `prism workspace fetch` re-resolves branches and
moves links to the new commit; `prism workspace prune` deletes clones nothing links to.

In a Go workspace (`go.work` above the repository, listing it), a consumer is analysed against the
linked checkout, as `go build` does, not against the version its `go.mod` pins.

## Link server

For consumers that are not on the machine at all.

```
# on a host everyone can reach
PRISM_LINK_TOKEN=... prism link-server --dir /var/lib/prism-links --port 8890

# in each repository's CI, after checkout
prism index . && PRISM_LINK_TOKEN=... prism workspace push --server https://links.example.internal

# in a repository that should ask it
workspace_server: https://links.example.internal      # in prism.yaml; token from PRISM_LINK_TOKEN
```

A push uploads where the repository references other repositories' symbols, one source line per
reference, and its declarations' signatures, at its commit. No source bodies. A tree with
uncommitted changes is refused. The server keeps one bundle per repository and commit and answers
from each repository's `main` (or `master`, or latest upload).

Answers from the server are marked as such, with the repository's commit and upload date. A
repository linked locally is not listed again from the server. A server that cannot be reached, or
rejects the token, is reported as not covered.

`prism workspace export -o bundle.json` writes the bundle to a file instead.

## Limits to keep in mind

- Answers say what was not searched. Repositories that are neither linked nor uploaded are not covered.
- Calls between services (HTTP, gRPC, queues), reflection and dependency injection are not symbol
  references and are not found.
- One bearer token gates the whole link server; there are no per-repository permissions.
- Cache clones have no size cap; prune them by hand.

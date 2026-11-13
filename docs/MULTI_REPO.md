# Working across repositories

Prism answers for one repository. Linking other repositories to it lets the same commands cover
them: `lookup` and `read` reach their source, `search` counts and lists their matches, and
`change-impact`, `verify` and `test-plan` report who uses a symbol outside the repository.

Each repository keeps its own index. Nothing is merged; links are followed when a question is asked.

## Quick start

```
cd library
prism workspace add ../app          # or any path: /somewhere/else/app
prism change-impact Client.Send     # now lists the call sites in app
```

One command, from either side. It writes the link into this repository's `prism.yaml`, records
the other end on this machine so `app` also sees `library`, and builds `app`'s index if it has none.

To find related checkouts instead of naming them: `prism workspace discover`, then
`prism workspace discover --link`.

## Three kinds of linked repository

| Kind | What it is | How to link |
|---|---|---|
| Local checkout | A directory on this machine | `prism workspace add <path>` |
| Cache clone | A repository that is not checked out, fetched read-only at one commit | `prism workspace add https://host/org/repo.git@v1.2.0` |
| Link server | Repositories known only from link data their CI uploaded | `workspace_server: https://...` in `prism.yaml` |

## Commands

```
prism workspace                     what is linked: path, branch@commit, index, why
prism workspace add <path>...       link checkouts (--id <name>, --no-index)
prism workspace add <git-url>[@ref] fetch into the cache, index, link on this machine
prism workspace remove <id>         unlink both ends; the checkout is not touched
prism workspace discover [--link]   related checkouts found in manifests, with the evidence
prism workspace fetch               re-resolve cache clones' branches
prism workspace prune               delete cache clones nothing links to
prism workspace export | push       write or upload this repository's link bundle
prism link-server --dir <store>     serve uploaded bundles
```

A plain folder that holds several checkouts can declare them once: run
`prism workspace add app lib other` in that folder, and each checkout sees the others.

## When a link takes effect

A `workspace_repo:` line in `prism.yaml` is a proposal. It takes effect on a machine once you run
a `prism workspace`, `prism init` or `prism index` command in the repository (or parent folder)
that declares it. Until then answers name the repository as "link not confirmed". This is so a
repository you cloned cannot make Prism read other directories through a file it committed.

Even confirmed, a target is refused if it contains this repository, is the home directory or
above it, or has no sign of being a repository. Under `GROVE_UNTRUSTED` nothing is linked.

`prism init` lists related checkouts and links them only with `--workspace auto` or a yes.

## Discovery

`discover` reads manifests only: `go.mod`, `go.work`, `package.json`, `Cargo.toml`,
`pyproject.toml`, `requirements.txt`, `pom.xml`, `build.gradle(.kts)` with `settings.gradle`,
`composer.json`, at the repository root and in package directories below it. It reports:

- a checkout the build already points at (`go.work`, `replace => ../x`, `file:`, Cargo `path`,
  Gradle `includeBuild`, `pip -e`, Composer `path`);
- a sibling directory that declares a package this one requires, or requires one this declares;
- the same, for any repository Prism has been set up in elsewhere on the machine;
- `unlinked:` lines for required packages with no checkout found, with a fetch URL where the
  package name gives one.

Gradle files are read by pattern: computed dependencies are not seen. `.csproj` is not read.

## What each command does

- `lookup Name` — a name this repository does not define is shown from the linked repository that
  does. A package qualifier must match (`store.Normalize` is looked for in a `store` package), a
  repository this one requires ranks first, and candidates that still tie are listed as ambiguous.
  `--repo <id>` asks one linked repository directly.
- `read ../grove/pkg/x.go` — reads a file in a linked repository.
- `search term` — ends with one line counting matches in each linked repository. `--repo <id>`
  lists them; `--repo all` appends every linked repository.
- `change-impact Name` — leads with the sites in other repositories, then this repository's.
- `verify` — its first line says when linked repositories reference a contract the diff changed or
  removed, looked up under the old name. `--strict` fails on that. `verify --removed` counts
  whole-identifier lines in linked repositories.
- `test-plan` — lists tests in other repositories that reference a changed symbol.
- `query` — answers for this repository and says so in one line when repositories are linked.

Paths into a linked checkout are relative to this repository's root (`../app/main.go`), so they
can be passed straight to an editor. Cache clones are printed with absolute paths, under a line
saying they are read-only reference source.

## How strong each answer is

Every row from another repository is one of:

| Row | Meaning |
|---|---|
| plain | The other repository's compiler resolved a reference to this symbol |
| `[implements X]` | A Go type there implements the changed interface |
| `[import-scoped]` | Python: the call names something imported under this module and name |
| `[name match, not resolved]` | A whole-word text match, in a file that imports the package or names the owner type |

Name matching is used for a repository where the compiler pass has not completed or there is no
index, and for symbols and languages with no cross-repository records. The header of such a
repository says `NAME-MATCHED ONLY` and why. Prism does not print "no references" for a repository
it did not analyse.

The `workspaceScope:` line then accounts for every linked repository: resolved, name-matched,
skipped because its manifest does not require the package, stale, not searched.

## What is recorded, per language

| Language | Recorded | Key |
|---|---|---|
| Go | calls, function values, types, variables, constants, struct fields, interface implementers | `go:<import path>#<Type>.<Member>` |
| Java | calls, constructors, type uses, extends and implements | `jvm:<package>#<Outer.Inner>.<member>` |
| TypeScript, JavaScript | calls, `new`, type uses, extends and implements | `npm:<package>#<Class>.<member>` |
| Python | calls through imported names | `py:<module>#<name>` |
| Rust, Kotlin, C#, PHP, C/C++, Ruby, Swift | name matches only | — |

Overloads share a key. TypeScript needs the dependency resolvable (`node_modules`); a package
whose types come from DefinitelyTyped is keyed by the shape of those types, which may differ from
its source. A Java consumer is analysed against a linked library's sources when the library's jar
is not on its classpath.

## Branches and staleness

An index describes the tree that was on disk when it was written, and records that commit and
branch. Before answering from a linked checkout, Prism compares that with what is checked out now,
including uncommitted files, and with the Prism version and the set of linked repositories the
index was built with. If they differ it runs the ordinary incremental index there, within 20
seconds (`PRISM_WORKSPACE_REFRESH=<duration>` or `off`). If that cannot finish, the header leads
with `STALE` and both commits, and a row is shown only while its line on disk still names the
symbol.

The workspace is whatever is checked out. Prism never switches a branch. Two branches of one
repository at once means two directories (`git worktree`), each linked and indexed on its own.

In a Go workspace (`go.work` above the repository, listing it), a consumer is analysed against the
linked checkout, as `go build` does, not against the version its `go.mod` pins.

## Link server

For consumers that are not on the machine at all.

```
PRISM_LINK_TOKEN=... prism link-server --dir /var/lib/prism-links --port 8890   # on a shared host
prism index . && PRISM_LINK_TOKEN=... prism workspace push --server https://links.example  # in each repository's CI
workspace_server: https://links.example        # in prism.yaml of a repository that should ask it
```

A push uploads where the repository references other repositories' symbols, one source line per
reference, and its declarations' signatures, at its commit. No source bodies. A tree with
uncommitted changes is refused. Answers from the server are marked with the repository's commit
and upload date. A repository linked locally is not listed again. A server that cannot be reached,
or rejects the token, is reported as not covered.

## Limits

- Repositories that are neither linked nor uploaded are not covered, and the answer says so.
- Calls between services (HTTP, gRPC, queues), reflection and dependency injection are not found.
- The link server has one bearer token for reading and writing, answers from each repository's
  `main` (or `master`, or latest upload), and has no per-repository permissions.
- Cache clones have no size cap; prune them by hand. A clone has no dependencies installed, so
  TypeScript and Java consumers in a clone are name-matched.
- The graph explorer shows one repository.

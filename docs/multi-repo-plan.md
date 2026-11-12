# Prism multi-repo mode — design and implementation plan

Date: 2026-11-12. Status: proposal, nothing built. Baseline: prism v1.2.0, grove v0.63.27, astkit v0.16.10.

Contents

1. Context and what exists today
2. Goals and non-goals
3. Design
4. UX, use cases, agent flow
5. Setup, `prism init`, dependency discovery
6. Branches
7. Local, auto-clone, server: three sources on one layer
8. Branching and isolation of this work
9. Implementation steps with acceptance criteria
10. Verification
11. Risks and known gaps
12. Open decisions

---

## 1. Context and what exists today

### 1.1 The problem

Prism answers for one repository root. Three facts make it single-repo:

- **One root string.** The CLI resolves it in `cliRoot` (`internal/cli/root.go:22`); the MCP server fixes
  it once in `Handler.Root`; `grove.Client` holds one `root` and one engine (`internal/grove/client.go:26`).
- **One index per root, no identity beyond the path.** The index is `<root>/.grove/grove.db`. A symbol ID
  is `<repo-relative path>::<qualifiedName>@<blobSHA>` (`grove/internal/parser/treesitter.go:703`). It has
  no module, package, or repo in it. `qualifiedName` is unique only within a file (C++ namespaces are the
  one exception).
- **References that leave the tree are dropped.** The tree-sitter resolver discards a call whose import
  resolves to no in-repo file (`narrowByImport`, `grove/internal/graph/edges.go:4966`). The compiler passes
  know the external target but keep only a bare name to suppress wrong guesses.

Consequences an agent hits today:

- `change-impact` on a library function lists callers in that repo only. Callers in the five other repos
  that import it are invisible; the answer says "consumers outside this repository" at best.
- `lookup` of a symbol that lives in a dependency returns nothing, even when the dependency is checked
  out next door.
- A path into a sibling checkout is rejected (`safePathWithinRoot`, `internal/mcp/tools.go:4271`).

### 1.2 Prior work this builds on

- `docs/prism-feedback-plan.md`, Phase 7 (2026-09-09, P3, never started). Its requirements are kept:
  roots explicit and allowlisted; per-root index isolation; no implicit traversal into siblings; text ops
  before graph claims; cross-repo call edges only once symbol identity and import resolution are proven.
- Shipped guards that stay in force: `safePathWithinRoot`, `sameRoot`, `scopeInstruction`
  (commits 5454da9, 4f22f39, 5a1a025).
- Root discovery from a subdirectory (0b7cd48).

### 1.3 An accidental multi-repo mode already exists

`~/Projects/provasign/` is not a git repo but has its own `prism.yaml` and `.grove`. `cliRoot` looks for
`prism.yaml` in every ancestor before it looks for `.git`. So:

- A session opened at the parent gets one union index over every sibling repo.
- Running the CLI inside a sibling that has no `prism.yaml` of its own (kit, stele-archive, shale-test-bed,
  and others) roots at the parent.

The union index is weak for Go: Grove runs `go list ./...` with `GOWORK=off` from the root, and the parent
has no `go.mod`, so resolution there is likely name-based rather than compiler-backed (inferred from
code, not measured). This plan replaces the union with a federation and fixes the walk-up.

### 1.4 Measurements that shape the design

- CLI reopens the engine on every call: 3–4 s per index-backed call on a 1.1 GB index versus about 0.1 s
  from a warm MCP server (2026-08-15). CLI is now the default interface. A design that opens N engines
  per call is not acceptable.
- Index sizes: django 322 MB, guava 801 MB, grafana 941 MB. Per-branch or per-workspace copies are costly.
- Every `ExtractorVersion` bump forces a full rebuild for every user. Bumps must be batched.
- Startup text is about 45% of a cell's cost. Steering must barely grow.
- Agents use graph ops in 2–10% of Prism calls and rarely call `change-impact` unprompted.

---

## 2. Goals and non-goals

Goals

1. `change-impact`, `lookup`, `read`, `search`, `query`, `verify`, `test-plan` answer across a declared set
   of repositories, with the same completeness labelling discipline as today.
2. Dependencies between repos are discovered from manifests, with the evidence shown; the user confirms
   anything not explicit.
3. The same linking works for local checkouts, auto-cloned dependencies, and a shared server.
4. With no workspace configured, output is byte-identical to today.

Non-goals

- Service-to-service links (HTTP, gRPC, queues, DI wiring, reflection). These are not symbol references.
- Prism editing, checking out, or running anything in another repo. It reports sites; the agent acts.
- Code search over repos nobody declared (no crawling of the disk or the org).

---

## 3. Design

### 3.1 Federation, not a union index

Each repo keeps its own `.grove/grove.db`, unchanged in meaning. A workspace layer in Prism joins repos
at query time on a **package-qualified symbol key**. This is the Sourcegraph/SCIP approach: per-repo
indexes that export and import globally meaningful names.

Why not one big index over a parent folder:

- Symbol identity would still be path-based, so the same package checked out twice collides.
- Each repo's compiler pass needs its own module root to resolve properly.
- One edit would re-index edges for the union (kubernetes-scale edge materialization is about 28 s).
- It cannot extend to a clone cache or a server.

### 3.2 The symbol key

```
<ecosystem>:<namespace>#<descriptor>
```

| Language | Namespace | Example | Source of truth |
|---|---|---|---|
| Go | package import path | `go:github.com/provasign/grove/pkg/grove#Engine.Open` | go/types `Object.Pkg().Path()` |
| Java/Kotlin | package + outer class | `jvm:com.google.gson.TypeAdapter#write` | javac owner (`ExtOwner`) |
| TypeScript/JS | npm package name | `npm:@scope/pkg#createClient` | tsc declaring file under `node_modules/<pkg>` |
| Rust | crate name | `cargo:serde_json#value::Value` | rust-analyzer definition crate |
| Python | dotted module | `py:django.db.models#Model.save` | import specifier (not compiler-backed) |
| PHP | namespace | `php:Vendor\Pkg\Client#send` | PSR-4 + `use` |
| C# | namespace | `nuget:Acme.Core.Client#Send` | `using` + ProjectReference |
| C/C++ | header path / namespace | `c:acme/client.h#acme::Client::send` | include + namespace |

Rules:

- The key names a symbol independent of where it is checked out, which commit, or which file bytes. Links
  survive edits; blob-SHA symbol IDs are never used across repos.
- Version is not part of the key. Version is carried beside the link (section 3.7).
- Overloads: v1 keys are name-level. Where both sides are compiler-backed (Java), a parameter-type
  suffix can be added later without changing the scheme. Name-level matches on an overloaded name are
  labelled.

### 3.3 Two new facts per repo (Grove)

Grove records neither today.

**(a) What this repo declares — `packages` and a per-file namespace.**

```sql
CREATE TABLE packages (
  dir        TEXT PRIMARY KEY,   -- repo-relative dir of the manifest ('' = root)
  ecosystem  TEXT NOT NULL,      -- go | jvm | npm | cargo | py | php | nuget
  name       TEXT NOT NULL,      -- module path, group:artifact, package name, crate name
  version    TEXT NOT NULL DEFAULT '',
  manifest   TEXT NOT NULL       -- repo-relative manifest path
);
CREATE TABLE requires (
  manifest   TEXT NOT NULL,
  ecosystem  TEXT NOT NULL,
  name       TEXT NOT NULL,      -- required package
  version    TEXT NOT NULL DEFAULT '',   -- pin / range as written
  local_path TEXT NOT NULL DEFAULT '',   -- replace/path/file: target if any
  evidence   TEXT NOT NULL       -- 'go.mod:7 require github.com/provasign/grove v0.63.27'
);
ALTER TABLE file_index ADD COLUMN namespace TEXT NOT NULL DEFAULT '';
  -- Go import path of the file's package, Java package, Python dotted module, PHP/C# namespace
```

A symbol's exported key is computed at query time from `file_index.namespace` + `symbols.qualified_name`.
Symbol rows and IDs do not change.

**(b) What this repo references outside itself — `extrefs`.**

```sql
CREATE TABLE extrefs (
  file_path   TEXT NOT NULL,     -- repo-relative; rows are replaced per file on re-index
  from_qname  TEXT NOT NULL,     -- enclosing symbol's qualified name ('' = file scope)
  target_key  TEXT NOT NULL,
  kind        TEXT NOT NULL,     -- call | type | extends | implements | override | import
  line        INTEGER NOT NULL,
  col         INTEGER NOT NULL DEFAULT 0,
  confidence  TEXT NOT NULL      -- compiler | import-scoped
);
CREATE INDEX idx_extref_target ON extrefs(target_key);
CREATE INDEX idx_extref_file   ON extrefs(file_path);
```

Keyed by `file_path` + `from_qname` rather than symbol ID so incremental re-index is "delete this file's
rows, insert new ones" with no old-ID-to-new-ID remap.

Where the data comes from — in most cases it is already computed and discarded:

| Language | Today | Change |
|---|---|---|
| Go | `goObjectDirs` returns nil for a package outside `go list`'s map (`native/go.go:1090`); only a bare-name mark is kept (`:628`). `EdgeExternalContract` / `EdgeExternalIfaceCall` already keep `importpath.Type` strings. | Emit an extref with `obj.Pkg().Path()` + name at the same point. |
| Java | Resolver reports `Ext` and `ExtOwner`; `ExtOwner` is parsed and never used (`native/javac.go:905-928`). | Emit extref `jvm:<ExtOwner>#<Ext>`. |
| TypeScript | `externalName` knows every declaration is outside the root or in `node_modules` (`native/js_ts.go:755`). | Map the declaring path to its package name (nearest `package.json`), emit extref. |
| Rust | `rustURIFile` returns "" outside the root and the site is skipped (`native/rust_worker.go:300`). | Take the crate from the definition path / `cargo metadata`, emit extref. |
| Python, PHP, C#, C/C++ | Dropped in `narrowByImport` / never attempted. | Emit from import specifier + referenced name, `confidence: import-scoped`. |

All schema and extraction changes land under **one** `ExtractorVersion` bump.

### 3.4 Workspace layer (Prism) — `internal/workspace`

```go
type Source int // Local, Cache, Remote

type Root struct {
    ID          string        // short name: "grove"
    Path        string        // absolute; empty for Remote
    Source      Source
    Packages    []Package     // what it declares
    IndexedHead string        // commit SHA the index describes
    IndexedRef  string        // branch name at index time, informational
    Dirty       bool
    Evidence    string        // why it is linked: "go.work use ./grove"
}

type Workspace struct {
    Primary *Root
    Roots   []*Root
}

type Linker interface {       // one implementation per Source
    Consumers(ctx context.Context, keys []string) ([]Site, error) // who references these keys
    Define(ctx context.Context, key string) ([]Definition, error) // where is this key declared
}
```

- **Local and Cache linkers** open the other root's `grove.db` read-only and run indexed SQL
  (`extrefs.target_key`, `symbols.qualified_name` + `file_index.namespace`). They do not open a Grove
  engine or build a graph. Target: tens of milliseconds per root, which keeps the CLI viable.
- **Remote linker** makes the same two calls over HTTP (section 7).
- The **primary root** keeps the full engine exactly as today.
- A second full engine is opened only when an op needs a graph walk inside another root (transitive
  impact there). Lazy; kept warm in MCP mode; in CLI mode it is paid once per call and reported.
- `Handler` gets a `Workspace` field; `safePathWithinRoot` accepts a path if it is inside any
  allowlisted workspace root, and rejects everything else as today.

### 3.5 What each op does in a workspace

| Op | Behaviour |
|---|---|
| `change-impact` | In-repo answer as today. Then per dependent root: sites referencing the symbol's key (and the keys of overriding/implementing members), as `path:line:col: text`. `completenessScope` names roots covered, roots not indexed or stale, and version pins. |
| `lookup` | Name not defined in the primary root but defined in a workspace root → return that body, with a repo header. Defined in both → primary first, others listed. |
| `search` | Default scope is the primary root (keeps payloads the size they are today). One trailing line gives match counts in other roots. `--repo <id>` or `--repo all` widens. |
| `read` | Accepts a path into any workspace root. |
| `query` | Anchors resolve across roots. Neighbours stay inside the anchor's root, plus direct cross-repo callers/callees. |
| `verify` | After a removal or contract change, extrefs to the affected keys in other roots are reported as unchecked callers. |
| `test-plan` | Lists tests in consumer repos that reference changed symbols, grouped by repo. |
| `graph` | A repo level above directories; cross-repo edges drawn. Built last. |

Illustrative `change-impact` output (shape only, numbers invented):

```
change-impact Engine.Open
declaration   Engine.Open  pkg/grove/grove.go:149
callers (this repository, compiler-backed go): 6
  ...
consumers in workspace repositories:
// repo prism   ../prism   requires grove v0.63.27 (go.work: local checkout)
  ../prism/internal/grove/client.go:88:14:  eng, err := groveeng.Open(ctx, groveeng.Config{...})
// repo stele   ../stele   requires grove v0.63.27 (go.work: local checkout)
  ../stele/internal/engine/open.go:41:12:  ...
// repo fuse    ../fuse    requires grove v0.15.0 — checkout is v0.63.27+; sites are against fuse's code as checked out
  ../fuse/internal/graph/load.go:33:9:  ...
completenessScope: workspace — 3 of 4 dependent repositories indexed
  not covered: mason (index stale: indexed 4f1c2ab main, now 9d0e77c feature-x, 12 files changed)
  not in workspace: consumers in repositories that are not linked
```

Output rules (from standing feedback):

- A root Prism did not look at is always named. "I did not look" never reads as "not there".
- Prism writes nothing in other repos and gives the agent no commands to run.
- Labels say what the evidence is: `compiler` or `import-scoped`.

### 3.6 Paths in output

Other-root results are printed as real paths relative to the primary root (`../grove/internal/store/store.go`)
under a per-repo header line. No `root-id/path` or `repo:path` syntax.

Reason: the agent's own Read and Edit tools accept these paths directly, and transcripts show agents
already send `../sibling/...` paths (that is what 5a1a025 was reacting to). A new path syntax would need
translation on every hand-off. For cache clones the path is the absolute cache path; for remote roots
there is no local path and the header says so (`repo@sha, not checked out`).

### 3.7 Version pins and skew

A consumer may require `grove v0.15.0` while the local grove checkout is far ahead. The link is by key, so
sites are still found. The answer states, per root, the pin from `requires` and what the checkout is.
Prism does not guess whether the pinned version had the symbol. To answer against the pinned code
exactly, fetch that tag as a cache clone (section 7.2).

---

## 4. UX, use cases, agent flow

### 4.1 Use cases, in priority order

1. **Library change, consumers elsewhere.** An agent in `grove` changes a signature. `change-impact` lists
   call sites in `prism`, `stele`, `mason`, `fuse`. A silent miss here breaks a downstream build after
   release, and no single-repo tool can answer it.
2. **Consumer needs a dependency's source.** An agent in `prism` runs `lookup` on a Grove symbol and gets
   the body from the checkout, at the right version state, instead of nothing or a module-cache hunt.
3. **Cross-repo task from a parent folder.** The session is opened at `provasign/`. Queries fan out; each
   repo keeps its own compiler-backed resolution; the agent edits several repos.
4. **Org-wide "who uses this"** (server). The answer for repos not on the machine.
5. **Release check.** Before tagging a library, `verify` and `test-plan` list affected consumers and their
   tests.

### 4.2 How it works for an agent

- **No new tool, no new op, no workspace argument.** The six ops return more. The MCP schema does not grow.
- **Steering grows by one generated line**, written by `prism init` only when a workspace exists:
  `Workspace: grove (../grove), astkit (../astkit); change-impact and lookup cover them.`
- **The agent does the cross-repo edits itself** with its normal tools, using the paths Prism printed.
- **Session rooted in one repo** is the normal case: that repo is primary, the rest are read through
  links. **Session rooted at a parent folder**: the repo containing the file being asked about is
  primary for that call.
- **MCP vs CLI.** Both work. CLI pays indexed SQL per extra root (small). MCP keeps other roots' stores
  open and any second engine warm, so it remains the recommendation for large repos.

### 4.3 What a human sees

- `prism workspace` — one table: repo, path or source, declared packages, how it was linked, pin vs
  checkout, branch and SHA, index age, stale or not.
- `prism init` — prints discovered dependencies with evidence and asks before linking anything not
  explicit.

### 4.4 Honest expectation

This raises correctness when a cross-repo question is asked. It will not by itself make agents ask more
often: on current benches they call `change-impact` rarely. The measurable claim is engine-level
(cross-repo recall and precision against a compiler oracle). Any claim about agent outcomes needs its
own paired run, and none is planned without asking first.

---

## 5. Setup, `prism init`, dependency discovery

### 5.1 Commands

| Command | Effect |
|---|---|
| `prism init` (in a repo) | As today, plus: parse manifests, discover dependencies (5.2), print findings with evidence, link explicit ones, ask about the rest. Registers the repo in the user-level registry. `--workspace auto` accepts all offered links; `--workspace none` skips. |
| `prism init` (in a non-git folder that contains repos) | Writes a workspace-only `prism.yaml` listing the child repos. No union index. Offers to run init in children that lack one. |
| `prism workspace` | Status table (4.3). |
| `prism workspace add <path> [--id x]` | Link a local checkout. |
| `prism workspace add <git-url>[@ref]` | Link as a cache clone (7.2). |
| `prism workspace remove <id>` | Unlink. Never deletes a checkout. |
| `prism workspace fetch [--pinned]` | Clone/update cache clones; `--pinned` fetches the versions consumers pin. |
| `prism workspace export` / `push` | Write / upload a link bundle (7.3). |
| `prism index` | Unchanged for the current root; `--workspace` indexes every local root. |

### 5.2 How dependencies are found

Three tiers, most certain first. Every link stores its evidence line and shows it in `prism workspace`.

**Tier 1 — explicit local pointers. Linked without asking, because the build already treats them as one.**

| Ecosystem | Signal |
|---|---|
| Go | `go.work` `use` entries; `go.mod` `replace X => ../path` |
| npm / pnpm / yarn | `workspaces` in `package.json`; `pnpm-workspace.yaml`; dependency values `file:`, `link:`, `workspace:`; tsconfig `references` |
| Rust | `path = "../x"` dependencies; `[workspace] members` |
| Maven | `<modules>`; `<parent><relativePath>` |
| Gradle | `includeBuild(...)`; `include(...)` with `projectDir` outside the root |
| Python | path / editable dependencies in `pyproject.toml`, `-e ../x` in requirements |
| PHP | `repositories` of type `path` in `composer.json` |
| .NET | `ProjectReference` pointing outside the root |
| Any | `.gitmodules` |

**Tier 2 — a declared dependency matches a known local repo. Offered at init.**

Repo A's manifest requires package P. The registry lists a repo on this machine that declares P.
The registry is `os.UserConfigDir()/prism/repos.json`, written by `prism init` and `prism index`: path,
declared packages, remote URL. It contains only repos the user has run Prism in — that is the consent.

**Tier 3 — sibling scan. Offered, never silent.**

Read only the manifest files of directories next to the repo (one level), match as in tier 2. This finds
checkouts Prism has never been run in. Nothing is indexed or linked until the user says yes.

**Not found locally.** Listed as `unlinked: <package> <version>` with a source URL when the manifest or
package metadata gives one. That list is the input to `workspace fetch` and to the server.

**The reverse direction.** A library does not know its consumers. Local consumers are found by scanning
`requires` across every workspace root and registry entry for the library's declared packages. Consumers
that are not on the machine come only from the server.

### 5.3 Manifest parsing (new, in Grove: `internal/manifest`)

Today Grove only checks that manifests exist and hashes them into cache keys; Prism reads the Go `module`
line (`internal/mcp/lookupname.go:354`, `internal/grove/readiness.go:151`). New parsing, identity plus
requires, for: `go.mod`, `go.work`, `package.json`, `pnpm-workspace.yaml`, `pom.xml`, Gradle settings,
`Cargo.toml` (or `cargo metadata` without `--no-deps`), `pyproject.toml` / `setup.cfg`, `composer.json`,
`.csproj`. Results fill `packages` and `requires`. Parsing is static and offline; no package manager is
run that is not already run today.

### 5.4 Configuration

`prism.yaml` is a flat `key: value` file with a hand-rolled parser (`internal/config/config.go:50`) and no
nesting, so links use a repeated key, the same pattern as `arch_deny`:

```yaml
workspace_repo: grove=../grove
workspace_repo: astkit=../astkit
workspace_server: https://prism.example.internal
```

- Relative paths only, so the file is safe to commit and works for a teammate with the same layout.
- Machine-specific links (registry matches, cache clones) live in the user-level registry, not the repo.
- A missing path is reported as `not checked out`, never an error that blocks single-repo answers.

### 5.5 Root resolution fix

`cliRoot` currently prefers any ancestor `prism.yaml` over the nearest `.git`. New order: nearest `.git`
boundary wins unless a `prism.yaml` sits at or below it; an ancestor workspace `prism.yaml` is then read
only to learn the sibling roots. This removes the accidental union mode and makes a parent-folder
workspace explicit.

---

## 6. Branches

The rule: **an index describes one tree state, identified by commit SHA plus a dirty flag, never by
branch name.** A branch is a moving pointer to a SHA. Each source applies the rule differently.

### 6.1 Local checkouts — the index follows the working tree

- A local workspace means "the trees on disk right now", whatever branch each is on, including
  uncommitted edits. That is the code that would build together, so it is the right answer for an agent
  editing across repos. Prism never checks out or switches a branch in any repo.
- One `grove.db` per checkout, not one per branch. A branch switch is handled like any edit: Grove hashes
  content per file (`file_index.blob_sha`) and re-extracts only files whose bytes changed.
- Gap today: only the primary root is refreshed before an answer, and Grove stores no git state. Fix:
  - New meta keys written at index time: `index-head` (SHA), `index-ref` (branch, informational),
    `index-dirty`.
  - Before a cross-repo answer, each consulted root gets a cheap check: current HEAD vs `index-head`,
    then the existing size+mtime stat walk. If it differs, run the normal incremental re-index for that
    root under a time budget.
  - Over budget: answer from the old index and say so per root — indexed SHA/branch, current SHA/branch,
    number of files changed since. Each reported site in a changed file is marked
    `file changed since index`. Never silently stale.
- `git worktree` checkouts are separate directories, so each is its own root with its own index. That is
  the supported way to have two branches of one repo live at once.
- Feature work spanning repos (branch `feature-x` in both grove and prism): nothing special, the working
  trees are the workspace. `prism workspace` shows each repo's branch and SHA and notes when linked repos
  are on differently named branches. Informational only.

### 6.2 Which state of a dependency does a consumer mean?

The link evidence decides:

- **Local override** (`go.work`, `replace => ../x`, `file:`, Cargo `path`): the checkout's working tree is
  the truth, on whatever branch it is.
- **Version pin** with a local checkout at a different commit: answer from the checkout and state the pin
  and the checkout SHA. For the pinned code exactly, fetch that tag as a cache clone.

### 6.3 Cache clones — immutable, keyed by SHA

A branch or tag name is resolved to a SHA at fetch time and stored as `<repo>@<sha>`, so that index can
never go stale. One bare clone per repo with a git worktree per SHA, so objects are shared. One index
per SHA. LRU eviction under a size cap. Fetching a branch name again re-resolves it and adds a new entry.

### 6.4 Server — bundles keyed by `(repo, SHA)`, plus a refs table

- CI uploads a link bundle per pushed commit on the branches the org chooses: default and release
  branches kept; PR branches optional with a TTL.
- A refs table maps branch/tag → SHA. A query names a ref per repo or takes the default branch head.
  Every answer is stamped `repo@sha (ref)`.
- The primary repo is always the local tree, so "my uncommitted branch against everyone's main" works:
  keys are derived locally; consumers come from the server at their stamped SHAs.
- Storage: bundles hold link rows only, so start with whole per-commit bundles. If size is measured to
  matter, dedupe per file blob (two branches differing in 20 files share the rest). Caveat if that is
  built: a file's resolved references can change when its dependencies change though its own bytes did
  not, so the dedupe key must include the manifest/lockfile hash, not the blob alone.

### 6.5 Not supported

Asking a local workspace about a branch that is not checked out anywhere. That needs a worktree, a cache
clone, or the server.

---

## 7. Local, auto-clone, server: three sources on one layer

All three implement the `Linker` interface (3.4). Ops do not know which source answered, except that the
answer is stamped with it.

### 7.1 Local

Roots are directories on disk. Full source available, edits possible, freshness handled as in 6.1.

### 7.2 Cache clones (auto-clone)

- Location: `os.UserCacheDir()/prism/repos/<host>/<org>/<repo>/` (bare clone) with worktrees `@<sha>`.
- Triggered only by an explicit command (`workspace add <url>`, `workspace fetch`). Never during a query.
- Uses the user's existing git credentials. Never pushes. Checkouts are read-only for Prism and are
  presented to the agent as reference source, not as an edit target.
- Indexed once per SHA. Size cap with LRU eviction; `prism workspace` shows cache use.
- Shallow, blob-filtered clones are avoided for indexing: a blobless clone that lazily fetches per file
  froze the machine during the 2026-10-05 harvest.

### 7.3 Shared server

- **Link bundle.** `prism workspace export` writes `packages`, `requires`, `extrefs`, exported symbol rows
  (key, kind, signature, file, span), and the commit SHA. No source bodies. Small relative to the index.
- **Upload.** `prism workspace push` from CI on each push to a tracked branch.
- **Server.** `prism serve` already exists (`internal/httpapi/http.go:22`, one `POST /<tool>` per tool). It
  gains a bundle store and two endpoints: `POST /link/consumers` (keys → sites) and `POST /link/define`
  (key → definitions), plus `POST /link/bundle` for upload. Storage is SQLite with the same two indexes,
  one logical bundle per `(repo, SHA)`.
- **Client.** `workspace_server:` in `prism.yaml`. A `Root{Source: Remote}` answers through HTTP. Sites
  come back as `repo@sha path:line:col` with the line text stored in the bundle.
- **Source bodies.** Not served in the first version. `lookup` of a remote-only symbol returns its
  signature and location and says the body needs a checkout or `workspace fetch`.
- **Access control.** Minimum: bearer token, per-repo read permission mirrored from the git host. This
  needs its own design before step 8 starts; it is the largest unknown in the server part.

---

## 8. Branching and isolation of this work

Built on a `multi-repo` branch in grove and in prism (astkit only if a parser change turns out to be
needed). `main` in both stays releasable for production bug fixes; if the work fails, the branches are
deleted and nothing on `main` has changed. This is a deliberate exception to the usual
commit-to-local-main flow.

- **Separate worktrees, so both lines are live at once.** `git worktree add` each branch under a new
  parent, e.g. `~/Projects/provasign-mr/{grove,prism}`, with its own `go.work` using those two
  directories. The existing `provasign/{grove,prism}` checkouts stay on `main` with the existing
  `go.work`. No branch switching in the main checkouts.
- **Separate binary.** The branch builds to `prism-mr`, never to `~/bin/prism` or the Homebrew path. The
  branch bumps `ExtractorVersion`; a different stamp forces a full re-extract, so the two binaries must
  never share a `.grove`.
- **Separate test bed.** Fresh clones of the family (astkit, stele, mason, fuse, plus the two worktrees)
  under `provasign-mr/`, indexed only by `prism-mr`. The live sibling indexes are not touched.
- **Separate user state.** On the branch the registry is `prism-mr/repos.json` and the clone cache
  `prism-mr/repos/` under the user config/cache dirs. Deleting the experiment is: remove the worktrees,
  the two branches, and those two folders.
- **CI on the branch.** prism's `go.mod` on the branch requires grove at the branch's pseudo-version
  (`go get github.com/provasign/grove@multi-repo` after pushing the grove branch). CI runs `go test` and
  the engine invariants on both branches. No tags and no releases are cut from the branches.
- **Keeping up with main.** Merge `main` into `multi-repo` in each repo after every production fix or
  release (grove first, then prism; `fuse_*` checks around each merge). Bug fixes are made on `main` and
  flow into the branch, never the other way.
- **Merge back.** One decision at the end, per repo, grove first, only after the full gate is green on
  the branch. Then tag grove, repoint prism's `go.mod` to the tag, merge prism, tag. The
  `ExtractorVersion` bump reaches users only at that point.

---

## 9. Implementation steps with acceptance criteria

| # | Work | Main files | Done when |
|---|---|---|---|
| 0 | Worktrees, `go.work`, `prism-mr` build target, branch CI | both repos | `prism-mr version` builds from the worktrees; main checkouts untouched |
| 1 | `internal/workspace` model; `workspace_repo` config key; `prism workspace` status; allowlist in `safePathWithinRoot`; `cliRoot` `.git`-boundary fix | prism `internal/config/config.go`, `internal/cli/root.go`, `internal/mcp/tools.go` | Linked root is readable; unlinked sibling still rejected; no-workspace output byte-identical |
| 2 | `read` across roots; `search --repo`; `lookup` fallback via SQL on the other root's `symbols` | prism `internal/mcp`, `internal/textsearch` | `prism-mr lookup` of a Grove symbol from prism returns the body from `../grove` |
| 3 | `internal/manifest`; `packages`, `requires`, `file_index.namespace`; discovery tiers 1–3; init prompt; registry | grove `internal/manifest`, `internal/store/store.go`; prism `internal/cli/commands.go` (`cmdInit`) | On the family, discovery reproduces the real edges: prism→grove→astkit, stele→grove, mason→prism, fuse→grove+astkit, each with its evidence line |
| 4 | `extrefs` for Go, then Java, TypeScript, Rust; single `ExtractorVersion` bump | grove `internal/native/{go,javac,js_ts,rust_worker}.go` | Go cross-repo recall/precision vs the `go.work` oracle at the single-repo bar |
| 4b | `index-head` / `index-ref` / `index-dirty`; per-root freshness check, budgeted refresh, stale labelling | grove `internal/index/indexer.go`, `core/types.go`; prism `internal/mcp/graphcache.go` | Branch-switch test (section 10) passes |
| 5 | Local `Linker`; cross-repo `change-impact`; completeness wording; `verify`; `test-plan` | prism `internal/mcp/{tools,verify,testplan}.go` | End-to-end checks in section 10 pass |
| 6 | Import-scoped extrefs for Python, PHP, C#, C/C++ | grove `internal/graph/edges.go` | Labelled `import-scoped`; precision measured on a split corpus repo before enabling by default |
| 7 | Cache clones: `workspace add <url>`, `fetch`, LRU | prism `internal/workspace` | A pinned tag is fetched, indexed once, answered from, evicted under the cap |
| 8 | Bundle export/push; server endpoints; remote root; auth | prism `internal/httpapi`, `internal/workspace` | A consumer repo absent from the machine is reported via the server with `repo@sha` |
| 9 | `graph` repo level; steering line; read-guard path collision across roots (`prism_read_tracker.py` matches by path suffix) | prism `internal/graphui`, `internal/cli/surface.go`, `internal/cli/assets` | Same relative path in two roots does not collide |

Natural stopping points: after step 2 (text ops across repos, no engine change), after step 5 (real
cross-repo impact for Go/Java/TS/Rust on local checkouts), after step 7, after step 8.

---

## 10. Verification

- **Unit and regression.** `go test ./...` in astkit, grove, prism. `ci_invariants.py` unchanged and green
  on a durable corpus root. With no workspace configured, output must be byte-identical to `main`.
- **Go ground truth, no LLM.** On the provasign family, build an oracle with `go/types` under a `go.work`
  that loads all modules as one program, and list every reference from one module into another. Compare
  to `extrefs` joined by key. Add to the grove-eval scorecard so a later drop is a measured regression.
- **Java ground truth.** Split a multi-module corpus repo into separate roots. The existing single-root
  compiler-backed answer is the ground truth for the federated answer.
- **End-to-end by hand.**
  - In `grove`: `prism-mr change-impact Engine.Open --format text` lists the call at
    `prism/internal/grove/client.go:88` and shows fuse's `v0.15.0` pin as skew.
  - In `prism`: `prism-mr lookup` of a Grove symbol returns the body from `../grove`.
  - `prism-mr read ../kit/...` (not linked) is still rejected.
- **Branches.** Index grove on `main`, switch it to a branch that removes a call site, run
  `change-impact` from prism without re-indexing by hand: the answer reflects the new tree, or names the
  root as stale with both SHAs. Repeat with a `git worktree` of grove as a second root, and with an
  uncommitted edit (`index-dirty`).
- **Latency.** Time the cross-repo section on the CLI with five linked roots. Budget: under 300 ms added.
- **Agent benchmarks** are paid. None run without asking first; one-unit probe before any fleet; Sonnet only.

---

## 11. Risks and known gaps

| Risk | Handling |
|---|---|
| `ExtractorVersion` bump forces every user to rebuild | One bump for the whole feature, delivered at merge-back only |
| TypeScript export names through re-exporting entry points | Matched by package + name; ambiguous matches labelled |
| Tree-sitter languages have no compiler truth | `import-scoped` label; measured before default-on (step 6) |
| Overloads at name level | Labelled; signature suffix later where both sides are compiler-backed |
| Stale non-primary indexes | Budgeted refresh, otherwise stated per root (6.1) |
| Same package declared by two local checkouts | Both reported; `prism workspace` flags the duplicate; explicit `workspace_repo` wins over registry |
| Disk growth from cache clones | Size cap, LRU, explicit fetch only |
| Server access control | Needs its own design before step 8 |
| Agents may not call the ops more often | Claim is engine correctness, not agent outcomes (4.4) |
| Long-lived branch drifts from main | Merge main in after every fix/release (section 8) |

Out of scope, stated in output where relevant: reflection, DI wiring, HTTP/gRPC/queue calls between
services.

---

## 12. Open decisions

Defaults are chosen; each can be changed before the step that depends on it.

1. Branch name `multi-repo`, worktree parent `~/Projects/provasign-mr/`.
2. `search` stays primary-root by default with a count line for other roots (alternative: search all roots).
3. Steps 1–2 stay on the branch with everything else (alternative: merge them to `main` early, since they
   need no engine change).
4. Language order for extrefs: Go, Java, TypeScript, Rust, then the tree-sitter languages.
5. Server serves link data only in its first version, no source bodies.

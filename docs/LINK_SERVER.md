# Link server

A link server lets Prism answer "who uses this symbol?" for repositories that are not on your
machine. Each consuming repository's CI uploads a small record of where it uses other
repositories' symbols. When you run `change-impact`, `verify`, `test-plan` or `lookup`, Prism asks
the server and lists those sites next to the ones it found locally.

It is one of three ways to bring another repository into an answer
([docs/MULTI_REPO.md](MULTI_REPO.md) covers all three):

| Kind | Use it when | Source on your machine |
|---|---|---|
| Local checkout (`prism workspace add ../app`) | You have the repository checked out | Yes |
| Cache clone (`prism workspace add https://host/org/app.git@ref`) | You can clone it, but don't work in it | Yes, read-only, at one commit |
| Link server (`workspace_server:` in `prism.yaml`) | Many consumers, or ones you can't or shouldn't clone | No: one line per use, plus signatures |

## Contents

- [How it works](#how-it-works)
- [Quick start on one machine](#quick-start-on-one-machine)
- [Who runs it](#who-runs-it)
- [What a repository uploads](#what-a-repository-uploads)
- [Running the server](#running-the-server)
- [Hosting it for a team](#hosting-it-for-a-team)
- [Uploading from CI](#uploading-from-ci)
- [Asking the server](#asking-the-server)
- [Which commit is answered](#which-commit-is-answered)
- [Reading an answer](#reading-an-answer)
- [HTTP API](#http-api)
- [Security model](#security-model)
- [Limits](#limits)
- [Troubleshooting](#troubleshooting)
- [What was tested](#what-was-tested)

## How it works

```
 consumer repositories (CI, on each merge to main)          shared host
 ┌──────────────┐  prism index .                           ┌───────────────────────────┐
 │ app          │  prism workspace push ──── POST /link/bundle ──▶ prism link-server  │
 │ svc          │  (one bundle per commit)                 │   --dir /var/lib/...      │
 └──────────────┘                                          │   files on disk, no DB    │
                                                           └─────────────▲─────────────┘
 your machine, in the library                                            │
 ┌──────────────────────────────────────────┐                            │
 │ prism.yaml:  workspace_server: https://… │  POST /link/consumers ─────┘
 │ prism change-impact Normalize            │  POST /link/define
 │   → local callers + every uploaded site  │
 └──────────────────────────────────────────┘
```

- **Nothing is merged.** Each repository keeps its own index, and the server stores what each one
  uploaded. Prism asks at query time.
- **Symbols are matched by key**, such as `go:github.com/acme/lib#Normalize`: the ecosystem, the
  package, and the name. A consumer's reference is recorded under the key of the symbol it
  resolves to, so the match is exact, not a text search.
- **Each answer is at a commit.** Every site names the consumer's commit and upload date.

## Quick start on one machine

These commands were run on macOS with Prism v1.3.5. A library (`lib`) and two consumers (`app`,
`svc`) are separate git repositories; nothing is linked locally.

```sh
# 1. Start a server (any machine the others can reach)
export PRISM_LINK_TOKEN=change-me
prism link-server --dir ./link-store --port 8890
# prism link server on :8890: 0 repositories in ./link-store

# 2. In each consumer, index and upload (normally a CI step)
cd app && prism index . && prism workspace push --server http://127.0.0.1:8890
# pushed github.com/acme/app@68ba9726 (main) to http://127.0.0.1:8890: 1 reference(s), 1 declaration(s)

# 3. In the library, point Prism at the server and ask
cd lib && echo 'workspace_server: http://127.0.0.1:8890' >> prism.yaml
prism change-impact Normalize --format text
```

The answer lists the library's own sites, then:

```
consumers in other repositories:
// repo github.com/acme/app@68ba9726 (main)  from the server, not checked out  requires github.com/acme/lib v0.1.0  uploaded 2026-11-27
  github.com/acme/app:main.go:5:27: func main() { println(lib.Normalize("A")) }  [in main]
// repo github.com/acme/svc@556ea478 (main)  from the server, not checked out  requires github.com/acme/lib v0.1.0  uploaded 2026-11-27
  github.com/acme/svc:handler.go:5:43: func Handle(k string) string { return lib.Normalize(k) }  [in Handle]
workspaceScope: server http://127.0.0.1:8890: 2 site(s) in 2 of 2 uploaded repositories, each at its uploaded commit; repositories neither linked nor uploaded are not covered
```

These consumers build the library through `replace github.com/acme/lib => ../lib`, so from v1.3.6
their lines read `requires github.com/acme/lib v0.1.0, built against a local checkout when
uploaded`. A consumer that uses the published version shows the version only.

## Who runs it

You do. Provasign does not host a link server. It is self-hosted inside your organization, like an
internal package registry. There is no separate product: it is the same `prism` binary, started as
`prism link-server`.

A typical split:

| Who | What they do |
|---|---|
| Platform or developer-tools team | Runs one server, holds the token, backs up its directory |
| Each consuming repository | One CI step on merges to `main`: `prism index .` then `prism workspace push` |
| Library owners and their agents | Add `workspace_server:` to `prism.yaml` and set `PRISM_LINK_TOKEN` |

One server can hold every repository in the organization. Uploads from different repositories
don't interact.

## What a repository uploads

`prism workspace push` builds a bundle from the repository's existing index and sends it.
`prism workspace export -o bundle.json` writes the same bundle to a file, so you can inspect it
before uploading anything.

A bundle, abridged from the quick start:

```json
{
  "repo": "github.com/acme/app",
  "sha": "68ba972628e85f3d795ba927a43260357fa9148d",
  "ref": "main",
  "created": "2026-11-27T17:15:46Z",
  "packages": [{ "ecosystem": "go", "name": "github.com/acme/app", "manifest": "go.mod", "dir": "" }],
  "requires": [{ "ecosystem": "go", "name": "github.com/acme/lib", "version": "v0.1.0", "manifest": "go.mod", "local": true }],
  "refs": [{
    "key": "go:github.com/acme/lib#Normalize",
    "file": "main.go", "from": "main", "line": 5, "col": 27,
    "text": "func main() { println(lib.Normalize(\"A\")) }"
  }],
  "defs": [{
    "key": "go:github.com/acme/app#main", "kind": "function", "name": "main",
    "signature": "func main()", "file": "main.go", "line": 5
  }]
}
```

| Field | What it holds |
|---|---|
| `repo` | Stable name: `host/org/repo` from the `origin` remote, else the directory name. `--repo` overrides it |
| `sha`, `ref` | The commit, and the branch it was exported from (`--ref` overrides it) |
| `packages`, `requires` | What the repository's build manifests declare and require: ecosystem, name, pinned version and manifest file. `local: true` means the dependency was built from a local directory (a Go `replace`, `go.work`, an npm `file:` dependency), so the pinned version is not what it was built against. The directory itself is not uploaded |
| `refs` | Each use of another repository's symbol: key, file, enclosing symbol, line, column, **the one source line**, and whether it is in a test file |
| `defs` | Each symbol the repository declares, with its signature, file and line |

**Not uploaded:** function bodies, whole files, or history. The only source text is the one line
per reference and each declaration's signature.

The one source line per reference is still source code. Treat the server's directory with the
same care as the repositories it describes, and run `prism workspace export -o bundle.json` once
to review what a repository will send.

Since v1.3.6, no path from the uploading machine is sent. v1.3.5 sent a local dependency's
absolute path and manifest line; a v1.3.6 server drops both from uploads by older clients
before storing them.

## Running the server

```sh
PRISM_LINK_TOKEN=<secret> prism link-server --dir <store> [--port N]
```

| Setting | Meaning |
|---|---|
| `--dir` | Where bundles are stored. Required. Created if missing |
| `--addr` | Host or IP address to listen on, such as `127.0.0.1` behind a proxy. Default: every interface (v1.3.6 and later) |
| `--port` | TCP port. Default 8890 |
| `PRISM_LINK_TOKEN` | The bearer token every upload and query must send. Without it the server starts, prints `WARNING no PRISM_LINK_TOKEN set; anyone who can reach this port can read and upload bundles`, and accepts everyone |

**Network.** The server speaks plain HTTP. It has no TLS. By default it listens on every interface
(`*:<port>`); `--addr 127.0.0.1` makes it reachable only from the same host, for use behind a proxy
that terminates HTTPS (see [Hosting it for a team](#hosting-it-for-a-team)).

**Storage.** Files only, no database:

```
<store>/
  github.com~acme~app/
    refs.json                                     branch -> commit, and the latest upload
    68ba972628e85f3d795ba927a43260357fa9148d.json one bundle per uploaded commit
    eceb012ba59ceeee7018d50f0384278f609c3772.json
  github.com~acme~svc/
    ...
```

`refs.json` looks like this:

```json
{ "repo": "github.com/acme/app",
  "refs": { "feature": "eceb012b…", "main": "eceb012b…" },
  "latest": "eceb012b…" }
```

- Files are written atomically, so a crash mid-upload leaves the previous state.
- A restart keeps everything; the server reads the directory as it is.
- To back it up, copy the directory. To remove a repository, stop the server and delete its folder.
- Old commits are never deleted automatically (see [Limits](#limits)).

**Health check.** `GET /health` returns `{"service":"prism-link","status":"ok"}` and needs no token,
so a load balancer can probe it.

## Hosting it for a team

Any host that can run a Go binary works: a small VM, a container, or a Kubernetes pod with a
persistent volume. Requirements are small: the store holds one JSON file per uploaded commit, and
a bundle is roughly proportional to the number of cross-repository references.

The examples below are starting points. They show the shape of a deployment but were not run as
part of the tests listed at the end.

**systemd (Linux VM):**

```ini
# /etc/systemd/system/prism-link.service
[Unit]
Description=Prism link server
After=network-online.target

[Service]
User=prism-link
EnvironmentFile=/etc/prism-link.env          # PRISM_LINK_TOKEN=...
ExecStart=/usr/local/bin/prism link-server --dir /var/lib/prism-links --addr 127.0.0.1 --port 8890
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

**HTTPS in front (Caddy):**

```
links.example.internal {
    reverse_proxy 127.0.0.1:8890
}
```

With `--addr 127.0.0.1` the proxy is the only way in. On v1.3.5, which has no `--addr`, block the
port from outside with a firewall or security group instead.

**Container:** there is no published image. A two-line image built on the release binary is
enough: copy `prism` in, mount a volume at the store path, pass `PRISM_LINK_TOKEN` as a secret, and
run `prism link-server --dir /data --port 8890` (in a container, keep the default address so the
port mapping reaches it).

## Uploading from CI

Run the upload on merges to the default branch. Each consumer needs three things:

1. **An index built the way the repository builds.** Install dependencies first (`go mod download`,
   `npm ci`, `mvn -o` with a warm cache), so references are resolved by the compiler. Without
   them, `prism index` exits 3 and its references are name-based. Fix the build step, or pass
   `--allow-heuristic` to accept name-based results.
2. **A committed tree.** Push refuses a tree with uncommitted changes to tracked files, because its
   commit would not describe what was uploaded. Untracked files don't block it.
3. **A branch name.** Push records the branch from `git symbolic-ref`. A detached HEAD, which many
   CI checkouts use, records no branch: push warns `HEAD is detached, so no branch is recorded`,
   and the upload counts only as the latest one. A server that already has `main` for that
   repository keeps answering from `main`, and a commit already stored from `main` keeps its
   branch (v1.3.6 and later). Pass `--ref main` when your CI checks out a detached commit.

GitHub Actions example (needs a repository secret `PRISM_LINK_TOKEN` and a variable
`PRISM_LINK_SERVER`):

```yaml
on:
  push:
    branches: [main]
jobs:
  prism-links:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5        # or setup-node / setup-java: what the build needs
        with: { go-version-file: go.mod }
      - run: go mod download
      - run: curl -fsSL https://raw.githubusercontent.com/provasign/prism/v1.3.5/install.sh | VERSION=v1.3.5 bash
      - run: prism index .
      - run: prism workspace push --server "$PRISM_LINK_SERVER" --ref main
        env:
          PRISM_LINK_TOKEN: ${{ secrets.PRISM_LINK_TOKEN }}
          PRISM_LINK_SERVER: ${{ vars.PRISM_LINK_SERVER }}
```

`prism workspace push` options:

| Option | Effect |
|---|---|
| `--server <url>` | Where to upload. Else `workspace_server:` from `prism.yaml` |
| `--ref <branch>` | Record this branch instead of the checked-out one |
| `--repo <name>` | Store under this name instead of the one derived from `origin` |

Keep `--repo` consistent. A repository uploaded under two names is listed twice in answers.

Exit codes: 0 uploaded; 1 rejected (wrong or missing token, uncommitted changes, server
unreachable); 2 no server given.

## Asking the server

In the repository whose symbols you change (the library), add one line to `prism.yaml`:

```yaml
workspace_server: https://links.example.internal
```

Then set the token where Prism runs: in your shell for the CLI, or in the environment of your
agent or MCP server:

```sh
export PRISM_LINK_TOKEN=<secret>
```

The token is read only from the environment, never from a repository file, so a repository you
clone cannot carry one.

The server is asked by:

| Command | What the server adds |
|---|---|
| `change-impact` | Every uploaded site that uses the symbol, under "consumers in other repositories" |
| `verify` (after a signature change or removal) | The uploaded sites the change reaches. The verdict says they were **not checked** |
| `test-plan` | A gap line: sites in other repositories whose tests are not traced from here |
| `lookup` | For a name this repository does not define: where the server knows it is declared, with its signature. Bodies need a checkout or a cache clone |

`search`, `read` and `query` don't ask the server; they work on source, which the server doesn't
hold.

A repository you also link locally (`prism workspace add`) is answered from the checkout, not
repeated from the server.

## Which commit is answered

For each repository, the server answers from the commit of its `main` branch, else `master`, else
the most recent upload. An upload from a feature branch is stored but doesn't change the answer
until `main` moves to it.

Every bundle stays stored by commit; only `refs.json` moves. The HTTP API accepts a branch per
repository (`refs` in the request), but Prism's own commands always ask for the default.

## Reading an answer

The `workspaceScope` line always says what was covered:

| You see | Meaning |
|---|---|
| `server …: 2 site(s) in 2 of 2 uploaded repositories, each at its uploaded commit` | Asked, and this many uploaded repositories use the symbol |
| `server …: 0 site(s) in 0 of 2 uploaded repositories` | Asked; no uploaded repository uses it |
| `server … NOT REACHED (401 Unauthorized: missing or wrong bearer token)` | Token missing or wrong on your side |
| `server … NOT REACHED (… connection refused)` | Server down or address wrong |
| `link server not asked (this kind of symbol has no cross-repository records)` | The symbol kind is not recorded across repositories |
| `repositories neither linked nor uploaded are not covered` | Always present: the server knows only what was uploaded |

When the server is not reached, `safeToClaimComplete` is `false` and `completenessScope` says
linked repositories are not all resolved. An agent reading the answer cannot claim it found
everything.

With `--format json`, the server's sites are in `workspaceConsumers` and the coverage line is in
`workspaceScope`.

A repository line ending `requires github.com/acme/lib v0.1.0, built against a local checkout when
uploaded` means that consumer built the library from a local directory, not from the pinned
version.

## HTTP API

Every endpoint except `/health` needs `Authorization: Bearer <PRISM_LINK_TOKEN>` when the server
has a token. A missing or wrong token gets `401`. Requests and responses are JSON.

| Method and path | Request | Response |
|---|---|---|
| `GET /health` | none | `{"service":"prism-link","status":"ok"}` |
| `POST /link/bundle` | A bundle (up to 512 MB) | `{"stored":"<repo>@<sha>","refs":N,"defs":N}`; `400` for a bundle with no repo or sha, or one marked dirty |
| `POST /link/consumers` | `{"keys":[...], "excludePackages":["go:github.com/acme/app"], "refs":{"<repo>":"<branch>"}}` (up to 8 MB) | `{"repos":[{"repo","sha","ref","created","requires","sites":[...]}], "searched":N}` |
| `POST /link/define` | `{"key":"go:github.com/acme/lib#Normalize"}` or `{"name":"Normalize"}` | `{"repos":[{"repo","sha","ref","defs":[...]}]}`: the repositories that declare it |

An answer with no matches has `"repos": []` (v1.3.5 returned `null`).

`searched` in a consumers answer is the number of repositories examined, so "no uses" (`searched`
above 0, no repos) can be told apart from "nothing uploaded" (`searched` 0).
`excludePackages` skips repositories the asker already covers locally.

## Security model

- **One shared token** controls both reading and uploading. Anyone with it can read the reference
  lines of every uploaded repository and upload under any repository name.
- **No per-repository permissions.** If teams may not see each other's code, run one server per
  access group.
- **No TLS in the server.** Use HTTPS through a proxy (with `--addr 127.0.0.1`), or keep it on a
  private network.
- **No bodies stored**, but each reference keeps its one source line, and each declaration its
  signature. No path from the uploading machine is stored.
- **The client never takes a token from a repository.** Only `PRISM_LINK_TOKEN` in the environment
  is used.
- **A dirty tree is refused** on both client and server, so a stored commit always describes what
  it holds.
- **The health check is unauthenticated** and reveals only that the service is up.

## Limits

- Only uploaded repositories are covered, and every answer says so.
- Calls between services (HTTP, gRPC, queues), reflection and dependency injection are not found;
  the same limits apply to local links.
- No retention: every uploaded commit is kept. Prune old `<sha>.json` files by hand; never prune
  the commit `refs.json` points at.
- Only the default branch is answered by Prism's commands.
- One process, no clustering. Parsed bundles are cached in memory and re-read when a file
  changes; run one instance per store directory.
- Re-uploading the same commit replaces its bundle. The content is the same; an upload with no
  branch keeps the branch already recorded for that commit.

## Troubleshooting

| Message | Cause and fix |
|---|---|
| `workspace push: 401 Unauthorized: missing or wrong bearer token` | `PRISM_LINK_TOKEN` is not set or differs from the server's |
| `workspace push: the tree has uncommitted changes, so its commit does not describe it; commit first` | A tracked file is modified. Commit, or push from a clean CI checkout |
| `workspace push: no server; pass --server <url> or set workspace_server in prism.yaml` | Give the server URL (exit 2) |
| `HEAD is detached, so no branch is recorded` (and `pushed …@<sha> (no branch)`) | Add `--ref main` to the push in CI |
| `server … NOT REACHED (401 …)` in an answer | Set `PRISM_LINK_TOKEN` where Prism runs (shell, agent, MCP server) |
| `server … NOT REACHED (… connection refused)` | Server down, wrong port, or a firewall |
| A consumer is missing from the answer | It was never uploaded, it uploaded only from a branch other than `main`, or its index had no compiler-resolved references (check `prism index` exit code in its CI) |
| A consumer appears twice | Uploaded under two `--repo` names. Delete one folder from the store |
| `lookup` shows a signature but no body | Expected: the server holds signatures only. Link a checkout or a cache clone for the body |

## What was tested

On 2026-11-27, with the released v1.3.5 binary on macOS, using a Go library and three Go consumers
as separate git repositories:

- **Server:** start-up with and without a token (warning printed), `401` for a missing or wrong
  token, the open health check, the bind address (`*:<port>`), and answers surviving a restart.
- **Uploads:**
  - Refused without a token (exit 1), from a dirty tree (exit 1), and with no server (exit 2).
  - A good upload stored one bundle per commit plus `refs.json`.
  - `export -o` wrote the same bundle to a file.
  - `--repo` and `--ref` overrides, and a push from a detached HEAD.
- **Queries:**
  - `change-impact` listed both uploaded consumers with their commit, required version and upload
    date.
  - A symbol nobody uses gave 0 of 2.
  - No token, a wrong token and a stopped server each gave `NOT REACHED` and
    `safeToClaimComplete: false`.
  - The JSON fields are as described above.
- **Branches:** a feature-branch upload did not change the answer, and the answer followed `main`
  once `main` moved.
- **Local links:** a consumer linked locally was answered from the checkout and not repeated from
  the server.
- **Other commands:** `verify` after a signature change listed the uploaded sites as not checked;
  `test-plan` reported them as a gap; `lookup` from a repository that does not define the symbol
  returned the server's declaration and signature.

The v1.3.6 fixes were then checked the same way:

- An exported bundle has `local: true` and no path or manifest line.
- `--addr 127.0.0.1` listens only on loopback: the health check answers on `127.0.0.1` and is
  refused on the machine's LAN address. Without `--addr` the server still listens on every
  interface.
- A detached re-upload of a commit stored from `main` warns and is still answered as `main`.
- Empty answers return `"repos": []`.

Not tested here: languages other than Go through the server (Java, TypeScript and Python record
cross-repository references the same way), a multi-machine network, HTTPS through a proxy, and
the hosting examples above.

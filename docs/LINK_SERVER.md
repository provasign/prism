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

This guide describes v1.4.0. Differences from v1.3.x are marked; see also
[Upgrading from v1.3](#upgrading-from-v13).

## Contents

- [How it works](#how-it-works)
- [Quick start on one machine](#quick-start-on-one-machine)
- [Who runs it](#who-runs-it)
- [What a repository uploads](#what-a-repository-uploads)
- [Credentials and access](#credentials-and-access)
- [Running the server](#running-the-server)
- [Storage and replicas](#storage-and-replicas)
- [TLS and mTLS](#tls-and-mtls)
- [Audit log](#audit-log)
- [Metrics and health](#metrics-and-health)
- [Limits and timeouts](#limits-and-timeouts)
- [Retention, deletion, integrity, backup](#retention-deletion-integrity-backup)
- [Deploying](#deploying)
- [Uploading from CI](#uploading-from-ci)
- [Asking the server](#asking-the-server)
- [Which commit is answered](#which-commit-is-answered)
- [Reading an answer](#reading-an-answer)
- [HTTP API](#http-api)
- [Sizing and performance](#sizing-and-performance)
- [Security model](#security-model)
- [Limits](#limits)
- [Upgrading from v1.3](#upgrading-from-v13)
- [Troubleshooting](#troubleshooting)
- [What was tested](#what-was-tested)

## How it works

```
 consumer repositories (CI, on each merge to main)          shared host(s)
 ┌──────────────┐  prism index .                           ┌──────────────────────────────┐
 │ app          │  prism workspace push ── POST /link/bundle ──▶ prism link-server        │
 │ svc          │  (one bundle per commit,                 │   token file: who may do what │
 └──────────────┘   a token scoped to the repository)      │   store: a directory, or an   │
                                                           │   S3 bucket shared by replicas│
 your machine, in the library                              └──────────────▲───────────────┘
 ┌──────────────────────────────────────────┐                             │
 │ prism.yaml:  workspace_server: https://… │  POST /link/consumers ──────┘
 │ prism change-impact Normalize            │  POST /link/define
 │   → local callers + every readable site  │  (a token scoped to what you may read)
 └──────────────────────────────────────────┘
```

- **Nothing is merged.** Each repository keeps its own index, and the server stores what each one
  uploaded. Prism asks at query time.
- **Symbols are matched by key**, such as `go:github.com/acme/lib#Normalize`: the ecosystem, the
  package, and the name. A consumer's reference is recorded under the key of the symbol it
  resolves to, so the match is exact, not a text search.
- **Each answer is at a commit.** Every site names the consumer's commit and upload date.
- **Every answer says what it did not cover:** repositories never uploaded, ones the token may not
  read, and a server that could not be reached.

## Quick start on one machine

```sh
# 1. Credentials: one token per CI pipeline, one for readers
prism link-server token create --tokens tokens.json --name ci-app --upload github.com/acme/app > ci-app.token
prism link-server token create --tokens tokens.json --name readers --read 'github.com/acme/*' > readers.token

# 2. Start a server
prism link-server --dir ./link-store --tokens tokens.json --addr 127.0.0.1 --port 8890

# 3. In each consumer, index and upload (normally a CI step)
cd app && prism index . && PRISM_LINK_TOKEN=$(cat ../ci-app.token) prism workspace push --server http://127.0.0.1:8890
# pushed github.com/acme/app@68ba9726 (main) to http://127.0.0.1:8890: 1 reference(s), 1 declaration(s)

# 4. In the library, point Prism at the server and ask
cd lib && echo 'workspace_server: http://127.0.0.1:8890' >> prism.yaml
PRISM_LINK_TOKEN=$(cat ../readers.token) prism change-impact Normalize --format text
```

The answer lists the library's own sites, then:

```
consumers in other repositories:
// repo github.com/acme/app@68ba9726 (main)  from the server, not checked out  requires github.com/acme/lib v0.1.0  uploaded 2026-11-27
  github.com/acme/app:main.go:5:27: func main() { println(lib.Normalize("A")) }  [in main]
workspaceScope: server http://127.0.0.1:8890: 1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit; repositories neither linked nor uploaded are not covered
```

A consumer that builds the library from a local directory (a Go `replace => ../lib`) shows
`requires github.com/acme/lib v0.1.0, built against a local checkout when uploaded`.

## Who runs it

You do. Provasign does not host a link server. It is self-hosted inside your organization, like an
internal package registry. It is the same `prism` binary, started as `prism link-server`, and is
also published as a container image with a Helm chart.

| Who | What they do |
|---|---|
| Platform or developer-tools team | Runs the server, owns the token file, the storage and its backups, reads the audit log |
| Each consuming repository | One CI step on merges to `main`, with a token that may upload only that repository |
| Library owners and their agents | Add `workspace_server:` to `prism.yaml` and use a read token for the repositories they may see |

## What a repository uploads

`prism workspace push` builds a bundle from the repository's existing index and sends it.
`prism workspace export -o bundle.json` writes the same bundle to a file, so you can inspect it
before uploading anything.

```json
{
  "format": 1,
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
| `format` | The bundle format version (1). v1.3.x bundles have none and are read as 0 |
| `repo` | Stable name: `host/org/repo` from the `origin` remote, else the directory name. `--repo` overrides it |
| `sha`, `ref` | The commit, and the branch it was exported from (`--ref` overrides it) |
| `packages`, `requires` | What the repository's build manifests declare and require: ecosystem, name, pinned version and manifest file. `local: true` means the dependency was built from a local directory, so the pinned version is not what it was built against. The directory itself is not uploaded |
| `refs` | Each use of another repository's symbol: key, file, enclosing symbol, line, column, **the one source line**, and whether it is in a test file |
| `defs` | Each symbol the repository declares, with its signature, file and line |

**Not uploaded:** function bodies, whole files, history, or any path from the uploading machine.
The only source text is the one line per reference and each declaration's signature. That is
still source code: treat the store with the same care as the repositories it describes, and give
read tokens only to people who may read those repositories.

Keys by language:

| Language | Key | Pin shown from |
|---|---|---|
| Go | `go:<module path>/<package>#Name` | `go.mod` require |
| Java | `jvm:<java package>#Type.member` | `pom.xml` / Gradle dependency (`maven:group:artifact`) |
| TypeScript / JavaScript | `npm:<package>#Name` | `package.json` dependency |
| Python | `py:<distribution>#name` | `pyproject.toml` dependency (`pypi:`); sites are labelled `[import-scoped]` |

## Credentials and access

v1.4 adds per-token credentials. Each token has a name, the repositories it may **upload**, the
repositories it may **read**, an optional **admin** right, and an optional **expiry**. Repository
patterns are globs on the repository name (`github.com/acme/app`, `github.com/acme/*`, `*`).

```sh
prism link-server token create --tokens tokens.json --name ci-app --upload github.com/acme/app --expires 365d
prism link-server token create --tokens tokens.json --name team-payments --read 'github.com/acme/pay-*' --read 'github.com/acme/ledger'
prism link-server token create --tokens tokens.json --name ops --admin --read '*'
prism link-server token list   --tokens tokens.json
prism link-server token revoke --tokens tokens.json <id>
```

- `token create` prints the secret once, on stdout (`plk_<id>_<random>`). The file stores only
  its SHA-256 hash, with mode 0600. Losing a secret means revoking it and creating another.
- A running server re-reads the file when it changes. A new or revoked token takes effect within
  a second, with no restart. A file that fails to parse mid-edit is ignored, and the last good one
  is kept.
- An expired token is refused like an unknown one. Expiry is stored in whole seconds, rounded up.

What each right allows:

| Right | Allows |
|---|---|
| `upload` | `POST /link/bundle` for a matching repository name. Anything else gets 403 |
| `read` | `consumers`, `define` and `repos` answer only from matching repositories. The answer counts the rest as `unreadable` |
| `admin` | `POST /link/delete` and `POST /link/prune` |

Recommended layout: one upload token per CI pipeline, scoped to its own repository; one read
token per team or access group; one admin token held by the operators. Readers who should see
everything get `--read '*'`.

**Shared token (v1.3 model).** Without `--tokens`, a server with `PRISM_LINK_TOKEN` set accepts that
one secret with every right and warns at start. With `--tokens` and `PRISM_LINK_TOKEN` both set, both
are accepted, which allows a gradual move to per-token credentials. Unset the shared one when the
move is done.

**No credentials.** A server with neither refuses to start unless you pass `--no-auth`. (v1.3
started open with a warning.)

Clients send their token as `Authorization: Bearer`. Prism reads it only from `PRISM_LINK_TOKEN` in
the environment, never from a repository file.

## Running the server

```sh
prism link-server <storage> [options]
```

Storage is one of `--dir`, `--s3`, `--azure` or `--gcs`; see [Storage and replicas](#storage-and-replicas).

| Option | Default | Meaning |
|---|---|---|
| `--dir <store>` | | Filesystem storage. One host only |
| `--s3 s3://bucket[/prefix]` | | AWS S3 or any S3-compatible store; several replicas can share it |
| `--azure https://<account>.blob.core.windows.net/<container>[/prefix]` | | Azure Blob Storage; several replicas can share it |
| `--gcs gs://bucket[/prefix]` | | Google Cloud Storage; several replicas can share it |
| `--tokens <file>` | | Per-token credentials. Else `PRISM_LINK_TOKEN`; else `--no-auth` |
| `--no-auth` | off | Serve without credentials |
| `--addr <host>` | every interface | Listen address, such as `127.0.0.1` behind a proxy |
| `--port N` | 8890 | Listen port |
| `--tls-cert F --tls-key F` | | HTTPS. Re-read when the files change |
| `--client-ca F` | | Require client certificates signed by this CA (mTLS) |
| `--ops-addr host:port` | off | Plain-HTTP `/health` and `/metrics`, for probes and scraping |
| `--audit-log <file\|-\|off>` | `-` (stdout) | One JSON line per request |
| `--rate N` | 50 | Requests per second per token (burst 2N); 0 turns it off |
| `--max-conns N` | 512 | Concurrent connections |
| `--max-bundle-mb N` | 512 | Upload size limit |
| `--keep N` | 20 | Commits kept per repository; 0 keeps all |
| `--refresh D` | 10s | How often to re-read the store for other replicas' uploads |
| `--trust-proxy` | off | Take the client address from `X-Forwarded-For` (only behind your proxy) |
| `--single-replica` | off | Run on a shared store that ignores conditional writes (refused otherwise); never with a second replica |

Server events go to stderr as JSON lines. The audit log is separate (see [Audit log](#audit-log)).
SIGINT or SIGTERM shuts down gracefully, letting requests in flight finish (up to 30 s).

## Storage and replicas

### Filesystem (`--dir`)

```
<store>/
  github.com~acme~app/
    refs.json                                     branch -> commit, latest, upload history
    68ba972628e85f3d795ba927a43260357fa9148d.json one bundle per uploaded commit
  github.com~acme~svc/
    ...
```

- Writes are atomic (a temporary file, fsync, rename), and `refs.json` changes under a lock file.
- One host only. Two processes on one machine are safe; machines sharing a network file system
  are not. Use S3 for replicas.

Which to use:

| Where you run | Storage | Replicas |
|---|---|---|
| One VM or server, anywhere | `--dir` on a local disk | 1 |
| AWS | `--s3` (S3) | any |
| Azure, Azure Stack Hub | `--azure` (Blob Storage) | any |
| Google Cloud | `--gcs` (Cloud Storage) | any |
| A private data centre | `--s3` against its S3-compatible object store (MinIO, Ceph RGW, NetApp StorageGRID, Dell ECS, Pure FlashBlade, Cloudian, VersityGW, SeaweedFS, ...), if it supports conditional writes; otherwise `--dir` | any (`--dir`: 1) |
| Other clouds with an S3 API (Wasabi, Backblaze B2, Cloudflare R2, OCI, IBM COS, DigitalOcean Spaces) | `--s3` with `--s3-endpoint`, if the provider supports conditional writes | any |

Every shared backend must support conditional writes (compare-and-swap on an object's ETag or
generation), because that is how replicas update a repository's branches without losing each
other's uploads. Azure Blob Storage and Google Cloud Storage always do.

**The server checks this at start.** On `--s3`, `--azure` and `--gcs` it writes a probe object
and confirms that a create-only write over it, and a write with a stale ETag, are both refused.
A store that accepts and ignores the conditions is refused:

```
link-server: s3://prism-links/link at …: the store ignores conditional writes (If-Match / If-None-Match: *),
so replicas sharing it would lose each other's branch updates (a write with a stale ETag was accepted)
  Use a store that supports conditional writes, or run exactly one replica with --single-replica.
```

`--single-replica` starts on such a store with a warning. One process serialises its own branch
updates, so it is safe, as long as no second replica ever shares the store. LocalStack 3.8 (the
last community image) is such a store; the test suite found it. For an S3 API, check the
product's documentation for `If-Match` and `If-None-Match: *` on PUT. Or test it: the backend
contract test (`PRISM_TEST_S3=s3://bucket PRISM_TEST_S3_ENDPOINT=https://… go test -run
S3BlobAgainstARealServer ./internal/linkserver/`) checks conditional writes and two replicas with
concurrent uploads against any endpoint. VersityGW and SeaweedFS passed it here; AWS S3 and MinIO
document conditional writes; the others were not tested.

All backends store the same objects under the same keys, so `backup` and `restore` move a store
between any two of them.

### S3 and S3-compatible (`--s3`)

```sh
AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
prism link-server --s3 s3://prism-links/prod --s3-region us-east-1 --tokens tokens.json ...
```

| Option | Meaning |
|---|---|
| `--s3 s3://bucket[/prefix]` | Bucket, and an optional prefix inside it |
| `--s3-endpoint URL` | Default `https://s3.<region>.amazonaws.com`. Set it for MinIO, Ceph, SeaweedFS, VersityGW, or GCS interoperability |
| `--s3-region R` | Default `$AWS_REGION`, then `us-east-1` |
| `--s3-path-style` | Bucket in the path, not the host name (most self-hosted servers) |

- **Credentials** come from the environment. Use either `AWS_ACCESS_KEY_ID` /
  `AWS_SECRET_ACCESS_KEY` (plus `AWS_SESSION_TOKEN`), or web identity (`AWS_ROLE_ARN` and
  `AWS_WEB_IDENTITY_TOKEN_FILE`, as EKS IAM roles for service accounts set them). Instance-profile
  credentials (EC2 IMDS, ECS task roles) are not read; use IRSA or static keys.
- **Permissions needed:** `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on `bucket/prefix/*`,
  and `s3:ListBucket` on the bucket.
- **Conditional writes are required.** `refs.json` is updated with `If-Match` /
  `If-None-Match: *`, so the store must support conditional PUTs. AWS S3 does, as do current
  MinIO, VersityGW and SeaweedFS. A store without them would let concurrent uploads overwrite
  each other's branches.
- **Requests are signed with Signature Version 4** in Prism itself, with no AWS SDK dependency.

### Azure Blob Storage (`--azure`)

```sh
prism link-server --azure https://acmelinks.blob.core.windows.net/prism-links/prod --tokens tokens.json ...
```

| Option | Meaning |
|---|---|
| `--azure <container URL>[/prefix]` | `https://<account>.blob.core.windows.net/<container>[/prefix]`; Azure Stack Hub and sovereign clouds use their own host names |
| `--azure-path-style` | The account is the first path segment (`http://host:10000/<account>/<container>`), as the Azurite emulator and some private endpoints use |

Credentials, from the environment; the first that is set wins:

| Credential | Variables | Typical use |
|---|---|---|
| Account key (Shared Key) | `AZURE_STORAGE_KEY` (and `AZURE_STORAGE_ACCOUNT` if the host name does not give it) | Simple setups, Azurite |
| SAS token | `AZURE_STORAGE_SAS_TOKEN`, scoped to the container with read, write, delete and list | Least-privilege keys without Entra ID |
| AKS workload identity | `AZURE_FEDERATED_TOKEN_FILE`, `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` (injected by the webhook) | Kubernetes on Azure |
| Service principal | `AZURE_CLIENT_SECRET`, `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` | CI, VMs outside Azure |
| Managed identity | `AZURE_USE_MANAGED_IDENTITY=1` (and `AZURE_CLIENT_ID` for a user-assigned identity) | VMs, App Service, Container Apps |

- **Role needed** with Entra ID: *Storage Blob Data Contributor* on the container.
- **Sovereign clouds:** `AZURE_AUTHORITY_HOST` selects the Entra ID authority, such as
  `https://login.microsoftonline.us`.
- **Conditional writes:** `If-Match` and `If-None-Match: *` on `Put Blob`.
- **No SDK:** the requests are signed or carry the bearer token in Prism itself.

### Google Cloud Storage (`--gcs`)

```sh
prism link-server --gcs gs://acme-prism-links/prod --tokens tokens.json ...
```

| Option | Meaning |
|---|---|
| `--gcs gs://bucket[/prefix]` | The bucket and an optional prefix |
| `--gcs-endpoint URL` | Default `https://storage.googleapis.com`; set it for a private endpoint |

Credentials:

| Credential | How |
|---|---|
| GKE workload identity, GCE, Cloud Run | Nothing to set: the metadata server provides the attached service account's token |
| Service-account key | `GOOGLE_APPLICATION_CREDENTIALS` pointing at the key file |
| Emulator | `STORAGE_EMULATOR_HOST=host:port` (no credentials) |

- **Role needed:** *Storage Object User*, or `storage.objects.{get,create,delete,list}`, on the
  bucket.
- **Conditional writes:** `ifGenerationMatch`. The object generation serves as the ETag.
- **Not supported:** workload identity federation key files (`"type": "external_account"`). Use
  the metadata server or a service-account key.
- **No SDK:** the JSON API is called directly.

### Replicas

Any number of server processes can share one S3 store behind a load balancer:

- An upload goes to one replica, which writes the bundle and moves the repository's refs by
  compare-and-swap. Concurrent uploads through different replicas lose no branch: retried writes
  re-read the refs.
- Each replica answers queries from memory. It re-reads every repository's refs every `--refresh`
  (unchanged ones cost a conditional request), then loads the bundles that changed.
- **Consistency:** a replica's own uploads are visible at once. Other replicas see them within
  `--refresh` plus the time to load the bundle. A client that uploads and immediately asks a
  different replica may not see its upload for that long.
- The token file must be the same on every replica (a shared Secret in Kubernetes).
- Replicas work the same on S3, Azure Blob Storage and Google Cloud Storage; only `--dir` is
  limited to one.

## TLS and mTLS

```sh
prism link-server ... --tls-cert server.pem --tls-key server-key.pem            # HTTPS
prism link-server ... --tls-cert server.pem --tls-key server-key.pem --client-ca clients-ca.pem   # mTLS
```

- TLS 1.2 or later. The certificate and key are re-read when the files change, so a rotation
  needs no restart. A half-written pair keeps the previous one.
- With `--client-ca`, every connection must present a client certificate signed by that CA, on
  top of the bearer token. A client without one fails at the handshake.
- Under mTLS, probes cannot reach `/health` on the public port without a certificate. Serve them
  from `--ops-addr`.

Client settings, all from the environment:

| Variable | Meaning |
|---|---|
| `PRISM_LINK_TOKEN` | Bearer token |
| `PRISM_LINK_CA` | PEM file of extra trusted roots, added to the system's (a private CA) |
| `PRISM_LINK_CLIENT_CERT`, `PRISM_LINK_CLIENT_KEY` | Client certificate and key for mTLS |

Alternatively, terminate TLS at your proxy and run the server with `--addr 127.0.0.1` (or on a
private network) and `--trust-proxy`.

## Audit log

One JSON line per API request, to stdout by default or to `--audit-log <file>`:

```json
{"time":"2026-11-27T18:05:12.48Z","request":"5f0c2d9a1b7e4c11","endpoint":"upload","method":"POST","path":"/link/bundle",
 "status":200,"client":"10.0.4.17","tokenId":"2396d72bfa9e","tokenName":"ci-app","repo":"github.com/acme/app",
 "sha":"68ba972628e85f3d795ba927a43260357fa9148d","bytesIn":2417,"bytesOut":86,"ms":3.21}
```

| Field | Meaning |
|---|---|
| `request` | Request id, also returned as the `X-Request-Id` header (a client's own id is kept if it sends one) |
| `endpoint`, `method`, `path`, `status` | What was asked and the HTTP status |
| `client` | Peer address, or the first `X-Forwarded-For` hop with `--trust-proxy` |
| `tokenId`, `tokenName` | The credential used. Absent on authentication failures. **The secret is never logged** |
| `repo`, `sha` | Uploads and deletions |
| `repos`, `unreadable` | Reads: repositories answered, and those the token could not read |
| `bytesIn`, `bytesOut`, `ms` | Sizes and latency |

To rotate a file log, move it and send SIGHUP: the server reopens the path (logrotate
`postrotate kill -HUP <pid>`). In containers, keep the default stdout and let the platform collect
it.

`/health` and `/metrics` are not audited.

## Metrics and health

`/metrics` (Prometheus text format) is served on `--ops-addr` only. It is unauthenticated, so bind
it to a private address.

| Series | Type | Meaning |
|---|---|---|
| `prism_link_requests_total{endpoint,code}` | counter | Requests by endpoint and HTTP status |
| `prism_link_request_seconds{endpoint}` | histogram | Latency (5 ms to 10 s buckets) |
| `prism_link_uploads_total{result}` | counter | Uploads: `ok`, `rejected` (4xx), `error` (5xx) |
| `prism_link_auth_failures_total` | counter | Missing, unknown, revoked or expired tokens |
| `prism_link_rate_limited_total` | counter | Requests refused with 429 |
| `prism_link_repositories` | gauge | Repositories in the catalog |
| `prism_link_cached_bundles`, `prism_link_cached_bytes` | gauge | Bundles held in memory, and their stored size |
| `prism_link_heap_inuse_bytes` | gauge | Go heap in use |
| `prism_link_uptime_seconds` | gauge | Seconds since start |

`GET /health` returns `{"service":"prism-link","status":"ok","formats":[0,1]}` on both the public
port (no token needed) and the ops port. `formats` lists the bundle formats the server accepts.

Suggested alerts:
- `rate(prism_link_uploads_total{result="error"}[15m]) > 0`: storage failing.
- A rise in `prism_link_auth_failures_total`: a leaked or expired CI token, or guessing.
- `prism_link_heap_inuse_bytes` near the memory limit.

## Limits and timeouts

| Limit | Value |
|---|---|
| Read header | 10 s |
| Read whole request | 5 min (large uploads) |
| Write response | 2 min |
| Idle keep-alive | 2 min |
| Header size | 1 MiB |
| Concurrent connections | `--max-conns` (512); further connections wait to be accepted |
| Rate | `--rate` per token (50/s, burst 100). Requests with a bad token share a bucket per client address, so guessing is limited too. Over the limit: 429 with `Retry-After: 1` |
| Upload size | `--max-bundle-mb` (512); larger gets 413 |
| Query bodies | 8 MiB (`consumers`), 1 MiB (`define`, admin) |

## Retention, deletion, integrity, backup

**Retention.** After each upload, the server keeps the newest `--keep` commits (20) of that
repository and deletes older ones. Commits a branch points at, and the latest upload, are never
deleted. To run it by hand or across every repository:

```sh
prism link-server prune --dir <store> [--repo github.com/acme/app] --keep 10
curl -X POST https://links/link/prune -H "Authorization: Bearer $ADMIN" -d '{"keep":10}'
```

**Deletion**, for example after a secret was committed and uploaded:

```sh
prism link-server delete --dir <store> --repo github.com/acme/app --sha <commit>   # one commit
prism link-server delete --dir <store> --repo github.com/acme/app                  # the repository
curl -X POST https://links/link/delete -H "Authorization: Bearer $ADMIN" -d '{"repo":"github.com/acme/app","sha":"<commit>"}'
```

Deleting the commit a branch points at removes that branch, so answers fall back to the
repository's other branches or its latest remaining upload.

**Integrity.**

```sh
prism link-server check --dir <store> [--json]
```

It reads every object. It reports unreadable refs or bundles, bundles stored under the wrong
repository, and branches or `latest` pointing at missing commits. It exits 1 when it finds any.

**Backup and restore** work with any storage, so they also move a store between the filesystem
and S3:

```sh
prism link-server backup  --s3 s3://prism-links/prod --to /backups/links-2026-11-27
prism link-server restore --dir /var/lib/prism-links --from /backups/links-2026-11-27
prism link-server check   --dir /var/lib/prism-links
```

Bundles are copied before refs, so an interrupted copy never has refs pointing at commits it
lacks. On S3 you can rely on bucket versioning and replication instead. A store is cheap to
rebuild, too: every consumer's next CI run uploads again.

## Deploying

The repository ships three ways to run it under `deploy/`:

| Path | What it is |
|---|---|
| `deploy/docker/Dockerfile` | Static binary on a distroless, non-root image. Published as `ghcr.io/provasign/prism:<version>` (linux/amd64, linux/arm64) |
| `deploy/helm/prism-link-server/` | Helm chart: Deployment, Service, token-file and TLS Secrets, storage on a PVC, S3, Azure or GCS, cloud identity through service-account annotations (IRSA, AKS workload identity, GKE workload identity), probes on the ops port, a PodDisruptionBudget, an optional Ingress and ServiceMonitor, and `GOMEMLIMIT` from the memory limit. More than one replica requires S3, Azure or GCS |
| `deploy/compose/` | Docker Compose: an S3 store, two replicas and a TLS proxy, with `run-e2e.sh`, which checks the whole stack |

`values.yaml` documents every setting, and `ci/*-values.yaml` has a worked example per storage
mode. Production installs:

```sh
kubectl create secret generic prism-link-tokens --from-file=tokens.json

# AWS, IRSA
helm install links deploy/helm/prism-link-server --set replicaCount=3 \
  --set storage.mode=s3 --set storage.s3.bucket=prism-links --set storage.s3.prefix=prod \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/prism-links \
  --set auth.tokens.existingSecret=prism-link-tokens

# Azure, AKS workload identity
helm install links deploy/helm/prism-link-server --set replicaCount=3 \
  --set storage.mode=azure --set storage.azure.url=https://acmelinks.blob.core.windows.net/prism-links/prod \
  --set serviceAccount.annotations."azure\.workload\.identity/client-id"=<client-id> \
  --set podLabels."azure\.workload\.identity/use"=true \
  --set auth.tokens.existingSecret=prism-link-tokens

# Google Cloud, GKE workload identity
helm install links deploy/helm/prism-link-server --set replicaCount=3 \
  --set storage.mode=gcs --set storage.gcs.bucket=acme-prism-links --set storage.gcs.prefix=prod \
  --set serviceAccount.annotations."iam\.gke\.io/gcp-service-account"=prism-links@acme.iam.gserviceaccount.com \
  --set auth.tokens.existingSecret=prism-link-tokens

# Private data centre, MinIO or another S3 API, static keys from a Secret
helm install links deploy/helm/prism-link-server --set replicaCount=3 \
  --set storage.mode=s3 --set storage.s3.bucket=prism-links --set storage.s3.endpoint=https://minio.storage:9000 \
  --set storage.s3.pathStyle=true --set storage.s3.existingSecret=prism-link-s3 \
  --set auth.tokens.existingSecret=prism-link-tokens
```

Token changes reach running pods without a restart. The Secret is mounted as a directory, so the
kubelet's update replaces the file and the server re-reads it within a second.

Without Kubernetes, a systemd unit works the same way:

```ini
[Service]
User=prism-link
EnvironmentFile=/etc/prism-link.env          # AWS_* for S3, if used
ExecStart=/usr/local/bin/prism link-server --dir /var/lib/prism-links --tokens /etc/prism-link/tokens.json \
  --tls-cert /etc/prism-link/tls.pem --tls-key /etc/prism-link/tls-key.pem --ops-addr 127.0.0.1:9890 \
  --audit-log /var/log/prism-link/audit.log
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
```

## Uploading from CI

Run the upload on merges to the default branch. Each consumer needs:

1. **An index built the way the repository builds.** Install dependencies first (`go mod download`,
   `npm ci`, a warm Maven cache), so references are resolved by the compiler. Without them,
   `prism index` exits 3 and its references are name-based. Fix the build step, or pass
   `--allow-heuristic` to accept name-based results.
2. **A committed tree.** Push refuses uncommitted changes to tracked files, because the commit
   would not describe what was uploaded. Untracked files don't block it.
3. **A branch name.** A detached HEAD, which most CI checkouts use, records no branch. Push warns
   `HEAD is detached, so no branch is recorded`, and the upload counts only as the latest one. A
   commit already stored from `main` keeps its branch. Pass `--ref main`.
4. **Its own upload token**, scoped to the repository, as a CI secret.

GitHub Actions:

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
      - run: curl -fsSL https://raw.githubusercontent.com/provasign/prism/v1.4.0/install.sh | VERSION=v1.4.0 bash
      - run: prism index .
      - run: prism workspace push --server "$PRISM_LINK_SERVER" --ref main
        env:
          PRISM_LINK_TOKEN: ${{ secrets.PRISM_LINK_UPLOAD_TOKEN }}
          PRISM_LINK_SERVER: ${{ vars.PRISM_LINK_SERVER }}
          PRISM_LINK_CA: ${{ runner.temp }}/corp-ca.pem       # only with a private CA
```

`prism workspace push` options:

| Option | Effect |
|---|---|
| `--server <url>` | Where to upload. Else `workspace_server:` from `prism.yaml` |
| `--ref <branch>` | Record this branch instead of the checked-out one |
| `--repo <name>` | Store under this name instead of the one derived from `origin` (the token must allow it) |

Keep `--repo` consistent: a repository uploaded under two names is listed twice. Exit codes: 0
uploaded; 1 rejected (401, 403, uncommitted changes, unreachable, a format the server does not
accept); 2 no server given.

## Asking the server

In the repository whose symbols you change (the library), add one line to `prism.yaml`:

```yaml
workspace_server: https://links.example.internal
```

Then set `PRISM_LINK_TOKEN` (a read token) where Prism runs: your shell for the CLI, or the
environment of your agent or MCP server. Add `PRISM_LINK_CA` and the client-certificate variables
when your server needs them.

| Command | What the server adds |
|---|---|
| `change-impact` | Every readable uploaded site that uses the symbol, under "consumers in other repositories" |
| `verify` (after a signature change or removal) | The uploaded sites the change reaches. The verdict says they were **not checked** |
| `test-plan` | A gap line: sites in other repositories whose tests are not traced from here |
| `lookup` | For a name this repository does not define: where the server knows it is declared, with its signature. Bodies need a checkout or a cache clone |

`search`, `read` and `query` don't ask the server; they work on source, which the server doesn't
hold. A repository you also link locally is answered from the checkout, not repeated from the
server.

## Which commit is answered

For each repository, the server answers from the commit of its `main` branch, else `master`, else
the most recent upload. An upload from a feature branch is stored, but doesn't change the answer
until `main` moves to it. The HTTP API accepts a branch per repository (`refs` in the request),
but Prism's own commands always ask for the default.

## Reading an answer

The `workspaceScope` line always says what was covered:

| You see | Meaning |
|---|---|
| `server …: 2 site(s) in 2 of 5 uploaded repositories, each at its uploaded commit` | Asked; 2 of the 5 readable uploaded repositories use the symbol |
| `…; 3 uploaded repositories NOT READABLE with this token, not searched` | The token's read scope excludes 3 repositories. The answer is incomplete |
| `server … NOT REACHED (401 Unauthorized: …)` | Token missing, unknown, revoked or expired |
| `server … NOT REACHED (429 Too Many Requests: …)` | Rate limited; retry |
| `server … NOT REACHED (… connection refused)` / `(… certificate …)` | Server down, wrong address, or a TLS/mTLS setting |
| `link server not asked (this kind of symbol has no cross-repository records)` | The symbol kind is not recorded across repositories |
| `repositories neither linked nor uploaded are not covered` | Always present: the server knows only what was uploaded |

When the server is not reached, or some repositories are not readable, `safeToClaimComplete` is
`false`, so an agent cannot claim it found everything. With `--format json`, the server's sites
are in `workspaceConsumers` and the coverage line in `workspaceScope`.

Site tags: `[test]` for a test file; `[import-scoped]` for a Python site, which is matched by import
and name rather than resolved by a compiler.

## HTTP API

Every endpoint except `/health` needs `Authorization: Bearer <token>`. Errors are
`{"error": "..."}` with the HTTP status. Every response carries `X-Request-Id`.

| Method and path | Right | Request | Response |
|---|---|---|---|
| `GET /health` | none | | `{"service","status","formats"}` |
| `POST /link/bundle` | upload | A bundle (up to `--max-bundle-mb`) | `{"stored":"<repo>@<sha>","refs":N,"defs":N}`; 400 bad bundle or newer format, 403 not allowed, 413 too large |
| `POST /link/consumers` | read | `{"keys":[...], "excludePackages":[...], "packages":[...], "refs":{"<repo>":"<branch>"}}` | `{"repos":[{"repo","sha","ref","created","requires","sites":[...]}], "searched":N, "unreadable":N}` |
| `POST /link/define` | read | `{"key":"go:…#Name"}` or `{"name":"Name"}` | `{"repos":[{"repo","sha","ref","defs":[...]}], "unreadable":N}` |
| `GET /link/repos` | read | | `{"repos":[{"repo","refs","latest","commits"}]}`, readable ones only |
| `POST /link/delete` | admin | `{"repo":"…"}` or `{"repo":"…","sha":"…"}` | `{"deleted":…}` |
| `POST /link/prune` | admin | `{"keep":N}` or `{"repo":"…","keep":N}` | `{"deleted":{"<repo>":["<sha>",…]}}` |
| `GET /metrics` (ops port) | none | | Prometheus text |

Request fields for `consumers`:
- `excludePackages` skips repositories the asker covers locally.
- `packages` are the asker's own packages. They give the `requires` pin even where the key and
  the manifest name the package differently (Java, Python).
- `searched` counts the readable repositories examined, so "no uses" (searched > 0, no repos) and
  "nothing uploaded" (searched 0) can be told apart.

## Sizing and performance

Measured on 2026-11-27 on an Apple M-series laptop with `internal/linkserver/loadtest`:
- 1,000 synthetic repositories, each with 50–2,000 references (1.02 million in all) and 300
  declarations, 198 MB of bundles;
- then 10,000 queries (80% consumers, 20% define) from 32 concurrent clients.

| Setup | Upload | Queries/s | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| Filesystem, one server | 7.4 s (136 repos/s) | 6,000 | 3.1 ms | 9.7 ms | 18 ms | 0.4 s (first, cold) |
| Filesystem, after restart | | 6,500 | 3.0 ms | 8.8 ms | 16 ms | 0.36 s (cold) |
| S3 (SeaweedFS), replica that uploaded | 1.2 s (813 repos/s) | 2,500 | 3.2 ms | 10.7 ms | 22 ms | 2.7 s (cold) |
| S3, second replica after warm-up | | 8,300 | 3.0 ms | 9.2 ms | 15 ms | 29 ms |

All runs had 0 errors. The "cold" maxima came from runs before the warm-up existed. The server
now loads every current bundle in the background at start and after each refresh. That took
about 1 s for 1,000 repositories on S3, after which the slowest query was 29 ms.

Memory: the Go heap held 0.6–0.9 GB for 198 MB of stored bundles, roughly 3–4.5× the stored size
of the current bundles. Process RSS ran higher (up to 2.4 GB right after the bulk upload, before
the runtime returned memory). Plan the memory limit at about 5× the current bundles' size,
`prism_link_cached_bytes`, and set `GOMEMLIMIT` to about 80% of it. The Helm chart does this from
the container's memory limit.

Answer size grows with how widely a symbol is used: in this test a consumers answer averaged 128 KB.

## Security model

- **Per-token rights:** CI tokens upload one repository; read tokens see only their repositories.
  Answers count what was hidden but do not name it.
- **Secrets are stored hashed** (SHA-256, file mode 0600), compared in constant time, never logged.
- **Revocation and expiry** take effect within a second, without a restart.
- **Transport:** HTTPS with TLS 1.2+, optional mTLS. Or a private network behind your proxy.
- **Every request is audited**, including authentication failures.
- **Rate limits apply to bad tokens too,** per client address.
- **No bodies and no machine paths are stored.** Each reference keeps its one source line, and
  each declaration its signature.
- **Dirty trees are refused** on both client and server, so a stored commit always describes
  what it holds.
- **Clients never take credentials from a repository,** only from their environment.
- **Not yet:** single sign-on (OIDC/SAML) for people. Tokens are created by operators with the CLI.

## Limits

- Only uploaded repositories are covered, and every answer says so.
- Calls between services (HTTP, gRPC, queues), reflection and dependency injection are not found,
  as with local links.
- Only the default branch is answered by Prism's commands.
- No single sign-on yet; operators issue tokens.
- The filesystem store serves one host. Replicas need S3 with conditional writes, Azure Blob
  Storage or Google Cloud Storage.
- S3 credentials: static keys or web identity; not EC2 or ECS instance credentials. GCS:
  metadata server or service-account keys; not `external_account` federation files.

## Upgrading from v1.3

| v1.3 | v1.4 |
|---|---|
| Starts without a token, with a warning | Refuses to start without `--tokens`, `PRISM_LINK_TOKEN` or `--no-auth` |
| One shared token | Per-token file (`--tokens`); the shared token still works alongside |
| `--dir` only | `--dir`, `--s3`, `--azure` or `--gcs`; several replicas on the shared ones |
| Plain HTTP on every interface | `--addr`, TLS and mTLS, `--ops-addr` |
| Keeps every commit | Keeps 20 per repository by default (`--keep 0` keeps all) |
| Bundles carried a local dependency's absolute path | Only `local: true`. The server drops the old fields from v1.3 clients' uploads |
| Detached-HEAD upload erased the commit's branch | The branch is kept, and push warns |
| `"repos": null` when empty | `"repos": []` |

The store format is compatible: a v1.4 server reads a v1.3 directory as it is. Commits stored
before v1.4 have no upload history, so retention counts them as the oldest. v1.3 clients can
upload to a v1.4 server, with a token that allows it. v1.4 clients work with a v1.3 server too,
but get no `unreadable` count and no pins for Java and Python.

## Troubleshooting

| Message | Cause and fix |
|---|---|
| `link-server: no credentials: pass --tokens …` | Create a token file, set `PRISM_LINK_TOKEN`, or pass `--no-auth` |
| `workspace push: 401 Unauthorized: missing, unknown, revoked or expired token` | Wrong, revoked or expired token; check `token list` |
| `workspace push: 403 …: token "ci-app" may not upload "…"` | The repository name doesn't match the token's `--upload`; check `--repo` and the token |
| `workspace push: 400 …: bundle format 2 is newer than this server accepts` | Upgrade the server |
| `workspace push: the tree has uncommitted changes …` | Commit, or push from a clean CI checkout |
| `workspace push: HEAD is detached, so no branch is recorded …` | Add `--ref main` in CI |
| `… NOT READABLE with this token, not searched` in an answer | Your read token doesn't cover those repositories; ask for a wider one if you need them |
| `server … NOT REACHED (… x509: certificate signed by unknown authority)` | Set `PRISM_LINK_CA` to your CA's PEM |
| `server … NOT REACHED (… tls: certificate required)` | The server requires mTLS: set `PRISM_LINK_CLIENT_CERT` and `PRISM_LINK_CLIENT_KEY` |
| `S3 bucket …: S3 403 …: AccessDenied` at start | Credentials or bucket policy (see the permissions above) |
| `the store ignores conditional writes …` at start | The object store does not honour `If-Match`. Use one that does, or run one replica with `--single-replica` |
| Uploads through one replica missing from another | Within `--refresh`, expected. Longer: check that both use the same `--s3` URL and the logs for `catalog refresh failed` |
| `check` reports `branch main points at missing commit` | A bundle was deleted outside Prism; re-run the consumer's CI or delete the branch's commit with `delete --sha` |
| A consumer appears twice | Uploaded under two `--repo` names; delete one with `delete --repo` |

## What was tested

On 2026-11-27, on macOS:

- **Unit and integration tests** (`go test ./internal/linkserver/ ./internal/cli/`), each checked to
  fail with its feature disabled:
  - token scopes for upload, read and admin, including the `unreadable` count;
  - revocation and expiry without a restart; no secret in the token file or audit log;
  - one audit line per request; 429 with `Retry-After`; metrics and health;
  - format versions (v1.3 bundles accepted, newer refused); upload size limit;
  - retention keeping branches and the newest; delete, check, backup and restore;
  - mTLS refusing a client without a certificate and admitting Prism's client configured through
    `PRISM_LINK_CA` / `PRISM_LINK_CLIENT_*`;
  - Signature V4 against the example in AWS's documentation;
  - the storage contract (conditional get, create-only, compare-and-swap, list, delete) and two
    stores with 20 concurrent uploads losing no branch, on the filesystem, two S3-compatible
    servers (VersityGW, SeaweedFS), Azurite (Azure Blob Storage, with Shared Key signing checked
    by the emulator) and fake-gcs-server (Google Cloud Storage);
  - each cloud credential flow against a local stand-in for its token service: AKS workload
    identity, managed identity, SAS, GCS service-account JWT (signature verified), the GCS
    metadata server, and AWS web identity (STS);
  - the operator subcommands through the real CLI.
- **Languages end to end:** Go, Java (Maven), TypeScript (npm `file:` dependency) and Python
  consumers pushing to a server, and the library's `change-impact` listing their sites with pins
  (`[import-scoped]` for Python).
- **The earlier v1.3.5 checks:** branch selection, local links not repeated, `verify`, `test-plan`
  and `lookup`, the `NOT REACHED` cases, `--addr` limiting to loopback.
- **Load:** the table above, on the filesystem and on two replicas sharing an S3 bucket.
- **Every backend through the container image** (`deploy/compose/run-backends.sh`, 51 checks,
  all passing). Each backend ran as two replicas on one store: VersityGW and SeaweedFS (S3),
  Azurite (Azure Blob Storage) and fake-gcs-server (Google Cloud Storage). The checks covered:
  - uploads with a scoped CI token, 403 for another repository, and `NOT READABLE` for a token
    without read rights;
  - one replica answering the other's upload, and revocation on both replicas within seconds;
  - retention at `--keep 2`, followed by `check`;
  - the audit log (no secrets), metrics, and backup and restore into a directory.

  LocalStack 3.8 ignores conditional writes. The server refused to start on it, and started with
  a warning under `--single-replica`.
- **The compose stack** (`deploy/compose/run-e2e.sh`, 44 checks, all passing): two replicas on S3
  behind a TLS proxy, adding TLS, mTLS, rate limits and the forwarded client address.
- **The Helm chart:** linted and rendered for the filesystem, S3, Azure and GCS, including the
  refusals for invalid settings.
- **Single-repository answers are unchanged from v1.3.5:** all 57 Prism commands recorded in the
  10-task benchmark's transcripts gave identical output from both versions, with identical
  steering files.

Not tested: the real cloud services (AWS S3, Azure Storage, Google Cloud Storage) and their real
token services; each protocol was tested against emulators or other implementations and local
stand-ins. A Kubernetes cluster was not tested either: the chart was linted, rendered and
schema-checked for every storage mode, not installed.

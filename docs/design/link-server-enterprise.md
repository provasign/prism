# Link server: enterprise hardening (design, 2026-11-27)

Scope: everything in the enterprise gap list except OIDC/SSO, which is deferred. Target release:
v1.4.0. The four v1.3.6 fixes (no local paths uploaded, `--addr`, detached uploads keep the
branch, `[]` for empty answers) are included.

## 1. Credentials and authorization

- **Token file** (`--tokens <file>`, JSON, mode 0600). Each entry has `id`, `name`, `hash` (SHA-256
  of the secret, never the secret), `upload` and `read` (lists of repository-name globs, `path.Match`
  syntax; `*` matches every name), `admin` (bool), optional `expires` (RFC 3339).
- **Secrets**: `plk_<id>_<32 random bytes, base64url>`. Printed once by `token create`.
- **Management** (offline, against the token file):
  - `prism link-server token create --tokens F --name ci-app --upload github.com/acme/app [--read GLOB]… [--admin] [--expires 90d]`
  - `token list`, `token revoke <id>`.
  - The server re-reads the file when it changes, so a revocation takes effect within a second,
    with no restart.
- **Upload**: the bundle's `repo` must match one of the token's `upload` globs; otherwise 403. A CI
  token is created per pipeline, scoped to its own repository.
- **Read**:
  - `consumers` and `define` answer only from repositories matching the token's `read` globs.
  - The answer carries `unreadable: N`, a count with no names. The client then reports
    "N uploaded repositories not readable with this token" and marks the answer incomplete.
- **Admin**: the deletion and retention endpoints need `admin: true`.
- **Compatibility**:
  - `PRISM_LINK_TOKEN` on the server still works. It acts as one full-access token, and the server
    warns that it is shared.
  - Starting with no credentials at all now requires `--no-auth` (v1.3.5 started open with a
    warning).
- Clients are unchanged: they send `PRISM_LINK_TOKEN` as the bearer token.

## 2. Audit log

- **Format**: JSON lines, to `--audit-log <file>` (default stdout). One line per request: time,
  request id, token id and name (never the secret), client address, method, path, status, bytes in
  and out, duration. Uploads add the repository and commit. Reads add the number of repositories
  answered and unreadable. Auth failures are logged with no token id.
- **Client address**: `X-Forwarded-For` is used only with `--trust-proxy`.
- **Rotation**: the log file reopens on SIGHUP, for logrotate.

## 3. Hardening

- **Timeouts**: `http.Server` with ReadHeader 10 s, Read 5 min (large uploads), Write 2 min,
  Idle 2 min, MaxHeaderBytes 1 MiB.
- **Connections**: `--max-conns` (default 512) caps concurrent connections.
- **Rate limit**: a token bucket per token, `--rate` requests/s (default 50, burst 2×). Over the
  limit gets 429 with `Retry-After`. Unauthenticated requests share one bucket per client address.
- **Upload size**: `--max-bundle-mb` (default 512).

## 4. TLS and mTLS

- **Server TLS**: `--tls-cert`, `--tls-key`. The certificate is re-read when its files change, so it
  can be rotated without a restart.
- **mTLS**: `--client-ca <pem>` requires and verifies client certificates.
- **Client settings**:
  - `PRISM_LINK_CA`: extra trusted roots, PEM.
  - `PRISM_LINK_CLIENT_CERT` and `PRISM_LINK_CLIENT_KEY`: the client certificate for mTLS.
- **Health under mTLS**: `/health` and `/metrics` are also served on `--ops-addr` (plain HTTP, off by
  default) for probes and scraping.

## 5. Storage, replicas, retention, backup

- **Blob backend interface**: get (with ETag), put, put-if-match (compare-and-swap), list, delete.
- **Filesystem backend** (`--dir`): one host. Compare-and-swap uses a per-repository lock file.
- **S3-compatible backend** (`--s3 s3://bucket/prefix`, endpoint and region flags, AWS credential
  environment variables):
  - Signature V4 implemented in the package; no SDK dependency.
  - `refs.json` is updated with conditional `If-Match` PUTs, so several replicas can write safely.
  - Several replicas are supported only on this backend; the docs and the Helm chart enforce that.
- **In-memory catalog**: a query no longer lists the store.
  - Replicas re-read refs every `--refresh` (default 10 s); a replica's own uploads are visible at
    once.
  - Bundles are cached by commit, which never changes.
- **Retention**:
  - `--keep N` (default 20) commits per repository, never one a branch points at. Applied after
    each upload.
  - `prism link-server prune --keep N` runs it by hand.
- **Deletion**: `prism link-server delete --repo R [--sha S]` and `DELETE /link/repo` (admin).
- **Integrity**: `prism link-server check` verifies every bundle parses and every ref points at a
  stored commit.
- **Backup and restore**: `prism link-server backup --to <dir>` and `restore --from <dir>` work with
  any backend, which also migrates between the filesystem and S3.

## 6. Bundle format version

- **Field**: bundles carry `format: 1`. A missing field is read as 0, the v1.3.x format, which is
  still accepted.
- **Too new**: a format newer than the server supports gets 400 with "upgrade the server".
- **Discovery**: `/health` lists the formats the server accepts.

## 7. Observability

- **`/metrics`**: Prometheus text format, hand-written, no dependency. Series:
  - requests by endpoint and code, plus a latency histogram;
  - uploads by result, rate-limited requests, auth failures;
  - repositories, bundles and cached bytes.
- **Server log**: JSON lines (`log/slog`) to stderr.

## 8. Packaging

- **Image**: a Dockerfile (static binary, distroless, non-root). The release workflow publishes a
  multi-arch image to `ghcr.io/provasign/prism`.
- **Helm chart**: `deploy/helm/prism-link-server`, covering Deployment, Service, token-file
  Secret, optional TLS Secret, filesystem PVC or S3 settings, probes on the ops port, and an
  optional ServiceMonitor. More than one replica requires S3.
- **Compose example**: `deploy/compose`, with MinIO, two replicas and a TLS proxy. It is the
  end-to-end test on a laptop.

## 9. Verification

- **Unit tests**: one for each behaviour above.
- **End to end on this laptop**:
  - Go, Java, TypeScript and Python consumers through a server;
  - the compose stack (two replicas on MinIO behind TLS), covering token scopes, mTLS, a replica
    seeing the other's upload, the audit log, rate limiting and a retention run.
- **Load test** (`go run ./internal/linkserver/loadtest`): 1,000 synthetic repositories, then
  concurrent queries, on both backends. Results (p50, p95 and p99 latency, memory) are recorded in
  `docs/LINK_SERVER.md`.

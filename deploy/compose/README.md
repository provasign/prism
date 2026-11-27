# Link server: compose stack and end-to-end test

Two `prism link-server` replicas share one S3 bucket. A TLS proxy sits in front of them. The
stack runs on a laptop with Docker, and `run-e2e.sh` tests it from the host.

```
 host: prism workspace push / change-impact
        │ https://localhost:18443  (PRISM_LINK_CA = the script's CA)
        ▼
 ┌─────────────┐   round robin   ┌────────────────────┐     ┌──────────────────────┐
 │ proxy       │ ───────────────▶│ lsa  :8890 / :9890 │ ──▶ │ s3 (versitygw :7070) │
 │ caddy:2     │ ───────────────▶│ lsb  :8890 / :9890 │ ──▶ │ bucket prism-e2e     │
 └─────────────┘                 └────────────────────┘     └──────────────────────┘
```

| Container | Image | Host port (127.0.0.1) |
|---|---|---|
| `prism-e2e-s3` | `versity/versitygw:latest` | 17070 (S3 API) |
| `prism-e2e-lsa` | `prism-e2e-image:dev`, built from `deploy/docker/Dockerfile` | 18891 (API), 19891 (ops) |
| `prism-e2e-lsb` | same | 18892 (API), 19892 (ops) |
| `prism-e2e-proxy` | `caddy:2` | 18443 (HTTPS) |
| `prism-e2e-rate` (started by the script) | same | 18893 (HTTPS + mTLS), 19893 (ops) |

The replicas run with `--s3 s3://prism-e2e/link --s3-path-style --refresh 2s --keep 2
--trust-proxy --ops-addr :9890`. They have a read-only root filesystem, run as uid 65532 and drop
every capability.

## Run the test

```sh
deploy/compose/run-e2e.sh                  # build, start, test, tear down
KEEP_STACK=1 deploy/compose/run-e2e.sh     # leave everything up afterwards
PRISM_BIN=/path/to/prism SKIP_BUILD=1 deploy/compose/run-e2e.sh   # reuse binaries
```

You need Docker, `openssl`, `curl`, `python3`, `git` and Go. The script:

1. Builds the image and a host `prism`.
2. Generates a throwaway CA plus server and client certificates in `.e2e/certs`.
3. Creates tokens in `.e2e/tokens/tokens.json`:
   - `admin` (admin, uploads and reads everything);
   - `ci-app` (uploads `example.com/acme/app` only);
   - `reader` (reads `example.com/acme/*`);
   - `outsider` (reads `other.org/*`);
   - `temp`, which the script later revokes.
4. Starts the S3 store, creates the bucket, then starts the replicas and the proxy.
5. Creates three Go repositories in a temporary directory. `lib` declares `Normalize`. `app` and
   `svc` call it through `replace example.com/acme/lib => ../lib`.
6. Runs the checks, prints `PASS`, `FAIL` or `KNOWN-BUG` for each, and exits non-zero if any check
   fails.
7. Removes every container, the network, `.e2e` and the repositories, even on failure.

What is checked:

| Area | Checks |
|---|---|
| Image | runs as 65532, prints `link-server help` by default |
| TLS | a push without `PRISM_LINK_CA` is refused; with it, it succeeds through the proxy |
| Scopes | the CI token pushing another repository gets 403; no token or an unknown token gets 401 |
| Replicas | an upload to replica A is served by replica B within one refresh; both answer with the branch head |
| Reads | `change-impact` in `lib` lists the `app` and `svc` sites; a token that cannot read them gets `NOT READABLE` and no sites |
| Revocation | `prism link-server token revoke` on the mounted file takes effect on both replicas in about a second |
| Retention | after 6 uploads with `--keep 2`, S3 holds 2 `app` bundles, including the head; `prism link-server check --s3` reports 0 problems |
| Audit | JSON lines on both replicas with token name, repository, 403 and 401 (no token id); no secret in any container log; the forwarded client address is recorded, not the proxy's |
| Metrics | ops-port `/metrics` counts the uploads, the 403, auth failures and rate-limited requests; `/metrics` is not on the API port |
| Rate limit and mTLS | a replica with `--rate 2` and `--client-ca`: no client certificate is refused, `PRISM_LINK_CLIENT_CERT/KEY` works, 20 rapid requests get 429 with `Retry-After`, another token is unaffected |

## Use it as a deployment starting point

- Replace `versitygw` with your S3 service. Pass the keys as `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY`, or use web identity (`AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE`).
- Replace the self-signed certificate in the `Caddyfile` with your own.
- **Mount the token file's directory, not the file.** `token create` and `token revoke` replace the
  file with a rename. A single-file bind mount keeps showing the old file, so a revocation would
  never reach the server.
- Keep the ops ports (`--ops-addr`) off the public network. They serve `/health` and `/metrics`
  without authentication.

## Bugs this test found (fixed in v1.4.0)

- `prism link-server token create`, `delete`, `backup`, `restore`, `prune --repo` and
  `check --json` were rejected with `unknown flag`: the shared flag check ran before the
  subcommand saw its arguments.
- `prism workspace push` in a repository nested inside another checkout uploaded the outer
  repository's bundle under the inner one's name. Push and export now use the git top level of
  the current directory.

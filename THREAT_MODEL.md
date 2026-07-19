# Prism Threat Model

## Scope

Prism is a local code-indexing and context-delivery tool. It reads repositories,
stores a Grove graph and delivery metadata, exposes CLI/MCP/optional loopback
HTTP surfaces, and may modify coding-tool configuration during `prism init`.

## Assets and boundaries

- Source, paths, symbols, documentation, and accidental credentials are assets.
- Repository content is untrusted input, including syntax, symlinks, and text
  that resembles agent instructions.
- MCP and local HTTP clients are separate processes. Loopback is not an
  authentication boundary against other local processes.
- Native analyzers, GitHub Actions, registries, and downloaded binaries cross
  dependency and supply-chain boundaries.

## Primary threats and controls

| Threat | Current control |
|---|---|
| Path traversal or symlink escape | Canonical roots and path-within-root checks |
| Credential/key indexing | Security-sensitive extensions and bare `.env` are excluded by Grove |
| Cross-conversation under-delivery | Direct CLI handlers do not load persistent conversation caches |
| Stale or partial graph presented as complete | Operations expose completeness; `prism doctor` reports readiness |
| Resource exhaustion | Bounded delivery, file limits, timeouts, and parser fallbacks |
| Local HTTP exposure | Server binds to `127.0.0.1`; do not expose it through a proxy |
| Installer substitution | Checksums are mandatory; releases publish provenance and SBOM artifacts |

## Retention

The Grove index and Prism ledger/session cache are local. They can reveal file
names, symbols, hashes, and delivery history. Remove repository-local state and
user cache entries when disposing of a project or workstation. Do not include
caches in support bundles. Direct CLI reads do not persist conversation delivery
state; persistent MCP sessions may retain delivery hashes for deduplication.

## Residual risks

Static analysis cannot fully model reflection, runtime dependency injection,
generated code, or dynamic dispatch. Native tool failure can degrade evidence.
Consumers must treat degraded results as evidence, not proof, and run
language-native builds and tests before release.

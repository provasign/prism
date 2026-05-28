# Prism — VS Code Extension

Delivers ranked, compressed code context to AI agents directly via VS Code's Language Model Tools API. The extension spawns the `prism` binary per call — no `prism serve` required.

## Requirements

- `prism` binary on `$PATH` (or set `prism.binaryPath`)
- `grove` binary on `$PATH` (auto-started by `prism` on first call)

## Tools

All 8 `prism_*` tools are registered with `vscode.lm.registerTool` and reference-able from chat prompts via `#prismQuery`, `#prismRead`, etc.

## Commands

- **Prism: Index Workspace** — manual reindex.
- **Prism: Query for Context** — interactive task input, opens JSON result.
- **Prism: Show Session Savings** — token savings dashboard.
- **Prism: Reset Session** — hint about CLI statelessness.

## Settings

- `prism.binaryPath` — path to `prism` (default `prism`).
- `prism.grovePath` — path to `grove`.
- `prism.autoIndex` — reindex on save (default `true`).
- `prism.profile` — default ranking profile.

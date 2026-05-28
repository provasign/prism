# Prism — VS Code Extension

Delivers ranked, compressed code context to AI agents directly via VS Code's Language Model Tools API. The extension spawns the `prism` binary per call — no `prism serve` required.

For full onboarding and integration modes, see `../README.md`.

Install and project setup are intentionally separate actions:

1. Install extension once.
2. Run Prism: Setup Workspace per project when you want project-level initialization (instructions + AI tool wiring).

## Requirements

- `prism` binary on `$PATH` (or set `prism.binaryPath`)
- `grove` binary on `$PATH` (auto-started by `prism` on first call)

## Tools

All 8 `prism_*` tools are registered with `vscode.lm.registerTool` and reference-able from chat prompts via `#prismQuery`, `#prismRead`, etc.

## Commands

- **Prism: Setup Workspace** — run `prism init` for the current project.
- **Prism: Index Workspace** — manual reindex.
- **Prism: Query for Context** — interactive task input, opens JSON result.
- **Prism: Show Session Savings** — token savings dashboard.
- **Prism: Reset Session** — hint about CLI statelessness.

## Settings

- `prism.binaryPath` — path to `prism` (default `prism`).
- `prism.grovePath` — path to `grove`.
- `prism.autoIndex` — reindex on save (default `true`).
- `prism.profile` — default ranking profile.

# Prism handoff

Updated: 2026-09-12

## Current release

Prism v0.73.0 moves setup and MCP delivery to a project-scoped model. The
binary remains globally installed, while each repository owns its harness
configuration, selected clients, compact MCP surface, and steering.

The release depends on Grove v0.49.0 and astkit v0.11.0. Both dependencies are
published from clean `main` branches and passed their hosted CI and release
workflows before Prism was pinned to them.

## Delivered behavior

- `prism init` writes project-local configuration only; `--global` is rejected.
- Interactive init discovers supported harnesses and asks which ones to
  configure. Non-interactive init requires `--harness`, while `--yes` can reuse
  the selection already recorded in `prism.yaml`.
- `prism.yaml` version 2 records selected harnesses and the compact MCP surface
  while preserving unrelated project settings.
- Claude Code, Codex, Cursor, VS Code, Gemini CLI, and OpenCode receive their
  documented project-local MCP configuration. Windsurf receives project
  steering and has unsupported legacy Prism registrations removed.
- Legacy Prism config and steering are migrated without deleting unrelated
  user content. Deprecated `.cursorrules` and `.windsurfrules` Prism sections
  are consolidated into `AGENTS.md`.
- `prism mcp` defaults to the one-tool compact gateway; `--legacy` explicitly
  exposes the six-tool compatibility surface.
- MCP startup performs read-only legacy-project detection and includes a
  migration warning in initialize instructions when it finds old config,
  arguments, or steering. Explicit `prism init` remains the only mutating path.
- Installers avoid creating a second user-bin copy when Homebrew's global path
  is the intended installation.

## Validation

The release candidate passed:

```text
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
sh -n install.sh scripts/bootstrap-prism-grove.sh
git diff --check
```

Fresh MCP handshakes advertised one compact `prism` tool (2,290 schema bytes)
and six legacy tools (9,766 aggregate schema bytes). A current v2 project
started without a migration warning; legacy fixtures produced the expected
read-only warning.

The full research release gate passed every ceiling, missing-implementation,
rename-plan, and determinism invariant. In particular, the previously blocked
Jackson `JsonNode.get(int)`, Jackson `SettableBeanProperty.set`, and Django
`BaseDatabaseOperations.quote_name` rows all reached 1.0 recall and 1.0
precision with Grove v0.49.0.

## Local operational note

An already-running harness does not reload its MCP schema or project config.
After installing v0.73.0, restart or reload Codex/Claude so the project-local
compact configuration launches the new binary. A laptop reboot is unnecessary.

Repository changes are made directly on `main`. Future commits and pushes still
require explicit user review and approval under `AGENTS.md`.

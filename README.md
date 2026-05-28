# Prism

Token-optimized context delivery for coding agents.

Prism has two integration modes:

1. MCP stdio mode (recommended default for normal development)
2. VS Code extension mode (for environments where MCP is not approved)

For most users in Claude Code CLI, Cursor, Windsurf, and other MCP-capable tools, use MCP stdio.

## Get Started in 5 Minutes

This is the fastest path for normal project work.

### 1) Install Prism binary

If you are developing from source:

```bash
cd prism
go build -o /tmp/prism ./cmd/prism
sudo cp /tmp/prism /opt/homebrew/bin/prism
```

Confirm:

```bash
prism version
```

### 2) Initialize one project

From the project root you want to enable:

```bash
prism init
```

What this does:

1. Writes `prism.yaml` in the project.
2. Writes Prism steering instructions (including Copilot instructions).
3. Registers MCP config for detected coding tools (Claude Code, Cursor, Windsurf, Zed).

### 3) Restart your coding tool once

This reloads MCP configuration.

### 4) Use Prism in normal prompts

Ask your coding agent to start with `prism_query`, then use `prism_read`, `prism_search`, and `prism_lookup`.

### 5) Verify quickly

```bash
prism index
prism query "find parser entrypoint"
prism savings
```

If you are on the latest Prism CLI, `prism savings` persists across separate CLI commands per project root.

## Recommended Default Workflow

Use MCP stdio mode for day-to-day coding.

Why:

1. No HTTP port management.
2. Works naturally with MCP-capable tools.
3. Per-session process model is simple and reliable.

## VS Code Extension Mode (No MCP Approval)

Use this mode when MCP is restricted by policy.

1. Install Prism VSIX once.
2. Open a project in VS Code.
3. Run `Prism: Setup Workspace` once per project.
4. Use Prism commands/tools from VS Code.

Notes:

1. Extension install and project setup are intentionally separate actions.
2. Status bar can show Prism savings percent.

## Advanced Setups

### Global initialization

```bash
prism init --global
```

Use when you want user-level MCP registration defaults in supported tools.

### Multiple projects (A and B)

Run `prism init` in each project root once.

Important:

1. `prism init` does not start `prism serve`.
2. MCP stdio mode does not bind to an HTTP port.
3. You do not get a new Prism HTTP server per project unless you explicitly run `prism serve`.

### HTTP server mode (optional)

```bash
prism serve --port 8888 /path/to/project
```

Use only when you need a persistent HTTP daemon (for non-MCP integrations or custom automation).

If running multiple project daemons, use different ports.

## Troubleshooting

### Savings stays at 0

1. Ensure you are running the expected binary:

```bash
command -v prism
prism version
```

2. Run query/read first, then `prism savings`.
3. Re-index if needed:

```bash
prism index
```

### Port 8888 already in use

You likely have another `prism serve` process running.

Options:

1. Stop the existing process.
2. Use another port (`--port 8889`).
3. Prefer MCP stdio mode if possible.

### Tool not using Prism after init

1. Restart the coding tool.
2. Check generated MCP config for that tool.
3. Re-run `prism init` in that project root.

## Command Cheat Sheet

```bash
prism init [--global] [dir]
prism index [dir]
prism query <task> [dir]
prism read <file> [dir]
prism search <keyword> [dir]
prism lookup <name> [dir]
prism savings [dir]
prism serve [--port 8888] [dir]
prism mcp [dir]
```

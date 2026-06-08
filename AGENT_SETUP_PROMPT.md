# Prism — AI Agent Setup Prompt

*This file is a prompt for your AI coding agent. Point any agent at it and it will install and configure Prism for you, always at the latest version.*

---

## How to Use This File

**One command (no agent required):**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex
```

**Claude Code:**
```
/read AGENT_SETUP_PROMPT.md  — then say "follow the setup instructions"
```
Or from outside the repo:
```
claude "Follow the setup instructions at https://raw.githubusercontent.com/provasign/prism/main/AGENT_SETUP_PROMPT.md"
```

**Any agent with web fetch:**
Paste this URL into chat and say "follow the setup instructions in this file":
```
https://raw.githubusercontent.com/provasign/prism/main/AGENT_SETUP_PROMPT.md
```

---

## Instructions for the Agent

You are setting up **Prism** — graph-ranked context delivery for AI coding agents.
The recommended setup is **both mode**: MCP tools (`prism_query`, `prism_read`,
`prism_search`, `prism_lookup`) as the primary surface, with CLI fallback
(`prism query/read/lookup --format text`) for subagents that don't inherit the
MCP session. MCP keeps the call-graph hot in memory; CLI reloads it per call.

Prism embeds Grove as a library; no separate Grove installation is required.

Before Step 1, ask:

> Do you want to **install/update** Prism, or **fully uninstall/reset** it?

- If **install/update**: continue with Step 1.
- If **uninstall/reset**: skip to **Step U1** below.

---

### Step 0 — Refresh These Instructions

Fetch the latest version of this file before doing anything else:

```
https://raw.githubusercontent.com/provasign/prism/main/AGENT_SETUP_PROMPT.md
```

If the fetched content differs from what you are reading now, use the fetched version for all remaining steps.

---

### Step 1 — Detect the Platform

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
esac
echo "Platform: ${OS}-${ARCH}"
```

**Windows (PowerShell):**
```powershell
$OS = "windows"
$ARCH = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
Write-Host "Platform: $OS-$ARCH"
```

---

### Step 2 — Check for an Existing Installation

```bash
if command -v prism &>/dev/null; then
  echo "prism: INSTALLED at $(which prism) — $(prism version 2>/dev/null | head -1)"
else
  echo "prism: not found"
fi
```

Fetch the latest release tag:

```bash
PRISM_VERSION=$(curl -sf "https://api.github.com/repos/provasign/prism/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
echo "Latest: $PRISM_VERSION"
```

- If installed and up to date: tell the user and skip to Step 4.
- If older: ask whether to upgrade.
- If not found: proceed with Step 3.

---

### Step 3 — Install

**Path A — `~/bin` (no sudo; agent runs directly):**

```bash
curl -fsSL \
  "https://github.com/provasign/prism/releases/download/${PRISM_VERSION}/install.sh" \
  | INSTALL_DIR=~/bin bash
```

**Path B — `/usr/local/bin` or any sudo-required path:**

```bash
# Agent downloads; user runs the privileged move
curl -fsSL \
  "https://github.com/provasign/prism/releases/download/${PRISM_VERSION}/install.sh" \
  -o /tmp/install-prism.sh
```

Tell the user:
> *"Script is ready. Run this in your terminal, then come back."*
```bash
sudo INSTALL_DIR=/usr/local/bin bash /tmp/install-prism.sh
```
Wait for the user to confirm before continuing.

**Windows (PowerShell):**
```powershell
$INSTALL_DIR = "$env:USERPROFILE\bin"   # or user-specified path
$tmpScript = "$env:TEMP\install-prism.ps1"
Invoke-WebRequest `
  "https://github.com/provasign/prism/releases/download/$PRISM_VERSION/install.ps1" `
  -OutFile $tmpScript
& $tmpScript -InstallDir $INSTALL_DIR
```

---

### Step 4 — Initialize

Ask the user for the path to their project.

Ask which agent mode they want:

> Which Prism agent mode should I configure?
>
> - **Both — MCP + CLI (recommended):** MCP tools as primary surface; CLI fallback for subagents.
> - **MCP only:** MCP tools only; subagents without MCP access won't have Prism context.
> - **CLI only:** agents use `prism query ... --format text` through Bash.

If the user does not choose, use both mode (the `prism init` default).

**Both mode (recommended):**

```bash
PROJECT="/path/to/your/project"
cd "$PROJECT"
prism init . --mode both
prism index .   # builds initial index (subsequent runs are delta-only)
echo "Prism initialized in both mode. Restart your AI coding tool to activate MCP and reload steering instructions."
```

> **Claude Code users:** `prism init` writes `.mcp.json` at the project root. When Claude Code restarts it may prompt "Allow MCP servers from .mcp.json?" — click **Allow**.

**MCP mode:**

```bash
PROJECT="/path/to/your/project"
cd "$PROJECT"
prism init . --mode mcp
prism index .
echo "Prism initialized in MCP mode. Restart your AI coding tool to activate the MCP server."
```

**CLI mode:**

```bash
PROJECT="/path/to/your/project"
cd "$PROJECT"
prism init . --mode cli
prism index .
echo "Prism initialized in CLI mode. Restart your AI coding tool so it reloads steering instructions."
```

---

### Step 5 — Smoke Test

```bash
prism version && echo "✅ prism binary ok" || echo "❌ prism binary failed"

echo "--- Context query ---"
RESULT=$(prism query "main entry point" --format text 2>/dev/null | head -5)
[ -n "$RESULT" ] \
  && echo "✅ prism query ok:" && echo "$RESULT" \
  || echo "❌ prism query returned nothing — run: prism index ."
```

**If MCP or both mode was selected, verify the MCP server connects (Claude Code):**

```bash
MCP_OUT="$(claude mcp list 2>&1)"
echo "$MCP_OUT"
if echo "$MCP_OUT" | grep -qiE "^prism:.*(✓|connected)"; then
  echo "✅ prism: connected"
elif echo "$MCP_OUT" | grep -qi "prism"; then
  echo "❌ prism: registered but NOT connected — see fixes below"
else
  echo "❌ prism: not found in mcp list — run: prism init . --mode both, then restart Claude Code"
fi
```

If `Failed to connect`, inspect the MCP log:
```bash
LOGDIR="$HOME/Library/Caches/claude-cli-nodejs"   # macOS
tail -n 5 "$(ls -t "$LOGDIR"/*/mcp-logs-prism/*.jsonl 2>/dev/null | head -1)" 2>/dev/null
```

**Common failures:**

| Symptom | Fix |
|---------|-----|
| `command not found` | Install directory not on `$PATH` — add it and restart shell |
| macOS "cannot be opened because the developer cannot be verified" | `xattr -d com.apple.quarantine $(which prism)` |
| macOS `zsh: killed` (exit 137) | `codesign -f -s - $(which prism)` |
| Agent uses CLI steering after MCP setup | Re-run `prism init . --mode both`; verify `prism.yaml` has `agent_mode: "both"` or `"mcp"` |
| `claude mcp list` shows prism **Failed to connect** | Upgrade to the latest release (`prism version` to confirm); fully restart your AI tool |
| `claude mcp list` doesn't show prism | Re-run `prism init . --mode both` from the project root, restart Claude Code, approve `.mcp.json` when prompted |
| Empty results from `prism query` | Run `prism index .` from the project root and retry |

---

### Step 6 — Report to the User

```
Prism installation complete
══════════════════════════════════════════
 prism  vX.Y.Z  ✅  ~/bin/prism
══════════════════════════════════════════

Next steps
──────────
  Both/MCP mode:  Restart your AI coding tool to activate the MCP server
  CLI mode:       Restart your AI coding tool so it reloads CLI steering
  Token savings:  prism savings   (after your first task)

Documentation: https://github.com/provasign/prism
```

---

## Step U1 — Uninstall / Reset

Ask for the target project path and whether to stop running Prism MCP processes:

> Should I stop currently running Prism MCP server processes during uninstall?

If yes, set `KILL_MCPS=1`. If no, tell the user to restart their AI tool after uninstall.

```bash
PROJECT="/path/to/your/project"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
KILL_MCPS="${KILL_MCPS:-0}"

PRISM_VERSION=$(curl -sf "https://api.github.com/repos/provasign/prism/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
curl -fsSL \
  "https://github.com/provasign/prism/releases/download/${PRISM_VERSION}/uninstall.sh" \
  | INSTALL_DIR="$INSTALL_DIR" PROJECT="$PROJECT" KILL_MCPS="$KILL_MCPS" bash
```

**Windows (PowerShell):**
```powershell
$INSTALL_DIR = "$env:USERPROFILE\bin"
$PROJECT = "C:\path\to\project"
$KILL_MCPS = "0"
$PRISM_VERSION = (Invoke-RestMethod "https://api.github.com/repos/provasign/prism/releases/latest").tag_name
$tmpScript = "$env:TEMP\uninstall-prism.ps1"
Invoke-WebRequest `
  "https://github.com/provasign/prism/releases/download/$PRISM_VERSION/uninstall.ps1" `
  -OutFile $tmpScript
& $tmpScript -InstallDir $INSTALL_DIR -Project $PROJECT -KillMCPs $KILL_MCPS
```

After uninstall, verify:
```bash
command -v prism || echo "prism removed"
```

---

*Prism is MIT licensed. No telemetry. Your code never leaves your machine.*

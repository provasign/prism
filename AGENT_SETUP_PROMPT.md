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
The recommended setup is **CLI text mode**: agents use `prism query/read/lookup
--format text` through Bash. This avoids JSON metadata overhead, works in
subagents, and still surfaces callers, callees, tests, docs, and coverage gaps.

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

```bash
PROJECT="/path/to/your/project"
cd "$PROJECT"
prism init .
prism index .   # builds initial index (subsequent runs are delta-only)
echo "Prism initialized in CLI text mode. Restart your AI coding tool so it reloads steering instructions."
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

**Common failures:**

| Symptom | Fix |
|---------|-----|
| `command not found` | Install directory not on `$PATH` — add it and restart shell |
| macOS "cannot be opened because the developer cannot be verified" | `xattr -d com.apple.quarantine $(which prism)` |
| macOS `zsh: killed` (exit 137) | `codesign -f -s - $(which prism)` |
| Agent still uses stale Prism instructions | Re-run `prism init .`; fully restart your AI tool |
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
  Agent setup:    Restart your AI coding tool so it reloads CLI steering
  Token savings:  prism savings   (after your first task)

Documentation: https://github.com/provasign/prism
```

---

## Step U1 — Uninstall / Reset

Ask for the target project path.

```bash
PROJECT="/path/to/your/project"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"

PRISM_VERSION=$(curl -sf "https://api.github.com/repos/provasign/prism/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
curl -fsSL \
  "https://github.com/provasign/prism/releases/download/${PRISM_VERSION}/uninstall.sh" \
  | INSTALL_DIR="$INSTALL_DIR" PROJECT="$PROJECT" bash
```

**Windows (PowerShell):**
```powershell
$INSTALL_DIR = "$env:USERPROFILE\bin"
$PROJECT = "C:\path\to\project"
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

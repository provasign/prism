#!/usr/bin/env bash
#
# Uninstall Prism from GitHub Releases install.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/uninstall.sh | bash
#
# Environment variables (all optional):
#   INSTALL_DIR    directory where prism was installed   (default: $HOME/bin)
#   PROJECT        project dir to deregister MCP from    (default: none)
#
set -euo pipefail

PRODUCT="prism"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
PROJECT="${PROJECT:-}"
PRISM="${INSTALL_DIR}/${PRODUCT}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✅\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m❌\033[0m %s\n' "$*" >&2; }

# Deregister from global AI tool configs
if [ -x "$PRISM" ]; then
  info "Deregistering prism from AI tool configs…"
  for client in claude-code cursor windsurf; do
    "$PRISM" mcp uninstall "$client" 2>/dev/null && ok "deregistered from $client" || true
  done
fi

# Remove project-local MCP entry
if [ -n "$PROJECT" ] && [ -f "${PROJECT}/.mcp.json" ]; then
  info "Removing prism from ${PROJECT}/.mcp.json…"
  python3 -c "
import json, sys
p = '${PROJECT}/.mcp.json'
with open(p) as f: d = json.load(f)
d.get('mcpServers', {}).pop('prism', None)
with open(p, 'w') as f: json.dump(d, f, indent=2)
print('removed prism entry from .mcp.json')
" 2>/dev/null && ok "cleaned ${PROJECT}/.mcp.json" || true
fi

# Remove prism from Claude Code's enabledMcpjsonServers
SETTINGS="$HOME/.claude/settings.json"
if [ -f "$SETTINGS" ]; then
  python3 -c "
import json
p = '$SETTINGS'
with open(p) as f: d = json.load(f)
servers = d.get('enabledMcpjsonServers', [])
if 'prism' in servers:
    servers.remove('prism')
    d['enabledMcpjsonServers'] = servers
    with open(p, 'w') as f: json.dump(d, f, indent=2)
    print('removed prism from enabledMcpjsonServers')
" 2>/dev/null && true
fi

# Remove binary
if [ -f "$PRISM" ]; then
  rm -f "$PRISM"
  ok "removed $PRISM"
else
  info "$PRISM: not found (already removed?)"
fi

printf '\n%s uninstalled from %s\n' "$PRODUCT" "$INSTALL_DIR"

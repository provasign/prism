#!/usr/bin/env bash
#
# Uninstall Prism from GitHub Releases install.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/uninstall.sh | bash
#
# Environment variables (all optional):
#   INSTALL_DIR    directory where prism was installed   (default: $HOME/bin)
#   PROJECT        project dir to deregister MCP from    (default: none)
#   KILL_MCPS      set to 1 to stop running MCP processes; 0 to skip
#
set -euo pipefail

PRODUCT="prism"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
PROJECT="${PROJECT:-}"
KILL_MCPS="${KILL_MCPS:-}"
PRISM="${INSTALL_DIR}/${PRODUCT}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✅\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m❌\033[0m %s\n' "$*" >&2; }

cleanup_json_key() {
  local path="$1"
  local key="$2"
  local name="$3"
  [ -f "$path" ] || return 0
  if command -v jq >/dev/null 2>&1; then
    local tmp="${path}.tmp.$$"
    jq --arg key "$key" --arg name "$name" '
      if (.[$key] | type) == "object" then
        del(.[$key][$name])
      elif (.[$key] | type) == "array" then
        .[$key] |= map(select(((type == "object") and (.name == $name)) | not))
      else
        .
      end
    ' "$path" > "$tmp" && mv "$tmp" "$path" && ok "cleaned $name from $path"
    rm -f "$tmp"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" "$key" "$name" <<'PY' 2>/dev/null || true
import json
import sys

p, key, name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(p, "r", encoding="utf-8") as f:
  d = json.load(f)
v = d.get(key)
changed = False
if isinstance(v, dict) and name in v:
  v.pop(name, None)
  changed = True
elif isinstance(v, list):
  new_v = [x for x in v if not (isinstance(x, dict) and x.get("name") == name)]
  if len(new_v) != len(v):
    d[key] = new_v
    changed = True
if changed:
  with open(p, "w", encoding="utf-8") as f:
    json.dump(d, f, indent=2)
  print(f"removed {name} from {p}")
PY
    return 0
  fi
  info "jq/python3 not found; skipped JSON MCP cleanup for $path"
}

cleanup_json_string_array() {
  local path="$1"
  local key="$2"
  local name="$3"
  [ -f "$path" ] || return 0
  if command -v jq >/dev/null 2>&1; then
    local tmp="${path}.tmp.$$"
    jq --arg key "$key" --arg name "$name" '
      if (.[$key] | type) == "array" then
        .[$key] |= map(select(. != $name))
      else
        .
      end
    ' "$path" > "$tmp" && mv "$tmp" "$path" && ok "cleaned $name from $path"
    rm -f "$tmp"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" "$key" "$name" <<'PY' 2>/dev/null || true
import json
import sys

p, key, name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(p, "r", encoding="utf-8") as f:
  d = json.load(f)
v = d.get(key)
changed = False
if isinstance(v, list):
  new_v = [x for x in v if x != name]
  if len(new_v) != len(v):
    d[key] = new_v
    changed = True
if changed:
  with open(p, "w", encoding="utf-8") as f:
    json.dump(d, f, indent=2)
  print(f"removed {name} from {p}")
PY
    return 0
  fi
  info "jq/python3 not found; skipped JSON list cleanup for $path"
}

kill_match() {
  local pat="$1"
  if command -v pkill >/dev/null 2>&1; then
    pkill -f "$pat" 2>/dev/null || true
  else
    ps -Ao pid=,command= | grep -E "$pat" | grep -v grep | awk '{print $1}' | xargs -n1 kill 2>/dev/null || true
  fi
}

cleanup_codex_config() {
  local cfg="$HOME/.codex/config.toml"
  [ -f "$cfg" ] || return 0
  local tmp="${cfg}.tmp.$$"
  awk '
    function trim(s) { sub(/^[[:space:]]+/, "", s); sub(/[[:space:]]+$/, "", s); return s }
    function starts_header(s) { return trim(s) ~ /^\[/ }
    function flush_legacy() {
      if (legacy == 0) return
      if (legacy_name != "prism") {
        printf "%s", legacy_buf
      }
      legacy = 0
      legacy_buf = ""
      legacy_name = ""
    }
    {
      t = trim($0)
      if (skip_named && starts_header($0)) {
        skip_named = 0
      }
      if (skip_named) next
      if (legacy && starts_header($0)) {
        flush_legacy()
      }
      if (t == "[mcp_servers.prism]") {
        skip_named = 1
        next
      }
      if (t == "[[mcp_servers]]") {
        legacy = 1
        legacy_buf = $0 ORS
        legacy_name = ""
        next
      }
      if (legacy) {
        legacy_buf = legacy_buf $0 ORS
        if (match($0, /^[[:space:]]*name[[:space:]]*=[[:space:]]*"[^"]+"/)) {
          legacy_name = $0
          sub(/^[^"]*"/, "", legacy_name)
          sub(/".*$/, "", legacy_name)
        }
        next
      }
      print
    }
    END { flush_legacy() }
  ' "$cfg" > "$tmp" && mv "$tmp" "$cfg"
  ok "cleaned Codex MCP entries in $cfg"
}

should_kill_mcps() {
  case "$KILL_MCPS" in
    1|true|TRUE|yes|YES) return 0 ;;
    0|false|FALSE|no|NO) return 1 ;;
  esac
  if [ -t 0 ]; then
    printf 'Stop running prism MCP processes now? [y/N] '
    read -r reply || return 1
    case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
  fi
  info "Skipping running MCP process termination; set KILL_MCPS=1 to stop it during non-interactive uninstall."
  return 1
}

if should_kill_mcps; then
  info "Stopping running prism MCP processes…"
  kill_match '(^|[ /])prism([[:space:]]|$).*mcp([[:space:]]|$)'
  ok "requested process termination"
fi

info "Deregistering prism from AI tool configs…"
cleanup_json_key "$HOME/.claude.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.cursor/mcp.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.windsurf/mcp.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.codeium/windsurf/mcp_config.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.continue/config.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.config/zed/settings.json" "context_servers" "prism"

cleanup_codex_config

# Remove project-local MCP entry
if [ -n "$PROJECT" ]; then
  info "Removing prism from project MCP configs in ${PROJECT}…"
  cleanup_json_key "${PROJECT}/.mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.cursor/mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.windsurf/mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.vscode/mcp.json" "servers" "prism"
fi

# Remove prism from Claude Code's enabledMcpjsonServers
SETTINGS="$HOME/.claude/settings.json"
cleanup_json_string_array "$SETTINGS" "enabledMcpjsonServers" "prism"

# Remove binary
if [ -f "$PRISM" ]; then
  rm -f "$PRISM"
  ok "removed $PRISM"
else
  info "$PRISM: not found (already removed?)"
fi

printf '\n%s uninstalled from %s\n' "$PRODUCT" "$INSTALL_DIR"

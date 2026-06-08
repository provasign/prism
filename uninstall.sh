#!/usr/bin/env bash
#
# Uninstall Prism from GitHub Releases install.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/uninstall.sh | bash
#
# Environment variables (all optional):
#   INSTALL_DIR    directory where prism was installed   (default: $HOME/bin)
#   PROJECT        project dir to deregister MCP and steering from (default: none)
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

# ── JSON helpers ──────────────────────────────────────────────────────────────

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

# Delete a config file if the given key is now empty (no entries remain).
remove_if_empty_mcp_config() {
  local path="$1"
  local key="$2"
  [ -f "$path" ] || return 0
  local count=1
  if command -v jq >/dev/null 2>&1; then
    count=$(jq --arg key "$key" '(.[$key] // {}) | length' "$path" 2>/dev/null || echo "1")
  elif command -v python3 >/dev/null 2>&1; then
    count=$(python3 -c "
import json, sys
d = json.load(open(sys.argv[1]))
print(len(d.get(sys.argv[2], {})))
" "$path" "$key" 2>/dev/null || echo "1")
  fi
  if [ "$count" = "0" ]; then
    rm -f "$path"
    ok "removed empty $path"
  fi
}

# ── Steering helper ───────────────────────────────────────────────────────────

# Remove the "## Prism — context delivery" section from an agent instruction
# file. If the file contained only that section it is deleted entirely.
remove_steering_section() {
  local path="$1"
  [ -f "$path" ] || return 0
  local marker="## Prism — context delivery"
  grep -qF "$marker" "$path" || return 0

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" "$marker" <<'PY' 2>/dev/null || true
import sys, os
path, marker = sys.argv[1], sys.argv[2]
with open(path, 'r', encoding='utf-8') as f:
    content = f.read()
# Section starts at the marker, which is always either at the very start
# or preceded by a newline (how prism init appends it).
idx = content.find('\n' + marker)
if idx < 0 and content.startswith(marker):
    idx = 0
if idx < 0:
    sys.exit(0)
before = content[:idx].rstrip('\n')
if not before:
    os.remove(path)
    print(f"removed {path} (was only prism steering)")
else:
    with open(path, 'w', encoding='utf-8') as f:
        f.write(before + '\n')
    print(f"removed prism steering section from {path}")
PY
    return 0
  fi

  # awk fallback
  local tmp="${path}.tmp.$$"
  awk -v m="$marker" 'index($0, m){exit} {print}' "$path" > "$tmp"
  # trim trailing blank lines
  local trimmed
  trimmed=$(awk 'NF{buf=buf $0 ORS; blank=""} !NF{blank=blank $0 ORS} END{printf "%s", buf}' "$tmp")
  rm -f "$tmp"
  if [ -z "$trimmed" ]; then
    rm -f "$path"
    ok "removed $path (was only prism steering)"
  else
    printf '%s\n' "$trimmed" > "$path"
    ok "removed prism steering section from $path"
  fi
}

# ── PATH helper ───────────────────────────────────────────────────────────────

# Remove the PATH export line that install.sh appended to shell profiles.
cleanup_path_entry() {
  local dir="$1"
  local rc
  for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.zprofile" "$HOME/.bash_profile"; do
    [ -f "$rc" ] || continue
    if grep -qF "export PATH=\"${dir}:" "$rc" 2>/dev/null; then
      local tmp="${rc}.tmp.$$"
      grep -vF "export PATH=\"${dir}:" "$rc" > "$tmp" && mv "$tmp" "$rc"
      ok "removed PATH entry from $rc"
    fi
  done
}

# ── Process kill ──────────────────────────────────────────────────────────────

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

# ── Main ──────────────────────────────────────────────────────────────────────

if should_kill_mcps; then
  info "Stopping running prism MCP processes…"
  kill_match '(^|[ /])prism([[:space:]]|$).*mcp([[:space:]]|$)'
  ok "requested process termination"
fi

info "Removing PATH entry from shell profiles…"
cleanup_path_entry "$INSTALL_DIR"

info "Deregistering prism from global AI tool configs…"
cleanup_json_key "$HOME/.claude.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.cursor/mcp.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.windsurf/mcp.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.codeium/windsurf/mcp_config.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.continue/config.json" "mcpServers" "prism"
cleanup_json_key "$HOME/.config/zed/settings.json" "context_servers" "prism"
cleanup_codex_config

# Remove global MCP config files that are now empty
remove_if_empty_mcp_config "$HOME/.claude.json" "mcpServers"
remove_if_empty_mcp_config "$HOME/.cursor/mcp.json" "mcpServers"
remove_if_empty_mcp_config "$HOME/.windsurf/mcp.json" "mcpServers"

# Remove prism from Claude Code's enabledMcpjsonServers
SETTINGS="$HOME/.claude/settings.json"
cleanup_json_string_array "$SETTINGS" "enabledMcpjsonServers" "prism"

# ── Project-local cleanup ─────────────────────────────────────────────────────

if [ -n "$PROJECT" ]; then
  info "Removing prism from project in ${PROJECT}…"

  # MCP config files
  cleanup_json_key "${PROJECT}/.mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.cursor/mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.windsurf/mcp.json" "mcpServers" "prism"
  cleanup_json_key "${PROJECT}/.vscode/mcp.json" "servers" "prism"
  cleanup_json_key "${PROJECT}/.kiro/settings/mcp.json" "mcpServers" "prism"

  # Remove project MCP config files that are now empty
  remove_if_empty_mcp_config "${PROJECT}/.mcp.json" "mcpServers"
  remove_if_empty_mcp_config "${PROJECT}/.cursor/mcp.json" "mcpServers"
  remove_if_empty_mcp_config "${PROJECT}/.windsurf/mcp.json" "mcpServers"
  remove_if_empty_mcp_config "${PROJECT}/.vscode/mcp.json" "servers"
  remove_if_empty_mcp_config "${PROJECT}/.kiro/settings/mcp.json" "mcpServers"

  # Steering instruction files — remove the Prism section (or the whole file
  # if prism created it entirely, e.g. .kiro/steering/prism.md)
  info "Removing prism steering instructions from agent files…"
  remove_steering_section "${PROJECT}/CLAUDE.md"
  remove_steering_section "${PROJECT}/AGENTS.md"
  remove_steering_section "${PROJECT}/GEMINI.md"
  remove_steering_section "${PROJECT}/.cursorrules"
  remove_steering_section "${PROJECT}/.windsurfrules"
  remove_steering_section "${PROJECT}/.clinerules"
  remove_steering_section "${PROJECT}/.github/copilot-instructions.md"
  remove_steering_section "${PROJECT}/.devin/instructions.md"
  # .kiro/steering/prism.md is a prism-owned file — delete it entirely
  if [ -f "${PROJECT}/.kiro/steering/prism.md" ]; then
    rm -f "${PROJECT}/.kiro/steering/prism.md"
    ok "removed ${PROJECT}/.kiro/steering/prism.md"
  fi

  # prism.yaml
  if [ -f "${PROJECT}/prism.yaml" ]; then
    rm -f "${PROJECT}/prism.yaml"
    ok "removed ${PROJECT}/prism.yaml"
  fi
fi

# ── Binary ────────────────────────────────────────────────────────────────────

# Auto-detect binary location if not in INSTALL_DIR
if [ ! -f "$PRISM" ]; then
  detected=$(command -v prism 2>/dev/null || true)
  if [ -n "$detected" ]; then
    PRISM="$detected"
    info "Found prism at $PRISM (not in default INSTALL_DIR=${INSTALL_DIR})"
  fi
fi

if [ -f "$PRISM" ]; then
  rm -f "$PRISM"
  ok "removed $PRISM"
else
  info "prism binary not found (already removed?)"
fi

# ── Cache / ledger ────────────────────────────────────────────────────────────

if [ "$(uname)" = "Darwin" ]; then
  CACHE_DIR="$HOME/Library/Caches/prism"
else
  CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/prism"
fi
if [ -d "$CACHE_DIR" ]; then
  rm -rf "$CACHE_DIR"
  ok "removed cache directory $CACHE_DIR"
fi

printf '\n%s uninstalled.\n' "$PRODUCT"

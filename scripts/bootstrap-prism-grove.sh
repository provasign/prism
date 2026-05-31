#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRISM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GROVE_ROOT_DEFAULT="$(cd "$PRISM_ROOT/../grove" 2>/dev/null && pwd || true)"

WORKSPACE_DIR="${1:-$PWD}"
GLOBAL_INIT="${PRISM_GLOBAL_INIT:-1}"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is required but was not found in PATH" >&2
  exit 1
fi

GOBIN="${GOBIN:-$(go env GOPATH)/bin}"
mkdir -p "$GOBIN"

install_local_binary() {
  local repo_root="$1"
  local cmd_name="$2"
  local out_bin="$GOBIN/$cmd_name"

  if [[ ! -f "$repo_root/go.mod" ]]; then
    return 1
  fi

  echo "[bootstrap] building $cmd_name from $repo_root"
  (
    cd "$repo_root"
    go build -o "$out_bin" "./cmd/$cmd_name"
  )
}

# Install Grove first.
if [[ -n "$GROVE_ROOT_DEFAULT" && -d "$GROVE_ROOT_DEFAULT" ]]; then
  install_local_binary "$GROVE_ROOT_DEFAULT" "grove"
else
  echo "error: local grove repository not found next to prism." >&2
  echo "expected path: $PRISM_ROOT/../grove" >&2
  exit 1
fi

# Install Prism.
install_local_binary "$PRISM_ROOT" "prism"

echo "[bootstrap] installed binaries into $GOBIN"

if [[ ":$PATH:" != *":$GOBIN:"* ]]; then
  echo "[bootstrap] note: $GOBIN is not in PATH for this shell"
  echo "[bootstrap] run: export PATH=\"$GOBIN:\$PATH\""
fi

# Initialize Prism configuration and tool registration for the chosen workspace.
PRISM_BIN="$GOBIN/prism"

if [[ ! -x "$PRISM_BIN" ]]; then
  echo "error: prism binary was not installed at $PRISM_BIN" >&2
  exit 1
fi

if [[ "$GLOBAL_INIT" == "1" ]]; then
  echo "[bootstrap] running prism init --global $WORKSPACE_DIR"
  "$PRISM_BIN" init --global "$WORKSPACE_DIR"
else
  echo "[bootstrap] running prism init $WORKSPACE_DIR"
  "$PRISM_BIN" init "$WORKSPACE_DIR"
fi

echo "[bootstrap] done"
echo "[bootstrap] next steps:"
echo "  1) prism index $WORKSPACE_DIR"
echo "  2) prism query \"your task\" $WORKSPACE_DIR"
echo
echo "[bootstrap] agent steering files written into $WORKSPACE_DIR:"
echo "  - CLAUDE.md, AGENTS.md, GEMINI.md, .cursorrules, .windsurfrules"
echo "  - .github/copilot-instructions.md"
echo "  - .vscode/mcp.json (VS Code native MCP host)"
echo "  - per-tool .claude/mcp.json / .cursor/mcp.json / .windsurf/mcp.json"

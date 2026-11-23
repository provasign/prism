#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRISM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GROVE_ROOT_DEFAULT="$(cd "$PRISM_ROOT/../grove" 2>/dev/null && pwd || true)"

WORKSPACE_DIR="${1:-$PWD}"

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

: "${HARNESS:?set HARNESS to a comma-separated list such as claude,codex; bootstrap never assumes all harnesses}"
echo "[bootstrap] running prism init --harness $HARNESS $WORKSPACE_DIR"
"$PRISM_BIN" cleanup-global
"$PRISM_BIN" init --harness "$HARNESS" "$WORKSPACE_DIR"

# prism init above printed each file it wrote for $HARNESS.
echo "[bootstrap] done; next: prism index $WORKSPACE_DIR"

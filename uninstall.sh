#!/usr/bin/env bash
#
# Uninstall Prism from a GitHub Releases install.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/uninstall.sh | bash
#
# Environment variables:
#   INSTALL_DIR    directory where prism was installed   (default: $HOME/bin)
#
set -euo pipefail

PRODUCT="prism"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
PRISM="${INSTALL_DIR}/${PRODUCT}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✅\033[0m %s\n' "$*"; }

if [ -f "$PRISM" ]; then
  rm -f "$PRISM"
  ok "removed $PRISM"
else
  info "$PRISM: not found (already removed?)"
fi

printf '\n%s uninstalled from %s\n' "$PRODUCT" "$INSTALL_DIR"

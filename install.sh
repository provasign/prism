#!/usr/bin/env bash
#
# Install Prism from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
#
# Environment variables (all optional):
#   VERSION        release tag to install   (default: latest)
#   INSTALL_DIR    install directory         (default: $HOME/bin)
#
# Supported platforms: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64
# Windows: use install.ps1 instead.
#
# Note: Prism embeds Grove as a library — no separate grove installation is
# required for Prism to function. Install grove separately only if you want
# the grove CLI for direct queries.
set -euo pipefail

PRODUCT="prism"
REPO="provasign/prism"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✅\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m❌\033[0m %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"; }

need curl

# ── Platform detection ───────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS — use install.ps1 on Windows" ;;
esac
info "Platform: ${OS}-${ARCH}"

# ── Version resolution ───────────────────────────────────────────────────────
VERSION="${VERSION:-}"
if [ -z "$VERSION" ]; then
  info "Resolving latest release…"
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
  [ -n "$VERSION" ] || die "could not determine latest version — set VERSION=vX.Y.Z"
fi
info "Version: ${VERSION}"

FILE="${PRODUCT}-${VERSION}-${OS}-${ARCH}"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ── Download ─────────────────────────────────────────────────────────────────
info "Downloading ${FILE}…"
curl -fSL --progress-bar "${BASE}/${FILE}" -o "${TMP}/${FILE}" \
  || die "download failed — check https://github.com/${REPO}/releases"

# ── Checksum verification ────────────────────────────────────────────────────
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
ACTUAL="$(sha256 "${TMP}/${FILE}")"
curl -fsSL "${BASE}/checksums.txt" -o "${TMP}/checksums.txt" \
  || die "could not download checksums.txt; refusing an unverified install"
EXPECTED="$(grep -E "  (\./)?${FILE}\$" "${TMP}/checksums.txt" | awk '{print $1}' | head -1)"
[ -n "$EXPECTED" ] || die "checksums.txt has no entry for ${FILE}"
[ "$EXPECTED" = "$ACTUAL" ] \
  || die "CHECKSUM MISMATCH — refusing to install\n  expected: $EXPECTED\n  actual:   $ACTUAL"
ok "${FILE}: checksum verified"

# ── Install ──────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
mv "${TMP}/${FILE}" "${INSTALL_DIR}/${PRODUCT}"
chmod +x "${INSTALL_DIR}/${PRODUCT}"
[ "$OS" = "darwin" ] && xattr -d com.apple.quarantine "${INSTALL_DIR}/${PRODUCT}" 2>/dev/null || true
[ "$OS" = "darwin" ] && codesign -f -s - "${INSTALL_DIR}/${PRODUCT}" 2>/dev/null || true
ok "${PRODUCT} ${VERSION} → ${INSTALL_DIR}/${PRODUCT}"

# ── PATH registration ────────────────────────────────────────────────────────
if [ "$OS" = "darwin" ] && [ "$INSTALL_DIR" = "$HOME/bin" ]; then
  if command -v sudo >/dev/null 2>&1 && [ ! -f /etc/paths.d/provasign ]; then
    echo "$INSTALL_DIR" | sudo tee /etc/paths.d/provasign >/dev/null 2>&1 \
      && ok "Registered ${INSTALL_DIR} system-wide via /etc/paths.d/provasign" || true
  fi
fi
SHELL_RC="$HOME/.zshrc"; [ -n "${BASH_VERSION:-}" ] && SHELL_RC="$HOME/.bashrc"
LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
if ! grep -qsF "$LINE" "$SHELL_RC" 2>/dev/null; then
  echo "$LINE" >> "$SHELL_RC" && info "Added ${INSTALL_DIR} to PATH in ${SHELL_RC}"
fi
export PATH="${INSTALL_DIR}:$PATH"

# ── Global AI tool registration ──────────────────────────────────────────────
info "Registering prism with detected AI coding tools (global)…"
"${INSTALL_DIR}/prism" init --global 2>/dev/null \
  && ok "prism registered globally with detected AI tools" \
  || info "prism global init skipped (run: prism init --global)"

# ── Optional project initialization ─────────────────────────────────────────
if [ -n "${PROJECT:-}" ]; then
  [ -d "$PROJECT" ] || die "project dir not found: $PROJECT"
  info "Initializing project: $PROJECT"
  ( cd "$PROJECT"
    "${INSTALL_DIR}/prism" init >/dev/null 2>&1 \
      && ok "prism: project initialized" \
      || err "prism init failed — run manually: prism init"
    "${INSTALL_DIR}/prism" index >/dev/null 2>&1 \
      && ok "prism: index built" \
      || err "prism index failed — run manually: prism index"
  )
fi

printf '\n%s %s installed. Open a new terminal or run:\n  export PATH="%s:$PATH"\n\nAI tool note:\n  Restart or reload your coding agent / IDE so it respawns MCP servers from the updated config.\n  For Claude Code, approve the .mcp.json servers if prompted, then verify with: claude mcp list\n\nNext: cd /your/project && prism init && prism index\n' \
  "$PRODUCT" "$VERSION" "$INSTALL_DIR"

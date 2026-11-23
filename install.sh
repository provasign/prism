#!/usr/bin/env bash
#
# Install Prism from GitHub Releases.
#
# Pin the installer to a release (recommended; main is a moving branch):
#   curl -fsSL https://raw.githubusercontent.com/provasign/prism/v1.2.0/install.sh | VERSION=v1.2.0 bash
#
# Options (after `bash -s --`, or as environment variables):
#   --modify-path     add INSTALL_DIR to your shell rc file (PRISM_MODIFY_PATH=1);
#                     on macOS also /etc/paths.d via sudo. Default: print the line.
#   --stop-servers    stop running `prism mcp` servers (PRISM_STOP_SERVERS=1).
#                     Default: leave them; each switches to the new binary on
#                     its next request.
#   --dry-run         print what would be downloaded and written, then exit
#
# Environment variables (all optional):
#   VERSION        release tag to install   (default: latest)
#   INSTALL_DIR    install directory         (default: $HOME/bin)
#   PRISM_REQUIRE_PROVENANCE=1   fail unless the release signature or build
#                  attestation verifies (needs cosign or gh)
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

MODIFY_PATH="${PRISM_MODIFY_PATH:-0}"
STOP_SERVERS="${PRISM_STOP_SERVERS:-0}"
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --modify-path)  MODIFY_PATH=1 ;;
    --stop-servers) STOP_SERVERS=1 ;;
    --dry-run)      DRY_RUN=1 ;;
    *) die "unknown option: $arg (see the header of install.sh)" ;;
  esac
done

need curl

# Stop only long-running Prism MCP servers. An executing Unix binary can remain
# alive after its path is replaced, so without this step an apparently
# successful upgrade can leave every open agent serving the old release.
stop_running_prism_mcps() {
  command -v ps >/dev/null 2>&1 || return 0
  local pids pid remaining attempts
  pids="$(ps -axo pid=,command= 2>/dev/null | awk -v self="$$" '
    $1 != self {
      exe = $2
      sub(/^.*\//, "", exe)
      if ((exe == "prism" || exe == "prism.exe") && $3 == "mcp") print $1
    }
  ')"
  [ -n "$pids" ] || return 0

  set -- $pids
  info "Stopping $# running Prism MCP server(s) before upgrade…"
  for pid in "$@"; do
    kill "$pid" 2>/dev/null || true
  done

  attempts=0
  while [ "$attempts" -lt 20 ]; do
    remaining=""
    for pid in "$@"; do
      if kill -0 "$pid" 2>/dev/null; then
        remaining="${remaining} ${pid}"
      fi
    done
    [ -z "$remaining" ] && break
    sleep 0.1
    attempts=$((attempts + 1))
  done
  [ -z "$remaining" ] || die "could not stop old Prism MCP process(es):${remaining}; close coding agents and retry"
  ok "Stopped old Prism MCP server(s); coding agents can now respawn the installed version"
}

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
SHELL_RC="$HOME/.zshrc"; [ -n "${BASH_VERSION:-}" ] && [ "$(basename "${SHELL:-}")" = "bash" ] && SHELL_RC="$HOME/.bashrc"
if [ "$DRY_RUN" = 1 ]; then
  printf 'dry run; nothing is written.\n'
  printf '  download  %s/%s\n' "$BASE" "$FILE"
  printf '  verify    %s/checksums.txt (+ checksums.txt.sigstore.json with cosign, or gh attestation)\n' "$BASE"
  printf '  write     %s/%s\n' "$INSTALL_DIR" "$PRODUCT"
  if [ "$MODIFY_PATH" = 1 ]; then
    printf '  append    export PATH line to %s\n' "$SHELL_RC"
    [ "$OS" = darwin ] && printf '  write     /etc/paths.d/provasign (sudo)\n'
  fi
  printf '  run       %s cleanup-global (removes legacy user-global MCP registrations)\n' "$INSTALL_DIR/$PRODUCT"
  [ "$STOP_SERVERS" = 1 ] && printf '  stop      running prism mcp servers\n'
  exit 0
fi
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

# ── Provenance ───────────────────────────────────────────────────────────────
# REL-04: checksums.txt comes from the same release as the binary, so it only
# proves the download is intact. The signature (cosign) or the build
# attestation (gh) proves the release workflow of provasign/prism made it.
provenance=""
if command -v cosign >/dev/null 2>&1 \
   && curl -fsSL "${BASE}/checksums.txt.sigstore.json" -o "${TMP}/checksums.txt.sigstore.json" 2>/dev/null; then
  if cosign verify-blob --bundle "${TMP}/checksums.txt.sigstore.json" \
       --certificate-identity-regexp '^https://github\.com/provasign/prism/\.github/workflows/release\.yml@refs/tags/v' \
       --certificate-oidc-issuer https://token.actions.githubusercontent.com \
       "${TMP}/checksums.txt" >/dev/null 2>&1; then
    provenance="cosign signature of checksums.txt"
  else
    die "checksums.txt signature did NOT verify — refusing to install"
  fi
elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  if gh attestation verify "${TMP}/${FILE}" --repo "${REPO}" >/dev/null 2>&1; then
    provenance="GitHub build attestation"
  else
    die "build attestation did NOT verify for ${FILE} — refusing to install"
  fi
fi
if [ -n "$provenance" ]; then
  ok "provenance verified: ${provenance}"
elif [ "${PRISM_REQUIRE_PROVENANCE:-0}" = 1 ]; then
  die "provenance not verified (install cosign, or gh and log in) and PRISM_REQUIRE_PROVENANCE=1"
else
  info "provenance not verified (install cosign, or a logged-in gh, to check it); checksum only"
fi

# ── Install ──────────────────────────────────────────────────────────────────
if [ "$STOP_SERVERS" = 1 ]; then
  stop_running_prism_mcps
fi
mkdir -p "$INSTALL_DIR"
mv "${TMP}/${FILE}" "${INSTALL_DIR}/${PRODUCT}"
chmod +x "${INSTALL_DIR}/${PRODUCT}"
[ "$OS" = "darwin" ] && xattr -d com.apple.quarantine "${INSTALL_DIR}/${PRODUCT}" 2>/dev/null || true
[ "$OS" = "darwin" ] && codesign -f -s - "${INSTALL_DIR}/${PRODUCT}" 2>/dev/null || true
INSTALLED_VERSION="$("${INSTALL_DIR}/${PRODUCT}" version 2>/dev/null || true)"
[ "$INSTALLED_VERSION" = "prism ${VERSION}" ] \
  || die "installed binary verification failed: expected 'prism ${VERSION}', got '${INSTALLED_VERSION:-no output}'"
ok "${PRODUCT} ${VERSION} → ${INSTALL_DIR}/${PRODUCT}"

# ── PATH registration ────────────────────────────────────────────────────────
# REL-10: shell rc files and /etc are changed only with --modify-path.
LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
ON_PATH=0
case ":$PATH:" in *":${INSTALL_DIR}:"*) ON_PATH=1 ;; esac
if [ "$MODIFY_PATH" = 1 ]; then
  if [ "$OS" = "darwin" ] && [ "$INSTALL_DIR" = "$HOME/bin" ]; then
    if command -v sudo >/dev/null 2>&1 && [ ! -f /etc/paths.d/provasign ]; then
      echo "$INSTALL_DIR" | sudo tee /etc/paths.d/provasign >/dev/null 2>&1 \
        && ok "Registered ${INSTALL_DIR} system-wide via /etc/paths.d/provasign" || true
    fi
  fi
  if ! grep -qsF "$LINE" "$SHELL_RC" 2>/dev/null; then
    echo "$LINE" >> "$SHELL_RC" && info "Added ${INSTALL_DIR} to PATH in ${SHELL_RC}"
  fi
elif [ "$ON_PATH" = 0 ]; then
  info "${INSTALL_DIR} is not on PATH. Add this line to ${SHELL_RC} (or rerun with --modify-path):"
  printf '    %s\n' "$LINE"
fi
export PATH="${INSTALL_DIR}:$PATH"

# ── Legacy global-registration cleanup ──────────────────────────────────────────
info "Removing legacy user-global Prism MCP registrations…"
"${INSTALL_DIR}/prism" cleanup-global \
  && ok "legacy user-global Prism registrations removed" \
  || info "legacy global cleanup skipped (run: prism cleanup-global)"

# ── Optional project initialization ─────────────────────────────────────────
if [ -n "${PROJECT:-}" ]; then
  [ -d "$PROJECT" ] || die "project dir not found: $PROJECT"
  [ -n "${HARNESS:-}" ] || die "PROJECT requires HARNESS (for example HARNESS=claude,codex); Prism never assumes all harnesses"
  info "Initializing project: $PROJECT"
  ( cd "$PROJECT"
    "${INSTALL_DIR}/prism" init --harness "$HARNESS" \
      && ok "prism: project initialized" \
      || err "prism init failed — run manually: prism init"
    "${INSTALL_DIR}/prism" index >/dev/null 2>&1 \
      && ok "prism: index built" \
      || err "prism index failed — run manually: prism index"
  )
fi

# ── What it is for, and the next steps ──────────────────────────────────────
n=0; step() { n=$((n+1)); printf '  %d. %s\n' "$n" "$1"; }
printf '\n%s %s installed: context before the edit.\n' "$PRODUCT" "$VERSION"
printf 'Prism gives your coding agent every caller, implementation and test a change\naffects, resolved by the compiler, not grep.\n\nNext steps:\n'
step "Open a new terminal, or run:  export PATH=\"${INSTALL_DIR}:\$PATH\""
step "cd your-repo"
step "prism init       choose your coding agents; they run the prism command (CLI, the default) or use MCP"
step "prism index      build the code graph"
step "Restart or reload your coding agent. If you chose MCP (prism init --interface mcp):"
printf '     in Claude Code, approve the .mcp.json servers if asked, then check: claude mcp list\n'
step "Commit what init writes, so every clone's agent works the same way"

# Install Prism from GitHub Releases.
#
# Pin the installer to a release (recommended; main is a moving branch):
#   $env:VERSION="v1.2.0"; irm https://raw.githubusercontent.com/provasign/prism/v1.2.0/install.ps1 | iex
#
# Parameters (pass as env vars before piping, or dot-source and call directly):
#   $env:VERSION       release tag to install   (default: latest)
#   $env:INSTALL_DIR   install directory         (default: $HOME\bin)
#   $env:PRISM_STOP_SERVERS=1        stop running `prism mcp` servers first
#                                    (default: the old binary is moved aside;
#                                    each server switches on its next request)
#   $env:PRISM_REQUIRE_PROVENANCE=1  fail unless `gh attestation verify` passes
#
# Supported platforms: windows-amd64, windows-arm64 (falls back to the amd64
# build, run under emulation, for a release without an arm64 asset)
#
# Note: Prism embeds Grove as a library — no separate grove installation is
# required for Prism to function.
[CmdletBinding()]
param(
  [string]$Version    = $env:VERSION,
  [string]$InstallDir = $env:INSTALL_DIR,
  [switch]$StopServers = ($env:PRISM_STOP_SERVERS -eq "1")
)
$ErrorActionPreference = "Stop"

$PRODUCT = "prism"
$REPO    = "provasign/prism"
# REL-10: detect the architecture instead of assuming amd64.
$ARCH = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") { "arm64" } else { "amd64" }

if (-not $InstallDir) { $InstallDir = "$env:USERPROFILE\bin" }

function info($msg) { Write-Host "==> $msg" -ForegroundColor Blue }
function ok($msg)   { Write-Host "✅ $msg" -ForegroundColor Green }
function die($msg)  { Write-Error "❌ $msg"; exit 1 }

# Windows locks running executables, and every platform can otherwise leave an
# open agent attached to the old in-memory release. Stop only `prism.exe mcp`
# processes; watch/serve and ordinary CLI processes are left alone.
function Stop-RunningPrismMcps {
  $running = @(Get-CimInstance Win32_Process -Filter "Name='prism.exe'" -ErrorAction SilentlyContinue | Where-Object {
    if (-not $_.CommandLine -or -not $_.ExecutablePath) { return $false }
    $escapedExe = [regex]::Escape($_.ExecutablePath)
    return $_.CommandLine -match "^(?:`"$escapedExe`"|$escapedExe)\s+mcp(?:\s|$)"
  })
  if ($running.Count -eq 0) { return }

  info "Stopping $($running.Count) running Prism MCP server(s) before upgrade…"
  foreach ($process in $running) {
    Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop
  }
  ok "Stopped old Prism MCP server(s); coding agents can now respawn the installed version"
}

# ── Version resolution ───────────────────────────────────────────────────────
if (-not $Version) {
  info "Resolving latest release…"
  $rel = Invoke-RestMethod "https://api.github.com/repos/$REPO/releases/latest"
  $Version = $rel.tag_name
  if (-not $Version) { die "Could not determine latest version — set VERSION=vX.Y.Z" }
}
info "Version: $Version"

$BASE = "https://github.com/$REPO/releases/download/$Version"
# REL-04: a fresh private directory, not a predictable name in %TEMP%.
$TMP = Join-Path ([System.IO.Path]::GetTempPath()) ("prism-install-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TMP | Out-Null

# ── Download ─────────────────────────────────────────────────────────────────
$FILE    = "$PRODUCT-$Version-windows-$ARCH.exe"
$tmpFile = Join-Path $TMP $FILE
info "Downloading $FILE…"
try {
  Invoke-WebRequest "$BASE/$FILE" -OutFile $tmpFile -ErrorAction Stop
} catch {
  if ($ARCH -ne "arm64") { throw }
  info "no windows-arm64 build in $Version; using windows-amd64 (runs under emulation)"
  $ARCH    = "amd64"
  $FILE    = "$PRODUCT-$Version-windows-$ARCH.exe"
  $tmpFile = Join-Path $TMP $FILE
  Invoke-WebRequest "$BASE/$FILE" -OutFile $tmpFile
}

# ── Checksum verification ────────────────────────────────────────────────────
$actual = (Get-FileHash $tmpFile -Algorithm SHA256).Hash.ToLower()
try {
  Invoke-WebRequest "$BASE/checksums.txt" -OutFile (Join-Path $TMP "checksums.txt") -ErrorAction Stop
} catch {
  die "Could not download checksums.txt; refusing an unverified install"
}
$lines    = Get-Content (Join-Path $TMP "checksums.txt")
$expected = ($lines | Where-Object { $_ -match "\s(\./)?$([regex]::Escape($FILE))$" }) -split '\s+' | Select-Object -First 1
if (-not $expected) { die "checksums.txt has no entry for $FILE" }
if ($expected.ToLower() -ne $actual) { die "CHECKSUM MISMATCH`n  expected: $expected`n  actual:   $actual" }
ok "Checksum verified"

# ── Provenance ───────────────────────────────────────────────────────────────
# checksums.txt comes from the same release as the binary; the build
# attestation proves the provasign/prism release workflow made it.
$provenance = $false
if (Get-Command gh -ErrorAction SilentlyContinue) {
  & gh auth status *> $null
  if ($LASTEXITCODE -eq 0) {
    & gh attestation verify $tmpFile --repo $REPO *> $null
    if ($LASTEXITCODE -ne 0) { die "build attestation did NOT verify for $FILE; refusing to install" }
    $provenance = $true
    ok "Provenance verified: GitHub build attestation"
  }
}
if (-not $provenance) {
  if ($env:PRISM_REQUIRE_PROVENANCE -eq "1") { die "provenance not verified (install gh and log in) and PRISM_REQUIRE_PROVENANCE=1" }
  info "provenance not verified (install a logged-in gh to check it); checksum only"
}

# ── Install ──────────────────────────────────────────────────────────────────
if ($StopServers) { Stop-RunningPrismMcps }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$target = Join-Path $InstallDir "$PRODUCT.exe"
# Windows will not overwrite a running .exe but will rename one: move the
# old binary aside, so running MCP servers keep working and switch to the
# new binary on their next request. Leftovers from earlier upgrades go
# when no process holds them.
Get-ChildItem $InstallDir -Filter "$PRODUCT.exe.old-*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
if (Test-Path $target) {
  Move-Item $target "$target.old-$(Get-Date -Format yyyyMMddHHmmss)" -Force
}
Copy-Item $tmpFile $target -Force
Remove-Item $TMP -Recurse -Force -ErrorAction SilentlyContinue
$installedVersion = (& "$InstallDir\$PRODUCT.exe" version 2>$null | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $installedVersion -ne "prism $Version") {
  die "Installed binary verification failed: expected 'prism $Version', got '$installedVersion'"
}
ok "$PRODUCT $Version → $InstallDir\$PRODUCT.exe"

# ── PATH registration ────────────────────────────────────────────────────────
$currentPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
if ($currentPath -notlike "*$InstallDir*") {
  [System.Environment]::SetEnvironmentVariable("PATH", "$InstallDir;$currentPath", "User")
  ok "Added $InstallDir to PATH (open a new terminal for it to take effect)"
}

# ── Legacy global-registration cleanup ──────────────────────────────────────────
info "Removing legacy user-global Prism MCP registrations…"
& "$InstallDir\$PRODUCT.exe" cleanup-global
if ($LASTEXITCODE -eq 0) {
  ok "legacy user-global Prism registrations removed"
} else {
  info "legacy global cleanup skipped (run: prism cleanup-global)"
}

Write-Host ""
Write-Host "$PRODUCT $Version installed."
Write-Host "AI tool note:"
Write-Host "  prism init sets agents up to run the prism command (CLI, the default). Restart or reload your coding agent / IDE after running init."
Write-Host "  If you choose MCP (prism init --interface mcp), approve the .mcp.json servers in Claude Code if prompted, then verify with: claude mcp list"
Write-Host ""
Write-Host "Next: cd \your\project; prism init; prism index"

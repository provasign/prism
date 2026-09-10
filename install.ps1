# Install Prism from GitHub Releases.
#
#   irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex
#
# Parameters (pass as env vars before piping, or dot-source and call directly):
#   $env:VERSION       release tag to install   (default: latest)
#   $env:INSTALL_DIR   install directory         (default: $HOME\bin)
#
# Supported platforms: windows-amd64 (ARM64 Windows runs the amd64 build via emulation)
#
# Note: Prism embeds Grove as a library — no separate grove installation is
# required for Prism to function.
[CmdletBinding()]
param(
  [string]$Version    = $env:VERSION,
  [string]$InstallDir = $env:INSTALL_DIR
)
$ErrorActionPreference = "Stop"

$PRODUCT = "prism"
$REPO    = "provasign/prism"
$ARCH    = "amd64"

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

$FILE    = "$PRODUCT-$Version-windows-$ARCH.exe"
$BASE    = "https://github.com/$REPO/releases/download/$Version"
$TMP     = [System.IO.Path]::GetTempPath()
$tmpFile = "$TMP$FILE"

# ── Download ─────────────────────────────────────────────────────────────────
info "Downloading $FILE…"
Invoke-WebRequest "$BASE/$FILE" -OutFile $tmpFile

# ── Checksum verification ────────────────────────────────────────────────────
$actual = (Get-FileHash $tmpFile -Algorithm SHA256).Hash.ToLower()
try {
  Invoke-WebRequest "$BASE/checksums.txt" -OutFile "$TMP\provasign-checksums.txt" -ErrorAction Stop
} catch {
  die "Could not download checksums.txt; refusing an unverified install"
}
$lines    = Get-Content "$TMP\provasign-checksums.txt"
$expected = ($lines | Where-Object { $_ -match "\s(\./)?$([regex]::Escape($FILE))$" }) -split '\s+' | Select-Object -First 1
if (-not $expected) { die "checksums.txt has no entry for $FILE" }
if ($expected.ToLower() -ne $actual) { die "CHECKSUM MISMATCH`n  expected: $expected`n  actual:   $actual" }
ok "Checksum verified"

# ── Install ──────────────────────────────────────────────────────────────────
Stop-RunningPrismMcps
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item $tmpFile "$InstallDir\$PRODUCT.exe" -Force
Remove-Item $tmpFile -ErrorAction SilentlyContinue
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

# ── Global AI tool registration ──────────────────────────────────────────────
info "Registering prism with detected AI coding tools (global)…"
& "$InstallDir\$PRODUCT.exe" init --global 2>$null
if ($LASTEXITCODE -eq 0) {
  ok "prism registered globally with detected AI tools"
} else {
  info "prism global init skipped (run: prism init --global)"
}

Write-Host ""
Write-Host "$PRODUCT $Version installed."
Write-Host "AI tool note:"
Write-Host "  Restart or reload your coding agent / IDE so it respawns MCP servers from the updated config."
Write-Host "  For Claude Code, approve the .mcp.json servers if prompted, then verify with: claude mcp list"
Write-Host ""
Write-Host "Next: cd \your\project; prism init; prism index"

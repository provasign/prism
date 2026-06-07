# Uninstall Prism from a GitHub Releases install.
#
#   irm https://raw.githubusercontent.com/provasign/prism/main/uninstall.ps1 | iex
#
# Parameters:
#   $env:INSTALL_DIR   directory where prism was installed   (default: $HOME\bin)
#
[CmdletBinding()]
param(
  [string]$InstallDir = $env:INSTALL_DIR
)
$ErrorActionPreference = "Stop"

$PRODUCT = "prism"
if (-not $InstallDir) { $InstallDir = "$env:USERPROFILE\bin" }

function ok($msg)   { Write-Host "✅ $msg" -ForegroundColor Green }
function info($msg) { Write-Host "==> $msg" -ForegroundColor Blue }

$prismExe = "$InstallDir\$PRODUCT.exe"

if (Test-Path $prismExe) {
  Remove-Item $prismExe -Force
  ok "removed $prismExe"
} else {
  info "$prismExe : not found (already removed?)"
}

Write-Host ""
Write-Host "$PRODUCT uninstalled from $InstallDir"

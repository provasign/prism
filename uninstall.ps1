# Uninstall Prism from GitHub Releases install.
#
#   irm https://raw.githubusercontent.com/provasign/prism/main/uninstall.ps1 | iex
#
# Parameters (pass as env vars or dot-source):
#   $env:INSTALL_DIR   directory where prism was installed   (default: $HOME\bin)
#   $env:PROJECT       project dir to deregister MCP from    (default: none)
#
[CmdletBinding()]
param(
  [string]$InstallDir = $env:INSTALL_DIR,
  [string]$Project    = $env:PROJECT
)
$ErrorActionPreference = "Stop"
$PRODUCT = "prism"
if (-not $InstallDir) { $InstallDir = "$env:USERPROFILE\bin" }

function ok($msg)   { Write-Host "✅ $msg" -ForegroundColor Green }
function info($msg) { Write-Host "==> $msg" -ForegroundColor Blue }

$prismExe = "$InstallDir\$PRODUCT.exe"

# Remove project-local .mcp.json entry
if ($Project -and (Test-Path "$Project\.mcp.json")) {
  $mcp = Get-Content "$Project\.mcp.json" | ConvertFrom-Json
  if ($mcp.mcpServers.PSObject.Properties.Name -contains "prism") {
    $mcp.mcpServers.PSObject.Properties.Remove("prism")
    $mcp | ConvertTo-Json -Depth 10 | Set-Content "$Project\.mcp.json"
    ok "removed prism from $Project\.mcp.json"
  }
}

# Remove binary
if (Test-Path $prismExe) {
  Remove-Item $prismExe -Force
  ok "removed $prismExe"
} else {
  info "$prismExe : not found (already removed?)"
}

Write-Host ""
Write-Host "$PRODUCT uninstalled from $InstallDir"

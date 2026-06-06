# Uninstall Prism from GitHub Releases install.
#
#   irm https://raw.githubusercontent.com/provasign/prism/main/uninstall.ps1 | iex
#
# Parameters (pass as env vars or dot-source):
#   $env:INSTALL_DIR   directory where prism was installed   (default: $HOME\bin)
#   $env:PROJECT       project dir to deregister MCP from    (default: none)
#   $env:KILL_MCPS     set to "1" to stop running MCP processes; "0" to skip
#
[CmdletBinding()]
param(
  [string]$InstallDir = $env:INSTALL_DIR,
  [string]$Project    = $env:PROJECT,
  [string]$KillMCPs   = $env:KILL_MCPS
)
$ErrorActionPreference = "Stop"
$PRODUCT = "prism"
if (-not $InstallDir) { $InstallDir = "$env:USERPROFILE\bin" }

function ok($msg)   { Write-Host "✅ $msg" -ForegroundColor Green }
function info($msg) { Write-Host "==> $msg" -ForegroundColor Blue }

function Test-ShouldKillMCPs {
  if ($KillMCPs -match '^(1|true|yes)$') { return $true }
  if ($KillMCPs -match '^(0|false|no)$') { return $false }
  if ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
    $reply = Read-Host "Stop running prism MCP processes now? [y/N]"
    return $reply -match '^(y|yes)$'
  }
  info "Skipping running MCP process termination; set KILL_MCPS=1 to stop it during non-interactive uninstall."
  return $false
}

function Remove-JsonServerEntry {
  param(
    [string]$Path,
    [string]$Key,
    [string]$Name
  )
  if (-not (Test-Path $Path)) { return }
  try {
    $raw = Get-Content $Path -Raw
    if (-not $raw) { return }
    $doc = $raw | ConvertFrom-Json
    $changed = $false
    if ($doc.PSObject.Properties.Name -contains $Key) {
      $v = $doc.$Key
      if ($v -is [System.Collections.IDictionary]) {
        if ($v.PSObject.Properties.Name -contains $Name) {
          $v.PSObject.Properties.Remove($Name)
          $changed = $true
        }
      } elseif ($v -is [System.Collections.IEnumerable] -and -not ($v -is [string])) {
        $newItems = @($v | Where-Object { -not (($_ -is [psobject]) -and ($_.PSObject.Properties.Name -contains "name") -and ($_.name -eq $Name)) })
        if ($newItems.Count -ne @($v).Count) {
          $doc | Add-Member -NotePropertyName $Key -NotePropertyValue $newItems -Force
          $changed = $true
        }
      }
    }
    if ($changed) {
      $doc | ConvertTo-Json -Depth 20 | Set-Content $Path
      ok "removed $Name from $Path"
    }
  } catch {
    Write-Verbose "could not clean $Path: $_"
  }
}

function Remove-CodexMCPEntries {
  $path = Join-Path $env:USERPROFILE ".codex\config.toml"
  if (-not (Test-Path $path)) { return }
  $lines = Get-Content $path
  $out = New-Object System.Collections.Generic.List[string]
  for ($i = 0; $i -lt $lines.Count; ) {
    $line = $lines[$i].Trim()
    if ($line -eq "[mcp_servers.prism]") {
      $i++
      while ($i -lt $lines.Count -and -not $lines[$i].TrimStart().StartsWith("[")) { $i++ }
      continue
    }
    if ($line -eq "[[mcp_servers]]") {
      $j = $i + 1
      $name = ""
      while ($j -lt $lines.Count -and -not $lines[$j].TrimStart().StartsWith("[")) {
        if ($lines[$j] -match '^\s*name\s*=\s*"([^"]+)"') { $name = $Matches[1] }
        $j++
      }
      if ($name -eq "prism") {
        $i = $j
        continue
      }
      for ($k = $i; $k -lt $j; $k++) { $out.Add($lines[$k]) }
      $i = $j
      continue
    }
    $out.Add($lines[$i])
    $i++
  }
  Set-Content $path $out
  ok "cleaned Codex MCP entries in $path"
}

$prismExe = "$InstallDir\$PRODUCT.exe"

if (Test-ShouldKillMCPs) {
  info "Stopping running prism MCP processes…"
  Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.CommandLine -match '(^|[\\/ ])prism(\.exe)?([ ]|$).*mcp([ ]|$)' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  ok "requested process termination"
}

info "Deregistering prism from AI tool configs…"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".claude.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".cursor\mcp.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".windsurf\mcp.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".codeium\windsurf\mcp_config.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".continue\config.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".config\zed\settings.json") -Key "context_servers" -Name "prism"
Remove-CodexMCPEntries

$claudeSettings = Join-Path $env:USERPROFILE ".claude\settings.json"
if (Test-Path $claudeSettings) {
  try {
    $settings = (Get-Content $claudeSettings -Raw) | ConvertFrom-Json
    $servers = @($settings.enabledMcpjsonServers)
    if ($servers -contains "prism") {
      $settings.enabledMcpjsonServers = @($servers | Where-Object { $_ -ne "prism" })
      $settings | ConvertTo-Json -Depth 20 | Set-Content $claudeSettings
      ok "removed prism from enabledMcpjsonServers"
    }
  } catch {
    Write-Verbose "could not update $claudeSettings: $_"
  }
}

# Remove project-local .mcp.json entry
if ($Project) {
  Remove-JsonServerEntry -Path "$Project\.mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.cursor\mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.windsurf\mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.vscode\mcp.json" -Key "servers" -Name "prism"
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

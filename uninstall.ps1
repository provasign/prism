# Uninstall Prism from GitHub Releases install.
#
#   irm https://raw.githubusercontent.com/provasign/prism/main/uninstall.ps1 | iex
#
# Parameters (pass as env vars or dot-source):
#   $env:INSTALL_DIR   directory where prism was installed   (default: $HOME\bin)
#   $env:PROJECT       project dir to deregister MCP and steering from (default: none)
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

# ── JSON helpers ──────────────────────────────────────────────────────────────

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

# Delete a config file if the given key is now empty (no entries remain).
function Remove-IfEmptyMcpConfig {
  param([string]$Path, [string]$Key)
  if (-not (Test-Path $Path)) { return }
  try {
    $doc = (Get-Content $Path -Raw) | ConvertFrom-Json
    if ($doc.PSObject.Properties.Name -contains $Key) {
      $v = $doc.$Key
      $count = 0
      if ($v -is [System.Collections.IEnumerable] -and -not ($v -is [string])) {
        $count = @($v).Count
      } elseif ($null -ne $v) {
        $count = @($v.PSObject.Properties).Count
      }
      if ($count -eq 0) {
        Remove-Item $Path -Force
        ok "removed empty $Path"
      }
    }
  } catch {
    Write-Verbose "could not check $Path: $_"
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

# ── Steering helper ───────────────────────────────────────────────────────────

# Remove the "## Prism — context delivery" section from an agent instruction
# file. If the file contained only that section it is deleted entirely.
function Remove-SteeringSection {
  param([string]$Path)
  if (-not (Test-Path $Path)) { return }
  $marker = "## Prism — context delivery"
  $content = Get-Content $Path -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
  if (-not $content -or -not $content.Contains($marker)) { return }

  # Section starts at the marker — always at start of file or preceded by newline.
  $idx = $content.IndexOf("`n" + $marker)
  if ($idx -lt 0 -and $content.StartsWith($marker)) { $idx = 0 }
  if ($idx -lt 0) { return }

  $before = $content.Substring(0, $idx).TrimEnd([char]"`r", [char]"`n")
  if (-not $before) {
    Remove-Item $Path -Force
    ok "removed $Path (was only prism steering)"
  } else {
    [System.IO.File]::WriteAllText($Path, $before + "`n", [System.Text.Encoding]::UTF8)
    ok "removed prism steering section from $Path"
  }
}

# ── PATH helper ───────────────────────────────────────────────────────────────

# Remove InstallDir from the User-scope PATH in the registry.
function Remove-PathEntry {
  param([string]$Dir)
  try {
    $userPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
    if (-not $userPath) { return }
    $parts = $userPath -split ';' | Where-Object { $_ -ne $Dir -and $_ -ne "$Dir\" }
    $newPath = ($parts | Where-Object { $_ }) -join ';'
    if ($newPath -ne $userPath) {
      [System.Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
      ok "removed $Dir from User PATH"
    }
  } catch {
    Write-Verbose "could not update PATH: $_"
  }
}

# ── Process kill ──────────────────────────────────────────────────────────────

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

# ── Main ──────────────────────────────────────────────────────────────────────

$prismExe = "$InstallDir\$PRODUCT.exe"

if (Test-ShouldKillMCPs) {
  info "Stopping running prism MCP processes…"
  Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.CommandLine -match '(^|[\\/ ])prism(\.exe)?([ ]|$).*mcp([ ]|$)' } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  ok "requested process termination"
}

info "Removing PATH entry…"
Remove-PathEntry -Dir $InstallDir

info "Deregistering prism from global AI tool configs…"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".claude.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".cursor\mcp.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".windsurf\mcp.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".codeium\windsurf\mcp_config.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".continue\config.json") -Key "mcpServers" -Name "prism"
Remove-JsonServerEntry -Path (Join-Path $env:USERPROFILE ".config\zed\settings.json") -Key "context_servers" -Name "prism"
Remove-CodexMCPEntries

# Remove global MCP config files that are now empty
Remove-IfEmptyMcpConfig -Path (Join-Path $env:USERPROFILE ".claude.json") -Key "mcpServers"
Remove-IfEmptyMcpConfig -Path (Join-Path $env:USERPROFILE ".cursor\mcp.json") -Key "mcpServers"
Remove-IfEmptyMcpConfig -Path (Join-Path $env:USERPROFILE ".windsurf\mcp.json") -Key "mcpServers"

# Remove prism from Claude Code's enabledMcpjsonServers
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

# ── Project-local cleanup ─────────────────────────────────────────────────────

if ($Project) {
  info "Removing prism from project in $Project…"

  # MCP config files
  Remove-JsonServerEntry -Path "$Project\.mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.cursor\mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.windsurf\mcp.json" -Key "mcpServers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.vscode\mcp.json" -Key "servers" -Name "prism"
  Remove-JsonServerEntry -Path "$Project\.kiro\settings\mcp.json" -Key "mcpServers" -Name "prism"

  # Remove project MCP config files that are now empty
  Remove-IfEmptyMcpConfig -Path "$Project\.mcp.json" -Key "mcpServers"
  Remove-IfEmptyMcpConfig -Path "$Project\.cursor\mcp.json" -Key "mcpServers"
  Remove-IfEmptyMcpConfig -Path "$Project\.windsurf\mcp.json" -Key "mcpServers"
  Remove-IfEmptyMcpConfig -Path "$Project\.vscode\mcp.json" -Key "servers"
  Remove-IfEmptyMcpConfig -Path "$Project\.kiro\settings\mcp.json" -Key "mcpServers"

  # Steering instruction files
  info "Removing prism steering instructions from agent files…"
  Remove-SteeringSection -Path "$Project\CLAUDE.md"
  Remove-SteeringSection -Path "$Project\AGENTS.md"
  Remove-SteeringSection -Path "$Project\GEMINI.md"
  Remove-SteeringSection -Path "$Project\.cursorrules"
  Remove-SteeringSection -Path "$Project\.windsurfrules"
  Remove-SteeringSection -Path "$Project\.clinerules"
  Remove-SteeringSection -Path "$Project\.github\copilot-instructions.md"
  Remove-SteeringSection -Path "$Project\.devin\instructions.md"
  # .kiro/steering/prism.md is a prism-owned file — delete it entirely
  $kiroSteering = "$Project\.kiro\steering\prism.md"
  if (Test-Path $kiroSteering) {
    Remove-Item $kiroSteering -Force
    ok "removed $kiroSteering"
  }

  # prism.yaml
  $prismYaml = "$Project\prism.yaml"
  if (Test-Path $prismYaml) {
    Remove-Item $prismYaml -Force
    ok "removed $prismYaml"
  }
}

# ── Binary ────────────────────────────────────────────────────────────────────

# Auto-detect binary location if not in InstallDir
if (-not (Test-Path $prismExe)) {
  $detected = Get-Command prism -ErrorAction SilentlyContinue
  if ($detected) {
    $prismExe = $detected.Source
    info "Found prism at $prismExe (not in default InstallDir)"
  }
}

if (Test-Path $prismExe) {
  Remove-Item $prismExe -Force
  ok "removed $prismExe"
} else {
  info "prism binary not found (already removed?)"
}

# ── Cache / ledger ────────────────────────────────────────────────────────────

$cacheDir = Join-Path $env:LOCALAPPDATA "prism"
if (Test-Path $cacheDir) {
  Remove-Item $cacheDir -Recurse -Force
  ok "removed cache directory $cacheDir"
}

Write-Host ""
Write-Host "$PRODUCT uninstalled."

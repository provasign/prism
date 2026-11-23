# Parse each PowerShell script given; print every syntax error and exit 1
# if there is one. Used by CI (lint job) for install.ps1 and uninstall.ps1.
$failed = $false
foreach ($f in $args) {
    $tokens = $null
    $errors = $null
    $path = (Resolve-Path -LiteralPath $f).Path
    [void][System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors)
    if ($errors -and $errors.Count -gt 0) {
        Write-Host "${f}: $($errors.Count) syntax error(s)"
        foreach ($e in $errors) {
            Write-Host "  line $($e.Extent.StartLineNumber): $($e.Message)"
        }
        $failed = $true
    } else {
        Write-Host "${f}: parses"
    }
}
if ($failed) { exit 1 }
exit 0

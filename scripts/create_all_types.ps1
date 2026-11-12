# Create one temporary connection per creatable backend, so the credential panel
# can be checked for EVERY type instead of the one that happened to be looked at.
#
# Usage (point it at a daemon whose rclone config is DISPOSABLE):
#   .\.scratch\weftsync-ui.exe daemon .scratch\demo\job.toml --config .scratch\check.conf --addr 127.0.0.1:5740
#   .\scripts\create_all_types.ps1 -Base http://127.0.0.1:5740
#   # ... open the page, run scripts/check_field_layout.js ...
#   .\scripts\create_all_types.ps1 -Base http://127.0.0.1:5740 -Cleanup
#
# Names are prefixed chk- so cleanup finds them without touching anything the user
# made themselves.
param(
    [string]$Base = 'http://127.0.0.1:5740',
    [switch]$Cleanup
)

$ErrorActionPreference = 'Stop'

function Prefix([string]$name) { 'chk-' + ($name -replace '[^a-zA-Z0-9]+', '-') }

if ($Cleanup) {
    $list = Invoke-RestMethod "$Base/api/credentials"
    $gone = 0
    foreach ($r in $list.remotes) {
        if ($r.name -notlike 'chk-*') { continue }
        try {
            Invoke-RestMethod -Method Delete -Uri "$Base/api/credentials?name=$($r.name)&force=1" | Out-Null
            $gone++
        } catch {
            Write-Output ('  cleanup failed: ' + $r.name + ': ' + $_.Exception.Message)
        }
    }
    Write-Output "deleted=$gone"
    exit 0
}

$list = Invoke-RestMethod "$Base/api/credentials"
$created = @()
$failed = @()
$existing = @{}
foreach ($r in $list.remotes) { $existing[$r.name] = $true }

foreach ($t in $list.types) {
    if ($t.pending) { continue }
    $name = Prefix $t.name
    if ($existing.ContainsKey($name)) { continue }
    try {
        Invoke-RestMethod -Method Post -Uri "$Base/api/credentials?name=$name&type=$($t.name)" | Out-Null
        $created += $name
    } catch {
        $failed += ($t.name + ': ' + $_.Exception.Message)
    }
}

Write-Output ('created=' + $created.Count)
Write-Output ('failed=' + $failed.Count)
$failed | ForEach-Object { Write-Output ('  ' + $_) }

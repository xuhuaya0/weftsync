# Build the custom rclone binary with the Feishu backend.
#
# Pure ASCII on purpose: Windows PowerShell 5.1 reads .ps1 as GBK,
# so non-ASCII characters produce misleading parse errors.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/build-go.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/build-go.ps1 -Target linux

param(
    [ValidateSet('windows', 'linux', 'both')]
    [string]$Target = 'windows',

    [string]$OutDir = 'dist'
)

$ErrorActionPreference = 'Stop'

# Locate the Go toolchain even if PATH has not been refreshed.
$go = 'C:\Program Files\Go\bin\go.exe'
if (-not (Test-Path $go)) {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) { $go = $cmd.Source }
}
if (-not (Test-Path $go)) {
    Write-Error 'go.exe not found. Install Go first (winget install GoLang.Go).'
    exit 2
}

$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$goDir = Join-Path $repoRoot 'src/go'
$distDir = Join-Path $repoRoot $OutDir

if (-not (Test-Path $distDir)) { New-Item -ItemType Directory -Path $distDir | Out-Null }

Push-Location $goDir
try {
    Write-Host "go     : $(& $go version)"
    Write-Host "module : $(Get-Location)"
    Write-Host ''

    foreach ($os in @($Target)) {
        if ($os -eq 'both') {
            $targets = @('windows', 'linux')
        } else {
            $targets = @($os)
        }

        foreach ($t in $targets) {
            $ext = if ($t -eq 'windows') { '.exe' } else { '' }
            $out = Join-Path $distDir "weftsync-$t-amd64$ext"

            Write-Host "building $t/amd64 -> $out"
            $env:GOOS = $t
            $env:GOARCH = 'amd64'
            $env:CGO_ENABLED = '0'

            & $go build -trimpath -ldflags '-s -w' -o $out .
            if ($LASTEXITCODE -ne 0) {
                Write-Error "build failed for $t (exit $LASTEXITCODE)"
                exit 1
            }

            $size = [math]::Round((Get-Item $out).Length / 1MB, 1)
            Write-Host "  ok, $size MB"
        }
    }
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

Write-Host ''
Write-Host 'done.'

# build.ps1 - rebuild one service (default gameserver) with the repo's standard flags
#
# Usage:
#   .\tools\build.ps1              # gameserver
#   .\tools\build.ps1 gateway      # any of gameserver/gateway/admin/seed/launcher
#   .\tools\build.ps1 all
#
# Important: keep -s -w. An unstripped 26MB build was blocked by Windows
# Smart App Control ("An Application Control policy has blocked this file");
# the stripped build runs normally.
param([string]$Target = 'gameserver')

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root

$go = $null
$cmd = Get-Command go -ErrorAction SilentlyContinue
if ($cmd) { $go = $cmd.Source }
if (-not $go) {
    foreach ($p in @('C:\Program Files\Go\bin\go.exe', 'C:\Go\bin\go.exe')) {
        if (Test-Path $p) { $go = $p; break }
    }
}
if (-not $go) { throw 'go not found (install Go or add it to PATH)' }

$version = Get-Date -Format 'yyyyMMddHHmmss'
$ld = "-s -w -X 'rf4server/internal/buildinfo.Version=$version'"
$targets = if ($Target -eq 'all') { @('gateway', 'gameserver', 'admin', 'seed') } else { @($Target) }

foreach ($t in $targets) {
    $out = "bin/$t.exe"
    & $go build -trimpath -ldflags $ld -o $out "./cmd/$t"
    if ($LASTEXITCODE -ne 0) { throw "build $t failed" }
    Write-Host ("built {0}  ({1:N1} MB)" -f $out, ((Get-Item $out).Length / 1MB)) -ForegroundColor Green
}

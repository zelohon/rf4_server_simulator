# clientlog.ps1 - extract RF4 client (Unity) exceptions from Player.log
#
# Usage:
#   .\tools\clientlog.ps1                 # last 10 exception blocks
#   .\tools\clientlog.ps1 -Last 30 -Frames 6
#   .\tools\clientlog.ps1 -Since '12:01'  # only blocks after that time
#
# Player.log lives under LocalLow and is often flushed late; pass -Prev to read
# Player-prev.log instead.
param(
    [int]$Last = 10,
    [int]$Frames = 6,
    [string]$Since = '',
    [switch]$Prev,
    [string]$Path = ''
)

$ErrorActionPreference = 'Stop'
if (-not $Path) {
    $dir = Join-Path $env:USERPROFILE 'AppData\LocalLow\Russian Fishing LLC\Russian Fishing 4'
    $name = if ($Prev) { 'Player-prev.log' } else { 'Player.log' }
    $Path = Join-Path $dir $name
}
if (-not (Test-Path $Path)) {
    Write-Host ('no log: ' + $Path) -ForegroundColor Red
    Write-Host 'start the game, reproduce the bug, then quit the game (the log is written on exit).'
    return
}

$lines = Get-Content $Path -Encoding UTF8
$blocks = @()
for ($i = 0; $i -lt $lines.Count; $i++) {
    $ln = $lines[$i]
    if ($ln -match '^\d{4}\.\d{2}\.\d{2} \d{2}:\d{2}:\d{2}\.\d+\+\d{2}:\d{2} (System\.\w+Exception|.*Exception:)') {
        $ts = $ln.Substring(0, 19)
        $type = ($ln -split ' ', 3)[2]
        $frameList = @()
        $j = $i + 1
        while ($j -lt $lines.Count -and $frameList.Count -lt $Frames -and $lines[$j] -match '^\s+at ') {
            $frameList += $lines[$j].Trim()
            $j++
        }
        $blocks += [pscustomobject]@{ Time = $ts; Type = $type; Frames = $frameList }
    }
}
if ($Since) {
    $blocks = $blocks | Where-Object { $_.Time -ge $Since }
}
if ($blocks.Count -eq 0) {
    Write-Host ('no exceptions found in ' + $Path) -ForegroundColor Yellow
    return
}

$show = $blocks | Select-Object -Last $Last
Write-Host ('file: ' + $Path) -ForegroundColor DarkGray
Write-Host ('exceptions: ' + $blocks.Count + '  (showing last ' + $show.Count + ')') -ForegroundColor Cyan
foreach ($b in $show) {
    Write-Host ''
    Write-Host ($b.Time + '  ' + $b.Type) -ForegroundColor Red
    foreach ($f in $b.Frames) { Write-Host ('    ' + $f) -ForegroundColor Gray }
}

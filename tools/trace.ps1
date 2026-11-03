# trace.ps1 - capture RF4 client<->server command exchanges
#
# Usage:
#   .\tools\trace.ps1                      # watch 120s, hide noisy commands
#   .\tools\trace.ps1 -Seconds 300 -Filter '21/|22/'
#   .\tools\trace.ps1 -Restart             # restart gameserver first, then watch
#
# Noise (24/1 social/move etc.) is filtered out by default; the launcher ring
# buffer only keeps ~800 lines, so this polls fast and writes matches to
# logs/_trace.txt while also printing them.
param(
    [string]$Name = 'gameserver',
    [int]$Seconds = 120,
    [string]$Filter = '',
    [switch]$Restart,
    [string]$Out = '',
    [int]$PollMs = 200
)

$ErrorActionPreference = 'Continue'
$root = Split-Path $PSScriptRoot -Parent
$base = 'http://127.0.0.1:9500'

# Commands that flood the log; hidden unless -Filter mentions them explicitly.
$noise = @('cmd=24/1', 'cmd=14/12', 'cmd=2/2', 'cmd=2/1', 'cmd=14/7', '14/12 鱼状态')

if (-not $Out) { $Out = Join-Path $root 'logs\_trace.txt' }

if ($Restart) {
    Write-Host ('restarting ' + $Name + ' ...')
    try {
        Invoke-WebRequest ($base + '/api/restart?name=' + $Name) -Method POST -UseBasicParsing -TimeoutSec 30 | Out-Null
    } catch {
        Write-Host ('restart failed: ' + $_.Exception.Message) -ForegroundColor Red
    }
    Start-Sleep -Seconds 6
}

$header = 'trace start ' + (Get-Date -Format 'HH:mm:ss') + '  filter=' + $Filter
[IO.File]::WriteAllText($Out, $header + "`r`n", (New-Object Text.UTF8Encoding($false)))
Write-Host $header -ForegroundColor Cyan
Write-Host ('writing to ' + $Out) -ForegroundColor DarkGray
Write-Host 'reproduce the bug now; press Ctrl+C to stop early.' -ForegroundColor Yellow

$next = 0
$seen = @{}
$deadline = (Get-Date).AddSeconds($Seconds)
$hits = 0

while ((Get-Date) -lt $deadline) {
    try {
        $j = (Invoke-WebRequest ($base + '/api/logs?name=' + $Name + '&since=' + $next) -UseBasicParsing -TimeoutSec 5).Content | ConvertFrom-Json
    } catch {
        Start-Sleep -Milliseconds $PollMs
        continue
    }
    if ($j.lines -and $j.lines.Count -gt 0) {
        foreach ($line in $j.lines) {
            if ($seen.ContainsKey($line)) { continue }
            $seen[$line] = 1
            if ($Filter -ne '' -and $line -notmatch $Filter) { continue }
            if ($Filter -eq '') {
                $skip = $false
                foreach ($n in $noise) { if ($line.Contains($n)) { $skip = $true; break } }
                if ($skip) { continue }
            }
            $hits++
            Write-Host $line
            [IO.File]::AppendAllText($Out, $line + "`r`n", (New-Object Text.UTF8Encoding($false)))
        }
    }
    if ($null -ne $j.next) { $next = [int]$j.next }
    Start-Sleep -Milliseconds $PollMs
}

$footer = 'trace end ' + (Get-Date -Format 'HH:mm:ss') + '  lines=' + $hits
[IO.File]::AppendAllText($Out, $footer + "`r`n", (New-Object Text.UTF8Encoding($false)))
Write-Host $footer -ForegroundColor Cyan

# hosts.ps1 - 在"本地服"和"官方服"之间切换 api.rf4game.ru 的解析
#
# 用法：
#   .\tools\hosts.ps1 local      # 指向 127.0.0.1（我们的网关）
#   .\tools\hosts.ps1 official   # 注释掉本项目的行，恢复官方解析
#   .\tools\hosts.ps1 status     # 查看当前状态
#
# 每次修改前会备份到 hosts.rf4s-backup-<时间戳>，可用 -Restore <文件> 还原。
param(
    [Parameter(Position = 0)][ValidateSet('local', 'official', 'status')][string]$Mode = 'status',
    [string]$Restore = ''
)

$ErrorActionPreference = 'Stop'
$hosts = Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
$marker = '# rf4s-local'
$lines = @(
    '127.0.0.1 api.rf4game.ru',
    '127.0.0.1 api.rf4game.com'
)

if ($Restore) {
    Copy-Item $Restore $hosts -Force
    Write-Host ('restored from ' + $Restore) -ForegroundColor Green
    return
}

$text = Get-Content $hosts -Raw -Encoding UTF8
if ($null -eq $text) { $text = '' }
$current = @($text -split "`r?`n")

function Show-Status {
    $active = @($current | Where-Object { $_ -match '^\s*127\.0\.0\.1\s+api\.rf4game\.' })
    $disabled = @($current | Where-Object { $_ -match ('^\s*' + [regex]::Escape($marker)) })
    if ($active.Count -gt 0) {
        Write-Host '当前: 本地服（api.rf4game.ru -> 127.0.0.1）' -ForegroundColor Yellow
        $active | ForEach-Object { Write-Host ('  ' + $_) }
    } else {
        Write-Host '当前: 官方服（本项目的 hosts 行已注释/不存在）' -ForegroundColor Green
    }
    if ($disabled.Count -gt 0) { Write-Host ('已注释行: ' + $disabled.Count) -ForegroundColor DarkGray }
}

if ($Mode -eq 'status') { Show-Status; return }

$backup = Join-Path (Split-Path $hosts) ('hosts.rf4s-backup-' + (Get-Date -Format 'yyyyMMddHHmmss'))
Copy-Item $hosts $backup -Force
Write-Host ('backup -> ' + $backup) -ForegroundColor DarkGray

if ($Mode -eq 'local') {
    $out = @()
    foreach ($ln in $current) {
        $t = $ln.Trim()
        if ($t -match ('^' + [regex]::Escape($marker))) { $out += ($t -replace ('^' + [regex]::Escape($marker) + '\s*'), '') }
        else { $out += $ln }
    }
    $has = @($out | Where-Object { $_ -match '^\s*127\.0\.0\.1\s+api\.rf4game\.ru' }).Count
    if ($has -eq 0) { $out += $lines }
    else {
        $out = @($out | Where-Object { $_ -notmatch '^\s*127\.0\.0\.1\s+api\.rf4game\.(ru|com)' })
        $out += $lines
    }
    [IO.File]::WriteAllText($hosts, (($out -join "`r`n") + "`r`n"), (New-Object Text.UTF8Encoding($false)))
    Write-Host '已切换: 本地服' -ForegroundColor Yellow
} else {
    $out = @()
    foreach ($ln in $current) {
        if ($ln -match '^\s*127\.0\.0\.1\s+api\.rf4game\.(ru|com)') { $out += ($marker + ' ' + $ln.Trim()) }
        else { $out += $ln }
    }
    [IO.File]::WriteAllText($hosts, (($out -join "`r`n") + "`r`n"), (New-Object Text.UTF8Encoding($false)))
    Write-Host '已切换: 官方服（用自己的账号正常登录即可）' -ForegroundColor Green
}
Write-Host ''
Show-Status

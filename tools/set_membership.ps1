# 在测试机上运行：给指定账号开通高级会员
# 用法：保存为 set_membership.ps1，右键“使用 PowerShell 运行”

$ErrorActionPreference = "Stop"
$base = "http://127.0.0.1:8080"

$pass = Read-Host "请输入管理员密码（打包时生成的，在 快速开始.txt 里）"
try {
    $loginBody = @{ username = "admin"; password = $pass } | ConvertTo-Json
    $login = Invoke-RestMethod -Uri "$base/api/login" -Method Post -ContentType "application/json" -Body $loginBody
    $token = $login.token
} catch {
    Write-Host "登录失败：$($_.Exception.Message)" -ForegroundColor Red
    Read-Host "按回车退出"
    exit 1
}
Write-Host "登录成功" -ForegroundColor Green

$h = @{ Authorization = "Bearer $token" }

Write-Host "`n=== 当前玩家列表 ===" -ForegroundColor Cyan
$players = Invoke-RestMethod -Uri "$base/api/players" -Headers $h
$players | Format-Table uid, name, level, silver, gold -AutoSize

$uidInput = Read-Host "`n输入要开通会员的 uid"
$daysInput = Read-Host "天数（0 = 永久，默认 365）"
if (-not $daysInput) { $daysInput = "365" }

$body = @{ uid = [int64]$uidInput; days = [int]$daysInput } | ConvertTo-Json
try {
    $r = Invoke-RestMethod -Uri "$base/api/players/membership/grant" -Method Post -ContentType "application/json" -Headers $h -Body $body
    Write-Host "`n开通成功" -ForegroundColor Green
    Write-Host "  membership      = $($r.membership)   （1=计时 2=永久）"
    Write-Host "  到期时间戳       = $($r.membership_expire_at)   （永久为 0）"
    Write-Host "  剩余天数         = $($r.days_left)"
} catch {
    Write-Host "开通失败：$($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n现在请完全退出游戏客户端（任务管理器确认 rf4_x64.exe 已结束），再重新登录看顶栏。" -ForegroundColor Yellow
Read-Host "`n按回车退出"

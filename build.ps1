# 构建 Windows 服务端程序和 launcher，输出到 bin/。
$ErrorActionPreference = "Stop"

$version = Get-Date -Format "yyyyMMddHHmmss"
$ldflags = "-s -w -X 'rf4server/internal/buildinfo.Version=$version'"
New-Item -ItemType Directory -Force -Path "bin" | Out-Null

foreach ($cmd in @("gateway", "gameserver", "seed")) {
    go build -trimpath -ldflags $ldflags -o "bin/$cmd.exe" "./cmd/$cmd"
    if ($LASTEXITCODE -ne 0) { throw "build $cmd failed" }
    Write-Host "build bin/$cmd.exe ok"
}

# launcher 是托盘程序，不显示控制台窗口。
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "bin/launcher.exe" "./cmd/launcher"
if ($LASTEXITCODE -ne 0) { throw "build launcher failed" }
Write-Host "build bin/launcher.exe ok"

# 新架构不再生成 admin.exe；清理旧版本遗留，避免误启动独立管理端口。
Remove-Item -LiteralPath "bin/admin.exe" -Force -ErrorAction SilentlyContinue
Write-Host "done -> bin/"

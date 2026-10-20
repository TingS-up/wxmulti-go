# 编译脚本：一条命令出 exe
#   .\build.ps1            普通编译（产物在 已编译成品\）
#   .\build.ps1 -Clean     先清 已编译成品 目录（注意：里面的 data 也会一起没）
#   .\build.ps1 -Run       编译完顺手运行
#   .\build.ps1 -Upx       编译完再用 upx 压一遍（需要 upx 在 PATH 里，
#                          11 MB 能压到 4 MB 左右；代价是启动慢一点、杀软可能误报）
param(
    [switch]$Clean,
    [switch]$Run,
    [switch]$Upx
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$exeName = '微信多开助手.exe'
$outDir = '已编译成品'      # 别叫 build，容易被当成临时目录误删
$exePath = Join-Path $outDir $exeName

if ($Clean) {
    Write-Host "[1/4] 清理 $outDir\（含 data，账号会话会没）" -ForegroundColor Cyan
    Remove-Item ".\$outDir" -Recurse -Force -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force -Path ".\$outDir" | Out-Null

Write-Host '[2/4] 生成图标资源' -ForegroundColor Cyan
go run ./tools/mkicon app.ico frontend\logo.png

$rsrc = Join-Path $env:USERPROFILE 'go\bin\rsrc.exe'
if (Test-Path $rsrc) {
    & $rsrc -ico app.ico -o rsrc.syso
    Write-Host '      exe 图标已写入 rsrc.syso'
} elseif (Test-Path .\rsrc.syso) {
    Write-Host '      没装 rsrc，沿用已有 rsrc.syso' -ForegroundColor DarkYellow
} else {
    Write-Host '      没装 rsrc 且没有 rsrc.syso，exe 会用默认图标（不影响功能）' -ForegroundColor DarkYellow
    Write-Host '      想补图标：go install github.com/akavel/rsrc@latest' -ForegroundColor DarkYellow
}

Write-Host '[3/4] go mod tidy' -ForegroundColor Cyan
go mod tidy

Write-Host '[4/4] 编译' -ForegroundColor Cyan
go build -tags desktop,production -trimpath -ldflags '-w -s -H windowsgui' -o ".\$exePath" .
if ($LASTEXITCODE -ne 0) { throw "编译失败（exit $LASTEXITCODE）" }

$size = [math]::Round((Get-Item ".\$exePath").Length / 1MB, 2)
Write-Host "完成 → $outDir\$exeName ($size MB)" -ForegroundColor Green

if ($Upx) {
    # 变量别叫 $upx —— PowerShell 不区分大小写，会跟上面那个开关参数撞车
    $upxCmd = Get-Command upx -ErrorAction SilentlyContinue
    if (-not $upxCmd) {
        Write-Host '      没装 upx，跳过压缩（scoop install upx / choco install upx 都可以）' -ForegroundColor DarkYellow
    } else {
        Write-Host '      upx 压缩中…' -ForegroundColor Cyan
        & $upxCmd.Source --best --lzma ".\$exePath" | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $packed = [math]::Round((Get-Item ".\$exePath").Length / 1MB, 2)
            Write-Host "      压缩后 $packed MB（原 $size MB）" -ForegroundColor Green
        } else {
            Write-Host '      upx 压缩失败，保留未压缩的 exe' -ForegroundColor DarkYellow
        }
    }
}

if ($Run) {
    Start-Process -FilePath ".\$exePath"
}

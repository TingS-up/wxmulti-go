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

# 版本号只认 internal/core/version.go 这一处：程序里显示的和 exe 属性里的都从这儿取
$verText = Get-Content -LiteralPath '.\internal\core\version.go' -Raw
if ($verText -match 'AppVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"') {
    $ver = $Matches[1]
} else {
    throw '在 internal\core\version.go 里没找到 AppVersion'
}
$vp = $ver.Split('.')
$vMajor = [int]$vp[0]; $vMinor = [int]$vp[1]; $vPatch = [int]$vp[2]; $vBuild = 0
$ver4 = "$vMajor.$vMinor.$vPatch.$vBuild"

if ($Clean) {
    Write-Host "[1/5] 清理 $outDir\（含 data，账号会话会没）" -ForegroundColor Cyan
    Remove-Item ".\$outDir" -Recurse -Force -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force -Path ".\$outDir" | Out-Null

Write-Host "[2/5] 生成图标" -ForegroundColor Cyan
go run ./tools/mkicon app.ico frontend\logo.png
Write-Host '      app.ico（9 个尺寸）+ frontend\logo.png 已生成'

Write-Host "[3/5] 生成版本资源（属性面板里的说明 / 版本 / 版权 / 语言）" -ForegroundColor Cyan
$viObj = [ordered]@{
    FixedFileInfo  = [ordered]@{
        FileVersion    = [ordered]@{ Major = $vMajor; Minor = $vMinor; Patch = $vPatch; Build = $vBuild }
        ProductVersion = [ordered]@{ Major = $vMajor; Minor = $vMinor; Patch = $vPatch; Build = $vBuild }
        FileFlagsMask  = '3f'
        FileFlags      = '00'
        FileOS         = '040004'
        FileType       = '01'
        FileSubType    = '00'
    }
    StringFileInfo = [ordered]@{
        Comments         = 'https://github.com/TingS-up/wxmulti-go'
        CompanyName      = '听说'
        FileDescription  = '微信多开助手 - 微信多开与免扫码登录'
        FileVersion      = $ver4
        InternalName     = 'wxmulti-go'
        LegalCopyright   = '听说 qq:1224947739'
        LegalTrademarks  = ''
        OriginalFilename = $exeName
        PrivateBuild     = ''
        ProductName      = '微信多开助手'
        ProductVersion   = $ver4
        SpecialBuild     = ''
    }
    # 0804 = 简体中文(中国)，04B0 = Unicode(1200)
    VarFileInfo    = [ordered]@{
        Translation = [ordered]@{ LangID = '0804'; CharsetID = '04B0' }
    }
}
$viPath = Join-Path $PSScriptRoot 'versioninfo.json'
$viJson = $viObj | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText($viPath, $viJson, (New-Object System.Text.UTF8Encoding($false)))

$gviCmd = Get-Command goversioninfo -ErrorAction SilentlyContinue
$gviPath = $null
if ($gviCmd) {
    $gviPath = $gviCmd.Source
} else {
    $candidate = Join-Path $env:USERPROFILE 'go\bin\goversioninfo.exe'
    if (Test-Path $candidate) { $gviPath = $candidate }
}
if ($gviPath) {
    & $gviPath -icon app.ico -o resource_windows_amd64.syso $viPath
    if ($LASTEXITCODE -ne 0) { throw "goversioninfo 失败（exit $LASTEXITCODE）" }
    Write-Host "      图标 + 版本资源已写入 resource_windows_amd64.syso（$ver4，简体中文）"
} elseif (Test-Path .\resource_windows_amd64.syso) {
    Write-Host '      没装 goversioninfo，沿用已有 resource_windows_amd64.syso' -ForegroundColor DarkYellow
} else {
    Write-Host '      没装 goversioninfo 也没有现成的 syso，属性面板会是空的' -ForegroundColor DarkYellow
    Write-Host '      想补：go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest' -ForegroundColor DarkYellow
}

Write-Host '[4/5] go mod tidy' -ForegroundColor Cyan
go mod tidy

Write-Host '[5/5] 编译' -ForegroundColor Cyan
go build -tags desktop,production -trimpath -ldflags '-w -s -H windowsgui' -o ".\$exePath" .
if ($LASTEXITCODE -ne 0) { throw "编译失败（exit $LASTEXITCODE）" }

$size = [math]::Round((Get-Item ".\$exePath").Length / 1MB, 2)
Write-Host "完成 → $outDir\$exeName ($size MB, 版本 $ver4)" -ForegroundColor Green

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

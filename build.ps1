# HornetDownloader — 本机构建（Windows）
# 用法:
#   .\build.ps1           # 测试 + 出 zip
#   .\build.ps1 -SkipTest # 只构建
#   .\build.ps1 -Run      # 构建后直接运行 exe
param(
  [switch]$SkipTest,
  [switch]$Run
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

Write-Host "==> go mod download" -ForegroundColor Cyan
go mod download

if (-not $SkipTest) {
  Write-Host "==> go test" -ForegroundColor Cyan
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw "tests failed" }
}

New-Item -ItemType Directory -Force -Path build, dist | Out-Null
$out = Join-Path $PSScriptRoot "build\HornetDownloader.exe"
Write-Host "==> go build -> $out" -ForegroundColor Cyan
go build -trimpath -ldflags "-s -w -H windowsgui" -o $out .
if ($LASTEXITCODE -ne 0) { throw "build failed" }

$pkg = Join-Path $PSScriptRoot "build\pkg\HornetDownloader"
Remove-Item -Recurse -Force $pkg -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $pkg | Out-Null
Copy-Item $out (Join-Path $pkg "HornetDownloader.exe")
if (Test-Path "README.zh-CN.txt") {
  Copy-Item "README.zh-CN.txt" (Join-Path $pkg "README.txt")
}

$zip = Join-Path $PSScriptRoot "dist\HornetDownloader-win64.zip"
if (Test-Path $zip) { Remove-Item $zip -Force }
Compress-Archive -Path $pkg -DestinationPath $zip -Force

$hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
$len = (Get-Item $zip).Length
$exeLen = (Get-Item $out).Length
Write-Host ""
Write-Host "OK  exe  $exeLen bytes" -ForegroundColor Green
Write-Host "OK  zip  $zip ($len bytes)" -ForegroundColor Green
Write-Host "SHA256 $hash" -ForegroundColor Green

if ($Run) {
  Write-Host "==> launching" -ForegroundColor Cyan
  Start-Process $out
}

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
# Console companion CLI for scripts and agents (reliable stdout and exit codes)
$hdl = Join-Path $PSScriptRoot "build\hdl.exe"
Write-Host "==> go build -> $hdl" -ForegroundColor Cyan
go build -trimpath -ldflags "-s -w" -o $hdl ./cmd/hdl
if ($LASTEXITCODE -ne 0) { throw "hdl build failed" }

$pkg = Join-Path $PSScriptRoot "build\pkg\HornetDownloader"
Remove-Item -Recurse -Force $pkg -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $pkg | Out-Null
Copy-Item $out (Join-Path $pkg "HornetDownloader.exe")
Copy-Item $hdl (Join-Path $pkg "hdl.exe")
# Licenses: MIT for our code + third-party notices (aria2 is GPL-2.0-or-later)
Copy-Item "LICENSE" (Join-Path $pkg "LICENSE.txt")
Copy-Item "THIRD_PARTY_NOTICES.md" (Join-Path $pkg "THIRD_PARTY_NOTICES.md")
$lic = Join-Path $pkg "licenses"
New-Item -ItemType Directory -Force -Path $lic | Out-Null
Copy-Item "third_party\aria2-1.37.0-win-64bit-build1\COPYING" (Join-Path $lic "aria2-COPYING.txt")
Copy-Item "third_party\aria2-1.37.0-win-64bit-build1\LICENSE.OpenSSL" (Join-Path $lic "aria2-LICENSE.OpenSSL.txt")
if (Test-Path "README.zh-CN.txt") {
  Copy-Item "README.zh-CN.txt" (Join-Path $pkg "README.txt")
}

$zip = Join-Path $PSScriptRoot "dist\HornetDownloader-win64.zip"
if (Test-Path $zip) { Remove-Item $zip -Force }
Compress-Archive -Path $pkg -DestinationPath $zip -Force

# Standalone release assets with stable names + SHA256SUMS.txt
Copy-Item $out (Join-Path $PSScriptRoot "dist\HornetDownloader.exe") -Force
Copy-Item $hdl (Join-Path $PSScriptRoot "dist\hdl.exe") -Force
Copy-Item "scripts\install.ps1" (Join-Path $PSScriptRoot "dist\install.ps1") -Force
$sums = foreach ($n in "HornetDownloader-win64.zip", "HornetDownloader.exe", "hdl.exe", "install.ps1") {
  "{0}  {1}" -f (Get-FileHash (Join-Path $PSScriptRoot "dist\$n") -Algorithm SHA256).Hash.ToLower(), $n
}
[System.IO.File]::WriteAllText((Join-Path $PSScriptRoot "dist\SHA256SUMS.txt"), (($sums -join "`n") + "`n"))

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

<#
.SYNOPSIS
  Publish a GitHub Release from dist\ (run .\build.ps1 first). Requires gh (logged in).
  Assets use stable names so /releases/latest/download/<name> always works:
  HornetDownloader-win64.zip, HornetDownloader.exe, hdl.exe, install.ps1, SHA256SUMS.txt, release.json
.EXAMPLE
  .\scripts\release.ps1 -Tag v1.5.3 -NotesFile docs\release-notes\v1.5.3.md
#>
param(
  [Parameter(Mandatory = $true)][string]$Tag,
  [Parameter(Mandatory = $true)][string]$NotesFile,
  [string]$Repo = "julius06117-cloud/HornetDownloader",
  [string]$Gh = "gh"
)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot)
if (-not (Get-Command $Gh -ErrorAction SilentlyContinue) -and (Test-Path "C:\Program Files\GitHub CLI\gh.exe")) { $Gh = "C:\Program Files\GitHub CLI\gh.exe" }
$names = "HornetDownloader-win64.zip", "HornetDownloader.exe", "hdl.exe", "install.ps1", "SHA256SUMS.txt"
foreach ($n in $names) { if (-not (Test-Path "dist\$n")) { throw "missing dist\$n (run .\build.ps1)" } }
$version = $Tag.TrimStart("v")
$files = $names | ForEach-Object { "dist\$_" }
& $Gh release create $Tag @files --repo $Repo --title "HornetDownloader $Tag" --notes-file $NotesFile --latest
if ($LASTEXITCODE -ne 0) { throw "gh release create failed" }
$pub = (& $Gh release view $Tag --repo $Repo --json publishedAt | ConvertFrom-Json).publishedAt
$assets = foreach ($n in $names) {
  [ordered]@{
    name   = $n
    url    = "https://github.com/$Repo/releases/download/$Tag/$n"
    latest = "https://github.com/$Repo/releases/latest/download/$n"
    sha256 = (Get-FileHash "dist\$n" -Algorithm SHA256).Hash.ToLower()
    size   = (Get-Item "dist\$n").Length
  }
}
$manifest = [ordered]@{
  name         = "HornetDownloader"
  version      = $version
  tag          = $Tag
  published_at = $pub
  repo         = "https://github.com/$Repo"
  assets       = @($assets)
  min_windows  = "Windows 10 1809+ / Windows 11, x64 (WebView2 runtime or Edge for the UI; CLI works headless)"
  install      = [ordered]@{
    path     = '%LOCALAPPDATA%\Programs\HornetDownloader'
    commands = @(
      "Invoke-WebRequest https://github.com/$Repo/releases/latest/download/install.ps1 -OutFile `$env:TEMP\hd-install.ps1 -UseBasicParsing",
      "powershell -NoProfile -ExecutionPolicy Bypass -File `$env:TEMP\hd-install.ps1 -AddToPath -Json",
      "gh release download --repo $Repo --pattern HornetDownloader-win64.zip --pattern SHA256SUMS.txt"
    )
  }
  cli          = [ordered]@{
    console_exe = "hdl.exe"
    gui_exe     = "HornetDownloader.exe"
    help        = "hdl help --json"
    example     = 'hdl get "https://example.com/file.iso" --dir "D:\Downloads" --json'
  }
  cli_help_url = "https://github.com/$Repo/blob/$Tag/docs/AGENT_API.md"
  license      = "MIT (bundled aria2c.exe: GPL-2.0-or-later; see THIRD_PARTY_NOTICES.md)"
}
$json = $manifest | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText((Join-Path (Get-Location) "dist\release.json"), $json + "`n")
& $Gh release upload $Tag "dist\release.json" --repo $Repo --clobber
if ($LASTEXITCODE -ne 0) { throw "upload release.json failed" }
# 不用 --jq：Windows PowerShell 5.1 传给原生程序时会吞掉参数里的双引号，jq 表达式解析失败
$view = & $Gh release view $Tag --repo $Repo --json url,assets
$rel = ($view -join "`n") | ConvertFrom-Json
Write-Host $rel.url
foreach ($a in $rel.assets) { Write-Host ("{0}  {1}" -f $a.name, $a.size) }

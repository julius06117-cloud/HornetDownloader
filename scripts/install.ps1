<#
.SYNOPSIS
  Install or update HornetDownloader on Windows (per-user, no admin).

.DESCRIPTION
  Downloads the release assets (anonymous HTTPS from GitHub Releases by default), verifies
  SHA-256 against SHA256SUMS.txt, and installs to %LOCALAPPDATA%\Programs\HornetDownloader.
  Idempotent: re-running with the same version changes nothing.

  Download order: -FromDir (local folder) > HTTPS (optionally via -Mirror, with retries)
  > `gh release download` (if gh is installed) . Anonymous: no token is needed or sent.

.EXAMPLE
  irm https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/install.ps1 | iex
.EXAMPLE
  powershell -ExecutionPolicy Bypass -File install.ps1 -AddToPath -Json
.EXAMPLE
  .\install.ps1 -Mirror https://ghfast.top/ -Retries 5     # slow GitHub access (mainland China)
#>
param(
  [string]$Version = "latest",          # "latest" or a tag such as v1.5.3
  [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\HornetDownloader"),
  [string]$Mirror = "",                 # optional URL prefix, e.g. https://ghfast.top/
  [int]$Retries = 3,
  [string]$FromDir = "",                # install from a local folder containing the assets
  [switch]$AddToPath,
  [switch]$NoStop,                      # do not ask a running instance to quit before updating
  [switch]$Json
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$Repo = "julius06117-cloud/HornetDownloader"
$Zip = "HornetDownloader-win64.zip"
$log = New-Object System.Collections.Generic.List[string]
function Say([string]$m) { $log.Add($m); if (-not $Json) { Write-Host $m } }

function Get-Asset([string]$name, [string]$dest) {
  if ($FromDir) {
    Copy-Item (Join-Path $FromDir $name) $dest -Force
    return "local:$FromDir"
  }
  $base = if ($Version -eq "latest") { "https://github.com/$Repo/releases/latest/download" } else { "https://github.com/$Repo/releases/download/$Version" }
  $url = "$base/$name"
  $urls = @()
  if ($Mirror) { $urls += ($Mirror.TrimEnd('/') + "/" + $url) }
  $urls += $url
  $headers = @{ "User-Agent" = "HornetDownloader-install" }
  foreach ($u in $urls) {
    for ($i = 1; $i -le [Math]::Max(1, $Retries); $i++) {
      try {
        Invoke-WebRequest -Uri $u -OutFile $dest -Headers $headers -UseBasicParsing -TimeoutSec 300
        return $u
      } catch {
        Say "  download failed ($i/$Retries) ${u}: $($_.Exception.Message)"
        Start-Sleep -Seconds ([Math]::Min(10, 2 * $i))
      }
    }
  }
  $gh = Get-Command gh -ErrorAction SilentlyContinue
  if (-not $gh -and (Test-Path "C:\Program Files\GitHub CLI\gh.exe")) { $gh = Get-Command "C:\Program Files\GitHub CLI\gh.exe" }
  if ($gh) {
    $dir = Split-Path $dest
    $tagArgs = @(); if ($Version -ne "latest") { $tagArgs = @($Version) }
    & $gh.Source release download @tagArgs --repo $Repo --pattern $name --dir $dir --clobber 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0 -and (Test-Path (Join-Path $dir $name))) {
      if ((Join-Path $dir $name) -ne $dest) { Move-Item (Join-Path $dir $name) $dest -Force }
      return "gh"
    }
  }
  throw "could not download $name (try -Mirror https://ghfast.top/ or -FromDir)"
}

$result = [ordered]@{ ok = $false; version = $Version; installDir = $InstallDir; changed = $false; addedToPath = $false }
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("hdl-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
  Say "==> downloading $Zip ($Version)"
  $sumsPath = Join-Path $tmp "SHA256SUMS.txt"
  $zipPath = Join-Path $tmp $Zip
  $null = Get-Asset "SHA256SUMS.txt" $sumsPath
  $result.source = Get-Asset $Zip $zipPath
  try { $null = Get-Asset "release.json" (Join-Path $tmp "release.json"); $man = Get-Content (Join-Path $tmp "release.json") -Raw | ConvertFrom-Json; if ($man.version) { $result.version = $man.version } } catch { }

  $want = $null
  foreach ($line in Get-Content $sumsPath) {
    $p = $line -split "\s+", 2
    if ($p.Count -eq 2 -and $p[1].Trim().TrimStart('*') -eq $Zip) { $want = $p[0].ToLower() }
  }
  if (-not $want) { throw "SHA256SUMS.txt has no entry for $Zip" }
  $got = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want) { throw "SHA256 mismatch for ${Zip}: expected $want, got $got" }
  $result.sha256 = $got
  Say "==> SHA256 OK $got"

  $x = Join-Path $tmp "x"
  Expand-Archive -Path $zipPath -DestinationPath $x -Force
  $src = Join-Path $x "HornetDownloader"
  if (-not (Test-Path (Join-Path $src "HornetDownloader.exe"))) { throw "zip layout unexpected" }

  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  $exeDst = Join-Path $InstallDir "HornetDownloader.exe"
  $needExe = -not (Test-Path $exeDst) -or ((Get-FileHash $exeDst).Hash -ne (Get-FileHash (Join-Path $src "HornetDownloader.exe")).Hash)
  $needHdl = -not (Test-Path (Join-Path $InstallDir "hdl.exe")) -or ((Get-FileHash (Join-Path $InstallDir "hdl.exe")).Hash -ne (Get-FileHash (Join-Path $src "hdl.exe")).Hash)
  if ($needExe -or $needHdl) {
    $running = Get-Process HornetDownloader -ErrorAction SilentlyContinue | Where-Object { $_.Path -and ($_.Path -ieq $exeDst) }
    if ($running -and -not $NoStop) {
      Say "==> asking the running instance to quit"
      $data = Join-Path $env:APPDATA "HornetDownloader"
      try {
        $tok = (Get-Content (Join-Path $data "api-token") -Raw).Trim()
        $port = 16801; if (Test-Path (Join-Path $data "api-port")) { $port = [int](Get-Content (Join-Path $data "api-port") -Raw).Trim() }
        Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/api/quit" -Headers @{ "X-Token" = $tok } -Body "{}" -ContentType "application/json" -TimeoutSec 5 | Out-Null
      } catch { Say "  could not reach its API: $($_.Exception.Message)" }
      $running | ForEach-Object { $_.WaitForExit(15000) | Out-Null }
    }
    Copy-Item (Join-Path $src "*") $InstallDir -Recurse -Force
    $result.changed = $true
    Say "==> installed to $InstallDir"
  } else {
    Copy-Item (Join-Path $src "*") $InstallDir -Recurse -Force -Exclude "*.exe"
    Say "==> already up to date ($InstallDir)"
  }
  $result.exe = $exeDst
  $result.hdl = Join-Path $InstallDir "hdl.exe"

  if ($AddToPath) {
    # Read the raw (unexpanded) user PATH and keep its registry type, so entries like %USERPROFILE%\bin survive.
    $key = Get-Item "HKCU:\Environment"
    $cur = [string]$key.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    $parts = @(); if ($cur) { $parts = $cur -split ";" | Where-Object { $_ } }
    $already = $parts | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }
    if (-not $already) {
      $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
      if ($cur -and $key.GetValueNames() -contains "Path") { $kind = $key.GetValueKind("Path") }
      Set-ItemProperty -Path "HKCU:\Environment" -Name "Path" -Value (($parts + $InstallDir) -join ";") -Type $kind
      # Broadcast WM_SETTINGCHANGE so new terminals see it.
      [Environment]::SetEnvironmentVariable("HDL_INSTALL_TMP", "1", "User")
      [Environment]::SetEnvironmentVariable("HDL_INSTALL_TMP", $null, "User")
      $result.addedToPath = $true
      Say "==> added to user PATH (open a new terminal to use 'hdl')"
    } else { Say "==> already on user PATH" }
  }
  $result.ok = $true
} catch {
  $result.error = $_.Exception.Message
  if (-not $Json) { Write-Host "ERROR: $($result.error)" -ForegroundColor Red }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
$result.log = $log
if ($Json) { $result | ConvertTo-Json -Depth 4 -Compress }
if (-not $result.ok) { if ($PSCommandPath) { exit 1 } else { throw $result.error } }

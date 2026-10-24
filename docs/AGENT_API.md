# HornetDownloader — Agent / automation API

HornetDownloader is an **agent-first Windows downloader**: one portable exe that scripts, CI jobs and AI coding agents can drive from the command line (`hdl.exe`) or over a local HTTP API (`/api/v1/*`), with JSON output and stable exit codes. The same tasks show up in the GUI, tagged "Agent".

- Engines: aria2 (HTTP/FTP), pure-Go BitTorrent (magnet / .torrent), yt-dlp (video sites).
- Everything is local: UI/API bind to `127.0.0.1:16801`; embedded aria2c RPC is loopback-only.
- Version covered here: **v1.5.4**. Machine-readable summary: `hdl help --json`.

## 1. Install (Windows x64)

Release assets use **stable names**, so these URLs always point at the newest release:

| Asset | URL |
|-------|-----|
| `HornetDownloader-win64.zip` | https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/HornetDownloader-win64.zip |
| `SHA256SUMS.txt` | https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/SHA256SUMS.txt |
| `install.ps1` | https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/install.ps1 |
| `release.json` | https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/release.json |
| `HornetDownloader.exe`, `hdl.exe` | same pattern (standalone exes, also inside the zip) |

The repo is public: plain anonymous HTTPS works, no token or `gh` needed.

**One-liner (recommended):** download, verify SHA256, install to `%LOCALAPPDATA%\Programs\HornetDownloader`, print a JSON summary:

```powershell
$f = "$env:TEMP\hd-install.ps1"; Invoke-WebRequest https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/install.ps1 -OutFile $f -UseBasicParsing
powershell -NoProfile -ExecutionPolicy Bypass -File $f -AddToPath -Json
```

`install.ps1` parameters:

| Parameter | Default | Meaning |
|-----------|---------|---------|
| `-Version` | `latest` | or a tag like `v1.5.3` |
| `-InstallDir` | `%LOCALAPPDATA%\Programs\HornetDownloader` | no admin rights needed |
| `-Mirror` | (none) | URL prefix tried first, e.g. `https://ghfast.top/` (GitHub can be slow from mainland China) |
| `-Retries` | `3` | per-URL retries with back-off |
| `-FromDir` | (none) | install from a local folder holding the zip + `SHA256SUMS.txt` (offline / CI) |
| `-AddToPath` | off | add the install dir to the **user** PATH |
| `-NoStop` | off | do not stop a running instance that needs updating |
| `-Json` | off | print `{ok, version, installDir, changed, addedToPath, source, sha256, exe, hdl, log, error}` |

It is idempotent: re-running with the same version reports `"changed": false` and touches nothing. Download order: `-FromDir` → `-Mirror` → github.com (anonymous, no token) → `gh release download` fallback. Exit code 0 on success, 1 on failure.

**Manual install (anonymous HTTPS):**

```powershell
$base = "https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download"
Invoke-WebRequest "$base/HornetDownloader-win64.zip" -OutFile HornetDownloader-win64.zip -UseBasicParsing
Invoke-WebRequest "$base/SHA256SUMS.txt" -OutFile SHA256SUMS.txt -UseBasicParsing
$want = ((Get-Content SHA256SUMS.txt) -match "HornetDownloader-win64.zip$" -split "\s+")[0]
if ((Get-FileHash HornetDownloader-win64.zip -Algorithm SHA256).Hash -ne $want) { throw "SHA256 mismatch" }
Expand-Archive HornetDownloader-win64.zip "$env:LOCALAPPDATA\Programs" -Force   # -> ...\Programs\HornetDownloader\
& "$env:LOCALAPPDATA\Programs\HornetDownloader\hdl.exe" help --json
```

**Fallback with gh:** `gh release download --repo julius06117-cloud/HornetDownloader --pattern HornetDownloader-win64.zip --pattern SHA256SUMS.txt`

Requirements: Windows 10 1809+ / 11 x64. The GUI uses the WebView2 runtime (or Edge with `--browser`); the CLI and headless mode need neither. First use of a video URL downloads `yt-dlp.exe` into `%APPDATA%\HornetDownloader\bin\`.

## 2. CLI

Use **`hdl.exe`** (console subsystem) from scripts. `HornetDownloader.exe <command>` accepts the same commands, but it is a GUI-subsystem exe: PowerShell does not wait for it unless you pipe it (`HornetDownloader.exe list --json | Out-String`).

```
hdl get    <url|magnet|file.torrent> [--dir D] [--engine auto|aria2|bt|ytdlp] [--json] [--timeout 30m] [--force] [--quiet]
hdl add    <url|magnet|file.torrent> [--dir D] [--engine E] [--wait] [--json] [--timeout 30m] [--force]
hdl wait   <id> [--json] [--timeout 30m]
hdl status <id> [--json]
hdl list   [--json]
hdl remove <id> [--delete-files] [--json]
hdl pause  <id> [--json]
hdl resume <id> [--json]
hdl version [--json]
hdl help   [--json]
common: --data-dir D (default %APPDATA%\HornetDownloader)  --port N  --no-start
```

- `get` = add + wait; prints the final file path(s) (one per line) or, with `--json`, the final task object.
- `--dir` must be (or is made) absolute and is created if missing. Without it the app's configured download folder is used.
- Progress goes to **stderr** every few seconds (suppress with `--quiet`); stdout carries only the result.
- `--timeout` takes Go durations (`90s`, `30m`, `2h`). On timeout the task keeps running; the JSON still contains its `id`.
- **Autostart:** if the app is not running, the CLI starts it headless (`HornetDownloader.exe --no-window`: no window, no focus steal, tray-less) and waits up to 45 s for the API. `--no-start` disables this (exit 3 instead). If the user later opens the GUI, it attaches to the running instance.
- The exe is found via `HORNETDL_EXE`, then next to `hdl.exe`, then `%LOCALAPPDATA%\Programs\HornetDownloader\`.

### Version (v1.5.4)

`hdl version` (also `hdl --version`) prints the hdl version, the version of the running app (it **never starts** the app; `not running` otherwise), the Go toolchain and the git commit the binary was built from. It always exits 0.

```
hdl     1.5.4
app     1.5.4 (running, pid 1234, port 16801)
go      go1.26.10
commit  0123456789ab 2026-10-25T08:00:00Z
os      windows/amd64
```

`hdl version --json`:

```json
{
  "hdl": "1.5.4",
  "build": {"go": "go1.26.10", "commit": "<full sha>", "commitTime": "2026-10-25T08:00:00Z", "modified": false, "os": "windows", "arch": "amd64"},
  "app": {"running": true, "version": "1.5.4", "go": "go1.26.10", "commit": "<full sha>", "pid": 1234, "port": 16801}
}
```

`app.running` is false (with `app.error`) when the app is not reachable; `--data-dir` / `--port` select which instance to ask.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | OK (task complete / command done) |
| 1 | Task failed (error status, HTTP 404, …) or task id not found |
| 2 | Usage error (bad flags, missing argument, not a URL / magnet / .torrent) |
| 3 | App unavailable (could not start or reach HornetDownloader) |
| 4 | Timeout (`--timeout` reached; task left running) |
| 5 | Add rejected by the app (e.g. explicit engine failed) |

### Idempotency

`add` / `get` with the same source URL (or magnet / torrent) into the same `--dir` return the existing task (`"existing": true`) instead of downloading twice. A finished task counts only if its file still exists; failed/removed tasks never count. `--force` always adds a new task.

### Examples

```powershell
# Download a file and capture its path
$path = hdl get "https://deb.debian.org/debian/dists/stable/main/installer-amd64/current/images/netboot/debian-installer/amd64/linux" --dir "$env:TEMP\dl" --quiet
# JSON + exit code handling
$r = hdl get "https://example.com/file.iso" --dir D:\ISO --json --timeout 2h | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw "download failed ($LASTEXITCODE): $($r.error)" }
# Fire and forget, check later
$id = (hdl add "magnet:?xt=urn:btih:..." --dir D:\BT --json | ConvertFrom-Json).id
hdl status $id --json
hdl remove $id --delete-files
```

## 3. Task object

`--json` output of `get`/`wait`/`status`, items of `list` (`{"tasks":[...]}`), and `GET /api/v1/task`:

```json
{
  "id": "2089b05ecca3e5f1", "engine": "aria2", "status": "complete", "done": true, "ok": true,
  "name": "file.iso", "dir": "D:\\ISO", "files": ["D:\\ISO\\file.iso"],
  "totalBytes": 5000000, "doneBytes": 5000000, "progress": 1, "speedBps": 0,
  "peers": 0, "seeders": 0, "source": "https://example.com/file.iso", "addedBy": "agent"
}
```

- `status`: `waiting | active | paused | complete | error | removed`; `done` is true for the last three; `ok` only for `complete`.
- `error`: human-readable reason when `status` is `error` (in Chinese for UI-originated messages).
- `followedBy`: aria2 magnet metadata tasks are followed by the real download; `wait`/`get` follow it automatically.
- `peers` / `seeders`: BitTorrent peer and seeder counts (connections for HTTP).
- `addedBy`: `agent` for CLI / `/api/v1/add`, `user` for UI, clipboard, file association.
- Task ids: `bt-*` BitTorrent, `yt-*` yt-dlp, other hex ids aria2. Since v1.5.3 BitTorrent ids are derived from the infohash (`bt-` + 16 hex) and **stay the same across restarts**, so stored ids keep working.
- `speedBps` is 0 whenever `status` is not `active` (paused/waiting tasks never report a stale speed).
- If an aria2 (HTTP) download turns out to be an HTML page instead of the file (e.g. a media wiki page), the task ends as `error` with a readable message, the bogus file is deleted and `hdl get` exits 1. Since v1.5.4 such a task is also taken out of aria2's session, so it is **not downloaded again on restart**; it stays in the list as `error` (same id) and can still be removed or retried (retry adds the original URL again; the old id is aliased to the new task). Tasks that failed this way under v1.5.3 are converted the next time aria2 re-fetches them. Media pages such as `commons.wikimedia.org/wiki/File:…` are routed to yt-dlp by `auto`.
- HTTP 403/418: the task is retried once with the fallback User-Agent; the new aria2 id is aliased, so the original id still resolves.

## 4. HTTP API (`/api/v1`)

Base URL `http://127.0.0.1:<port>` (default port 16801; the current port is written to `%APPDATA%\HornetDownloader\api-port`).

**Auth:** header `Authorization: Bearer <token>` or `X-Token: <token>`. The persistent token is in `%APPDATA%\HornetDownloader\api-token` (created on first run, user-only file). The `Host` header must be `127.0.0.1:<port>` or `localhost:<port>` (DNS-rebinding guard); no CORS — browsers on other origins cannot call it.

| Method & path | Body / query | Response |
|---------------|--------------|----------|
| `GET /api/v1/ping` | — | `{ok, app:"HornetDownloader", version, pid, go, commit}` (`go`/`commit` since v1.5.4) |
| `POST /api/v1/add` | `{url}` or `{torrentB64, name}`; optional `dir` (absolute), `engine` (`auto`/`aria2`/`bt`/`ytdlp`), `selectFiles` (default false: download all), `force` | `{id, engine, recommended, existing, fallback, dir}`; 400 bad input, 422 engine rejected |
| `GET /api/v1/task?id=ID` | — | task object; 404 if unknown (tasks appear within ~1 s after add) |
| `GET /api/v1/tasks` | — | `{tasks:[task...]}` |
| `POST /api/v1/pause` / `resume` | `{id}` | `{ok, id}`; 404 unknown id |
| `POST /api/v1/remove` | `{id, deleteFiles}` | `{ok, id}`; 404 unknown id |

```powershell
$d = "$env:APPDATA\HornetDownloader"; $tok = (Get-Content "$d\api-token" -Raw).Trim(); $port = (Get-Content "$d\api-port" -Raw).Trim()
$h = @{ Authorization = "Bearer $tok" }
$r = Invoke-RestMethod -Method Post "http://127.0.0.1:$port/api/v1/add" -Headers $h -ContentType application/json -Body (@{ url = "https://deb.debian.org/debian/dists/stable/main/installer-amd64/current/images/netboot/debian-installer/amd64/linux"; dir = "$env:TEMP\dl" } | ConvertTo-Json)
Invoke-RestMethod "http://127.0.0.1:$port/api/v1/task?id=$($r.id)" -Headers $h
```

`/api/v1/*` is the stable contract. Other `/api/*` routes serve the bundled UI and may change between versions.

## 5. Configuration & diagnostics (v1.5.3+)

- **User-Agent** (`config.json`, Settings → 下载): `userAgent` (empty = `HornetDownloader/<version>`, honest default) and `userAgentFallback` (used once after HTTP 403/418; default `Wget/1.21.4`, empty disables the retry). `clipboardWatch` (default true) turns the clipboard prompt on/off.
- **Crash logs:** Go fatal errors are written to `%APPDATA%\HornetDownloader\crash-<yyyymmdd-hhmmss>.log`; the previous run's log is `app.prev.log`. After an unclean exit the next start logs a `[diag]` line in `app.log`.
- **Memory:** `app.log` gets a `[mem]` heartbeat every 5 min (`HORNET_MEMLOG_SEC=<seconds>` to change); if working set or private memory first exceeds 1 GiB, one `heap-<time>.pb.gz` snapshot is written to the data dir. `GOMEMLIMIT` is honoured.
- **pprof (opt-in):** `HornetDownloader.exe --pprof 127.0.0.1:6061` or env `HORNET_PPROF=127.0.0.1:6061` (loopback only) serves `/debug/pprof/*` and `/debug/mem` (JSON: heap, sys, goroutines, BT connections).
- **Persisted metadata:** `task-meta.json` (created times, removed-task tombstones, app-detected errors, UA-retry aliases) and `bt\meta\<infohash>.torrent` (BT metadata cache, deleted with the task). Since v1.5.4 a `.torrent` you add is stored there too — **no `.torrent` copy is written to the download folder** any more. Copies left in the download folder by v1.5.3 are not touched; they are still deleted together with the task when you remove it with “delete files”.
- Toolchain: release builds use Go 1.26.10. Go 1.26.0/1.26.1 crash on Windows under UDP load (golang/go#77975); don't build with them.

## 6. Networking & firewall

- UI/API: `127.0.0.1:16801` only. aria2c JSON-RPC: loopback only, DHT/PEX/LPD off, so idle aria2 opens no public port.
- The BitTorrent engine listens on one stable port (`Config.BTPort`, 6881–6999, TCP+UDP) on all interfaces, as BitTorrent needs inbound peers. Windows asks once; Settings → 防火墙 can add an allow rule for the **private** profile (optionally public). That button elevates through **UAC** (never silently). `HornetDownloader.exe --firewall-dry-run` prints the exact `netsh` commands without changing anything.

## 7. Legal use

Use HornetDownloader only for content you are allowed to download (Linux ISOs, your own torrents, public files, CC-licensed video). License: MIT; bundled aria2c is GPL-2.0-or-later — see `LICENSE` and `THIRD_PARTY_NOTICES.md`.

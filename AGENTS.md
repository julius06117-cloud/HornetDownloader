# AGENTS.md — HornetDownloader

Guide for coding agents (Cursor, GitHub Copilot, Codex, Claude Code, …). Read this before changing code; [`README.md`](README.md) has the architecture diagram and the hard invariants.
写给编程助手的项目说明；用户界面与日志是中文，代码注释中英混用。

## What this is

HornetDownloader (Hornet 下载器) is an **agent-first, ad-free, telemetry-free Windows downloader** meant to replace Xunlei (迅雷).
Humans use the WebView2 GUI; scripts and AI agents drive the same app through a CLI (`hdl.exe`, JSON output, stable exit codes) and a stable local HTTP API (`/api/v1/*`) — see [`docs/AGENT_API.md`](docs/AGENT_API.md).
It started as a personal gift for the owner's wife, so polish and "it just works" matter more than features.

- Ships as **one portable `HornetDownloader.exe`** (windows/amd64, `CGO_ENABLED=0`) plus the console companion `hdl.exe`, zipped as `dist\HornetDownloader-win64.zip`. Install path: `%LOCALAPPDATA%\Programs\HornetDownloader`.
- Three download engines: **aria2** (embedded `aria2c.exe` 1.37.0, JSON-RPC), **BitTorrent** (pure-Go `github.com/anacrolix/torrent`), **yt-dlp** (sidecar process, downloaded on first use).
- UI: single-page `web/index.html` served on `http://127.0.0.1:16801`, shown in a WebView2 window (fallback: Edge app window via `--browser`).
- Go module name: `hornetdl`; Go 1.26 (built with **go1.26.10** via the `toolchain` line in `go.mod`); current version v1.5.3.

## Repository map

| Path | Responsibility |
|------|----------------|
| `main.go` | Entry point, `Config` + defaults, CLI flags (`--no-window`, `--firewall-dry-run`, …), CLI sub-command dispatch to `internal/agentcli`, data dir, single instance / inbox hand-off, app wiring |
| `agent_api.go` | Automation API `/api/v1/*` (ping/add/task/tasks/pause/resume/remove), persistent `api-token` + `api-port`, agent task view, idempotent add, `addedBy` source tags (`task-source.json`) |
| `internal/agentcli/` | CLI `get/add/wait/status/list/remove/pause/resume/help` (JSON, exit codes, headless autostart); shared by `HornetDownloader.exe <cmd>` and `cmd/hdl` |
| `cmd/hdl/` | `hdl.exe`: console-subsystem companion for scripts (PowerShell waits for it and captures stdout) |
| `console_windows.go` | `AttachConsole(ATTACH_PARENT_PROCESS)` so the GUI exe can print CLI output |
| `firewall_cmds.go` | Pure `netsh` command construction (tested; used by the UAC path and `--firewall-dry-run`) |
| `server.go` | HTTP server (`routes()`), Host check + `X-Token` auth, aria2 RPC whitelist, config/delete/retry/inbox/assoc/readtorrent handlers, `//go:embed web` |
| `engines.go` | Multi-engine coordination: `routeAdd` (the **only** add path), `handleAdd`, `handleRecommend`, `engineForGID` |
| `recommend.go` | Engine IDs (`auto`/`aria2`/`bt`/`ytdlp`, plus `default`), `videoHosts`, `RecommendEngine`, `resolveEngine` |
| `aria2.go` | aria2c process + JSON-RPC client: two pools — fast (6 conns, 8 s) and slow (2 conns, used for any timeout > 8 s, e.g. 45/90/120 s) — and health tracking |
| `resilience.go` | "UI never waits on aria2": background task poller + cache (`/api/tasks`), single action queue (`/api/action`), paged file list (`/api/files`, 500/page), `/api/startbt`, UI log + heartbeat watchdog |
| `bt_engine.go` | BitTorrent engine (anacrolix): listens 6881–6999, UPnP/NAT-PMP on, tracker re-announce, part files + map piece completion, `kickDownloadLocked`, reselect, session `bt-session.json` |
| `ytdlp_engine.go` | yt-dlp sidecar: uses `bin\yt-dlp.exe`, then PATH, else downloads it (GitHub, then ghproxy mirror) |
| `diag.go`, `diag_windows.go` | Diagnostics (v1.5.3): `crash-*.log` via `debug.SetCrashOutput`, `app.prev.log`, unclean-exit detection, memory heartbeat, opt-in loopback pprof + `/debug/mem`, bad-toolchain warning |
| `taskmeta.go` | `task-meta.json`: created time, removed-task tombstones, app-detected errors (HTML guard), UA-retry aliases |
| `aria2_guard.go` | aria2 guard: 403/418 → retry once with `userAgentFallback`; HTML-instead-of-file guard; tombstone cleanup; `.aria2` cleanup |
| `details.go` | `/api/details` (tabs: overview / files / trackers / peers) and `/api/reselect` for all engines |
| `artifacts.go` | Paths of downloaded artifacts (open folder etc.) |
| `heartbeat.go` | Browser-window mode: exit when UI stops pinging |
| `embed_windows.go` | `//go:embed embed/aria2c.exe` → extracted to the fixed path `%APPDATA%\HornetDownloader\bin\aria2c.exe` (rewritten only if SHA-256 differs) |
| `firewall_windows.go` | Windows Firewall rules via UAC (`ShellExecuteEx runas` on self with `--firewall-rules`) + status via `netsh … show rule` |
| `tray_windows.go`, `window_windows.go`, `clipboard_windows.go`, `assoc_windows.go` | Tray (close-to-tray), WebView2 window, clipboard watcher, `magnet:` / `.torrent` association |
| `*_other.go` | Non-Windows stubs so `go test` / `go vet` also work on Linux/macOS |
| `web/index.html` | Entire UI (HTML+CSS+JS, no build step). Fonts in `web/fonts/` (Noto Sans SC for UI, LXGW WenKai for the love quote) |
| `assets/trackers.txt` | Default public trackers (embedded) |
| `embed/` | `aria2c.exe` + its license (embedded into the exe — do not delete) |
| `third_party/` | Upstream aria2 1.37.0 release files (source of `embed/aria2c.exe`) |
| `winres/`, `rsrc_windows_amd64.syso` | Icon / version resources linked into the exe |
| `scripts/ui_ops_check.mjs` | Node check mirroring `buildOps()` semantics |
| `scripts/install.ps1` | End-user / agent installer: download (anonymous HTTPS, `-Mirror`, retries, `gh` fallback), SHA256 verify, install, `-AddToPath`, `-Json`; also a release asset |
| `scripts/release.ps1` | Maintainer: publish a GitHub Release from `dist\` with stable asset names + `release.json` |
| `build.ps1` / `build.sh` | Windows build (primary) / Linux cross-build → `build\`, `dist\` (zip, standalone exes, `install.ps1`, `SHA256SUMS.txt`) |
| `docs/AGENT_API.md` | Automation contract: install, CLI, exit codes, task JSON, `/api/v1` |
| `QA-CHECKLIST.md` | Release QA matrix with last results |
| `LICENSE`, `THIRD_PARTY_NOTICES.md` | MIT (project) / aria2 GPL-2.0, anacrolix MPL-2.0, Go deps; both shipped in the zip |
| `README.md` | Maintainer/agent overview: architecture diagram, hard invariants, API summary (keep in sync with this file) |
| `DEV.md` | Local Home dev environment and daily commands |
| `README.zh-CN.txt` | End-user manual, shipped as `README.txt` in the zip |

Runtime data: `%APPDATA%\HornetDownloader\` → `config.json`, `app.log`, `app.prev.log`, `crash-*.log`, `task-meta.json`, `bt\meta\`, `aria2.log`, `aria2.session`, `bt-session.json`, `api-token`, `api-port`, `task-source.json`, `bin\` (aria2c, yt-dlp). Never commit any of these.

## Engine routing (do not regress)

| Link | Recommended engine |
|------|--------------------|
| `magnet:` or `.torrent` (file or URL) | `bt` |
| Host in `videoHosts` (YouTube, Bilibili, Douyin, …) | `ytdlp` |
| Any other http(s)/ftp | `aria2` |

Rules (covered by `add_api_test.go`, `recommend_test.go`):
1. **Every add goes through `routeAdd`** — new-task dialog, clipboard, file association, CLI args, drag-drop, torrent upload, second-instance hand-off.
2. `auto` (dialog "自动") **always** follows the recommendation and ignores `Config.DefaultEngine`. `default` (all non-dialog entry points) honours `DefaultEngine`, which itself may be `auto`.
3. An explicitly chosen engine that fails → **return the error**, never silently switch to aria2.
4. Duplicate magnet/torrent → return the existing task (`existing`), never add a second copy elsewhere.
5. Only in `auto`/`default` mode may a non-duplicate BT failure fall back to aria2, and the response must carry the `fallback` reason.
6. GID prefixes: `bt-*` BitTorrent, `yt-*` yt-dlp, everything else aria2 (`engineForGID`).

## Local HTTP API

All `/api/*` require `Host` = `127.0.0.1:<port>` or `localhost:<port>` (DNS-rebinding guard) and a token: `X-Token` or `Authorization: Bearer` with either the per-run UI token or the persistent `api-token` (constant-time compare). POST only, except GET allowed for `ping`, `config`, `tasks`, `inbox`, `assoc`, `firewall`, `v1/ping`, `v1/task`, `v1/tasks`, `task`. No CORS (OPTIONS → 405). Body limit 80 MB.

**Stable automation contract: `/api/v1/*` + the CLI** (documented in [`docs/AGENT_API.md`](docs/AGENT_API.md)). Do not break their JSON shapes or exit codes; add fields instead. The UI routes below may change freely. `/api/tasks` consumes UI notices and `/api/ping` is the UI heartbeat — automation must use `/api/v1/tasks` and `/api/v1/ping`.

| Endpoint | Handler | Purpose |
|----------|---------|---------|
| `/api/v1/ping`, `/api/v1/add`, `/api/v1/task`, `/api/v1/tasks`, `/api/v1/pause`, `/api/v1/resume`, `/api/v1/remove` | agent_api.go | Automation API (stable) |
| `/api/task` | agent_api.go | Alias of `/api/v1/task` |
| `/api/ping` | server.go | UI heartbeat |
| `/api/tasks` | resilience.go | Merged task list from cache (all engines); `stale` flag when aria2 is slow |
| `/api/action` | resilience.go | pause / unpause / stop … — queued, returns immediately |
| `/api/files` | resilience.go | Paged torrent file list (pending while fetching) |
| `/api/startbt` | resilience.go | Start a torrent with a file selection |
| `/api/reselect` | details.go | Change selected files at any time (aria2 + BT) |
| `/api/details` | details.go | Task details tabs: overview / files / trackers / peers |
| `/api/log` | resilience.go | UI → `app.log` |
| `/api/add` | engines.go | Add URLs (`urls` or single `url`) / torrent (base64) with engine choice; returns `id` of the first task |
| `/api/recommend` | engines.go | Engine recommendation for a link (advisory) |
| `/api/rpc`, `/api/multicall` | server.go | Whitelisted raw aria2 calls |
| `/api/config` | server.go | GET / POST settings (`Config` in main.go) |
| `/api/delete`, `/api/retry` | server.go | Delete task (optionally files) / retry failed task |
| `/api/inbox` | server.go | Links handed over from CLI / protocol / clipboard |
| `/api/assoc` | server.go | Query / set magnet & .torrent association |
| `/api/firewall` | server.go + firewall_windows.go | GET rule status; POST `{action: add\|remove, public}` → UAC-elevated `HornetDownloader.exe --firewall-rules=…` runs netsh (rule name `HornetDownloader`, private profile by default). `--firewall-dry-run` prints the commands without elevating |
| `/api/readtorrent` | server.go | Parse a .torrent for the add dialog |
| `/api/pickdir`, `/api/open`, `/api/quit` | server.go | Folder picker, open file/folder, quit app |

When you change an endpoint, update its caller in `web/index.html` and the matching `*_test.go`.

## Build & test

Primary dev machine is the owner's Windows PC ("Home"). Prefer building and QA'ing locally there; see `DEV.md`.

```powershell
$env:CGO_ENABLED = "0"
$env:GOPROXY = "https://goproxy.cn,direct"   # China mirror
go vet ./...
go test ./...
node scripts/ui_ops_check.mjs                # optional UI ops check
.\build.ps1             # tests + build\HornetDownloader.exe + build\hdl.exe + dist\ (zip, exes, install.ps1, SHA256SUMS.txt)
.\build.ps1 -SkipTest   # build only
.\build.ps1 -Run        # build, then launch
go run .                # dev run (console build)
```

Linux cross-build: `./build.sh` (needs `zip`). Release builds use `-trimpath -ldflags "-s -w -H windowsgui"` (no console window).

## Automation (CLI + API) — quick reference

```powershell
hdl help --json                                            # machine-readable command list + exit codes
hdl get <url|magnet|file.torrent> --dir D:\dl --json       # add + wait; exit 0 ok, 1 failed, 2 usage, 3 app unavailable, 4 timeout, 5 rejected
hdl add <url> --dir D:\dl --json ; hdl wait <id> --json ; hdl status <id> --json ; hdl list --json
hdl pause|resume <id> ; hdl remove <id> --delete-files
```

- If the app is not running, the CLI starts it headless (`--no-window`); a later GUI launch attaches to it.
- `add`/`get` are idempotent per (source, dir); `--force` re-downloads. API adds default to `selectFiles=false` (download everything, never wait on a file picker).
- Tasks added via CLI/API are tagged `addedBy: "agent"` and shown with an "Agent" badge + 来源 filter in the UI.
- Tests: `agent_api_test.go`, `internal/agentcli/agentcli_test.go`. Keep them loopback-only.

## Releases & install

- GitHub Releases with **stable asset names**: `HornetDownloader-win64.zip`, `HornetDownloader.exe`, `hdl.exe`, `install.ps1`, `SHA256SUMS.txt`, `release.json`. Latest: `https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/<asset>` (repo is public; anonymous HTTPS, `gh` optional).
- Install: `scripts/install.ps1` → `%LOCALAPPDATA%\Programs\HornetDownloader` (no admin), SHA256-verified, idempotent, `-AddToPath`, `-Mirror` for slow GitHub access, `-Json`.
- Publish (maintainer): bump `AppVersion` (agent_api.go) and `README.zh-CN.txt`, run `.\build.ps1`, complete `QA-CHECKLIST.md`, then `.\scripts\release.ps1 -Tag vX.Y.Z -NotesFile <notes.md>`.
- License: MIT (`LICENSE`). Bundled aria2c is GPL-2.0-or-later and runs as a separate process; keep `THIRD_PARTY_NOTICES.md` and `licenses\` in the zip and update the notices when Go deps change.

## Invariants & gotchas

- **Keep `CGO_ENABLED=0`.** No DLLs, no cgo libtorrent. Single-exe portability is a hard requirement.
- **Toolchain ≥ go1.26.2 (pinned `toolchain go1.26.10`).** go1.26.0/1.26.1 corrupt goroutine stacks during overlapped UDP I/O on Windows (golang/go#77975, fixed by #78041). Under BT load (DHT / UDP trackers) that killed the app with `0xc0000005` within minutes — the v1.5.x "silent exit". Do not lower the `toolchain` line; `TestToolchainNotAffectedByWindowsUDPStackBug` fails and the app logs a `[diag]` warning on an affected toolchain. Check with `go version -m build\HornetDownloader.exe`.
- **Diagnostics:** the release exe is `-H windowsgui` (no stderr), so Go fatal errors go to `<data>\crash-<time>.log` (`debug.SetCrashOutput`); `app.log` of the previous run is kept as `app.prev.log`. `--pprof 127.0.0.1:6061` (or `HORNET_PPROF`, loopback only) serves `/debug/pprof/*` and `/debug/mem`; `HORNET_MEMLOG_SEC` changes the memory heartbeat (default 300 s); `GOMEMLIMIT` is honoured.
- **IDs & times are stable:** BT ids are `bt-` + first 16 hex of the infohash (same after restart); created time lives in `task-meta.json` / `bt-session.json`. Speed is 0 for any task that is not `active`; BT speed counts useful bytes read from the network (re-hashing existing data after a restart is not speed).
- **User-Agent:** default is honest `HornetDownloader/<version>`; never impersonate a browser by default. 403/418 retries once with `userAgentFallback` (default `Wget/1.21.4`).
- **Never block the UI on aria2.** aria2 is single-threaded and can stall (e.g. big torrents). Read from the poller cache, put mutations on the action queue, use the slow pool for long calls. Do not put synchronous RPC back on request paths.
- **Windows file allocation:** `Config.effectiveFileAllocation()` maps `auto` → `prealloc` on Windows. `none`/`trunc` on NTFS causes zero-fill stalls that freeze aria2 RPC ("aria2 响应超时"). Read that comment before changing it.
- **BitTorrent:** keep part files + `NewMapPieceCompletion` (bolt DB locks files on Windows); keep tracker re-adds and `kickDownloadLocked`; don't disable downloading from write-error handlers.
- **Firewall / listeners:** aria2 is HTTP/FTP-first: DHT/DHT6/LPD/PEX are off and RPC is loopback-only, so idle aria2 never listens on a public interface (it only opens `--listen-port` while an aria2 BT fallback task runs). The BT engine keeps a stable port (`Config.BTPort`, chosen once from 6881–6999). Tests must stay loopback-only: `testmain_test.go` sets `btLoopbackOnly` (127.0.0.1, no DHT/IPv6/UPnP); test servers use `127.0.0.1:0`. Never add a test that listens on `0.0.0.0`/`::` (Windows Firewall prompt).
- **Embedded binaries:** `embed/aria2c.exe` must exist for Windows builds. yt-dlp is intentionally *not* embedded.
- **UI language is Simplified Chinese.** All user-facing strings, notices and errors in Chinese; keep wording short. UI font is Noto Sans SC — never fall back to Song/宋体.
- **UI requirements:** love quote shown inline on the main UI (no popup); whole-row click selects a task; one pause/resume button per row (`buildOps()`); full status in tooltip/details; context menu via right-click, `⋯`, or Shift+F10 / ContextMenu key; close-to-tray configurable (`closeToTray`); clipboard links prompt before adding; failed tasks can be retried.
- **Security:** keep the Host check, `X-Token`, CSP header and RPC whitelist in `server.go`.
- **PowerShell quirk (agent shells):** inline `powershell -Command "..."` may strip `$variables`. Write a `.ps1` file and run it with `powershell -NoProfile -ExecutionPolicy Bypass -File script.ps1`.
- **Encoding:** source files are UTF-8. Windows PowerShell 5.1 consoles may show Chinese as mojibake; that is a display issue, not file corruption.
- **Never commit** `build/`, `dist/`, logs, screenshots, `config.json`, `api-token`, or personal paths/usernames (the repo is public; see `.gitignore`).
- **Legal test content only:** Linux distro ISOs/torrents (Ubuntu, Debian), self-made torrents (see `bt_speed_test.go`), public HTTP files. Never test with or optimise for pirated content.

## Definition of done

1. `go vet ./...` clean and `go test ./...` passing **on Windows** (also run `node scripts/ui_ops_check.mjs` if `buildOps()` changed). For CLI/API changes, also update `docs/AGENT_API.md` and `helpJSON()`.
2. `.\build.ps1` succeeds; report the new zip SHA256.
3. Self-QA before handing over — the owner should not be the one finding bugs. Launch the exe and check at least:
   - add HTTP / magnet / .torrent / video link → correct engine chosen, label shown;
   - pause / resume / stop / retry / delete respond instantly, no duplicate buttons, no truncated status;
   - context menu + details tabs; reselect files mid-download;
   - UI stays responsive with a large multi-file torrent; no "aria2 响应超时";
   - close-to-tray, tray menu, quote, fonts; check `%APPDATA%\HornetDownloader\app.log` for errors.
4. User-visible changes → update `README.zh-CN.txt` (and this file if invariants changed). Before a release, run the full matrix in `QA-CHECKLIST.md`. Commit with a clear message.

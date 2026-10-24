# Release QA checklist

Run on a real Windows 10/11 x64 machine before every release (`.\build.ps1`, then install and exercise the build).
Test only with content you are allowed to download (Debian images are used below). Tests and QA never open ports beyond loopback except the BitTorrent listener.

Last run: **v1.5.4**, 2026-10-25 (Asia/Shanghai), Windows 11 x64, **Go 1.26.10** (`go.mod` toolchain), build `HornetDownloader-win64.zip` (see SHA256SUMS.txt of the release). Targeted QA of the v1.5.4 fixes plus a short regression pass (no soak).
QA instance isolated from any real install: `--data-dir <qa>\data --port 16941 --pprof 127.0.0.1:6081`, downloads only into `<qa>\dl`.

## v1.5.4 results

| # | Area | Check | Result | Notes |
|---|------|-------|--------|-------|
| B1 | CLI | `hdl version` (plain + `--json`): hdl 1.5.4, app 1.5.4 (pid/port) when running, `app not running` when stopped (exit 0, app not auto-started), go1.26.10, commit + time; `help --json` lists `version` | PASS | `go version -m`: go1.26.10, `vcs.modified=false` |
| B2 | HTML guard | Wikimedia `File:` page forced to aria2 → error with Chinese message, fake file deleted, task removed from aria2 session and kept as a parked failed task | PASS | |
| B3 | HTML guard | Restart (+20 s): page **not** re-downloaded (no file, no log activity), task still listed as error with link + message in details | PASS | fixes v1.5.3 A9 known issue |
| B4 | HTML guard | Parked task: pause rejected with Chinese hint; retry → new id (old id resolves to it), detected as HTML and parked again; `hdl remove` and UI delete (`/api/delete`) remove it; gone after restart | PASS | |
| B5 | BT | Local `.torrent` (Sintel) via `hdl add --engine bt`: complete 123.3 MB; **no `.torrent` in the download folder**; metainfo in `<data>\bt\meta\<infohash>.torrent`; same id and still no copy after restart | PASS | |
| B6 | BT | Simulated v1.5.3 session pointing at `<dl>\Sintel.torrent`: migrated to `bt\meta`, legacy copy recorded (not deleted silently); `remove --delete-files` deletes legacy copy + meta file | PASS | |
| B7 | Crash | Found during regression: GUI instance died on a magnet with a `wss://` tracker — `panic: write /dev/stderr: The handle is invalid` (anacrolix/log panics when the log handler errors; stderr is invalid in a GUI process) | FIXED | log writes never fail (stderr errors ignored); regression test added; affects v1.5.3 GUI instances |
| B8 | Regression | `hdl get` Debian netboot `linux` 11.6 MB, SHA256 matches Debian SHA256SUMS (11.1 s fresh) | PASS | |
| B9 | Regression | Sintel magnet complete 123.3 MB / 42 s with the window open, no crash, no `.torrent` in download folder | PASS | after crash fix |
| B10 | Regression | yt-dlp Wikimedia Commons 30.0 MB; `hdl list` exit 0 / 16 ms | PASS | |
| B11 | UI | App-window screenshots: main list, 失败 tab with parked HTML tasks (重新开始 / 打开), 已完成 tab | PASS | screenshots v154-main/failed/done |
| B12 | Build | `go vet` (windows + linux), `go test -count=3 ./...`, `node scripts/ui_ops_check.mjs`, `.\build.ps1` | PASS | |

## v1.5.3 results

| # | Area | Check | Result | Notes |
|---|------|-------|--------|-------|
| A1 | Toolchain | `go version -m HornetDownloader.exe` = go1.26.10; `app.log` start line logs `go=go1.26.10` | PASS | go1.26.0/1.26.1 corrupt stacks on Windows overlapped UDP I/O (golang/go#77975) |
| A2 | Crash repro | Same soak on the old go1.26.1 build | CRASH at ~1.2 min | `0xc0000005`, captured in `crash-*.log` (new crash log works) |
| A3 | Soak | go1.26.10 build, 21.9 min: BT DVD-1 (3.8 GB, peak 42.7 MB/s) + BT BD-1 (18.3 GB) + 22 HTTP `hdl get` loops + 3225 details API calls | PASS | no crash; 0 HTTP failures; 0 API errors |
| A4 | Memory | Soak working set 84→214 MB (plateau 190–214 MB with an 18 GB torrent), Go heap 17–133 MB non-monotonic, goroutines 193–557, aria2c ≤17 MB | PASS | heap dominated by anacrolix piece state, proportional to torrent size |
| A5 | HTTP | Debian netinst ISO (756 MB) via 16 conns: avg 31.2 / peak 76.0 MB/s, SHA256 matches | PASS | curl single-stream reference 57 MB/s |
| A6 | HTTP | Pause → speed 0; kill + restart restores the task with the same id; `.aria2` cleaned after completion | PASS | |
| A7 | UA | Default UA `HornetDownloader/1.5.3`; TUNA/BFSU 403 → fallback `Wget/1.21.4` succeeds | PASS | |
| A8 | HTML guard | Wikimedia `File:` page forced to aria2 → task error with Chinese message, fake file deleted, `hdl get` exit 1 | PASS | |
| A9 | HTML guard | Restart: aria2 re-downloads the errored page from its session → deleted again, task stays error | PASS | known: the page is re-fetched once per restart — fixed in v1.5.4 (B3) |
| A10 | BT | Magnet: stable id `bt-<infohash>` across restarts, metadata 2.1 s, SHA256 matches, createdAt preserved, no fake speed during recheck | PASS | |
| A11 | BT | Recheck hint 「正在校验已下载的数据…」 shown only while re-hashing after restart, not during a 50 MB/s download | PASS | |
| A12 | BT | Peers tab per-peer ↑ uses byte deltas; sum of peers ≈ task upload speed | PASS | previously showed bursts of tens of MB/s per peer |
| A13 | BT | Reselect files on completed multi-file torrent (Sintel 0.1 → 123.3 MB) | PASS | |
| A14 | Tasks | Removed tasks do not reappear after restart | PASS | |
| A15 | yt-dlp | Wikimedia Commons 30 MB / 6 s; bilibili 112 MB / 16 s; real file size shown | PASS | |
| A16 | CLI | `hdl list` warm ×10 median 13 ms; `get` warm 1.0 s / cold 1.3 s; app stopped → exit 3 in 13 ms | PASS | |
| A17 | UI | Details status in Chinese; one 「×」 in details; 1280×820 @150% layout; settings show UA / fallback UA / clipboard toggle | PASS | screenshots v153-main/details/peers/settings |
| A18 | Build | `go vet` (windows + linux), `go test -count=3 ./...`, `node scripts/ui_ops_check.mjs`, `.\build.ps1` | PASS | |

## Base checklist (results below are from the last full pass, v1.5.2; v1.5.3 re-ran the items covered in the table above plus the live-release install, row 11)

| # | Area | Check | Result | Notes |
|---|------|-------|--------|-------|
| 1 | Build | `go vet ./...` (windows + linux) | PASS | |
| 2 | Build | `go test -count=3 ./...` | PASS | loopback-only test servers |
| 3 | Build | `node scripts/ui_ops_check.mjs` (single pause/resume button per state) | PASS | 7 cases |
| 4 | Build | `.\build.ps1` → exe, hdl.exe, zip, SHA256SUMS | PASS | |
| 5 | Build | PE subsystem: HornetDownloader.exe = GUI (2), hdl.exe = console (3) | PASS | |
| 6 | Build | Zip contains hdl.exe, HornetDownloader.exe, LICENSE.txt, README.txt, THIRD_PARTY_NOTICES.md, licenses\ | PASS | |
| 7 | Install | Copy to `%LOCALAPPDATA%\Programs\HornetDownloader` | PASS | |
| 8 | Install | `install.ps1 -FromDir dist`: fresh install (changed=true), re-run (changed=false) | PASS | |
| 9 | Install | `install.ps1 -AddToPath`: PATH kept as REG_EXPAND_SZ, idempotent, restored after test | PASS | |
| 10 | Install | Tampered SHA256SUMS is rejected, nothing installed | PASS | |
| 11 | Install | `install.ps1` from the live release (anonymous HTTPS) | PASS (v1.5.3) | SHA256 OK, changed=true then re-run changed=false; installed exe version 1.5.3 |
| 12 | CLI | `hdl help --json` lists 8 commands | PASS | |
| 13 | CLI | Usage errors → exit 2 (no URL, unknown command, bad URL) | PASS | |
| 14 | CLI | App not running: `list` → 3, `get --no-start` → 3 | PASS | |
| 15 | CLI | Autostart is headless (`--no-window`, no window) | PASS | |
| 16 | CLI | `get` Debian kernel (~12 MB) → exit 0; SHA256 equals an independent curl download | PASS | |
| 17 | CLI | Idempotent re-`get` (same id, no new file) | PASS | |
| 18 | CLI | `add` / `wait` / `status` / `list` JSON; `addedBy=agent`; unknown id → 1 | PASS | |
| 19 | CLI | GUI exe CLI via pipe (`HornetDownloader.exe help --json | Out-String`) | PASS | |
| 20 | CLI | HTTP 404 → exit 1 with error text | PASS | |
| 21 | CLI | `--timeout` → exit 4, id reported | PASS | |
| 22 | CLI | `pause` / `resume`; `remove --delete-files` leaves nothing | PASS | |
| 23 | Network | aria2c RPC listens on 127.0.0.1 / ::1 only (netstat) | PASS | |
| 24 | Network | UI/API on 127.0.0.1:16801 only; BT 6881 on all interfaces by design | PASS | |
| 25 | BT | Debian netinst .torrent → bt engine, peers > 0, speed > 0 | PASS | ~3 MB/s, 15 peers |
| 26 | BT | Details tabs: overview / files / peers / trackers | PASS | |
| 27 | BT | Remove + delete files: files, .part, kept .torrent copy, empty dirs | PASS | a .part still mapped by the engine is queued in `bt/pending-delete.json` |
| 28 | BT | Queued files are deleted on next start | PASS | |
| 29 | yt-dlp | Video URL routes to yt-dlp; status is progress or a readable Chinese error | PASS | YouTube asks for sign-in from the test network (expected, shown as readable hint) |
| 30 | Firewall | `--firewall-dry-run` prints the netsh/UAC commands, no elevation | PASS | real add needs a UAC click; not clicked in QA |
| 31 | Firewall | `GET /api/firewall` reports rule state and btPort | PASS | |
| 32 | UI | Agent tag in task feed; source filter; context menu; details tabs; firewall buttons | PASS | |
| 33 | UI | Close-to-tray setting present | PASS | |
| 34 | GUI | Launch: HTTP 200, window shown, screenshot taken | PASS | |
| 35 | GUI | GUI mode: aria2c loopback only | PASS | |
| 36 | Cleanup | Test downloads removed, app stopped | PASS | |

Known limitations at this release: the BitTorrent engine keeps files memory-mapped until the process exits (deletion of those is deferred to the next start); some servers (e.g. speed.cloudflare.com `__down`) return a Content-Range aria2 rejects.

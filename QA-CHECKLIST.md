# Release QA checklist

Run on a real Windows 10/11 x64 machine before every release (`.\build.ps1`, then install and exercise the build).
Test only with content you are allowed to download (Debian images are used below). Tests and QA never open ports beyond loopback except the BitTorrent listener.

Last run: **v1.5.2**, 2026-10-24 (Asia/Shanghai), Windows 11 x64, Go 1.26.1, build `HornetDownloader-win64.zip` (see SHA256SUMS.txt of the release).

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
| 11 | Install | `install.ps1` from the live release (anonymous HTTPS) | after publish | run against the published release right after tagging |
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

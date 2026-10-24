# Copilot instructions — HornetDownloader

See [`AGENTS.md`](../AGENTS.md) for the full guide (file map, engine routing, API, build, definition of done).

- Agent-first, ad-free Windows downloader in Go: embedded aria2c (JSON-RPC), pure-Go BitTorrent (anacrolix/torrent), yt-dlp sidecar, WebView2 UI in `web/index.html` on 127.0.0.1:16801.
- Always `CGO_ENABLED=0`, single portable exe; no cgo/DLL dependencies.
- All adds go through `routeAdd` in `engines.go`; magnet/.torrent → BT, video sites → yt-dlp, else aria2. Explicit engine failure returns an error (no silent aria2 fallback).
- Never block HTTP handlers on aria2; use the cache/action queue in `resilience.go`.
- Automation: `hdl.exe` CLI (`get/add/wait/status/list/remove/pause/resume/help --json`, exit codes 0–5) and `/api/v1/*`; contract in [`docs/AGENT_API.md`](../docs/AGENT_API.md). Keep it backward compatible.
- Install/release: stable asset names at `https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/<asset>`; `scripts/install.ps1` installs to `%LOCALAPPDATA%\Programs\HornetDownloader`.
- User-facing strings are Simplified Chinese.
- Before finishing: `go vet ./...`, `go test ./...`, `.\build.ps1` on Windows. Test only with legal content.

# HornetDownloader

**Agent-first** 的 Windows 便携多引擎下载器（无广告、无上报）。单 `exe`：内嵌 aria2 + 纯 Go BitTorrent（anacrolix）+ 旁路 yt-dlp；本地 HTTP UI（WebView2）。
人用界面，脚本 / AI 编程助手用命令行 `hdl.exe`（JSON 输出、固定退出码、无窗口自启）和本地 API `/api/v1/*`，两边看到的是同一批任务。

**面向读者**：本文件优先给 coding agent / 维护者。代理协作约定见 [`AGENTS.md`](AGENTS.md)；自动化接口（CLI / API / 安装）见 [`docs/AGENT_API.md`](docs/AGENT_API.md)；终端用户说明见打包内的 `README.zh-CN.txt`；本机环境见 `DEV.md`。

当前版本：**v1.5.4** · 模块名 `hornetdl` · Go **1.26.10+**（`go.mod` 的 `toolchain` 行；1.26.0/1.26.1 在 Windows 上有 UDP 栈损坏崩溃，见 AGENTS.md） · **`CGO_ENABLED=0`**（必须保持，便于单文件分发）。

---

## 安装 / 下载（用户与 agent 通用）

Release 资产名固定，`latest` 链接永远指向最新版（仓库公开，匿名 HTTPS 即可，`gh` 可选）：

```powershell
# 下载 install.ps1 → 校验 SHA256 → 装到 %LOCALAPPDATA%\Programs\HornetDownloader（免管理员、可重复执行）
$f = "$env:TEMP\hd-install.ps1"
Invoke-WebRequest https://github.com/julius06117-cloud/HornetDownloader/releases/latest/download/install.ps1 -OutFile $f -UseBasicParsing
powershell -NoProfile -ExecutionPolicy Bypass -File $f -AddToPath -Json
# 国内访问 GitHub 慢：加 -Mirror https://ghfast.top/ -Retries 5；也可 gh release download 兜底
```

资产：`HornetDownloader-win64.zip`、`HornetDownloader.exe`、`hdl.exe`、`install.ps1`、`SHA256SUMS.txt`、`release.json`（版本、各资产 URL/SHA256/大小、安装命令）。手动安装与校验见 `docs/AGENT_API.md`。

---

## Agent 快速上手

```powershell
cd $env:USERPROFILE\dev\HornetDownloader   # 你的克隆目录
$env:CGO_ENABLED = "0"
$env:GOPROXY = "https://goproxy.cn,direct"   # 国内建议
go test ./...
go run .
.\build.ps1                # 测 + 打 zip → dist\（zip、exe、hdl.exe、install.ps1、SHA256SUMS.txt）
.\build.ps1 -SkipTest
.\build.ps1 -Run
```

产物：`build\HornetDownloader.exe`（GUI 子系统，无黑框）+ `build\hdl.exe`（控制台版，脚本用）。发布包把 `README.zh-CN.txt` 复制为 `README.txt`，并附 `LICENSE.txt`、`THIRD_PARTY_NOTICES.md`、`licenses\`。发布前按 [`QA-CHECKLIST.md`](QA-CHECKLIST.md) 全量自测。

合法内容测速：公开 HTTP、自建种子、发行版镜像。不要用盗版资源。

---

## 架构（改代码前先对上号）

```
[WebView2 / Edge]  →  127.0.0.1:16801 + X-Token
[hdl.exe / 脚本]   →  127.0.0.1:16801/api/v1/* + api-token   (agent_api.go, internal/agentcli)
        │
   server.go (embed web/)     resilience.go（任务缓存 / 操作队列 / 文件列表分页）
        │
   engines.go · routeAdd()    ← 所有「添加任务」的唯一入口
        ├── aria2.go          子进程 RPC（HTTP/FTP；自动模式下 BT 失败才回退）
        ├── bt_engine.go      anacrolix/torrent（磁力/.torrent 推荐）
        └── ytdlp_engine.go   旁路 yt-dlp.exe（视频站）
```

- **UI**：`web/index.html`（单页，`//go:embed web`）。改 UI 即改此文件；无独立前端构建。
- **安全**：API 仅本机 Host；`X-Token` 或 `Authorization: Bearer`（UI 每次启动的令牌，或持久的 `api-token`）；DNS 重绑定防护在 `server.go` `routes()`；无 CORS。
- **监听 / 防火墙**：UI/API 与 aria2 RPC 只绑回环；aria2 关 DHT/PEX/LPD，空闲时不开公网端口。只有 BT 引擎监听一个固定端口（`Config.BTPort`，6881–6999），所以 Windows 只会问一次；设置里「添加防火墙规则」经 **UAC** 提权写规则（默认仅专用网络），绝不静默提权；`--firewall-dry-run` 只打印 netsh 命令。测试只绑 `127.0.0.1`，跑测试不会弹防火墙。
- **Windows 壳**：托盘 / 窗口 / 剪贴板 / 文件关联 → `*_windows.go`；非 Windows stub → `*_other.go`。
- **内嵌二进制**：`embed/aria2c.exe`（`embed_windows.go` 首次释放到 `%APPDATA%\HornetDownloader\bin\`）。yt-dlp **不**内嵌，首次用时下载到同目录。

运行时数据：`%APPDATA%\HornetDownloader\`（`config.json`、`aria2.session`、`bt-session.json`、`bin\`、`app.log`、`api-token`、`api-port`、`task-source.json`）。不要提交这些文件（仓库公开）。

---

## 源码地图

| 文件 | 职责 |
|------|------|
| `main.go` | 启动、配置、单实例、inbox、CLI flags、分派 CLI 子命令 |
| `agent_api.go` | 自动化 API `/api/v1/*`、`api-token`、幂等添加、任务来源（agent / user） |
| `internal/agentcli/` · `cmd/hdl/` | 命令行 `get/add/wait/status/list/…`；`hdl.exe` 控制台版 |
| `firewall_cmds.go` / `firewall_windows.go` | netsh 命令构造（可测）/ UAC 提权执行 |
| `server.go` | HTTP 路由、RPC 白名单、任务合并 API |
| `engines.go` | `routeAdd` / 添加与引擎调度 |
| `recommend.go` | 引擎常量、`RecommendEngine`、`resolveEngine` |
| `aria2.go` | aria2 进程与 JSON-RPC |
| `bt_engine.go` | BitTorrent 引擎、kick、重选、详情快照 |
| `ytdlp_engine.go` | yt-dlp 进程与任务状态 |
| `resilience.go` | **UI 永不直等 aria2**：缓存、队列、选文件分页 |
| `details.go` | `/api/details`、`/api/reselect` |
| `artifacts.go` | 下载产物路径辅助 |
| `tray_*.go` / `window_*.go` / `clipboard_*.go` / `assoc_*.go` | 平台相关 |
| `scripts/install.ps1` · `scripts/release.ps1` | 安装（下载、校验、安装、PATH）/ 发布 GitHub Release |
| `*_test.go` | 引擎路由、API、CLI、BT 重选/速度等；改路由必跑相关测 |

---

## 硬性不变量（破坏即回归）

这些是 v1.5 修过的「总是变成 aria2 / 界面卡死」类问题。改引擎或添加路径时**必须**保持：

1. **所有添加走 `routeAdd`**（新建对话框、剪贴板、关联、命令行、拖放、种子上传）。不要旁路另写一套。
2. **`EngineAuto` vs `EngineDefault`**（`recommend.go`）  
   - 新建对话框选「自动」→ `auto`：**始终**按链接推荐，**忽略**设置里旧的默认引擎。  
   - 剪贴板 / 关联 / CLI / 拖放 → `default`：才尊重设置「默认引擎」。
3. **手动指定引擎失败：直接报错，绝不静默改 aria2。**  
   自动/默认下：重复磁力/种子返回已有任务（`existing`）；其它 BT 失败才回退 aria2，并带上 `fallback` 原因。
4. **界面不直接等待 aria2**（`resilience.go`）：列表读缓存；pause/remove 等进后台队列立刻返回；大种子文件列表服务端分页。不要把同步 RPC 挂回请求路径。
5. **保持 `CGO_ENABLED=0`**；BT 用 anacrolix 就是为了无 DLL。不要引入需 CGO 的 libtorrent 类依赖。
6. **Windows 磁盘分配默认 `prealloc`**（`Config.effectiveFileAllocation`）：NTFS 上 `none`/`trunc` 会卡死 aria2 RPC。改默认分配策略前先读该注释。

相关测试：`add_api_test.go`、`recommend_test.go`、`ui_ops_test.go`、`bt_reselect_test.go`。

---

## HTTP API（本地）

前缀 `/api/*`，需头 `X-Token`（与 UI 启动 URL 一致）或 `Authorization: Bearer <api-token>`。

**给 agent / 脚本的稳定接口是 `/api/v1/*`**（字段只增不改，详见 `docs/AGENT_API.md`）：

| 路径 | 用途 |
|------|------|
| `GET /api/v1/ping` | 存活检查（不算 UI 心跳） |
| `POST /api/v1/add` | 添加 `{url \| torrentB64, dir, engine, selectFiles, force}`；同源同目录幂等，返回 `existing` |
| `GET /api/v1/task?id=` · `GET /api/v1/tasks` | 统一任务视图（status/progress/files/peers/addedBy…），不消费 UI 通知 |
| `POST /api/v1/pause` · `resume` · `remove` | 控制；未知 id 返回 404 |

UI 内部路由（可随版本变化）常用：

| 路径 | 用途 |
|------|------|
| `GET/POST /api/tasks` | 合并任务列表（读 resilience 缓存） |
| `POST /api/add` | 添加 URL/种子 |
| `POST /api/recommend` | 推荐引擎（不强制） |
| `POST /api/action` | 暂停/继续等（入队） |
| `POST /api/files` / `/api/startbt` | BT 选文件 |
| `POST /api/reselect` | 任意状态重选文件 |
| `POST /api/details` | 任务详情页签数据 |
| `GET /api/config` · `POST` | 读写配置 |
| `GET /api/inbox` | 外部传入待添加项 |
| `/api/rpc` · `/api/multicall` | 受限 aria2 方法白名单 |

改 API 时同步 `web/index.html` 调用方，并补/改 `*_test.go`。

---

## 引擎路由速查

| 链接类型 | 推荐 |
|----------|------|
| `magnet:` / `.torrent` | `bt` |
| 已知视频站主机（B 站 / YouTube 等，见 `videoHosts`） | `ytdlp` |
| 其它 HTTP/FTP | `aria2` |

GID 前缀：`bt-*` → BitTorrent，`yt-*` → yt-dlp，其余 → aria2。`engineForGID` 以引擎 `Owns` 为准。

---

## CLI

### 自动化命令（agent / 脚本）

脚本里用 **`hdl.exe`**（控制台子系统，PowerShell 会等待并拿到输出）；`HornetDownloader.exe <命令>` 也支持，但需要管道（`| Out-String`）。

```
hdl get    <url|magnet|file.torrent> [--dir D] [--engine auto|aria2|bt|ytdlp] [--json] [--timeout 30m] [--force]
hdl add    <url|magnet|file.torrent> [--dir D] [--engine E] [--wait] [--json]
hdl wait   <id> [--json] [--timeout 30m]
hdl status <id> [--json]
hdl list   [--json]
hdl remove <id> [--delete-files]   ·   hdl pause|resume <id>
hdl version [--json]               # hdl / 正在运行的程序版本（不会启动程序）、Go 工具链、提交
hdl help   --json                  # 机器可读的命令 / 退出码清单
```

- `get` = 添加并等待，成功后输出文件路径（`--json` 输出完整任务对象）；进度只写 stderr。
- 退出码：`0` 成功 · `1` 任务失败 / 不存在 · `2` 参数错误 · `3` 应用不可用 · `4` 超时（任务继续跑，JSON 里带 id）· `5` 添加被拒。
- 应用没开时自动以 `--no-window` **无窗口**启动（不抢焦点）；之后双击 exe 会接到同一实例。`--no-start` 关闭自启。
- 同一链接 + 同一目录重复 `get` 直接返回已有任务（已完成且文件还在才算）；`--force` 重新下载。
- CLI / API 添加的任务在界面上带「Agent」标记，侧栏「来源」可筛选。

### 程序参数（调试）

```
HornetDownloader.exe [--port 16801] [--data-dir PATH] [--aria2 PATH]
                     [--browser] [--no-window] [--firewall-dry-run]
                     [--register-assoc | --unregister-assoc]
                     [URL | .torrent ...]
```

默认端口 `16801`。`--browser`：不用 WebView2，改用 Edge 应用窗口。`--no-window`：无窗口后台服务（CLI 自启用）。`--firewall-dry-run`：打印防火墙规则命令，不提权。

---

## 文档分工

| 文件 | 给谁 |
|------|------|
| **本 README** | Agent / 贡献者：架构、不变量、改哪里 |
| `AGENTS.md` | 编程助手协作约定：文件地图、路由规则、完成标准（`.cursor/rules`、`.github/copilot-instructions.md` 指向它） |
| `docs/AGENT_API.md` | 自动化契约：安装、CLI、退出码、任务 JSON、`/api/v1` |
| `QA-CHECKLIST.md` | 发布前 QA 清单与最近一次结果 |
| `DEV.md` | 本机 Home 开发环境与日常命令 |
| `README.zh-CN.txt` | 终端用户；打进 zip 为 `README.txt` |

改用户可见行为时：更新 `README.zh-CN.txt`（及必要时本文件的不变量说明）。不要把用户手册式长文塞进本 README。

---

## 许可证

本项目以 **MIT** 许可发布，见 [`LICENSE`](LICENSE)（Copyright (c) 2026 julius06117-cloud）。第三方组件各自许可，见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)；发布包内同时附带 `LICENSE.txt`、`THIRD_PARTY_NOTICES.md` 与 `licenses\`。

## 开源依赖（摘要）

- aria2 1.37.0（GPL-2.0-or-later，含 OpenSSL 例外）— 内嵌，作为独立进程运行；源码链接见 `THIRD_PARTY_NOTICES.md`
- [anacrolix/torrent](https://github.com/anacrolix/torrent)（MPL-2.0）
- yt-dlp（Unlicense；旁路，可选，首次用时下载，不打包）
- go-webview2 · 字体见 `web/fonts/LICENSES.txt`

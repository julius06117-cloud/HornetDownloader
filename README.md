# HornetDownloader

Windows 便携多引擎下载器（无广告、无上报）。单 `exe`：内嵌 aria2 + 纯 Go BitTorrent（anacrolix）+ 旁路 yt-dlp；本地 HTTP UI（WebView2）。

**面向读者**：本文件优先给 coding agent / 维护者。终端用户说明见打包内的 `README.zh-CN.txt`；本机环境见 `DEV.md`。

当前版本：**v1.5.1** · 模块名 `hornetdl` · Go **1.26** · **`CGO_ENABLED=0`**（必须保持，便于单文件分发）。

---

## Agent 快速上手

```powershell
cd %USERPROFILE%\dev\HornetDownloader
$env:CGO_ENABLED = "0"
$env:GOPROXY = "https://goproxy.cn,direct"   # 国内建议
go test ./...
go run .
.\build.ps1                # 测 + 打 zip → dist\HornetDownloader-win64.zip
.\build.ps1 -SkipTest
.\build.ps1 -Run
```

产物：`build\HornetDownloader.exe`，发布包把 `README.zh-CN.txt` 复制为 `README.txt`。

合法内容测速：公开 HTTP、自建种子、发行版镜像。不要用盗版资源。

---

## 架构（改代码前先对上号）

```
[WebView2 / Edge]  →  localhost:16801 + X-Token
        │
   server.go (embed web/)     resilience.go（任务缓存 / 操作队列 / 文件列表分页）
        │
   engines.go · routeAdd()    ← 所有「添加任务」的唯一入口
        ├── aria2.go          子进程 RPC（HTTP/FTP；自动模式下 BT 失败才回退）
        ├── bt_engine.go      anacrolix/torrent（磁力/.torrent 推荐）
        └── ytdlp_engine.go   旁路 yt-dlp.exe（视频站）
```

- **UI**：`web/index.html`（单页，`//go:embed web`）。改 UI 即改此文件；无独立前端构建。
- **安全**：API 仅本机 Host；自定义头 `X-Token`；DNS 重绑定防护在 `server.go` `routes()`。
- **Windows 壳**：托盘 / 窗口 / 剪贴板 / 文件关联 → `*_windows.go`；非 Windows stub → `*_other.go`。
- **内嵌二进制**：`embed/aria2c.exe`（`embed_windows.go` 首次释放到 `%APPDATA%\HornetDownloader\bin\`）。yt-dlp **不**内嵌，首次用时下载到同目录。

运行时数据：`%APPDATA%\HornetDownloader\`（`config.json`、`aria2.session`、`bt-session.json`、`bin\`、`app.log`）。

---

## 源码地图

| 文件 | 职责 |
|------|------|
| `main.go` | 启动、配置、单实例、inbox、CLI flags |
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
| `*_test.go` | 引擎路由、API、BT 重选/速度等；改路由必跑相关测 |

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

前缀 `/api/*`，需头 `X-Token`（与 UI 启动 URL 一致）。常用：

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

## CLI（调试）

```
HornetDownloader.exe [--port 16801] [--data-dir PATH] [--aria2 PATH]
                     [--browser] [--no-window]
                     [--register-assoc | --unregister-assoc]
                     [URL | .torrent ...]
```

默认端口 `16801`。`--browser`：不用 WebView2，改用 Edge 应用窗口。

---

## 文档分工

| 文件 | 给谁 |
|------|------|
| **本 README** | Agent / 贡献者：架构、不变量、改哪里 |
| `DEV.md` | 本机 Home 开发环境与日常命令 |
| `README.zh-CN.txt` | 终端用户；打进 zip 为 `README.txt` |

改用户可见行为时：更新 `README.zh-CN.txt`（及必要时本文件的不变量说明）。不要把用户手册式长文塞进本 README。

---

## 开源依赖（摘要）

- aria2 1.37.0（GPL-2.0）— 内嵌
- [anacrolix/torrent](https://github.com/anacrolix/torrent)（MPL-2.0）
- yt-dlp（旁路，可选）
- go-webview2 · 字体见 `web/fonts/LICENSES.txt`

# HornetDownloader 本机开发说明

## 环境（已在 Home 配好）

- Go 1.22+（本机为 1.26.1），`CGO_ENABLED=0`
- Git、Node（UI 脚本检查用）
- WebView2 Runtime（随 Edge，本机已有）
- `GOPROXY=https://goproxy.cn,direct`（国内拉模块）

工作目录：本机克隆的仓库根目录（例如 `%USERPROFILE%\dev\HornetDownloader`）

## 日常命令

```powershell
cd $env:USERPROFILE\dev\HornetDownloader
.\build.ps1          # 测试 + 打 zip → dist\（zip、exe、hdl.exe、install.ps1、SHA256SUMS.txt）
.\build.ps1 -SkipTest
.\build.ps1 -Run     # 构建并启动
go test ./...
go run .
```

## 约定

- 构建与真机 QA **优先在本机 Home** 完成；远端盒子只做无法在本机做的事（例如 Linux 对照）。
- 合法内容测试：Ubuntu/Debian 镜像、自建种子、公开 HTTP；不要用盗版资源测速度。
- 产物：`dist\HornetDownloader-win64.zip`（HornetDownloader.exe + hdl.exe + README + LICENSE + THIRD_PARTY_NOTICES.md + licenses\）。
- 仓库已公开：不要提交本机路径、用户名、日志、截图、`config.json`、`api-token`。

## Automation / agent usage（自动化 / 编程助手）

完整说明见 [`docs/AGENT_API.md`](docs/AGENT_API.md)。本机常用：

```powershell
.\build.ps1 -SkipTest
.\build\hdl.exe help --json
.\build\hdl.exe get "https://deb.debian.org/debian/dists/stable/main/installer-amd64/current/images/netboot/debian-installer/amd64/linux" --dir "$env:TEMP\hdl-test" --json
.\build\hdl.exe list --json
# 安装到 %LOCALAPPDATA%\Programs\HornetDownloader（从本地 dist\，不联网）
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -FromDir dist -Json
# 防火墙规则：只看命令，不提权
.\build\HornetDownloader.exe --firewall-dry-run | Out-String
```

- 应用没开时 CLI 会以 `--no-window` 无窗口启动后台实例；之后再双击 exe 会接到同一实例。
- 退出码：0 成功、1 任务失败、2 参数错误、3 应用不可用、4 超时、5 添加被拒。
- 发布前按 [`QA-CHECKLIST.md`](QA-CHECKLIST.md) 全量自测；发布用 `scripts\release.ps1`。

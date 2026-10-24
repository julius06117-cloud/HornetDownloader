# HornetDownloader 本机开发说明

## 环境（已在 Home 配好）

- Go 1.22+（本机为 1.26.1），`CGO_ENABLED=0`
- Git、Node（UI 脚本检查用）
- WebView2 Runtime（随 Edge，本机已有）
- `GOPROXY=https://goproxy.cn,direct`（国内拉模块）

工作目录：`%USERPROFILE%\dev\HornetDownloader`

## 日常命令

```powershell
cd %USERPROFILE%\dev\HornetDownloader
.\build.ps1          # 测试 + 打 zip → dist\HornetDownloader-win64.zip
.\build.ps1 -SkipTest
.\build.ps1 -Run     # 构建并启动
go test ./...
go run .
```

## 约定

- 构建与真机 QA **优先在本机 Home** 完成；远端盒子只做无法在本机做的事（例如 Linux 对照）。
- 合法内容测试：Ubuntu/Debian 镜像、自建种子、公开 HTTP；不要用盗版资源测速度。
- 产物：`dist\HornetDownloader-win64.zip`（单 exe + README）。

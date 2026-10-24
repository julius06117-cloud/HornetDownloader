#!/usr/bin/env bash
# 交叉编译单一便携 Windows 可执行文件（内嵌 aria2c）。
set -euo pipefail
cd "$(dirname "$0")"
test -f embed/aria2c.exe || { echo "缺少 embed/aria2c.exe"; exit 1; }
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o build/HornetDownloader.exe .
rm -rf build/pkg && mkdir -p build/pkg/HornetDownloader dist
cp build/HornetDownloader.exe build/pkg/HornetDownloader/
printf '\xef\xbb\xbf' > build/pkg/HornetDownloader/README.txt
sed 's/$/\r/' README.zh-CN.txt >> build/pkg/HornetDownloader/README.txt
rm -f dist/HornetDownloader-win64.zip
(cd build/pkg && zip -9 -r -q ../../dist/HornetDownloader-win64.zip HornetDownloader)
ls -la build/HornetDownloader.exe dist/HornetDownloader-win64.zip

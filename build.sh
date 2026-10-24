#!/usr/bin/env bash
# 交叉编译单一便携 Windows 可执行文件（内嵌 aria2c）。
set -euo pipefail
cd "$(dirname "$0")"
test -f embed/aria2c.exe || { echo "缺少 embed/aria2c.exe"; exit 1; }
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o build/HornetDownloader.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o build/hdl.exe ./cmd/hdl
rm -rf build/pkg && mkdir -p build/pkg/HornetDownloader dist
cp build/HornetDownloader.exe build/hdl.exe build/pkg/HornetDownloader/
cp LICENSE build/pkg/HornetDownloader/LICENSE.txt
cp THIRD_PARTY_NOTICES.md build/pkg/HornetDownloader/
mkdir -p build/pkg/HornetDownloader/licenses
cp third_party/aria2-1.37.0-win-64bit-build1/COPYING build/pkg/HornetDownloader/licenses/aria2-COPYING.txt
cp third_party/aria2-1.37.0-win-64bit-build1/LICENSE.OpenSSL build/pkg/HornetDownloader/licenses/aria2-LICENSE.OpenSSL.txt
printf '\xef\xbb\xbf' > build/pkg/HornetDownloader/README.txt
sed 's/$/\r/' README.zh-CN.txt >> build/pkg/HornetDownloader/README.txt
rm -f dist/HornetDownloader-win64.zip
(cd build/pkg && zip -9 -r -q ../../dist/HornetDownloader-win64.zip HornetDownloader)
cp build/HornetDownloader.exe build/hdl.exe scripts/install.ps1 dist/
(cd dist && sha256sum HornetDownloader-win64.zip HornetDownloader.exe hdl.exe install.ps1 > SHA256SUMS.txt)
ls -la build/HornetDownloader.exe build/hdl.exe dist/HornetDownloader-win64.zip

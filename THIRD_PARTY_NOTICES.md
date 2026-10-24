# Third-party notices

HornetDownloader's own source code is licensed under the MIT License (see [LICENSE](LICENSE)).
The release binaries bundle or use the following third-party software, each under its own license.

## aria2 1.37.0 (bundled: `aria2c.exe`)

- License: **GPL-2.0-or-later**, with the OpenSSL linking exception (see `licenses/aria2-COPYING.txt`
  and `licenses/aria2-LICENSE.OpenSSL.txt` in the release zip, copied from
  `third_party/aria2-1.37.0-win-64bit-build1/`).
- Upstream: <https://aria2.github.io/> · <https://github.com/aria2/aria2>
- **Corresponding source code** for the exact bundled version (aria2 1.37.0, official
  `aria2-1.37.0-win-64bit-build1` binary):
  <https://github.com/aria2/aria2/releases/tag/release-1.37.0>
  (source tarball: <https://github.com/aria2/aria2/releases/download/release-1.37.0/aria2-1.37.0.tar.xz>).
  If that link ever stops working, open an issue in this repository and a copy of the source will be provided.
- How it is used: `aria2c.exe` is an unmodified upstream binary, embedded in `HornetDownloader.exe`,
  extracted to `%APPDATA%\HornetDownloader\bin\aria2c.exe` at runtime and run as a **separate process**
  controlled over local JSON-RPC (aggregation; HornetDownloader is not a derivative work of aria2).

## yt-dlp (not bundled; downloaded / located at runtime)

- License: **Unlicense** (public domain dedication) — <https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE>
- HornetDownloader does not ship yt-dlp. It uses an existing `yt-dlp.exe` (e.g. installed via winget)
  or downloads the official release at runtime, and runs it as a separate process.
  The official yt-dlp Windows build itself bundles components under other licenses (see yt-dlp's
  `THIRD_PARTY_LICENSES.txt`).

## Microsoft Edge WebView2 Runtime (not bundled)

- Provided by Windows / Microsoft; used through `github.com/jchv/go-webview2` (MIT).

## Go standard library

- Go 1.26 — BSD-3-Clause — <https://go.dev/LICENSE>

## Go modules compiled into HornetDownloader.exe / hdl.exe

Generated from `go version -m HornetDownloader.exe` (modules actually linked into the binary).
Notable: **anacrolix/torrent and several anacrolix modules are MPL-2.0** (file-level copyleft;
unmodified, source available at the links below).

| Module | Version | License |
|---|---|---|
| [github.com/RoaringBitmap/roaring](https://pkg.go.dev/github.com/RoaringBitmap/roaring@v1.2.3?tab=licenses) | v1.2.3 | Apache-2.0 |
| [github.com/alecthomas/atomic](https://pkg.go.dev/github.com/alecthomas/atomic@v0.1.0-alpha2?tab=licenses) | v0.1.0-alpha2 | MIT |
| [github.com/anacrolix/btree](https://pkg.go.dev/github.com/anacrolix/btree@v0.0.0-20251201064447-d86c3fa41bd8?tab=licenses) | v0.0.0-20251201064447-d86c3fa41bd8 | Apache-2.0 |
| [github.com/anacrolix/chansync](https://pkg.go.dev/github.com/anacrolix/chansync@v0.7.0?tab=licenses) | v0.7.0 | MIT |
| [github.com/anacrolix/dht/v2](https://pkg.go.dev/github.com/anacrolix/dht/v2@v2.23.0?tab=licenses) | v2.23.0 | MPL-2.0 |
| [github.com/anacrolix/envpprof](https://pkg.go.dev/github.com/anacrolix/envpprof@v1.4.0?tab=licenses) | v1.4.0 | MIT |
| [github.com/anacrolix/generics](https://pkg.go.dev/github.com/anacrolix/generics@v0.1.1-0.20251125230353-15d98d46693b?tab=licenses) | v0.1.1-0.20251125230353-15d98d46693b | MPL-2.0 |
| [github.com/anacrolix/log](https://pkg.go.dev/github.com/anacrolix/log@v0.17.1-0.20251118025802-918f1157b7bb?tab=licenses) | v0.17.1-0.20251118025802-918f1157b7bb | MPL-2.0 |
| [github.com/anacrolix/missinggo](https://pkg.go.dev/github.com/anacrolix/missinggo@v1.3.0?tab=licenses) | v1.3.0 | MIT |
| [github.com/anacrolix/missinggo/perf](https://pkg.go.dev/github.com/anacrolix/missinggo/perf@v1.0.0?tab=licenses) | v1.0.0 | MIT |
| [github.com/anacrolix/missinggo/v2](https://pkg.go.dev/github.com/anacrolix/missinggo/v2@v2.10.0?tab=licenses) | v2.10.0 | MIT |
| [github.com/anacrolix/multiless](https://pkg.go.dev/github.com/anacrolix/multiless@v0.4.0?tab=licenses) | v0.4.0 | MPL-2.0 |
| [github.com/anacrolix/stm](https://pkg.go.dev/github.com/anacrolix/stm@v0.5.0?tab=licenses) | v0.5.0 | MIT |
| [github.com/anacrolix/sync](https://pkg.go.dev/github.com/anacrolix/sync@v0.5.5-0.20251119100342-d78dd1f686f1?tab=licenses) | v0.5.5-0.20251119100342-d78dd1f686f1 | MPL-2.0 |
| [github.com/anacrolix/torrent](https://pkg.go.dev/github.com/anacrolix/torrent@v1.61.0?tab=licenses) | v1.61.0 | MPL-2.0 |
| [github.com/anacrolix/upnp](https://pkg.go.dev/github.com/anacrolix/upnp@v0.1.4?tab=licenses) | v0.1.4 | MPL-2.0 |
| [github.com/anacrolix/utp](https://pkg.go.dev/github.com/anacrolix/utp@v0.1.0?tab=licenses) | v0.1.0 | MPL-2.0 |
| [github.com/bahlo/generic-list-go](https://pkg.go.dev/github.com/bahlo/generic-list-go@v0.2.0?tab=licenses) | v0.2.0 | BSD-3-Clause |
| [github.com/benbjohnson/immutable](https://pkg.go.dev/github.com/benbjohnson/immutable@v0.4.1-0.20221220213129-8932b999621d?tab=licenses) | v0.4.1-0.20221220213129-8932b999621d | MIT |
| [github.com/bradfitz/iter](https://pkg.go.dev/github.com/bradfitz/iter@v0.0.0-20191230175014-e8f45d346db8?tab=licenses) | v0.0.0-20191230175014-e8f45d346db8 | BSD-3-Clause |
| [github.com/cespare/xxhash](https://pkg.go.dev/github.com/cespare/xxhash@v1.1.0?tab=licenses) | v1.1.0 | MIT |
| [github.com/davecgh/go-spew](https://pkg.go.dev/github.com/davecgh/go-spew@v1.1.1?tab=licenses) | v1.1.1 | ISC |
| [github.com/dustin/go-humanize](https://pkg.go.dev/github.com/dustin/go-humanize@v1.0.0?tab=licenses) | v1.0.0 | MIT |
| [github.com/edsrzf/mmap-go](https://pkg.go.dev/github.com/edsrzf/mmap-go@v1.1.0?tab=licenses) | v1.1.0 | BSD-3-Clause |
| [github.com/go-logr/logr](https://pkg.go.dev/github.com/go-logr/logr@v1.4.3?tab=licenses) | v1.4.3 | Apache-2.0 |
| [github.com/go-logr/stdr](https://pkg.go.dev/github.com/go-logr/stdr@v1.2.2?tab=licenses) | v1.2.2 | Apache-2.0 |
| [github.com/google/btree](https://pkg.go.dev/github.com/google/btree@v1.1.2?tab=licenses) | v1.1.2 | Apache-2.0 |
| [github.com/google/go-cmp](https://pkg.go.dev/github.com/google/go-cmp@v0.7.0?tab=licenses) | v0.7.0 | BSD-3-Clause |
| [github.com/google/uuid](https://pkg.go.dev/github.com/google/uuid@v1.6.0?tab=licenses) | v1.6.0 | BSD-3-Clause |
| [github.com/gorilla/websocket](https://pkg.go.dev/github.com/gorilla/websocket@v1.5.0?tab=licenses) | v1.5.0 | BSD-2-Clause |
| [github.com/huandu/xstrings](https://pkg.go.dev/github.com/huandu/xstrings@v1.3.2?tab=licenses) | v1.3.2 | MIT |
| [github.com/jchv/go-webview2](https://pkg.go.dev/github.com/jchv/go-webview2@v0.0.0-20260205173254-56598839c808?tab=licenses) | v0.0.0-20260205173254-56598839c808 | MIT |
| [github.com/jchv/go-winloader](https://pkg.go.dev/github.com/jchv/go-winloader@v0.0.0-20250406163304-c1995be93bd1?tab=licenses) | v0.0.0-20250406163304-c1995be93bd1 | ISC |
| [github.com/klauspost/cpuid/v2](https://pkg.go.dev/github.com/klauspost/cpuid/v2@v2.2.3?tab=licenses) | v2.2.3 | MIT |
| [github.com/mr-tron/base58](https://pkg.go.dev/github.com/mr-tron/base58@v1.2.0?tab=licenses) | v1.2.0 | MIT |
| [github.com/multiformats/go-multihash](https://pkg.go.dev/github.com/multiformats/go-multihash@v0.2.3?tab=licenses) | v0.2.3 | MIT |
| [github.com/multiformats/go-varint](https://pkg.go.dev/github.com/multiformats/go-varint@v0.0.6?tab=licenses) | v0.0.6 | MIT |
| [github.com/pion/datachannel](https://pkg.go.dev/github.com/pion/datachannel@v1.5.9?tab=licenses) | v1.5.9 | MIT |
| [github.com/pion/dtls/v3](https://pkg.go.dev/github.com/pion/dtls/v3@v3.0.3?tab=licenses) | v3.0.3 | MIT |
| [github.com/pion/ice/v4](https://pkg.go.dev/github.com/pion/ice/v4@v4.0.2?tab=licenses) | v4.0.2 | MIT |
| [github.com/pion/interceptor](https://pkg.go.dev/github.com/pion/interceptor@v0.1.40?tab=licenses) | v0.1.40 | MIT |
| [github.com/pion/logging](https://pkg.go.dev/github.com/pion/logging@v0.2.3?tab=licenses) | v0.2.3 | MIT |
| [github.com/pion/mdns/v2](https://pkg.go.dev/github.com/pion/mdns/v2@v2.0.7?tab=licenses) | v2.0.7 | MIT |
| [github.com/pion/randutil](https://pkg.go.dev/github.com/pion/randutil@v0.1.0?tab=licenses) | v0.1.0 | MIT |
| [github.com/pion/rtcp](https://pkg.go.dev/github.com/pion/rtcp@v1.2.15?tab=licenses) | v1.2.15 | MIT |
| [github.com/pion/rtp](https://pkg.go.dev/github.com/pion/rtp@v1.8.18?tab=licenses) | v1.8.18 | MIT |
| [github.com/pion/sctp](https://pkg.go.dev/github.com/pion/sctp@v1.8.33?tab=licenses) | v1.8.33 | MIT |
| [github.com/pion/sdp/v3](https://pkg.go.dev/github.com/pion/sdp/v3@v3.0.9?tab=licenses) | v3.0.9 | MIT |
| [github.com/pion/srtp/v3](https://pkg.go.dev/github.com/pion/srtp/v3@v3.0.4?tab=licenses) | v3.0.4 | MIT |
| [github.com/pion/stun/v3](https://pkg.go.dev/github.com/pion/stun/v3@v3.0.0?tab=licenses) | v3.0.0 | MIT |
| [github.com/pion/transport/v3](https://pkg.go.dev/github.com/pion/transport/v3@v3.0.7?tab=licenses) | v3.0.7 | MIT |
| [github.com/pion/turn/v4](https://pkg.go.dev/github.com/pion/turn/v4@v4.0.0?tab=licenses) | v4.0.0 | MIT |
| [github.com/pion/webrtc/v4](https://pkg.go.dev/github.com/pion/webrtc/v4@v4.0.0?tab=licenses) | v4.0.0 | MIT |
| [github.com/pkg/errors](https://pkg.go.dev/github.com/pkg/errors@v0.9.1?tab=licenses) | v0.9.1 | BSD-2-Clause |
| [github.com/protolambda/ctxlock](https://pkg.go.dev/github.com/protolambda/ctxlock@v0.1.0?tab=licenses) | v0.1.0 | MIT |
| [github.com/rs/dnscache](https://pkg.go.dev/github.com/rs/dnscache@v0.0.0-20211102005908-e0241e321417?tab=licenses) | v0.0.0-20211102005908-e0241e321417 | MIT |
| [github.com/spaolacci/murmur3](https://pkg.go.dev/github.com/spaolacci/murmur3@v1.1.0?tab=licenses) | v1.1.0 | BSD-3-Clause |
| [github.com/tidwall/btree](https://pkg.go.dev/github.com/tidwall/btree@v1.8.1?tab=licenses) | v1.8.1 | MIT |
| [github.com/wlynxg/anet](https://pkg.go.dev/github.com/wlynxg/anet@v0.0.3?tab=licenses) | v0.0.3 | BSD-3-Clause |
| [go.etcd.io/bbolt](https://pkg.go.dev/go.etcd.io/bbolt@v1.3.6?tab=licenses) | v1.3.6 | MIT |
| [go.opentelemetry.io/auto/sdk](https://pkg.go.dev/go.opentelemetry.io/auto/sdk@v1.2.1?tab=licenses) | v1.2.1 | Apache-2.0 |
| [go.opentelemetry.io/otel](https://pkg.go.dev/go.opentelemetry.io/otel@v1.38.0?tab=licenses) | v1.38.0 | Apache-2.0 |
| [go.opentelemetry.io/otel/metric](https://pkg.go.dev/go.opentelemetry.io/otel/metric@v1.38.0?tab=licenses) | v1.38.0 | Apache-2.0 |
| [go.opentelemetry.io/otel/trace](https://pkg.go.dev/go.opentelemetry.io/otel/trace@v1.38.0?tab=licenses) | v1.38.0 | Apache-2.0 |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto@v0.44.0?tab=licenses) | v0.44.0 | BSD-3-Clause |
| [golang.org/x/exp](https://pkg.go.dev/golang.org/x/exp@v0.0.0-20251113190631-e25ba8c21ef6?tab=licenses) | v0.0.0-20251113190631-e25ba8c21ef6 | BSD-3-Clause |
| [golang.org/x/net](https://pkg.go.dev/golang.org/x/net@v0.47.0?tab=licenses) | v0.47.0 | BSD-3-Clause |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync@v0.18.0?tab=licenses) | v0.18.0 | BSD-3-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys@v0.49.0?tab=licenses) | v0.49.0 | BSD-3-Clause |
| [golang.org/x/text](https://pkg.go.dev/golang.org/x/text@v0.31.0?tab=licenses) | v0.31.0 | BSD-3-Clause |
| [golang.org/x/time](https://pkg.go.dev/golang.org/x/time@v0.14.0?tab=licenses) | v0.14.0 | BSD-3-Clause |
| [lukechampine.com/blake3](https://pkg.go.dev/lukechampine.com/blake3@v1.1.6?tab=licenses) | v1.1.6 | MIT |

Regenerate this table after dependency changes:
`go version -m build\HornetDownloader.exe` and check each module's license on pkg.go.dev.

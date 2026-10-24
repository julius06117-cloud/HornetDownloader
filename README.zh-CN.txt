Hornet 下载器 v1.5.4（Windows 64 位 · 多引擎便携版）
================================================

没有广告、没有数据上报的下载工具。内嵌多个下载引擎，解压后只需一个
HornetDownloader.exe 即可运行（yt-dlp 首次使用视频站链接时再下载旁路）。

支持：HTTP / HTTPS / FTP、磁力链接（magnet:）、.torrent 种子、
      常见视频站点页面（B 站 / YouTube 等，经 yt-dlp）。
暂不支持：网盘（百度等）、迅雷专用链接（thunder://）、ed2k。


一、怎么用
----------
1. 解压后双击 HornetDownloader.exe。
   第一次运行会把内嵌的 aria2c 释放到 %APPDATA%\HornetDownloader\bin\，
   并尝试注册 magnet: 与 .torrent 的打开方式。
2. 想让 BT/磁力更快：「设置 → 防火墙 → 添加防火墙规则」，在弹出的 Windows 管理员确认里点「是」。
   （首次运行若 Windows 防火墙弹窗询问，也可直接选「允许」。）
3. 「＋ 新建任务」粘贴链接（可多行）；也可 Ctrl+V，或把 .torrent 拖进窗口。
4. 新建任务时可选择下载引擎：自动 / aria2 / BitTorrent / yt-dlp。
   「自动」会按链接类型给出推荐（界面旁有简短说明）。
5. 复制了 http(s)/ftp/magnet 链接时，程序会弹出「是否添加下载」确认框。
6. 浏览器里点击磁力链接，或用「打开方式」选择本程序打开 .torrent，
   会调起本程序并加入任务（单实例：已打开时会转给现有窗口）。
7. 默认关闭窗口会最小化到系统托盘；托盘图标右键可「显示主窗口 / 退出」。


二、多引擎说明（v1.4）
--------------------
| 引擎        | 适合                         | 实现方式                          |
|-------------|------------------------------|-----------------------------------|
| aria2       | HTTP/FTP 直链、备用磁力/种子 | 内嵌官方 aria2c 1.37.0            |
| BitTorrent  | 磁力、.torrent（推荐）       | 纯 Go：anacrolix/torrent（同角色于 libtorrent，便于单文件便携，无 CGO/DLL） |
| yt-dlp      | 视频站点页面链接             | 旁路 yt-dlp.exe（首次用时尝试自动下载到 bin\） |

设置里可改「默认引擎」：自动，或强制某一个。
任务列表名称旁会显示实际使用的引擎标签。

引擎路由（v1.5 重要）：
- 新建对话框选「自动」时，始终按链接推荐（磁力/种子→BitTorrent，视频站→yt-dlp，其它→aria2），
  不会被设置里旧的「默认 aria2」覆盖。
- 剪贴板 / 关联 / 命令行 / 拖放 / 种子按钮：走设置「默认引擎」；若是「自动」则跟推荐。
- 手动指定引擎失败时不再偷偷换成 aria2；同一磁力已在列表时返回已有任务，不会再添加一份 aria2。
- 仅「自动/默认」且 BitTorrent 因其它原因失败时，才回退 aria2，并在提示里写明原因。

修正（v1.5.4）：
- 修复一处崩溃：BT 下载中出现某些警告（例如磁力里带 wss:// tracker）时，正常双击启动的程序
  可能直接退出（crash-*.log 里是「write /dev/stderr: The handle is invalid」）。
- 下载到网页（HTML）而判为失败的任务，重启后不再被 aria2 重新下载一遍；任务仍显示为失败，
  可以删除，也可以「重新开始」（按原链接重新下载）。v1.5.3 留下的这类任务会自动转换。
- 添加 .torrent 种子时，不再往下载文件夹里复制一份 .torrent；种子保存在
  %APPDATA%\HornetDownloader\bt\meta\ 里，删除任务时一起删除。
  旧版本留在下载文件夹里的 .torrent 不会被动；删除任务并勾选删除文件时照旧一并删除。
- 新增 hdl version（加 --json 输出 JSON）：显示 hdl 版本、正在运行的程序版本（不会启动程序）、
  Go 工具链版本和构建所用的提交。

稳定性与修正（v1.5.3）：
- 修复「用着用着程序自己消失」：原因是旧版用的 Go 1.26.1 在 Windows 上收发 UDP（BT 的
  DHT / Tracker）时会损坏内存，BT 下载一忙就直接崩溃退出。现改用 Go 1.26.10 构建，
  20 多分钟高负载 BT + HTTP 连续测试无崩溃。
- 万一再异常退出：%APPDATA%\HornetDownloader 里会留下 crash-时间.log，上一次的日志保留为
  app.prev.log，下次启动会在 app.log 里提示。app.log 每 5 分钟记一行内存占用。
- 下载时如实标明身份（User-Agent：HornetDownloader/版本），不再冒充 Chrome。清华 TUNA、
  北外 BFSU 等镜像以前会因此返回 403；遇到 403/418 会自动换备用 UA（默认 Wget）重试一次。
  设置里可改「User-Agent」和「备用 User-Agent」。
- BT 任务的编号由种子决定，重启后不变；创建时间重启后保留；重启后校验已有数据时不再显示假的
  超高速度；暂停中的任务速度显示 0。
- 已完成的多文件种子也能「选择文件」补下之前没选的文件。
- 上传速度在列表和详情里正确显示。
- 维基共享资源（commons.wikimedia.org/wiki/File:…）等媒体页面自动走 yt-dlp；如果直链下载到的
  其实是网页（HTML），任务判为失败并删除这个假文件；完成后的 .aria2 控制文件会清理掉。
- 已删除的任务重启后不会再「复活」；暂停/继续后马上退出，重启后状态不会错。
- 剪贴板：同一链接只问一次；设置里可关闭「剪贴板监视」。
- 小窗口 / 高缩放（如 1280×820、150%）下列表不再挤乱；任务详情只有一个关闭按钮「×」。
- exe 文件属性里的版本号与程序版本一致。

命令行与自动化（v1.5.2）：
- 新增 hdl.exe（与 HornetDownloader.exe 放在同一文件夹）：脚本 / 编程助手可以用
    hdl get "链接" --dir D:\下载 --json
  下载并等待完成；还有 add / wait / status / list / pause / resume / remove。
  程序没打开时会在后台无窗口启动；之后双击 exe 打开的是同一个实例。
- 命令行或脚本添加的任务在列表里带「Agent」标记，左侧「来源」可筛选「我添加的 / Agent 添加的」。
- 详细说明：https://github.com/julius06117-cloud/HornetDownloader/blob/main/docs/AGENT_API.md
- 删除 BT 任务并勾选删除文件时，未下载完的 .part 文件与旧版保存的 .torrent 副本也会删除
  （如果文件仍被占用，会在下次启动时删除）。
- 许可：MIT（见 LICENSE.txt）；第三方组件见 THIRD_PARTY_NOTICES.md 与 licenses 文件夹。

防火墙与端口（v1.5.2）：
- aria2 只负责 HTTP/FTP，平时只监听本机 127.0.0.1，不会再触发防火墙弹窗；
  它的 DHT/PEX/LPD 已关闭（磁力/种子默认由内置 BitTorrent 引擎处理）。
- BitTorrent 监听端口首次选定后固定保存在 config.json（btPort），规则与路由器映射不再失效。
- 设置页新增「添加防火墙规则 / 删除规则」：通过 Windows 管理员确认写入名为
  HornetDownloader 的入站规则，默认仅专用网络，可勾选同时允许公用网络；可重复点击。
- 内嵌 aria2c 固定释放到 %APPDATA%\HornetDownloader\bin\aria2c.exe，内容不变不重写。

界面修正（v1.5.1）：
- 任务详情切换页签不再被过期响应盖住；Esc 关闭右键菜单；yt-dlp 缺失提示更清楚。
- 操作列不再出现两个「暂停」；下载中只显示一个「暂停」，已暂停显示「继续」，「停止/删除」在右键菜单。
- 状态列改为短文案（如「下载中」「连接中 10/40」）+ 第二行 peer 摘要，完整信息在鼠标悬停与任务详情里。

任务操作（v1.5）：

- 右键任务行（或点 ⋯ / Shift+F10）：暂停/继续、停止（删任务留文件）、重试、删除、打开文件夹、
  复制链接、选择文件（BT）、任务详情。
- 「任务详情」含概览 / 文件 / Tracker / 来源·节点，后台刷新，不卡界面。
- 下载中 / 暂停 / 已完成均可重新选择多文件种子中的文件（aria2 与 BitTorrent）。

BitTorrent 速度（v1.5）：
- 有 peer 却 0 速度时自动 kick：重新 AllowDataDownload、恢复文件优先级。
- 存储改用 part files + 内存 piece completion，减轻「迅雷下载」等目录权限/长路径触发的停下。


三、多线程与断点续传
--------------------
- HTTP/HTTPS/FTP（aria2）默认多连接分段：单任务连接数默认 16（设置可改）。
- continue=true + 会话文件：暂停、退出后可续传。
- BitTorrent 引擎按分块落盘，重启后从会话恢复未完成任务。
- yt-dlp 依赖自身对未完成文件的续传能力。


四、国内网络与磁盘
------------------
- 内置偏国内可达的公共 Tracker（CnTrackersList / XIU2 等思路）。
- BitTorrent：DHT / PEX；监听端口在 6881–6999 中选定后固定；开启 UPnP/NAT-PMP。
- BitTorrent 引擎会定期补加公共 Tracker，状态栏显示「候选/握手/已连」节点数。
- 请在防火墙放行本程序（入站，设置页一键添加），否则磁力常显示「寻找节点中」且速度为 0。
- 「磁盘空间分配」默认「自动」：在 Windows 上用 prealloc，避免 NTFS
  随机写入补零时把 aria2（含 RPC）卡住、界面显示「响应超时」。
  可在设置里改为 none / trunc / 强制 prealloc。


五、界面卡顿防护（请保留）
--------------------------
大种子时：任务列表后台缓存、暂停进队列立刻返回、文件列表服务端分页、
可关闭的选文件对话框、「跳过选择全部下载」、aria2 健康看门狗。
请勿用旧版覆盖。


六、主要功能
------------
- 任务列表、暂停/继续/删除（可删文件）、批量操作、打开目录
- 失败任务「重新开始」
- 引擎选择与自动推荐、任务引擎标签
- 设置：下载目录、同时任务数、限速、连接数、做种、选文件、
  磁盘分配、默认引擎、关闭到托盘、Tracker、浏览器关联
- 剪贴板监视、magnet/.torrent 系统关联
- 无广告；本地 UI 带随机令牌；引擎 RPC 仅本机


七、文件位置
------------
- 下载文件：设置中的目录（默认 用户\Downloads）
- 配置与会话：%APPDATA%\HornetDownloader
  （config.json、aria2.session、bt-session.json、bin\aria2c.exe、
   bin\yt-dlp.exe、app.log、aria2.log）
  彻底重置：退出后删除该文件夹。


八、替换旧版
------------
1. 任务管理器结束 HornetDownloader.exe 与 aria2c.exe（若有）。
2. 用新版 HornetDownloader.exe 覆盖。
3. 重新打开即可。


九、常见问题
------------
- 白屏：默认 WebView2；没有则自动用 Edge 应用窗口，或加参数 --browser
- 磁力一直「获取元数据」：资源冷门或防火墙未放行
- yt-dlp 提示无法下载：检查网络，或手动把 yt-dlp.exe 放到
  %APPDATA%\HornetDownloader\bin\
- 杀毒误报：内嵌 aria2c 为官方开源构建，可加信任
- 取消关联：设置里点「取消关联」，或 HornetDownloader.exe --unregister-assoc


十、开源组件
------------
aria2 1.37.0          https://github.com/aria2/aria2          （GPL-2.0）
anacrolix/torrent     https://github.com/anacrolix/torrent    （MPL-2.0）
yt-dlp（旁路，可选）  https://github.com/yt-dlp/yt-dlp        （Unlicense）

界面字体（已子集化嵌入，SIL OFL 1.1）：
- Noto Sans SC — 界面
- 霞鹜文楷 LXGW WenKai — 顶部情诗行
详见 web/fonts/LICENSES.txt。

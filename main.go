// Hornet 下载器：多引擎无广告下载工具（aria2 + BitTorrent + yt-dlp）。
package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"hornetdl/internal/agentcli"
	// 必须在 anacrolix/torrent/storage 的 init() 之前生效（见包注释）。
	_ "hornetdl/internal/earlyenv"
)

const appName = "HornetDownloader"
const appTitle = "Hornet 下载器"
const defaultUIPort = 16801

var forceBrowser bool

//go:embed assets/trackers.txt
var trackersTxt string

type Config struct {
	DownloadDir   string `json:"downloadDir"`
	MaxConcurrent int    `json:"maxConcurrent"`
	MaxDownloadKB int    `json:"maxDownloadKB"` // 0 = 不限速
	MaxUploadKB   int    `json:"maxUploadKB"`   // 0 = 不限速
	SplitPerTask  int    `json:"splitPerTask"`  // HTTP 单任务连接数
	SeedMinutes   int    `json:"seedMinutes"`   // 0 = 下完即停止做种
	SelectBTFiles bool   `json:"selectBTFiles"` // 种子/磁力下载前先选择文件
	CloseToTray   bool   `json:"closeToTray"`   // 关闭窗口时最小化到托盘
	ExtraTrackers string `json:"extraTrackers"`
	// DefaultEngine: auto | aria2 | bt | ytdlp。新建任务选「自动」时若此项非 auto 则强制该引擎。
	DefaultEngine string `json:"defaultEngine"`
	// FileAllocation: auto | prealloc | none | trunc。auto 在 Windows 上用 prealloc：
	// NTFS 上 none/trunc 会在随机写入时同步补零，aria2 整个进程（含 RPC）卡住几十秒。
	FileAllocation string `json:"fileAllocation"`
	// BTPort: BitTorrent 监听端口，首次运行从 6881–6999 选一个后固定下来，
	// 这样防火墙 / 路由器端口映射规则不会因每次换端口而失效。0 = 尚未选定。
	BTPort int `json:"btPort,omitempty"`
	// UserAgent：HTTP/FTP 下载使用的 User-Agent，空 = 默认 HornetDownloader/<版本>。
	UserAgent string `json:"userAgent"`
	// UserAgentFallback：服务器返回 403/418 时自动重试一次所用的 User-Agent，空 = 不重试。
	UserAgentFallback string `json:"userAgentFallback"`
	// ClipboardWatch：监视剪贴板里新出现的下载链接并询问是否添加。
	ClipboardWatch bool `json:"clipboardWatch"`
}

// defaultUserAgent 如实标明客户端（不伪装浏览器）。
func defaultUserAgent() string { return appName + "/" + AppVersion }

// defaultUserAgentFallback：403/418 时的备用 UA。各大国内镜像对 Wget 均放行（v1.5.3 实测）。
const defaultUserAgentFallback = "Wget/1.21.4"

func (c Config) effectiveUserAgent() string {
	if ua := strings.TrimSpace(c.UserAgent); ua != "" {
		return ua
	}
	return defaultUserAgent()
}

func (c Config) effectiveFileAllocation() string {
	switch c.FileAllocation {
	case "prealloc", "none", "trunc":
		return c.FileAllocation
	}
	if runtimeIsWindows {
		return "prealloc"
	}
	return "none"
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DownloadDir:    filepath.Join(home, "Downloads"),
		MaxConcurrent:  3,
		SplitPerTask:   16,
		SeedMinutes:    0,
		SelectBTFiles:  true,
		CloseToTray:    true,
		FileAllocation: "auto",
		DefaultEngine:  EngineAuto,
		ClipboardWatch: true,

		UserAgentFallback: defaultUserAgentFallback,
	}
}

type App struct {
	dataDir  string
	cfgPath  string
	mu       sync.Mutex
	cfg      Config
	aria     *Aria2
	bt       *BTEngine
	yt       *YTDLPEngine
	art      *artifacts
	res      *resilience
	uiToken  string
	apiToken string // 持久本地 API 令牌（%APPDATA%\HornetDownloader\api-token）
	uiURL    string
	lastPing time.Time
	pingMu   sync.Mutex
	quit     chan struct{}
	quitOnce sync.Once
	// forceClose 为 true 时关闭窗口真正退出（托盘「退出」或设置关闭托盘后点关闭）
	forceClose atomic.Bool

	// 外部传入的待添加任务（命令行 / 协议关联 / 剪贴板确认后由前端取走）
	inboxMu sync.Mutex
	inbox   []inboxItem

	srcMu     sync.Mutex
	agentGIDs map[string]int64 // 由 /api/v1/add 添加的任务（task-source.json）
	metaOnce  sync.Once
	meta      *taskMetaStore
	guardKick chan struct{}
	clipSeen  clipSeenSet

	// 测试钩子：非 nil 时替代真实引擎添加
	addURLHook     func(eng, uri, dir string, selectBT bool) (string, error)
	addTorrentHook func(eng, b64, name, dir string, selectBT bool) (string, error)
}

type inboxItem struct {
	Kind    string `json:"kind"` // url | torrentPath
	Value   string `json:"value"`
	Source  string `json:"source"` // cli | protocol | clipboard
	AddedAt int64  `json:"addedAt"`
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (a *App) loadConfig() {
	a.cfg = defaultConfig()
	if b, err := os.ReadFile(a.cfgPath); err == nil {
		_ = json.Unmarshal(b, &a.cfg)
		// 旧配置没有 closeToTray 字段时保持默认开启
		var raw map[string]json.RawMessage
		if json.Unmarshal(b, &raw) == nil {
			if _, ok := raw["closeToTray"]; !ok {
				a.cfg.CloseToTray = true
			}
			if _, ok := raw["clipboardWatch"]; !ok {
				a.cfg.ClipboardWatch = true
			}
			if _, ok := raw["userAgentFallback"]; !ok {
				a.cfg.UserAgentFallback = defaultUserAgentFallback
			}
		}
	}
	a.normalize(&a.cfg)
}

func (a *App) normalize(c *Config) {
	d := defaultConfig()
	if strings.TrimSpace(c.DownloadDir) == "" {
		c.DownloadDir = d.DownloadDir
	}
	if c.MaxConcurrent < 1 {
		c.MaxConcurrent = 1
	}
	if c.MaxConcurrent > 50 {
		c.MaxConcurrent = 50
	}
	if c.SplitPerTask < 1 || c.SplitPerTask > 16 {
		c.SplitPerTask = 16
	}
	if c.MaxDownloadKB < 0 {
		c.MaxDownloadKB = 0
	}
	if c.MaxUploadKB < 0 {
		c.MaxUploadKB = 0
	}
	if c.SeedMinutes < 0 {
		c.SeedMinutes = 0
	}
	if c.BTPort < 1024 || c.BTPort > 65535 {
		c.BTPort = 0
	}
	c.UserAgent = sanitizeUA(c.UserAgent)
	c.UserAgentFallback = sanitizeUA(c.UserAgentFallback)
	switch c.FileAllocation {
	case "auto", "prealloc", "none", "trunc":
	default:
		c.FileAllocation = "auto"
	}
	switch strings.ToLower(strings.TrimSpace(c.DefaultEngine)) {
	case EngineAuto, EngineAria2, EngineBT, EngineYTDLP:
		c.DefaultEngine = strings.ToLower(strings.TrimSpace(c.DefaultEngine))
	default:
		c.DefaultEngine = EngineAuto
	}
}

// sanitizeUA：去掉换行/控制字符（会被拼进 aria2 命令行与 HTTP 头），限制长度。
func sanitizeUA(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (a *App) saveConfig() error {
	b, _ := json.MarshalIndent(a.cfg, "", "  ")
	tmp := a.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.cfgPath)
}

func (a *App) trackers() string {
	var out []string
	seen := map[string]bool{}
	for _, src := range []string{trackersTxt, a.cfg.ExtraTrackers} {
		for _, l := range strings.FieldsFunc(src, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ' ' }) {
			l = strings.TrimSpace(l)
			if l != "" && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return strings.Join(out, ",")
}

func (a *App) Quit() { a.quitOnce.Do(func() { close(a.quit) }) }

func main() {
	// 命令行子命令（get/add/wait/status/list）：作为客户端连接（或无窗口启动）本程序后退出。
	if len(os.Args) > 1 && agentcli.IsCommand(os.Args[1]) {
		attachParentConsole()
		self, _ := os.Executable()
		os.Exit(agentcli.Main(os.Args[1:], agentcli.Options{AppExe: self}))
	}
	noWindow := flag.Bool("no-window", false, "不打开窗口，只启动后台服务（调试用）")
	uiPort := flag.Int("port", defaultUIPort, "本地界面端口")
	dataDirFlag := flag.String("data-dir", "", "数据目录（默认为用户配置目录）")
	aria2Path := flag.String("aria2", "", "aria2c 可执行文件路径（默认与程序同目录）")
	flag.BoolVar(&forceBrowser, "browser", false, "Windows：不用 WebView2，改用 Edge 应用窗口")
	regAssoc := flag.Bool("register-assoc", false, "注册 magnet / .torrent 系统关联（Windows）")
	unregAssoc := flag.Bool("unregister-assoc", false, "取消 magnet / .torrent 系统关联（Windows）")
	fwAction := flag.String("firewall-rules", "", "add | remove：写入/删除 Windows 防火墙规则（需管理员，由设置页通过 UAC 调用）")
	fwPublic := flag.Bool("firewall-public", false, "防火墙规则同时允许公用网络")
	fwAria2 := flag.String("firewall-aria2", "", "同时放行的 aria2c.exe 路径")
	fwDry := flag.Bool("firewall-dry-run", false, "只打印「添加防火墙规则」会执行的 netsh 命令（不提权、不修改）")
	pprofFlag := flag.String("pprof", "", "诊断：在本机回环地址开启 pprof 与 /debug/mem，例如 127.0.0.1:6061（也可用环境变量 HORNET_PPROF）")
	flag.Parse()
	if *fwDry {
		exe, _ := os.Executable()
		aria := *fwAria2
		if aria == "" {
			if d, err := os.UserConfigDir(); err == nil {
				aria = filepath.Join(d, appName, "bin", "aria2c.exe")
			}
		}
		fmt.Printf("elevate (UAC, runas): \"%s\" --firewall-rules=add --firewall-aria2=\"%s\"\n", exe, aria)
		cmds, err := firewallNetshCommands("add", *fwPublic, exe, aria)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		for _, c := range cmds {
			fmt.Println("netsh " + c)
		}
		return
	}
	if *fwAction != "" {
		if err := applyFirewallRules(*fwAction, *fwPublic, *fwAria2); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if *regAssoc {
		if err := registerAssociations(); err != nil {
			fatal("注册关联失败: " + err.Error())
		}
		fmt.Println("已注册 magnet: 协议和 .torrent 文件关联")
		return
	}
	if *unregAssoc {
		if err := unregisterAssociations(); err != nil {
			fatal("取消关联失败: " + err.Error())
		}
		fmt.Println("已取消关联")
		return
	}

	dataDir := *dataDirFlag
	if dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dataDir = filepath.Join(base, appName)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		fatal("无法创建数据目录: " + err.Error())
	}
	logf, err := openAppLog(dataDir)
	if err == nil {
		log.SetOutput(multiWriter(logf, 64<<20))
	}
	log.Printf("%s %s 启动 pid=%d %s", appName, AppVersion, os.Getpid(), runtimeInfo())
	setupCrashOutput(dataDir)
	defer closeCrashOutput()
	applyMemoryLimit()

	app := &App{dataDir: dataDir, cfgPath: filepath.Join(dataDir, "config.json"), quit: make(chan struct{}), guardKick: make(chan struct{}, 1)}
	app.loadConfig()
	app.art = newArtifacts(dataDir)
	app.res = newResilience()
	externalAdds := collectExternalAdds(flag.Args())

	// 单实例：界面端口已被本程序占用时，把外部链接转交给已有实例并前置窗口。
	tokenPath := filepath.Join(dataDir, "ui.token")
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *uiPort))
	if err != nil {
		if tok, e := os.ReadFile(tokenPath); e == nil && pingExisting(*uiPort, string(tok)) {
			tokStr := strings.TrimSpace(string(tok))
			url := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", *uiPort, tokStr)
			log.Println("已有实例在运行，转交任务并打开已有界面")
			forwardInbox(*uiPort, tokStr, externalAdds)
			if !*noWindow {
				openExternalWindow(url, dataDir)
			}
			return
		}
		// 端口被别的程序占用，改用随机端口
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("无法监听本地端口: " + err.Error())
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	app.uiToken = randHex(16)
	_ = os.WriteFile(tokenPath, []byte(app.uiToken), 0600)
	app.apiToken = loadOrCreateAPIToken(dataDir)
	writeAPIPort(dataDir, port)
	app.uiURL = fmt.Sprintf("http://127.0.0.1:%d/?t=%s", port, app.uiToken)

	exe, err := findAria2(dataDir, *aria2Path)
	if err != nil {
		fatal("准备下载引擎 aria2c 失败: " + err.Error())
	}
	if err := os.MkdirAll(app.cfg.DownloadDir, 0755); err != nil {
		log.Println("创建下载目录失败:", err)
	}
	app.aria, err = StartAria2(exe, dataDir, app.ariaArgs())
	if err != nil {
		fatal("启动下载引擎 aria2c 失败: " + err.Error())
	}
	log.Printf("aria2c 已启动, RPC 端口 %d", app.aria.port)
	app.bt, err = StartBTEngine(dataDir, func() Config {
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.cfg
	}, app.trackerSlice)
	if err != nil {
		log.Println("BitTorrent 引擎启动失败（磁力/种子将回退 aria2）:", err)
		app.bt = nil
	} else {
		app.mu.Lock()
		if app.bt.listen > 0 && app.cfg.BTPort != app.bt.listen {
			log.Printf("BitTorrent 监听端口固定为 %d（原配置 %d）", app.bt.listen, app.cfg.BTPort)
			app.cfg.BTPort = app.bt.listen
			if err := app.saveConfig(); err != nil {
				log.Println("保存 BT 端口失败:", err)
			}
		}
		app.mu.Unlock()
	}
	app.loadTaskMeta()
	app.yt = StartYTDLPEngine(dataDir, func() Config {
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.cfg
	})
	app.pushInbox(externalAdds)

	srv := &http.Server{Handler: app.routes(port), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Println("界面服务出错:", err)
			app.Quit()
		}
	}()
	goLoop("janitor", app.quit, app.janitor)
	goLoop("taskPoller", app.quit, app.taskPoller)
	goLoop("actionWorker", app.quit, app.actionWorker)
	goLoop("uiWatchdog", app.quit, app.uiWatchdog)
	goLoop("clipboard", app.quit, app.watchClipboard)
	goLoop("memHeartbeat", app.quit, app.memHeartbeat)
	app.startPprof(pprofAddr(*pprofFlag))
	// 首次运行自动尝试注册关联（失败不影响使用）
	goSafe("assoc", func() { tryRegisterAssociationsOnce(dataDir) })
	log.Println("界面地址:", app.uiURL)
	if *noWindow {
		fmt.Println("UI:", app.uiURL)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; app.Quit() }()
	go func() { // aria2c 意外退出时，程序也退出
		<-app.aria.done
		select {
		case <-app.quit:
			return // 正常退出流程中由 shutdown 结束 aria2c
		default:
		}
		log.Printf("aria2c 意外退出（%v），程序随之退出。aria2.log 末尾可能有原因", app.aria.exitErr)
		writeCrashNote(fmt.Sprintf("aria2c exited unexpectedly: %v", app.aria.exitErr))
		app.Quit()
	}()

	if *noWindow {
		<-app.quit
	} else {
		runWindow(app) // 阻塞直到窗口关闭或 app.quit
	}
	app.shutdown(srv)
}

func (a *App) ariaArgs() []string {
	c := a.cfg
	session := filepath.Join(a.dataDir, "aria2.session")
	if _, err := os.Stat(session); err != nil {
		_ = os.WriteFile(session, nil, 0600)
	}
	// 默认面向国内宽带（电信/联通/移动）调优，参考 Motrix「均衡」档、aria2 官方手册、
	// chenjia404/CnTrackersList（国内可达 Tracker）与 XIU2/TrackersListCollection。
	args := []string{
		"--enable-rpc=true",
		"--rpc-listen-all=false",
		"--rpc-allow-origin-all=false",
		"--rpc-max-request-size=64M",
		"--dir=" + c.DownloadDir,
		"--input-file=" + session,
		"--save-session=" + session,
		"--save-session-interval=15",
		"--force-save=true",
		"--continue=true", // HTTP/FTP 断点续传；配合 session 崩溃/重启后续下
		"--auto-file-renaming=true",
		"--allow-overwrite=false",
		fmt.Sprintf("--max-concurrent-downloads=%d", c.MaxConcurrent),
		fmt.Sprintf("--split=%d", c.SplitPerTask),
		fmt.Sprintf("--max-connection-per-server=%d", c.SplitPerTask),
		"--min-split-size=4M", // 国内家宽「均衡」：过大难分段，过小开销高
		// Windows/NTFS 上 none 会在随机写入时同步补零，整个 aria2（含 RPC）卡住 → 界面「死掉」。
		// prealloc 由 aria2 分块写零，期间 RPC 照常响应；Linux/macOS 稀疏文件用 none 即可。
		"--file-allocation=" + c.effectiveFileAllocation(),
		"--disk-cache=64M",
		"--max-tries=8",
		"--retry-wait=2",
		"--connect-timeout=20",
		"--timeout=30",
		"--lowest-speed-limit=1K", // 丢弃假活连接，促使换源/重试
		fmt.Sprintf("--max-overall-download-limit=%dK", c.MaxDownloadKB),
		fmt.Sprintf("--max-overall-upload-limit=%dK", c.MaxUploadKB),
		// v1.5.3：如实标明客户端。旧版伪装成 Chrome/130，被清华 TUNA、北外 BFSU 等镜像判为
		// 过期浏览器/爬虫直接 403。遇到 403/418 时会用 userAgentFallback 自动重试一次。
		"--user-agent=" + c.effectiveUserAgent(),
		"--enable-http-pipelining=true",
		"--always-resume=true",
		"--max-resume-failure-tries=0",
		// v1.5.2：BT 由内置 BitTorrent 引擎（anacrolix）负责，aria2 主要做 HTTP/FTP。
		// 关闭 DHT/DHT6/LPD/PEX，aria2 平时只监听 127.0.0.1 上的 RPC，不会触发
		// Windows 防火墙弹窗。aria2 仅在「自动回退」或手动选 aria2 下种子时才做 BT，
		// 此时只在 --listen-port 上监听（靠 Tracker 找节点）。
		"--enable-dht=false",
		"--enable-dht6=false",
		"--bt-enable-lpd=false",
		"--enable-peer-exchange=false",
		"--listen-port=6881-6999", // 仅 aria2 BT 任务运行时才会打开
		"--bt-max-peers=128",
		"--bt-request-peer-speed-limit=40K",
		"--bt-tracker-connect-timeout=8",
		"--bt-tracker-timeout=8",
		"--bt-save-metadata=true",
		"--bt-load-saved-metadata=true",
		"--bt-remove-unselected-file=true",
		"--bt-hash-check-seed=true",
		"--follow-torrent=mem",
		fmt.Sprintf("--seed-time=%d", c.SeedMinutes),
		fmt.Sprintf("--pause-metadata=%t", c.SelectBTFiles),
		"--bt-tracker=" + a.trackers(),
		"--peer-id-prefix=-AR1370-",
		"--quiet=true",
		"--summary-interval=0",
		"--log-level=notice",
		"--log=" + filepath.Join(a.dataDir, "aria2.log"),
	}
	return args
}

func (a *App) shutdown(srv *http.Server) {
	log.Println("正在退出…")
	_ = os.Remove(filepath.Join(a.dataDir, "ui.token"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	if a.bt != nil {
		a.bt.Close()
	}
	if a.yt != nil {
		a.yt.Close()
	}
	a.aria.Shutdown(8 * time.Second)
	a.saveTaskMeta()
	log.Println("已退出")
}

func findAria2(dataDir, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	// 优先：与 exe 同目录的旁路 aria2c（便于开发调试）
	name := "aria2c"
	if runtimeIsWindows {
		name = "aria2c.exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if runtimeIsWindows {
		return ensureAria2(dataDir)
	}
	// 非 Windows：PATH
	return name, nil
}

func pingExisting(port int, token string) bool {
	cl := http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/ping", port), nil)
	req.Header.Set("X-Token", strings.TrimSpace(token))
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && resp.Header.Get("X-App") == appName
}

func collectExternalAdds(args []string) []inboxItem {
	var out []inboxItem
	now := time.Now().Unix()
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		low := strings.ToLower(a)
		switch {
		case strings.HasPrefix(low, "magnet:?"), strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "ftp://"), strings.HasPrefix(low, "sftp://"):
			out = append(out, inboxItem{Kind: "url", Value: a, Source: "protocol", AddedAt: now})
		case strings.HasSuffix(low, ".torrent"):
			if abs, err := filepath.Abs(a); err == nil {
				a = abs
			}
			out = append(out, inboxItem{Kind: "torrentPath", Value: a, Source: "protocol", AddedAt: now})
		}
	}
	return out
}

func (a *App) pushInbox(items []inboxItem) {
	if len(items) == 0 {
		return
	}
	a.inboxMu.Lock()
	a.inbox = append(a.inbox, items...)
	a.inboxMu.Unlock()
}

func (a *App) takeInbox() []inboxItem {
	a.inboxMu.Lock()
	defer a.inboxMu.Unlock()
	out := a.inbox
	a.inbox = nil
	return out
}

func forwardInbox(port int, token string, items []inboxItem) {
	if len(items) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]any{"items": items})
	cl := http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/inbox", port), strings.NewReader(string(body)))
	req.Header.Set("X-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// BTEngine：基于 anacrolix/torrent（纯 Go）的第二引擎，角色等同于社区常用的 libtorrent。
// 选择它是为了保持单一便携 exe、无需 CGO/DLL；界面标签为「BitTorrent」。
type BTEngine struct {
	cl       *torrent.Client
	dataDir  string
	sessPath string
	cfgFn    func() Config
	trackers func() []string
	listen   int

	mu    sync.Mutex
	tasks map[string]*btTask
	byIH  map[string]string
}

type btTask struct {
	gid       string
	t         *torrent.Torrent
	dir       string
	uri       string
	infoHash  string
	status    string
	errMsg    string
	wantPick  bool
	selected  map[int]bool
	addedAt   time.Time
	lastBytes int64
	lastAt    time.Time
	speed     int64
	upSpeed   int64
	lastUp    int64
	metaOK    bool
	lastHint  time.Time
	// v1.5.3：下载速度按网络收到的有效数据计算（BytesReadUsefulData），
	// 不再用「已完成字节」的差值——重启后校验已有数据会让后者瞬间暴涨，显示假速度。
	lastRead int64
	checking bool // 正在校验已有数据（重启后 .part 文件需要重新哈希）
	// 完成与做种
	completedAt time.Time
	seeding     bool // 下完后仍在做种（SeedMinutes > 0）
	seedUntil   time.Time
	uploaded    int64
}

// btGIDFor：BT 任务 ID 由 infohash 派生，重启后不变（以前每次启动随机生成，agent 记下的 ID 会失效）。
func btGIDFor(ih string) string {
	ih = strings.ToLower(ih)
	if len(ih) > 16 {
		ih = ih[:16]
	}
	return "bt-" + ih
}

// btPortFree 探测端口的 TCP 与 UDP（DHT/uTP 同端口）是否都能绑定。
func btPortFree(p int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	_ = ln.Close()
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	_ = pc.Close()
	return true
}

// pickBTListenPort 优先沿用上次固定下来的端口（防火墙/端口映射规则保持有效），
// 被占用时再从常见 BT 端口段 6881–6999 中选一个。
func pickBTListenPort(preferred int) int {
	if preferred > 0 && btPortFree(preferred) {
		return preferred
	}
	for p := 6881; p <= 6999; p++ {
		if btPortFree(p) {
			return p
		}
	}
	return 0 // 全占用时交给系统分配
}

// btLoopbackOnly is set only by tests (see testmain_test.go): bind the engine to
// 127.0.0.1 with DHT, IPv6 and UPnP/NAT-PMP off, so `go test` never opens
// wildcard listeners (which trigger Windows Firewall prompts). Production keeps
// the default false and is unaffected.
var btLoopbackOnly = false

func StartBTEngine(appData string, cfgFn func() Config, trackers func() []string) (*BTEngine, error) {
	dir := filepath.Join(appData, "bt")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	processPendingDeletes(dir)
	port := 0
	if !btLoopbackOnly {
		preferred := 0
		if cfgFn != nil {
			preferred = cfgFn().BTPort
		}
		port = pickBTListenPort(preferred)
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.ListenPort = port
	// anacrolix 日志只保留 Warn 以上，写进 app.log；过滤掉经典文件 IO 下哈希「正确」时
	// 也会附带的 short write 噪声（上游 limitWriter 行为，数据无误）
	cfg.Slogger = slog.New(btLogFilter{slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn})})
	// 默认开启 UPnP/NAT-PMP，改善家宽 NAT 入站（国内运营商尤其依赖）
	cfg.NoDefaultPortForwarding = false
	cfg.Seed = true // 下载中仍 reciprocal 上传；下完是否久做种由任务侧控制
	cfg.Debug = false
	cfg.DisablePEX = false
	cfg.NoDHT = false
	cfg.PeriodicallyAnnounceTorrentsToDht = true
	cfg.EstablishedConnsPerTorrent = 80
	cfg.HalfOpenConnsPerTorrent = 40
	cfg.TotalHalfOpenConns = 160
	cfg.TorrentPeersHighWater = 800
	cfg.TorrentPeersLowWater = 80
	cfg.NominalDialTimeout = 25 * time.Second
	cfg.MinDialTimeout = 4 * time.Second
	cfg.HandshakesTimeout = 8 * time.Second
	// IPv6 在双栈家宽有用；纯 IPv4 环境无害
	cfg.DisableIPv6 = false
	if btLoopbackOnly {
		cfg.ListenHost = func(string) string { return "127.0.0.1" }
		cfg.NoDHT = true
		cfg.PeriodicallyAnnounceTorrentsToDht = false
		cfg.NoDefaultPortForwarding = true
		cfg.DisableIPv6 = true
	}

	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("启动 BitTorrent 引擎失败: %w", err)
	}
	actual := cl.LocalPort()
	e := &BTEngine{
		cl:       cl,
		dataDir:  dir,
		sessPath: filepath.Join(appData, "bt-session.json"),
		cfgFn:    cfgFn,
		trackers: trackers,
		listen:   actual,
		tasks:    map[string]*btTask{},
		byIH:     map[string]string{},
	}
	go e.ticker()
	e.restoreSession()
	e.mu.Lock()
	n := len(e.tasks)
	e.mu.Unlock()
	nTr := 0
	if trackers != nil {
		nTr = len(trackers())
	}
	log.Printf("BitTorrent 引擎已启动（anacrolix/torrent），监听 %d，Tracker %d 条，已恢复 %d 个任务", actual, nTr, n)
	return e, nil
}

func (e *BTEngine) Close() {
	e.mu.Lock()
	e.saveSessionLocked()
	e.mu.Unlock()
	if e.cl != nil {
		e.cl.Close()
	}
}

func (e *BTEngine) ID() string    { return EngineBT }
func (e *BTEngine) Label() string { return engineLabel(EngineBT) }
func (e *BTEngine) Owns(gid string) bool {
	return strings.HasPrefix(gid, "bt-")
}
func (e *BTEngine) Has(gid string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.tasks[gid]
	return ok
}

type btDuplicateError struct{ GID string }

func (e *btDuplicateError) Error() string {
	return fmt.Sprintf("相同磁力/种子已在任务列表中（%s）", e.GID)
}

// AddTorrentURL：下载 http(s) 上的 .torrent 文件后交给 BitTorrent 引擎。
func (e *BTEngine) AddTorrentURL(u, dir string, selectFiles bool) (string, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("下载种子文件失败: %v", err)
	}
	if e.cfgFn != nil {
		req.Header.Set("User-Agent", e.cfgFn().effectiveUserAgent())
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载种子文件失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("下载种子文件失败: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", fmt.Errorf("下载种子文件失败: %v", err)
	}
	name := path.Base(resp.Request.URL.Path)
	if !strings.HasSuffix(strings.ToLower(name), ".torrent") {
		name = ""
	}
	return e.addTorrentBytes(raw, name, dir, selectFiles, nil, nil)
}

func (e *BTEngine) checkDup(ih string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.byIH[strings.ToLower(ih)]; ok {
		return &btDuplicateError{GID: old}
	}
	return nil
}

// pickGIDLocked：优先沿用会话里保存的 ID，否则由 infohash 派生；极少数冲突时加后缀。
func (e *BTEngine) pickGIDLocked(ih, prefer string) string {
	if prefer != "" && strings.HasPrefix(prefer, "bt-") {
		if _, taken := e.tasks[prefer]; !taken {
			return prefer
		}
	}
	gid := btGIDFor(ih)
	for i := 2; ; i++ {
		if _, taken := e.tasks[gid]; !taken {
			return gid
		}
		gid = fmt.Sprintf("%s-%d", btGIDFor(ih), i)
	}
}

// metaPath：元数据缓存（magnet 拿到元数据后保存，重启时不必再从 DHT/Tracker 重新获取）。
func (e *BTEngine) metaPath(ih string) string {
	return filepath.Join(e.dataDir, "meta", strings.ToLower(ih)+".torrent")
}

func (e *BTEngine) saveMetainfo(t *torrent.Torrent) {
	ih := strings.ToLower(t.InfoHash().HexString())
	p := e.metaPath(ih)
	if _, err := os.Stat(p); err == nil {
		return
	}
	mi := t.Metainfo()
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		log.Printf("[bt] 保存元数据失败 %s: %v", ih, err)
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0600); err == nil {
		_ = os.Rename(tmp, p)
	}
}

func (e *BTEngine) trackerList() [][]string {
	var list []string
	if e.trackers != nil {
		list = e.trackers()
	}
	if len(list) == 0 {
		return nil
	}
	// 分两档：前 40 条优先（国内可达优先写在 trackers.txt 前面），其余次要
	if len(list) <= 40 {
		return [][]string{list}
	}
	return [][]string{list[:40], list[40:]}
}

func (e *BTEngine) applyPublicTrackers(t *torrent.Torrent) {
	if t == nil {
		return
	}
	if tr := e.trackerList(); tr != nil {
		t.AddTrackers(tr)
	}
}

func (e *BTEngine) storageFor(dir string) storage.ClientImpl {
	_ = os.MkdirAll(dir, 0755)
	// 显式启用 part files + 内存 piece completion：
	// 默认把 .torrent.bolt.db 写到下载目录时，国内盘符/迅雷目录权限或长路径一旦 Stat/Open 失败，
	// anacrolix 会对整棵种子 DisallowDataDownload，表现为「有 peer 却永远 0 速度」。
	return storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		UsePartFiles:    g.Some(true),
		PieceCompletion: storage.NewMapPieceCompletion(),
	})
}

// btRestore：从会话恢复时沿用的信息（新添加的任务为 nil）。
type btRestore struct {
	GID         string
	AddedAt     time.Time
	CompletedAt time.Time
}

func (e *BTEngine) AddMagnet(uri, dir string, selectFiles bool) (string, error) {
	return e.addMagnet(uri, dir, selectFiles, nil, nil)
}

func (e *BTEngine) addMagnet(uri, dir string, selectFiles bool, selected map[int]bool, rs *btRestore) (string, error) {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(strings.ToLower(uri), "magnet:") {
		return "", fmt.Errorf("不是磁力链接")
	}
	if dir == "" {
		dir = e.cfgFn().DownloadDir
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return "", err
	}
	if err := e.checkDup(spec.InfoHash.HexString()); err != nil {
		return "", err
	}
	// 已缓存元数据：直接用，重启后无需等待 DHT/Tracker
	if raw, rerr := os.ReadFile(e.metaPath(spec.InfoHash.HexString())); rerr == nil {
		if meta, merr := metainfo.Load(bytes.NewReader(raw)); merr == nil {
			full := torrent.TorrentSpecFromMetaInfo(meta)
			if strings.EqualFold(full.InfoHash.HexString(), spec.InfoHash.HexString()) {
				full.Trackers = append(full.Trackers, spec.Trackers...)
				full.DisplayName = spec.DisplayName
				spec = full
			}
		}
	}
	spec.Storage = e.storageFor(dir)
	if tr := e.trackerList(); tr != nil {
		spec.Trackers = append(spec.Trackers, tr...)
	}
	t, _, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		return "", err
	}
	return e.register(t, dir, uri, selectFiles, selected, rs)
}

func (e *BTEngine) AddTorrentB64(b64, name, dir string, selectFiles bool) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("种子解码失败: %v", err)
	}
	return e.addTorrentBytes(raw, name, dir, selectFiles, nil, nil)
}

func (e *BTEngine) addTorrentBytes(raw []byte, name, dir string, selectFiles bool, selected map[int]bool, rs *btRestore) (string, error) {
	meta, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("解析种子失败: %v", err)
	}
	if dir == "" {
		dir = e.cfgFn().DownloadDir
	}
	spec := torrent.TorrentSpecFromMetaInfo(meta)
	if err := e.checkDup(spec.InfoHash.HexString()); err != nil {
		return "", err
	}
	spec.Storage = e.storageFor(dir)
	if tr := e.trackerList(); tr != nil {
		spec.Trackers = append(spec.Trackers, tr...)
	}
	t, _, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = t.InfoHash().HexString() + ".torrent"
	}
	_ = os.MkdirAll(dir, 0755)
	keep := filepath.Join(dir, name)
	if rs == nil || !fileExists(keep) {
		_ = os.WriteFile(keep, raw, 0600)
	}
	e.saveMetainfo(t)
	return e.register(t, dir, "file://"+keep, selectFiles, selected, rs)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (e *BTEngine) register(t *torrent.Torrent, dir, uri string, selectFiles bool, selected map[int]bool, rs *btRestore) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ih := strings.ToLower(t.InfoHash().HexString())
	if old, ok := e.byIH[ih]; ok {
		// 注意：不能 Drop——同一 infohash 在 anacrolix 里是同一个 *Torrent，Drop 会把已有任务一起删掉。
		return "", &btDuplicateError{GID: old}
	}
	prefer := ""
	added := time.Now()
	if rs != nil {
		prefer = rs.GID
		if !rs.AddedAt.IsZero() {
			added = rs.AddedAt
		}
	}
	gid := e.pickGIDLocked(ih, prefer)
	task := &btTask{
		gid:      gid,
		t:        t,
		dir:      dir,
		uri:      uri,
		infoHash: ih,
		status:   "waiting",
		wantPick: selectFiles,
		selected: selected,
		addedAt:  added,
		lastAt:   time.Now(),
	}
	if rs != nil {
		task.completedAt = rs.CompletedAt
	}
	// 写盘失败时默认会 DisallowDataDownload，导致「有元数据但速度永远为 0」。
	// 改为记录错误并保持可下，方便国内盘符/长路径场景自愈。
	t.SetOnWriteChunkError(func(err error) {
		log.Printf("[bt] 写盘失败 %s: %v", gid, err)
		e.mu.Lock()
		if task2, ok := e.tasks[gid]; ok {
			task2.errMsg = "写盘失败: " + err.Error()
			// 写失败时 anacrolix 默认会停下；我们保持可下并重申优先级
			e.kickDownloadLocked(task2)
		}
		e.mu.Unlock()
	})
	e.tasks[gid] = task
	e.byIH[ih] = gid
	e.saveSessionLocked()
	go e.watchMeta(task)
	return gid, nil
}

func (e *BTEngine) watchMeta(task *btTask) {
	t := task.t
	e.applyPublicTrackers(t)
	select {
	case <-t.GotInfo():
	case <-time.After(30 * time.Minute):
		e.mu.Lock()
		if e.tasks[task.gid] == task && task.status == "waiting" {
			task.status = "error"
			task.errMsg = "获取元数据超时（无 Tracker/DHT 节点）。可换 aria2 引擎或检查网络"
		}
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tasks[task.gid] != task {
		return
	}
	task.metaOK = true
	task.infoHash = strings.ToLower(t.InfoHash().HexString())
	task.errMsg = ""
	go e.saveMetainfo(t)
	e.applyPublicTrackers(t)
	t.AllowDataDownload()
	t.AllowDataUpload()
	if !task.completedAt.IsZero() && !task.wantPick && e.selectionCompleteLocked(task) {
		// 重启前已完成：不再重新连接节点；做种时间未到期才继续做种
		e.applySelectionLocked(task)
		task.status = "complete"
		e.afterCompleteLocked(task, time.Now())
	} else if task.wantPick {
		for _, f := range t.Files() {
			f.SetPriority(torrent.PiecePriorityNone)
		}
		task.status = "paused"
	} else {
		task.status = "active"
		e.kickDownloadLocked(task)
	}
	e.saveSessionLocked()
	log.Printf("[bt] 元数据就绪 %s name=%q files=%d wantPick=%v selected=%d",
		task.gid, t.Name(), len(t.Files()), task.wantPick, len(task.selected))
}

func (e *BTEngine) Pause(gid string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	task, ok := e.tasks[gid]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if task.t != nil && task.metaOK {
		for _, f := range task.t.Files() {
			f.SetPriority(torrent.PiecePriorityNone)
		}
		task.t.SetMaxEstablishedConns(0)
	}
	if task.status != "complete" && task.status != "error" {
		task.status = "paused"
	}
	task.speed = 0
	e.saveSessionLocked()
	return nil
}

func (e *BTEngine) Unpause(gid string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	task, ok := e.tasks[gid]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if !task.metaOK {
		task.status = "waiting"
		e.applyPublicTrackers(task.t)
		return nil
	}
	task.errMsg = ""
	e.applyPublicTrackers(task.t)
	task.wantPick = false
	task.status = "active"
	e.kickDownloadLocked(task)
	e.saveSessionLocked()
	return nil
}

func (e *BTEngine) applySelectionLocked(task *btTask) {
	files := task.t.Files()
	if len(task.selected) == 0 {
		task.t.DownloadAll()
		return
	}
	for i, f := range files {
		if task.selected[i+1] {
			f.SetPriority(torrent.PiecePriorityNormal)
		} else {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
}

// kickDownloadLocked：有 peer 却 0 速度时的自愈。
// anacrolix 在 piece completion / 写盘出错时会 DisallowDataDownload，且默认文件优先级为 None，
// 必须显式 Allow + 设优先级才会真正向 peer 要块。
func (e *BTEngine) kickDownloadLocked(task *btTask) {
	if task == nil || task.t == nil || !task.metaOK {
		return
	}
	if task.status != "active" && task.status != "waiting" {
		return
	}
	task.t.AllowDataDownload()
	task.t.AllowDataUpload()
	task.t.SetMaxEstablishedConns(80)
	if task.status == "active" && !task.wantPick {
		e.applySelectionLocked(task)
	}
}

// SetSelection：任意状态（下载中/暂停/已完成）重选文件；新增文件会从 complete→active。
func (e *BTEngine) SetSelection(gid string, selected map[int]bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	task, ok := e.tasks[gid]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if !task.metaOK {
		return fmt.Errorf("元数据尚未就绪")
	}
	if len(selected) == 0 {
		return fmt.Errorf("至少选择一个文件")
	}
	task.selected = selected
	task.wantPick = false
	task.errMsg = ""
	wasComplete := task.status == "complete" || task.status == "error"
	e.applySelectionLocked(task)
	if wasComplete || task.status == "paused" {
		// 新选中未下完的文件 → 恢复为下载中
		var need bool
		files := task.t.Files()
		for i, f := range files {
			if selected[i+1] && f.BytesCompleted() < f.Length() {
				need = true
				break
			}
		}
		if need {
			task.status = "active"
			task.completedAt = time.Time{}
			task.seeding = false
		} else if wasComplete {
			e.afterCompleteLocked(task, time.Now())
		}
	}
	// 只有真正要下载时才放开下载/连接；暂停或已完成且无需下载的任务保持安静
	e.kickDownloadLocked(task)
	e.saveSessionLocked()
	return nil
}

// PeerSnapshot：任务详情「来源/节点」页。
func (e *BTEngine) PeerSnapshot(gid string) ([]map[string]any, error) {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("任务不存在")
	}
	if task.t == nil {
		return nil, nil
	}
	conns := task.t.PeerConns()
	out := make([]map[string]any, 0, len(conns))
	numPieces := 0
	if task.metaOK {
		numPieces = task.t.NumPieces()
	}
	for _, cn := range conns {
		if cn == nil {
			continue
		}
		st := cn.Stats()
		addr := ""
		if cn.RemoteAddr != nil {
			addr = cn.RemoteAddr.String()
		}
		client := ""
		if v := cn.PeerClientName.Load(); v != nil {
			client = fmt.Sprint(v)
		}
		have := int(st.RemotePieceCount)
		prog := 0.0
		if numPieces > 0 {
			prog = float64(have) / float64(numPieces) * 100
		}
		out = append(out, map[string]any{
			"addr": addr, "client": client,
			"dl": finiteRate(st.DownloadRate), "ul": finiteRate(st.LastWriteUploadRate),
			"progress": prog, "pieces": have,
		})
	}
	return out, nil
}

// TrackerSnapshot：来自 metainfo announce list（状态仅能给出 URL）。
func (e *BTEngine) TrackerSnapshot(gid string) ([]map[string]any, error) {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("任务不存在")
	}
	if task.t == nil || !task.metaOK {
		return nil, nil
	}
	mi := task.t.Metainfo()
	seen := map[string]bool{}
	var out []map[string]any
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, map[string]any{"url": u, "status": ""})
	}
	add(mi.Announce)
	for _, tier := range mi.AnnounceList {
		for _, u := range tier {
			add(u)
		}
	}
	return out, nil
}

// FileProgress：详情「文件」页（含每文件已完成字节）。
func (e *BTEngine) FileProgress(gid string) (name string, items []map[string]any, err error) {
	name, entries, err := e.FileEntries(gid)
	if err != nil {
		return "", nil, err
	}
	e.mu.Lock()
	task := e.tasks[gid]
	e.mu.Unlock()
	items = make([]map[string]any, 0, len(entries))
	files := task.t.Files()
	for _, e := range entries {
		done := int64(0)
		if e.Index >= 1 && e.Index <= len(files) {
			done = files[e.Index-1].BytesCompleted()
		}
		items = append(items, map[string]any{
			"i": e.Index, "p": e.Rel, "s": e.Length, "sel": e.Selected, "done": done,
		})
	}
	return name, items, nil
}

// URIOf returns magnet/file URI for copy-link.
func (e *BTEngine) URIOf(gid string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if task, ok := e.tasks[gid]; ok {
		return task.uri
	}
	return ""
}

func (e *BTEngine) AddedAt(gid string) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if task, ok := e.tasks[gid]; ok {
		return task.addedAt
	}
	return time.Time{}
}

func (e *BTEngine) Remove(gid string, deleteFiles bool) error {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	var paths []string
	name := ""
	if task.t != nil {
		if task.metaOK {
			name = task.t.Name()
			for _, f := range task.t.Files() {
				paths = append(paths, filepath.Join(task.dir, filepath.FromSlash(f.Path())))
			}
		}
		task.t.Drop()
	}
	delete(e.tasks, gid)
	delete(e.byIH, task.infoHash)
	e.saveSessionLocked()
	e.mu.Unlock()
	if task.infoHash != "" {
		_ = os.Remove(e.metaPath(task.infoHash))
	}
	if deleteFiles {
		// 未完成的文件是 "<name>.part"（part files 存储）；本程序在下载目录里留的 .torrent 副本也一并删除。
		var targets []string
		for _, p := range paths {
			targets = append(targets, p, p+".part")
		}
		if keep, ok := strings.CutPrefix(task.uri, "file://"); ok && strings.HasSuffix(strings.ToLower(keep), ".torrent") &&
			filepath.Clean(filepath.Dir(keep)) == filepath.Clean(task.dir) {
			targets = append(targets, keep)
		}
		left := removeFilesRetry(targets, 3*time.Second)
		if name != "" {
			removeEmptyDirs(filepath.Join(task.dir, name), task.dir)
		}
		if len(left) > 0 {
			// anacrolix 的 mmap 存储在进程退出前不释放文件句柄（Windows 上无法删除）：记下来，下次启动时删。
			log.Printf("[bt] %d 个文件仍被占用，将在下次启动时删除: %v", len(left), left)
			pd := btPendingDelete{Files: left}
			if name != "" {
				pd.Dirs = [][2]string{{filepath.Join(task.dir, name), task.dir}}
			}
			addPendingDeletes(e.dataDir, pd)
		}
	}
	return nil
}

// btPendingDelete：删除任务时没能删掉的文件（被占用），下次启动 BT 引擎前处理。
type btPendingDelete struct {
	Files []string    `json:"files"`
	Dirs  [][2]string `json:"dirs,omitempty"` // {root, stop}：删完文件后清理空目录
	// 记录时的大小与修改时间；文件之后被改过（例如同一个种子又重新下载了）就不删。
	Stamp map[string]string `json:"stamp,omitempty"`
}

func fileStamp(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
}

func pendingDeletePath(dataDir string) string { return filepath.Join(dataDir, "pending-delete.json") }

func addPendingDeletes(dataDir string, add btPendingDelete) {
	var cur btPendingDelete
	if b, err := os.ReadFile(pendingDeletePath(dataDir)); err == nil {
		_ = json.Unmarshal(b, &cur)
	}
	if cur.Stamp == nil {
		cur.Stamp = map[string]string{}
	}
	for _, f := range add.Files {
		cur.Stamp[f] = fileStamp(f)
	}
	cur.Files = append(cur.Files, add.Files...)
	cur.Dirs = append(cur.Dirs, add.Dirs...)
	if b, err := json.Marshal(cur); err == nil {
		_ = os.WriteFile(pendingDeletePath(dataDir), b, 0600)
	}
}

// processPendingDeletes 删除上次没删掉的文件；仍失败的留到下次。
func processPendingDeletes(dataDir string) {
	b, err := os.ReadFile(pendingDeletePath(dataDir))
	if err != nil {
		return
	}
	var cur btPendingDelete
	if json.Unmarshal(b, &cur) != nil {
		_ = os.Remove(pendingDeletePath(dataDir))
		return
	}
	var todo []string
	for _, f := range cur.Files {
		if st, ok := cur.Stamp[f]; ok && st != "" && fileStamp(f) != st {
			continue // 文件已被重新写过，保留
		}
		todo = append(todo, f)
	}
	left := removeFilesRetry(todo, 0)
	for _, d := range cur.Dirs {
		removeEmptyDirs(d[0], d[1])
	}
	if len(left) == 0 {
		_ = os.Remove(pendingDeletePath(dataDir))
		return
	}
	cur.Files = left
	if b, err := json.Marshal(cur); err == nil {
		_ = os.WriteFile(pendingDeletePath(dataDir), b, 0600)
	}
}

// removeFilesRetry 删除文件；Windows 上 Drop 之后存储句柄可能还没关，被占用时短暂重试。返回仍未删掉的文件。
func removeFilesRetry(paths []string, max time.Duration) []string {
	deadline := time.Now().Add(max)
	for {
		var left []string
		for _, p := range paths {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				left = append(left, p)
			}
		}
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		paths = left
		time.Sleep(200 * time.Millisecond)
	}
}

// removeEmptyDirs 自底向上删除 root 下（含 root）的空目录，不越过 stop。
func removeEmptyDirs(root, stop string) {
	if filepath.Clean(root) == filepath.Clean(stop) {
		return
	}
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空则失败，保持原样
	}
}

func (e *BTEngine) StartSelected(gid, base string, except []int, skip bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	task, ok := e.tasks[gid]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if !task.metaOK {
		return fmt.Errorf("元数据尚未就绪")
	}
	files := task.t.Files()
	task.errMsg = ""
	task.t.AllowDataDownload()
	e.applyPublicTrackers(task.t)
	if skip {
		task.selected = nil
		task.wantPick = false
		task.t.SetMaxEstablishedConns(80)
		task.t.DownloadAll()
		task.status = "active"
		e.saveSessionLocked()
		return nil
	}
	ex := map[int]bool{}
	for _, i := range except {
		ex[i] = true
	}
	sel := map[int]bool{}
	for i := range files {
		idx := i + 1
		def := true
		switch base {
		case "all":
			def = true
		case "none":
			def = false
		case "keep":
			def = len(task.selected) == 0 || task.selected[idx]
		}
		if def != ex[idx] {
			sel[idx] = true
		}
	}
	if len(sel) == 0 {
		return fmt.Errorf("至少选择一个文件")
	}
	task.selected = sel
	task.wantPick = false
	task.status = "active"
	e.kickDownloadLocked(task)
	e.saveSessionLocked()
	log.Printf("[bt] 开始下载 %s 选中 %d/%d 文件", gid, len(sel), len(files))
	return nil
}

func (e *BTEngine) FileEntries(gid string) (name string, items []fileEntry, err error) {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	e.mu.Unlock()
	if !ok {
		return "", nil, fmt.Errorf("任务不存在")
	}
	if !task.metaOK {
		return "", nil, fmt.Errorf("元数据尚未就绪")
	}
	e.mu.Lock()
	selected := make(map[int]bool, len(task.selected))
	for k, v := range task.selected {
		selected[k] = v
	}
	e.mu.Unlock()
	name = task.t.Name()
	files := task.t.Files()
	items = make([]fileEntry, 0, len(files))
	for i, f := range files {
		// 没有显式选区 = 全部下载。不能看文件优先级：暂停/下完停止做种时优先级会被设为 None，
		// 以前因此在「已完成」任务上重选文件时所有文件都显示为未选。
		sel := true
		if len(selected) > 0 {
			sel = selected[i+1]
		}
		rel := f.DisplayPath()
		items = append(items, fileEntry{
			Index: i + 1, Rel: rel, Lower: strings.ToLower(rel),
			Length: f.Length(), Selected: sel,
		})
	}
	return name, items, nil
}

func (e *BTEngine) ListSnapshots() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]map[string]any, 0, len(e.tasks))
	for _, task := range e.tasks {
		out = append(out, e.snapshotLocked(task))
	}
	return out
}

func (e *BTEngine) snapshotLocked(task *btTask) map[string]any {
	st := task.status
	var total, done int64
	var btInfo any
	var files []map[string]any
	conn, seeds, totalPeers, pending, half := 0, 0, 0, 0, 0
	name := ""
	if task.t != nil {
		stats := task.t.Stats()
		conn = stats.ActivePeers
		seeds = stats.ConnectedSeeders
		totalPeers = stats.TotalPeers
		pending = stats.PendingPeers
		half = stats.HalfOpenPeers
		if task.metaOK {
			name = task.t.Name()
			total = task.t.Length()
			if len(task.selected) > 0 {
				var selTotal, selDone int64
				for i, f := range task.t.Files() {
					if task.selected[i+1] {
						selTotal += f.Length()
						selDone += f.BytesCompleted()
					}
				}
				total, done = selTotal, selDone
			} else {
				done = task.t.BytesCompleted()
			}
			if st == "active" && total > 0 && done >= total {
				e.onCompleteLocked(task, time.Now())
				st = task.status
			}
			btInfo = map[string]any{"info": map[string]any{"name": name}}
			files = []map[string]any{{
				"path":   filepath.Join(task.dir, name),
				"length": fmt.Sprintf("%d", total), "selected": "true",
			}}
		} else {
			btInfo = map[string]any{}
			files = []map[string]any{{"path": "[METADATA]" + task.infoHash}}
		}
	}
	spd, uspd := task.speed, task.upSpeed
	if st != "active" {
		spd = 0
	}
	if st != "active" && !(st == "complete" && task.seeding) {
		uspd = 0
	}
	errMsg := task.errMsg
	if st == "active" && task.checking && errMsg == "" {
		errMsg = "正在校验已下载的数据…"
	}
	// 活跃但无速度时，把 peer 发现状态写到 errorMessage，界面状态列/悬停可见
	if st == "active" && spd == 0 && errMsg == "" && task.metaOK {
		if conn == 0 && seeds == 0 {
			if totalPeers == 0 && pending == 0 && half == 0 {
				errMsg = fmt.Sprintf("寻找节点中（Tracker/DHT，监听 %d）", e.listen)
			} else {
				errMsg = fmt.Sprintf("连接中：候选 %d / 握手中 %d / 已连 %d", totalPeers, half, conn)
			}
		}
	}
	m := map[string]any{
		"gid": task.gid, "status": st,
		"totalLength": fmt.Sprintf("%d", total), "completedLength": fmt.Sprintf("%d", done),
		"uploadLength":  fmt.Sprintf("%d", task.uploaded),
		"downloadSpeed": fmt.Sprintf("%d", spd), "uploadSpeed": fmt.Sprintf("%d", uspd),
		"connections": fmt.Sprintf("%d", conn), "numSeeders": fmt.Sprintf("%d", seeds),
		"seeder": fmt.Sprintf("%t", task.seeding),
		"dir":    task.dir, "infoHash": task.infoHash, "bittorrent": btInfo, "files": files,
		"engine": EngineBT, "engineLabel": e.Label(),
		"errorMessage": errMsg, "errorCode": "",
		"btListenPort": e.listen,
		"btTotalPeers": totalPeers, "btPendingPeers": pending, "btHalfOpen": half,
	}
	return m
}

func (e *BTEngine) ticker() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	tick := 0
	for range tk.C {
		tick++
		e.mu.Lock()
		now := time.Now()
		for _, task := range e.tasks {
			if task.t == nil {
				continue
			}
			// 每 45 秒给活跃/等元数据任务补一轮国内 Tracker，对抗 NAT/丢包
			if tick%45 == 0 && (task.status == "active" || task.status == "waiting") {
				e.applyPublicTrackers(task.t)
				e.kickDownloadLocked(task)
			}
			if !task.metaOK {
				continue
			}
			done := task.t.BytesCompleted()
			if len(task.selected) > 0 {
				var selDone int64
				for i, f := range task.t.Files() {
					if task.selected[i+1] {
						selDone += f.BytesCompleted()
					}
				}
				done = selDone
			}
			st := task.t.Stats()
			up := (&st.BytesWrittenData).Int64()
			rd := (&st.BytesReadUsefulData).Int64()
			dt := now.Sub(task.lastAt).Seconds()
			if dt > 0.2 {
				dr := rd - task.lastRead
				if dr < 0 || task.lastAt.IsZero() {
					dr = 0
				}
				task.speed = int64(float64(dr) / dt)
				du := up - task.lastUp
				if du < 0 {
					du = 0
				}
				task.upSpeed = int64(float64(du) / dt)
			}
			task.lastBytes, task.lastUp, task.lastRead, task.lastAt = done, up, rd, now
			task.uploaded = up
			if task.status == "active" || task.status == "waiting" {
				task.checking = torrentChecking(task.t)
			} else {
				task.checking = false
			}
			total := task.t.Length()
			if len(task.selected) > 0 {
				var selTotal int64
				for i, f := range task.t.Files() {
					if task.selected[i+1] {
						selTotal += f.Length()
					}
				}
				total = selTotal
			}
			if task.status == "active" && total > 0 && done >= total {
				e.onCompleteLocked(task, now)
			}
			if task.status == "complete" && task.seeding && !task.seedUntil.IsZero() && now.After(task.seedUntil) {
				log.Printf("[bt] %s 做种时间已到，停止做种", task.gid)
				e.stopSeedingLocked(task)
				e.saveSessionLocked()
			}
			// 有连接却 0 速度：每 5 秒强制踢一次下载（对抗 piece completion / 写盘触发的 DisallowDataDownload）
			if task.status == "active" && task.speed == 0 && (st.ActivePeers > 0 || st.ConnectedSeeders > 0) {
				if tick%5 == 0 {
					e.kickDownloadLocked(task)
				}
				if now.Sub(task.lastHint) > 30*time.Second {
					task.lastHint = now
					log.Printf("[bt] %s 有 peer 无速度，已 kickDownload peers=%d active=%d seeds=%d half=%d listen=%d",
						task.gid, st.TotalPeers, st.ActivePeers, st.ConnectedSeeders, st.HalfOpenPeers, e.listen)
				}
			} else if task.status == "active" && task.speed == 0 && now.Sub(task.addedAt) > 90*time.Second {
				if now.Sub(task.lastHint) > 60*time.Second {
					task.lastHint = now
					log.Printf("[bt] %s 仍无速度 peers=%d active=%d seeds=%d half=%d listen=%d",
						task.gid, st.TotalPeers, st.ActivePeers, st.ConnectedSeeders, st.HalfOpenPeers, e.listen)
				}
			}
		}
		e.mu.Unlock()
	}
}

type btSessionFile struct {
	Tasks []btSessionItem `json:"tasks"`
}
type btSessionItem struct {
	GID      string `json:"gid"`
	URI      string `json:"uri"`
	Dir      string `json:"dir"`
	InfoHash string `json:"infoHash"`
	Paused   bool   `json:"paused"`
	WantPick bool   `json:"wantPick"`
	Selected []int  `json:"selected,omitempty"`
	// v1.5.3
	AddedAt     int64 `json:"addedAt,omitempty"`     // unix 毫秒
	CompletedAt int64 `json:"completedAt,omitempty"` // unix 毫秒；0 = 未完成
}

func (e *BTEngine) saveSessionLocked() {
	sf := btSessionFile{}
	for _, task := range e.tasks {
		it := btSessionItem{
			GID: task.gid, URI: task.uri, Dir: task.dir, InfoHash: task.infoHash,
			Paused: task.status == "paused", WantPick: task.wantPick,
			AddedAt: task.addedAt.UnixMilli(),
		}
		if !task.completedAt.IsZero() {
			it.CompletedAt = task.completedAt.UnixMilli()
		}
		for i, on := range task.selected {
			if on {
				it.Selected = append(it.Selected, i)
			}
		}
		sf.Tasks = append(sf.Tasks, it)
	}
	b, _ := json.MarshalIndent(sf, "", "  ")
	tmp := e.sessPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err == nil {
		_ = os.Rename(tmp, e.sessPath)
	}
}

func (e *BTEngine) restoreSession() {
	b, err := os.ReadFile(e.sessPath)
	if err != nil {
		return
	}
	var sf btSessionFile
	if json.Unmarshal(b, &sf) != nil {
		return
	}
	for _, it := range sf.Tasks {
		selectFiles := it.WantPick
		// 已选文件且未暂停：直接开下，不要再弹选文件
		var selected map[int]bool
		if len(it.Selected) > 0 {
			selected = map[int]bool{}
			for _, i := range it.Selected {
				selected[i] = true
			}
			if !it.WantPick {
				selectFiles = false
			}
		} else if it.Paused && !it.WantPick {
			// 用户手动暂停、无选区：恢复后保持暂停（通过 wantPick=false + Pause 路径）
			selectFiles = false
		}
		rs := &btRestore{GID: it.GID}
		if it.AddedAt > 0 {
			rs.AddedAt = time.UnixMilli(it.AddedAt)
		}
		if it.CompletedAt > 0 {
			rs.CompletedAt = time.UnixMilli(it.CompletedAt)
		}
		var gid string
		var aerr error
		switch {
		case strings.HasPrefix(strings.ToLower(it.URI), "magnet:"):
			gid, aerr = e.addMagnet(it.URI, it.Dir, selectFiles, selected, rs)
		case strings.HasPrefix(it.URI, "file://"):
			p := strings.TrimPrefix(it.URI, "file://")
			raw, rerr := os.ReadFile(p)
			if rerr != nil && it.InfoHash != "" {
				// 下载目录里的种子副本被删了：用元数据缓存恢复
				raw, rerr = os.ReadFile(e.metaPath(it.InfoHash))
			}
			if rerr != nil {
				log.Println("恢复 BT 任务失败:", rerr)
				continue
			}
			gid, aerr = e.addTorrentBytes(raw, filepath.Base(p), it.Dir, selectFiles, selected, rs)
		default:
			continue
		}
		if gid != "" && it.GID != "" && gid != it.GID {
			log.Printf("[bt] 恢复任务 ID 变化 %s → %s", it.GID, gid)
		}
		if aerr != nil {
			log.Println("恢复 BT 任务失败:", aerr)
			continue
		}
		if it.Paused && !it.WantPick && gid != "" {
			// 等元数据后再暂停；若已有 meta 则立刻暂停
			go func(g string) {
				deadline := time.Now().Add(2 * time.Minute)
				for time.Now().Before(deadline) {
					e.mu.Lock()
					task, ok := e.tasks[g]
					ready := ok && task.metaOK
					e.mu.Unlock()
					if ready {
						_ = e.Pause(g)
						return
					}
					time.Sleep(500 * time.Millisecond)
				}
			}(gid)
		}
	}
}

// selectionCompleteLocked：选中的文件是否都已下完。
func (e *BTEngine) selectionCompleteLocked(task *btTask) bool {
	if task.t == nil || !task.metaOK {
		return false
	}
	var total, done int64
	for i, f := range task.t.Files() {
		if len(task.selected) > 0 && !task.selected[i+1] {
			continue
		}
		total += f.Length()
		done += f.BytesCompleted()
	}
	return total > 0 && done >= total
}

// onCompleteLocked：任务刚下完（唯一入口；ticker 与快照都会调用，幂等）。
func (e *BTEngine) onCompleteLocked(task *btTask, now time.Time) {
	if task.status == "complete" {
		return
	}
	task.status = "complete"
	if task.completedAt.IsZero() { // 重启前已完成的任务沿用原完成时间
		task.completedAt = now
	}
	task.speed = 0
	log.Printf("[bt] %s 下载完成", task.gid)
	e.afterCompleteLocked(task, now)
	e.saveSessionLocked()
}

// afterCompleteLocked：按「下完后做种」设置决定继续做种多久（0 = 立即停止）。
// v1.5.2 的停止做种逻辑只在界面快照里执行，而后台 ticker 往往先把任务标成完成，
// 结果设置为 0 时也会一直做种（连接、上传与内存都不会下降）。
func (e *BTEngine) afterCompleteLocked(task *btTask, now time.Time) {
	minutes := 0
	if e.cfgFn != nil {
		minutes = e.cfgFn().SeedMinutes
	}
	until := task.completedAt.Add(time.Duration(minutes) * time.Minute)
	if minutes <= 0 || task.completedAt.IsZero() || !now.Before(until) {
		e.stopSeedingLocked(task)
		return
	}
	task.seeding = true
	task.seedUntil = until
	task.t.AllowDataUpload()
	task.t.SetMaxEstablishedConns(40)
	log.Printf("[bt] %s 做种到 %s", task.gid, until.Format("15:04:05"))
}

func (e *BTEngine) stopSeedingLocked(task *btTask) {
	task.seeding = false
	task.seedUntil = time.Time{}
	task.upSpeed = 0
	if task.t == nil || !task.metaOK {
		return
	}
	task.t.DisallowDataUpload()
	for _, f := range task.t.Files() {
		f.SetPriority(torrent.PiecePriorityNone)
	}
	task.t.SetMaxEstablishedConns(0)
}

// torrentChecking：是否有分块正在/等待哈希校验。
func torrentChecking(t *torrent.Torrent) bool {
	for _, r := range t.PieceStateRuns() {
		if r.Hashing || r.QueuedForHash {
			return true
		}
	}
	return false
}

// finiteRate：anacrolix 的速率在没有数据时可能是 NaN/Inf，转成 int64 会变成
// -9223372036854775808（详情「节点」页的 ↑ 列曾显示这个值）。
func finiteRate(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	if v > 1<<50 {
		return 0
	}
	return int64(v)
}

// connStats：诊断用，任务数与已建立的连接数。
func (e *BTEngine) connStats() (tasks, conns int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, task := range e.tasks {
		tasks++
		if task.t != nil {
			conns += len(task.t.PeerConns())
		}
	}
	return
}

// URIs：全部任务的磁力/种子链接（剪贴板去重用）。
func (e *BTEngine) URIs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.tasks))
	for _, task := range e.tasks {
		out = append(out, task.uri)
	}
	return out
}

type btLogFilter struct{ slog.Handler }

func (h btLogFilter) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "finished hashing piece" {
		correct := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "correct" {
				correct = a.Value.Kind() == slog.KindBool && a.Value.Bool()
				return false
			}
			return true
		})
		if correct {
			return nil
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h btLogFilter) WithAttrs(as []slog.Attr) slog.Handler {
	return btLogFilter{h.Handler.WithAttrs(as)}
}

func (h btLogFilter) WithGroup(name string) slog.Handler {
	return btLogFilter{h.Handler.WithGroup(name)}
}

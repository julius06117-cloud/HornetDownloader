package main

import (
	"bytes"
	"io"
	"net/http"
	"path"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
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
	doneOnce  bool
	lastHint  time.Time
}

func pickBTListenPort() int {
	// 与 aria2 一致：常见 BT 端口段，便于家宽路由器 UPnP / 手动映射。
	for p := 6881; p <= 6999; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		// 再探一下 UDP（DHT/uTP 同端口）；失败也不致命，TCP 能绑就用。
		pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p))
		if err == nil {
			_ = pc.Close()
			return p
		}
	}
	return 0 // 全占用时交给系统分配
}

func StartBTEngine(appData string, cfgFn func() Config, trackers func() []string) (*BTEngine, error) {
	dir := filepath.Join(appData, "bt")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	port := pickBTListenPort()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.ListenPort = port
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
	resp, err := cl.Get(u)
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
	return e.addTorrentBytes(raw, name, dir, selectFiles, nil)
}

func (e *BTEngine) checkDup(ih string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.byIH[strings.ToLower(ih)]; ok {
		return &btDuplicateError{GID: old}
	}
	return nil
}

func (e *BTEngine) newGID() string { return "bt-" + randHex(8) }

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

func (e *BTEngine) AddMagnet(uri, dir string, selectFiles bool) (string, error) {
	return e.addMagnet(uri, dir, selectFiles, nil)
}

func (e *BTEngine) addMagnet(uri, dir string, selectFiles bool, selected map[int]bool) (string, error) {
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
	spec.Storage = e.storageFor(dir)
	if tr := e.trackerList(); tr != nil {
		spec.Trackers = append(spec.Trackers, tr...)
	}
	t, _, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		return "", err
	}
	return e.register(t, dir, uri, selectFiles, selected)
}

func (e *BTEngine) AddTorrentB64(b64, name, dir string, selectFiles bool) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("种子解码失败: %v", err)
	}
	return e.addTorrentBytes(raw, name, dir, selectFiles, nil)
}

func (e *BTEngine) addTorrentBytes(raw []byte, name, dir string, selectFiles bool, selected map[int]bool) (string, error) {
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
	_ = os.WriteFile(keep, raw, 0600)
	return e.register(t, dir, "file://"+keep, selectFiles, selected)
}

func (e *BTEngine) register(t *torrent.Torrent, dir, uri string, selectFiles bool, selected map[int]bool) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ih := strings.ToLower(t.InfoHash().HexString())
	if old, ok := e.byIH[ih]; ok {
		// 注意：不能 Drop——同一 infohash 在 anacrolix 里是同一个 *Torrent，Drop 会把已有任务一起删掉。
		return "", &btDuplicateError{GID: old}
	}
	gid := e.newGID()
	task := &btTask{
		gid:      gid,
		t:        t,
		dir:      dir,
		uri:      uri,
		infoHash: ih,
		status:   "waiting",
		wantPick: selectFiles,
		selected: selected,
		addedAt:  time.Now(),
		lastAt:   time.Now(),
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
	e.applyPublicTrackers(t)
	t.AllowDataDownload()
	t.AllowDataUpload()
	if task.wantPick {
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
	task.doneOnce = false
	wasComplete := task.status == "complete" || task.status == "error"
	e.applySelectionLocked(task)
	task.t.AllowDataDownload()
	task.t.AllowDataUpload()
	task.t.SetMaxEstablishedConns(80)
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
		}
	}
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
			"dl": int64(st.DownloadRate), "ul": int64(st.LastWriteUploadRate),
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
				paths = append(paths, filepath.Join(task.dir, f.DisplayPath()))
			}
		}
		task.t.Drop()
	}
	delete(e.tasks, gid)
	delete(e.byIH, task.infoHash)
	e.saveSessionLocked()
	e.mu.Unlock()
	if deleteFiles {
		for _, p := range paths {
			_ = os.Remove(p)
		}
		if name != "" {
			_ = os.Remove(filepath.Join(task.dir, name))
		}
	}
	return nil
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
			def = files[i].Priority() != torrent.PiecePriorityNone
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
	name = task.t.Name()
	files := task.t.Files()
	items = make([]fileEntry, 0, len(files))
	for i, f := range files {
		sel := true
		if len(task.selected) > 0 {
			sel = task.selected[i+1]
		} else if !task.wantPick {
			sel = f.Priority() != torrent.PiecePriorityNone
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
				st = "complete"
				task.status = "complete"
				if !task.doneOnce {
					task.doneOnce = true
					if e.cfgFn().SeedMinutes <= 0 {
						for _, f := range task.t.Files() {
							f.SetPriority(torrent.PiecePriorityNone)
						}
						task.t.SetMaxEstablishedConns(0)
					}
				}
			}
			btInfo = map[string]any{"info": map[string]any{"name": name}}
			files = []map[string]any{{
				"path": filepath.Join(task.dir, name),
				"length": fmt.Sprintf("%d", total), "selected": "true",
			}}
		} else {
			btInfo = map[string]any{}
			files = []map[string]any{{"path": "[METADATA]" + task.infoHash}}
		}
	}
	spd, uspd := task.speed, task.upSpeed
	if st != "active" {
		spd, uspd = 0, 0
	}
	errMsg := task.errMsg
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
		"uploadLength": "0",
		"downloadSpeed": fmt.Sprintf("%d", spd), "uploadSpeed": fmt.Sprintf("%d", uspd),
		"connections": fmt.Sprintf("%d", conn), "numSeeders": fmt.Sprintf("%d", seeds),
		"dir": task.dir, "infoHash": task.infoHash, "bittorrent": btInfo, "files": files,
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
			dt := now.Sub(task.lastAt).Seconds()
			if dt > 0.2 {
				db := done - task.lastBytes
				if db < 0 {
					db = 0
				}
				task.speed = int64(float64(db) / dt)
				du := up - task.lastUp
				if du < 0 {
					du = 0
				}
				task.upSpeed = int64(float64(du) / dt)
			}
			task.lastBytes, task.lastUp, task.lastAt = done, up, now
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
				task.status = "complete"
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
}

func (e *BTEngine) saveSessionLocked() {
	sf := btSessionFile{}
	for _, task := range e.tasks {
		it := btSessionItem{
			GID: task.gid, URI: task.uri, Dir: task.dir, InfoHash: task.infoHash,
			Paused: task.status == "paused", WantPick: task.wantPick,
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
		var gid string
		var aerr error
		switch {
		case strings.HasPrefix(strings.ToLower(it.URI), "magnet:"):
			gid, aerr = e.addMagnet(it.URI, it.Dir, selectFiles, selected)
		case strings.HasPrefix(it.URI, "file://"):
			p := strings.TrimPrefix(it.URI, "file://")
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				log.Println("恢复 BT 任务失败:", rerr)
				continue
			}
			gid, aerr = e.addTorrentBytes(raw, filepath.Base(p), it.Dir, selectFiles, selected)
		default:
			continue
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

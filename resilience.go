package main

// 本文件：让界面永远不直接等待 aria2。
//   - 任务列表由后台协程定时从 aria2 拉取并缓存，/api/tasks 只读缓存，立即返回；
//     aria2 卡住时返回旧数据并标记 stale，界面显示「正在重连」而不是冻结。
//   - 暂停/开始/删除等操作进入单一后台队列，接口立即返回，结果以通知形式回传。
//   - 种子文件列表在服务端缓存并分页（/api/files），开始下载由服务端计算选择（/api/startbt）。
//   - 界面日志（/api/log）与心跳看门狗（界面 N 秒无心跳即写入 app.log）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	uiHeartbeatStale = 10 * time.Second // 界面超过这么久没心跳就记日志
	filesPageMax     = 500
	filesFetchWait   = 2500 * time.Millisecond // /api/files 最多等这么久，否则返回 pending
	filesFetchTO     = 90 * time.Second
	startBTTimeout   = 120 * time.Second
)

// ---------- 通知 ----------

type notice struct {
	Level string `json:"level"` // info | error
	Msg   string `json:"msg"`
	At    int64  `json:"at"`
}

type resilience struct {
	// 任务缓存
	tcMu      sync.Mutex
	tcResult  any
	tcAt      time.Time
	tcErr     string
	tcKick    chan struct{}
	noticeMu  sync.Mutex
	notices   []notice
	actions   chan func()
	filesMu   sync.Mutex
	files     map[string]*fileList
	logMu     sync.Mutex
	logWindow time.Time
	logCount  int
	// 界面心跳
	uiMu       sync.Mutex
	uiLastPing time.Time
	uiHidden   bool
	uiStalled  bool
}

func newResilience() *resilience {
	return &resilience{
		tcKick:  make(chan struct{}, 1),
		actions: make(chan func(), 256),
		files:   map[string]*fileList{},
	}
}

func (a *App) notify(level, msg string) {
	r := a.res
	r.noticeMu.Lock()
	r.notices = append(r.notices, notice{Level: level, Msg: msg, At: time.Now().UnixMilli()})
	if len(r.notices) > 50 {
		r.notices = r.notices[len(r.notices)-50:]
	}
	r.noticeMu.Unlock()
}

func (a *App) takeNotices() []notice {
	r := a.res
	r.noticeMu.Lock()
	defer r.noticeMu.Unlock()
	out := r.notices
	r.notices = nil
	if out == nil {
		out = []notice{}
	}
	return out
}

// ---------- 任务列表缓存 ----------

func (a *App) kickTasks() {
	select {
	case a.res.tcKick <- struct{}{}:
	default:
	}
}

func (a *App) taskPoller() {
	r := a.res
	backoff := time.Second
	for {
		res, err := a.fetchTasks()
		r.tcMu.Lock()
		if err == nil {
			r.tcResult, r.tcAt, r.tcErr = res, time.Now(), ""
			backoff = time.Second
		} else {
			r.tcErr = err.Error()
			if backoff < 4*time.Second {
				backoff *= 2
			}
		}
		wait := time.Second
		if err != nil {
			wait = backoff
		}
		r.tcMu.Unlock()
		select {
		case <-a.quit:
			return
		case <-r.tcKick:
			time.Sleep(150 * time.Millisecond) // 合并短时间内的多次刷新请求
		case <-time.After(wait):
		}
	}
}

func (a *App) handleTasks(w http.ResponseWriter, r *http.Request) {
	rs := a.res
	rs.tcMu.Lock()
	res, at, errStr := rs.tcResult, rs.tcAt, rs.tcErr
	rs.tcMu.Unlock()
	ok, stalledMs, lastErr := a.aria.Health()
	if errStr == "" && !ok {
		errStr = lastErr
	}
	age := int64(-1)
	if !at.IsZero() {
		age = time.Since(at).Milliseconds()
	}
	writeJSON(w, 200, map[string]any{
		"result":  res, // 可能为 nil（尚未连上）
		"ageMs":   age,
		"stale":   at.IsZero() || age > 3500,
		"aria":    map[string]any{"ok": ok, "stalledMs": stalledMs, "error": errStr},
		"notices": a.takeNotices(),
	})
}

// ---------- 后台操作队列 ----------

func (a *App) actionWorker() {
	for {
		select {
		case <-a.quit:
			return
		case f := <-a.res.actions:
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("后台操作异常: %v", p)
					}
				}()
				f()
			}()
			a.kickTasks()
		}
	}
}

func (a *App) enqueue(f func()) bool {
	select {
	case a.res.actions <- f:
		return true
	default:
		return false
	}
}

func (a *App) handleAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string   `json:"action"` // pause | unpause | delete
		GIDs        []string `json:"gids"`
		DeleteFiles bool     `json:"deleteFiles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.GIDs) == 0 {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	var job func()
	switch req.Action {
	case "pause":
		job = func() {
			var bad []string
			for _, g := range req.GIDs {
				if err := a.pauseOne(g); err != nil {
					bad = append(bad, err.Error())
				}
			}
			if len(bad) > 0 {
				a.notify("error", "暂停失败："+bad[0])
			}
		}
	case "unpause":
		job = func() {
			var bad []string
			for _, g := range req.GIDs {
				if err := a.unpauseOne(g); err != nil {
					bad = append(bad, err.Error())
				}
			}
			if len(bad) > 0 {
				a.notify("error", "开始失败："+bad[0])
			}
		}
	case "delete":
		job = func() {
			var errs []string
			for _, gid := range req.GIDs {
				a.dropFiles(gid)
				if err := a.deleteRouted(gid, req.DeleteFiles); err != nil {
					errs = append(errs, err.Error())
				}
			}
			_ = a.aria.Call("aria2.saveSession", nil, nil)
			if len(errs) > 0 {
				a.notify("error", "删除失败："+strings.Join(errs, "；"))
			} else if req.DeleteFiles {
				a.notify("info", "已删除任务和文件")
			} else {
				a.notify("info", "已删除任务")
			}
		}
	default:
		writeErr(w, fmt.Errorf("未知操作"))
		return
	}
	if !a.enqueue(job) {
		writeErr(w, fmt.Errorf("操作太多，请稍后再试"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "queued": true})
}

// ---------- 种子文件列表：服务端缓存 + 分页 ----------

type fileEntry struct {
	Index    int
	Rel      string
	Lower    string
	Length   int64
	Selected bool
}

type fileList struct {
	done       chan struct{}
	err        error
	name       string
	items      []fileEntry
	totalBytes int64
	selCount   int
	selBytes   int64
	at         time.Time
}

func (a *App) dropFiles(gid string) {
	a.res.filesMu.Lock()
	delete(a.res.files, gid)
	a.res.filesMu.Unlock()
}

// readyFileList：已读好的文件列表；没有缓存时现读（最多等 filesFetchWait）。
func (a *App) readyFileList(gid string) (*fileList, error) {
	a.res.filesMu.Lock()
	fl := a.res.files[gid]
	a.res.filesMu.Unlock()
	if fl != nil {
		select {
		case <-fl.done:
			if fl.err == nil {
				return fl, nil
			}
		default:
		}
	}
	fl, err := a.fileListFor(gid, fl != nil && fl.err != nil)
	if err != nil {
		return nil, err
	}
	if fl == nil {
		return nil, fmt.Errorf("文件列表尚未读取完成，请稍后再试")
	}
	return fl, nil
}

// fileListFor 返回缓存的文件列表；正在读取时最多等 filesFetchWait，然后返回 nil（pending）。
func (a *App) fileListFor(gid string, refresh bool) (*fileList, error) {
	rs := a.res
	rs.filesMu.Lock()
	for k, v := range rs.files { // 清理过期缓存
		select {
		case <-v.done:
			if time.Since(v.at) > 15*time.Minute {
				delete(rs.files, k)
			}
		default:
		}
	}
	fl := rs.files[gid]
	if fl != nil && refresh {
		select {
		case <-fl.done:
			fl = nil
		default:
		}
	}
	if fl == nil {
		fl = &fileList{done: make(chan struct{})}
		rs.files[gid] = fl
		go a.loadFileList(gid, fl)
	}
	rs.filesMu.Unlock()
	select {
	case <-fl.done:
		if fl.err != nil {
			a.dropFiles(gid) // 下次重试
			return nil, fl.err
		}
		return fl, nil
	case <-time.After(filesFetchWait):
		return nil, nil
	}
}

func (a *App) loadFileList(gid string, fl *fileList) {
	defer func() {
		if p := recover(); p != nil {
			fl.err = fmt.Errorf("读取文件列表异常: %v", p)
		}
		fl.at = time.Now()
		close(fl.done)
	}()
	if a.bt != nil && a.bt.Has(gid) {
		name, items, err := a.bt.FileEntries(gid)
		if err != nil {
			fl.err = err
			return
		}
		fl.name = name
		fl.items = items
		for _, e := range items {
			fl.totalBytes += e.Length
			if e.Selected {
				fl.selCount++
				fl.selBytes += e.Length
			}
		}
		return
	}
	t0 := time.Now()
	var st struct {
		Dir        string `json:"dir"`
		Bittorrent *struct {
			Info *struct {
				Name string `json:"name"`
			} `json:"info"`
		} `json:"bittorrent"`
	}
	if raw, err := a.aria.CallRawTimeout("aria2.tellStatus", []any{gid, []string{"dir", "bittorrent"}}, filesFetchTO); err == nil {
		_ = json.Unmarshal(raw, &st)
	} else {
		fl.err = err
		return
	}
	raw, err := a.aria.CallRawTimeout("aria2.getFiles", []any{gid}, filesFetchTO)
	if err != nil {
		fl.err = err
		return
	}
	var files []struct {
		Index    string `json:"index"`
		Path     string `json:"path"`
		Length   string `json:"length"`
		Selected string `json:"selected"`
	}
	if err := json.Unmarshal(raw, &files); err != nil {
		fl.err = fmt.Errorf("解析文件列表失败: %v", err)
		return
	}
	norm := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	dir := strings.TrimRight(norm(st.Dir), "/")
	prefixes := []string{}
	if st.Bittorrent != nil && st.Bittorrent.Info != nil && st.Bittorrent.Info.Name != "" {
		fl.name = st.Bittorrent.Info.Name
		prefixes = append(prefixes, dir+"/"+st.Bittorrent.Info.Name+"/")
	}
	prefixes = append(prefixes, dir+"/")
	fl.items = make([]fileEntry, 0, len(files))
	for _, f := range files {
		idx, _ := strconv.Atoi(f.Index)
		ln, _ := strconv.ParseInt(f.Length, 10, 64)
		rel := norm(f.Path)
		for _, p := range prefixes {
			if strings.HasPrefix(rel, p) {
				rel = rel[len(p):]
				break
			}
		}
		e := fileEntry{Index: idx, Rel: rel, Lower: strings.ToLower(rel), Length: ln, Selected: f.Selected == "true"}
		fl.items = append(fl.items, e)
		fl.totalBytes += ln
		if e.Selected {
			fl.selCount++
			fl.selBytes += ln
		}
	}
	log.Printf("读取种子文件列表: %s 共 %d 个文件，%.1f MB 响应，耗时 %v", gid, len(files), float64(len(raw))/1e6, time.Since(t0).Round(time.Millisecond))
}

func (a *App) handleFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GID     string `json:"gid"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Q       string `json:"q"`
		Refresh bool   `json:"refresh"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GID == "" {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	fl, err := a.fileListFor(req.GID, req.Refresh)
	if err != nil {
		writeErr(w, err)
		return
	}
	if fl == nil {
		writeJSON(w, 200, map[string]any{"pending": true})
		return
	}
	if req.Limit <= 0 || req.Limit > filesPageMax {
		req.Limit = 200
	}
	q := strings.ToLower(strings.TrimSpace(req.Q))
	matched := fl.items
	if q != "" {
		matched = make([]fileEntry, 0, 256)
		for _, e := range fl.items {
			if strings.Contains(e.Lower, q) {
				matched = append(matched, e)
			}
		}
	}
	if req.Offset < 0 || req.Offset >= len(matched) {
		req.Offset = 0
	}
	end := req.Offset + req.Limit
	if end > len(matched) {
		end = len(matched)
	}
	items := make([]map[string]any, 0, end-req.Offset)
	for _, e := range matched[req.Offset:end] {
		items = append(items, map[string]any{"i": e.Index, "p": e.Rel, "s": e.Length, "sel": e.Selected})
	}
	writeJSON(w, 200, map[string]any{
		"name": fl.name, "total": len(fl.items), "totalBytes": fl.totalBytes,
		"selCount": fl.selCount, "selBytes": fl.selBytes,
		"matched": len(matched), "offset": req.Offset, "items": items,
	})
}

// compressIndexes 把 [1,2,3,7,9,10] 压成 "1-3,7,9-10"（aria2 select-file 支持区间）。
func compressIndexes(idx []int) string {
	sort.Ints(idx)
	var b strings.Builder
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && idx[j+1] == idx[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if j > i {
			fmt.Fprintf(&b, "%d-%d", idx[i], idx[j])
		} else {
			b.WriteString(strconv.Itoa(idx[i]))
		}
		i = j + 1
	}
	return b.String()
}

// handleStartBT：base=all 时 except 为排除项；base=none 时 except 为选中项；skip=true 直接全部下载。
func (a *App) handleStartBT(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GID    string `json:"gid"`
		Base   string `json:"base"`
		Except []int  `json:"except"`
		Skip   bool   `json:"skip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GID == "" {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	selectFile := ""
	count := -1
	if !req.Skip {
		fl, ferr := a.readyFileList(req.GID)
		if ferr != nil {
			writeErr(w, fmt.Errorf("%v，可点「跳过选择，全部下载」", ferr))
			return
		}
		ex := make(map[int]bool, len(req.Except))
		for _, i := range req.Except {
			ex[i] = true
		}
		var sel []int
		for _, e := range fl.items {
			def := e.Selected // base=keep：沿用当前选择
			switch req.Base {
			case "all":
				def = true
			case "none":
				def = false
			}
			on := def != ex[e.Index]
			if on {
				sel = append(sel, e.Index)
			}
		}
		if len(sel) == 0 {
			writeErr(w, fmt.Errorf("至少选择一个文件"))
			return
		}
		count = len(sel)
		if len(sel) != len(fl.items) || fl.selCount != len(fl.items) {
			selectFile = compressIndexes(sel)
		}
	}
	gid := req.GID
	base, except, skip := req.Base, req.Except, req.Skip
	ok := a.enqueue(func() {
		t0 := time.Now()
		if a.bt != nil && a.bt.Has(gid) {
			if err := a.bt.StartSelected(gid, base, except, skip); err != nil {
				a.notify("error", "开始下载失败："+err.Error())
				return
			}
			a.dropFiles(gid)
			what := "全部文件"
			if count >= 0 {
				what = strconv.Itoa(count) + " 个文件"
			}
			log.Printf("种子任务 %s 已开始（%s / BitTorrent），耗时 %v", gid, what, time.Since(t0).Round(time.Millisecond))
			return
		}
		if selectFile != "" {
			if _, err := a.aria.CallRawTimeout("aria2.changeOption", []any{gid, map[string]string{"select-file": selectFile}}, startBTTimeout); err != nil {
				a.notify("error", "设置下载文件失败："+err.Error())
				return
			}
		}
		if _, err := a.aria.CallRawTimeout("aria2.unpause", []any{gid}, startBTTimeout); err != nil {
			a.notify("error", "开始下载失败："+err.Error())
			return
		}
		a.dropFiles(gid)
		what := "全部文件"
		if count >= 0 {
			what = strconv.Itoa(count) + " 个文件"
		}
		log.Printf("种子任务 %s 已开始（%s），aria2 处理耗时 %v", gid, what, time.Since(t0).Round(time.Millisecond))
	})
	if !ok {
		writeErr(w, fmt.Errorf("操作太多，请稍后再试"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "queued": true, "count": count})
}

// ---------- 界面日志与心跳看门狗 ----------

func (a *App) handleUILog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
		Stack string `json:"stack"`
		Src   string `json:"src"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	rs := a.res
	rs.logMu.Lock()
	if time.Since(rs.logWindow) > time.Minute {
		rs.logWindow, rs.logCount = time.Now(), 0
	}
	rs.logCount++
	n := rs.logCount
	rs.logMu.Unlock()
	if n <= 40 {
		clip := func(s string, n int) string {
			if len(s) > n {
				return s[:n] + "…"
			}
			return s
		}
		log.Printf("[界面:%s] %s %s %s", clip(req.Level, 16), clip(req.Msg, 1000), clip(req.Src, 200), clip(strings.ReplaceAll(req.Stack, "\n", " | "), 1500))
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) notePing(r *http.Request) {
	var hb struct {
		LagMs  int64 `json:"lagMs"`
		Hidden bool  `json:"hidden"`
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&hb)
	}
	now := time.Now()
	a.pingMu.Lock()
	a.lastPing = now
	a.pingMu.Unlock()
	rs := a.res
	rs.uiMu.Lock()
	wasHidden := rs.uiHidden
	if rs.uiStalled {
		log.Printf("[watchdog] 界面心跳恢复，中断约 %.1f 秒", now.Sub(rs.uiLastPing).Seconds())
		rs.uiStalled = false
	}
	rs.uiLastPing, rs.uiHidden = now, hb.Hidden
	rs.uiMu.Unlock()
	// 窗口隐藏（最小化到托盘）时 WebView2 会把定时器节流到约每分钟一次，
	// 这时的「卡顿」是节流造成的，不是真的卡住（v1.5.2 每分钟记两条误报）。
	if hb.LagMs >= 2000 && !hb.Hidden && !wasHidden {
		log.Printf("[watchdog] 界面主线程卡顿约 %d ms（由界面自报）", hb.LagMs)
	}
}

// 隐藏窗口的定时器被节流到约 60 秒一次，超过这个时间才算异常。
const uiHiddenHeartbeatStale = 150 * time.Second

// uiWatchdog：界面超过 uiHeartbeatStale 没有心跳就写日志（每次中断只记一次）。
func (a *App) uiWatchdog() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
			rs := a.res
			rs.uiMu.Lock()
			last, hidden, flagged := rs.uiLastPing, rs.uiHidden, rs.uiStalled
			stale := uiHeartbeatStale
			if hidden {
				stale = uiHiddenHeartbeatStale
			}
			if !last.IsZero() && !flagged && time.Since(last) > stale {
				rs.uiStalled = true
				rs.uiMu.Unlock()
				ok, stalled, _ := a.aria.Health()
				log.Printf("[watchdog] 界面已 %.0f 秒没有心跳（窗口隐藏=%t，aria2 正常=%t，aria2 无响应 %d ms）",
					time.Since(last).Seconds(), hidden, ok, stalled)
				continue
			}
			rs.uiMu.Unlock()
		}
	}
}

func (a *App) pauseOne(gid string) error {
	switch a.engineForGID(gid) {
	case EngineBT:
		if a.bt == nil {
			return fmt.Errorf("BitTorrent 引擎不可用")
		}
		return a.bt.Pause(gid)
	case EngineYTDLP:
		if a.yt == nil {
			return fmt.Errorf("yt-dlp 引擎不可用")
		}
		return a.yt.Pause(gid)
	default:
		if _, err := a.aria.CallRawTimeout("aria2.forcePause", []any{gid}, 30*time.Second); err != nil {
			_, err2 := a.aria.CallRawTimeout("aria2.pause", []any{gid}, 30*time.Second)
			if err2 == nil {
				a.saveAriaSessionSoon()
			}
			return err2
		}
		a.saveAriaSessionSoon()
		return nil
	}
}

func (a *App) unpauseOne(gid string) error {
	switch a.engineForGID(gid) {
	case EngineBT:
		if a.bt == nil {
			return fmt.Errorf("BitTorrent 引擎不可用")
		}
		return a.bt.Unpause(gid)
	case EngineYTDLP:
		if a.yt == nil {
			return fmt.Errorf("yt-dlp 引擎不可用")
		}
		return a.yt.Unpause(gid)
	default:
		_, err := a.aria.CallRawTimeout("aria2.unpause", []any{gid}, startBTTimeout)
		if err == nil {
			a.saveAriaSessionSoon()
		}
		return err
	}
}

// saveAriaSessionSoon：暂停/继续后尽快写会话文件。aria2 每 15 秒才自动保存一次，
// 以前「暂停→继续」后若在 15 秒内被强制结束，重启后任务会停在「已暂停」。
func (a *App) saveAriaSessionSoon() {
	if a.aria == nil {
		return
	}
	go func() {
		time.Sleep(1500 * time.Millisecond) // 等 forcePause / unpause 生效
		_ = a.aria.Call("aria2.saveSession", nil, nil)
	}()
}

func (a *App) deleteRouted(gid string, deleteFiles bool) error {
	switch a.engineForGID(gid) {
	case EngineBT:
		if a.bt == nil {
			return fmt.Errorf("BitTorrent 引擎不可用")
		}
		return a.bt.Remove(gid, deleteFiles)
	case EngineYTDLP:
		if a.yt == nil {
			return fmt.Errorf("yt-dlp 引擎不可用")
		}
		return a.yt.Remove(gid, deleteFiles)
	default:
		return a.deleteOne(gid, deleteFiles)
	}
}

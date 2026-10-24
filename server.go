package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

var allowedMethods = map[string]bool{
	"aria2.tellActive": true, "aria2.tellWaiting": true, "aria2.tellStopped": true, "aria2.tellStatus": true,
	"aria2.getFiles": true, "aria2.getPeers": true, "aria2.getServers": true, "aria2.getUris": true, "aria2.getGlobalStat": true,
	"aria2.addUri": true, "aria2.addTorrent": true,
	"aria2.pause": true, "aria2.forcePause": true, "aria2.unpause": true,
	"aria2.pauseAll": true, "aria2.forcePauseAll": true, "aria2.unpauseAll": true,
	"aria2.remove": true, "aria2.forceRemove": true, "aria2.removeDownloadResult": true, "aria2.purgeDownloadResult": true,
	"aria2.changeOption": true, "aria2.getOption": true, "aria2.changePosition": true, "aria2.getVersion": true,
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, 200, map[string]any{"error": err.Error()})
}

func (a *App) routes(port int) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		static.ServeHTTP(w, r)
	}))

	api := http.NewServeMux()
	api.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		a.notePing(r)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	api.HandleFunc("/api/action", a.handleAction)
	api.HandleFunc("/api/files", a.handleFiles)
	api.HandleFunc("/api/startbt", a.handleStartBT)
	api.HandleFunc("/api/reselect", a.handleReselect)
	api.HandleFunc("/api/details", a.handleDetails)
	api.HandleFunc("/api/log", a.handleUILog)
	api.HandleFunc("/api/add", a.handleAdd)
	api.HandleFunc("/api/recommend", a.handleRecommend)
	api.HandleFunc("/api/rpc", a.handleRPC)
	api.HandleFunc("/api/multicall", a.handleMulticall)
	api.HandleFunc("/api/tasks", a.handleTasks)
	api.HandleFunc("/api/inbox", a.handleInbox)
	api.HandleFunc("/api/assoc", a.handleAssoc)
	api.HandleFunc("/api/firewall", a.handleFirewall)
	// 稳定的自动化 API（docs/AGENT_API.md）
	api.HandleFunc("/api/v1/ping", a.handleV1Ping)
	api.HandleFunc("/api/v1/add", a.handleV1Add)
	api.HandleFunc("/api/v1/task", a.handleV1Task)
	api.HandleFunc("/api/v1/tasks", a.handleV1Tasks)
	api.HandleFunc("/api/v1/remove", a.handleV1Control)
	api.HandleFunc("/api/v1/pause", a.handleV1Control)
	api.HandleFunc("/api/v1/resume", a.handleV1Control)
	api.HandleFunc("/api/task", a.handleV1Task)
	api.HandleFunc("/api/readtorrent", a.handleReadTorrent)
	api.HandleFunc("/api/config", a.handleConfig)
	api.HandleFunc("/api/delete", a.handleDelete)
	api.HandleFunc("/api/retry", a.handleRetry)
	api.HandleFunc("/api/pickdir", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		cur := a.cfg.DownloadDir
		a.mu.Unlock()
		p, err := pickFolder(cur)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"path": p})
	})
	api.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeErr(w, fmt.Errorf("参数错误"))
			return
		}
		if err := revealInFolder(req.Path); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	api.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
		go func() { time.Sleep(200 * time.Millisecond); a.Quit() }()
	})

	okHosts := map[string]bool{fmt.Sprintf("127.0.0.1:%d", port): true, fmt.Sprintf("localhost:%d", port): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 防 DNS 重绑定：只接受本机 Host
		if !okHosts[r.Host] {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("X-App", appName)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// 自定义请求头 + 随机令牌：防止网页跨站调用本地接口
			if !a.tokenOK(r) {
				http.Error(w, "unauthorized", 401)
				return
			}
			allowGet := r.URL.Path == "/api/ping" || r.URL.Path == "/api/config" || r.URL.Path == "/api/tasks" || r.URL.Path == "/api/inbox" || r.URL.Path == "/api/assoc" || r.URL.Path == "/api/firewall" || r.URL.Path == "/api/v1/ping" || r.URL.Path == "/api/v1/task" || r.URL.Path == "/api/v1/tasks" || r.URL.Path == "/api/task"
			if r.Method != http.MethodPost && !(r.Method == http.MethodGet && allowGet) {
				http.Error(w, "method not allowed", 405)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 80<<20)
			api.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *App) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req rpcCall
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	if !allowedMethods[req.Method] {
		writeErr(w, fmt.Errorf("不允许的方法 %s", req.Method))
		return
	}
	to := ariaRPCTimeout
	switch req.Method {
	case "aria2.getFiles", "aria2.addTorrent", "aria2.addUri":
		to = ariaRPCLongTimeout
	}
	res, err := a.aria.CallRawTimeout(req.Method, req.Params, to)
	if err != nil {
		if req.Method == "aria2.addUri" || req.Method == "aria2.addTorrent" {
			writeErr(w, fmt.Errorf("%s", friendlyAriaErr(err)))
		} else {
			writeErr(w, err)
		}
		return
	}
	if req.Method == "aria2.addTorrent" && len(req.Params) > 0 {
		a.recordUpload(req.Params, res)
	}
	writeJSON(w, 200, map[string]any{"result": res})
}

func (a *App) recordUpload(params []any, res json.RawMessage) {
	b64, _ := params[0].(string)
	name := uploadedTorrentName(b64)
	var gid string
	if name == "" || json.Unmarshal(res, &gid) != nil {
		return
	}
	a.mu.Lock()
	dir := a.cfg.DownloadDir
	a.mu.Unlock()
	if len(params) > 2 {
		if o, ok := params[2].(map[string]any); ok {
			if d, ok := o["dir"].(string); ok && d != "" {
				dir = d
			}
		}
	}
	a.art.add(gid, filepath.Join(dir, name))
}

func (a *App) handleMulticall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Calls []rpcCall `json:"calls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	for _, c := range req.Calls {
		if !allowedMethods[c.Method] {
			writeErr(w, fmt.Errorf("不允许的方法 %s", c.Method))
			return
		}
	}
	res, err := a.aria.Multicall(req.Calls)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"result": res})
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.mu.Lock()
		c := a.cfg
		a.mu.Unlock()
		engines := []map[string]string{
			{"id": EngineAuto, "label": engineLabel(EngineAuto)},
			{"id": EngineAria2, "label": engineLabel(EngineAria2)},
			{"id": EngineBT, "label": engineLabel(EngineBT)},
			{"id": EngineYTDLP, "label": engineLabel(EngineYTDLP)},
		}
		writeJSON(w, 200, map[string]any{
			"config": c, "dataDir": a.dataDir, "canPickDir": canPickFolder,
			"trackerCount": strings.Count(a.trackers(), ",") + 1,
			"engines":      engines,
			"btReady":      a.bt != nil, "ytdlpReady": a.yt != nil && a.yt.findExe() != "",
			"defaultUserAgent": defaultUserAgent(), "version": AppVersion,
		})
		return
	}
	a.mu.Lock()
	c := a.cfg
	a.mu.Unlock()
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, err)
		return
	}
	a.normalize(&c)
	c.DownloadDir = filepath.Clean(c.DownloadDir)
	if !filepath.IsAbs(c.DownloadDir) {
		writeErr(w, fmt.Errorf("下载目录必须是完整路径"))
		return
	}
	if err := os.MkdirAll(c.DownloadDir, 0755); err != nil {
		writeErr(w, fmt.Errorf("无法创建下载目录: %v", err))
		return
	}
	a.mu.Lock()
	a.cfg = c
	opts := map[string]string{
		"dir":                        c.DownloadDir,
		"max-concurrent-downloads":   strconv.Itoa(c.MaxConcurrent),
		"max-overall-download-limit": fmt.Sprintf("%dK", c.MaxDownloadKB),
		"max-overall-upload-limit":   fmt.Sprintf("%dK", c.MaxUploadKB),
		"split":                      strconv.Itoa(c.SplitPerTask),
		"max-connection-per-server":  strconv.Itoa(c.SplitPerTask),
		"seed-time":                  strconv.Itoa(c.SeedMinutes),
		"pause-metadata":             strconv.FormatBool(c.SelectBTFiles),
		"file-allocation":            c.effectiveFileAllocation(),
		"bt-tracker":                 a.trackers(),
		"user-agent":                 c.effectiveUserAgent(),
	}
	err := a.saveConfig()
	a.mu.Unlock()
	if err != nil {
		writeErr(w, fmt.Errorf("保存设置失败: %v", err))
		return
	}
	if err := a.aria.Call("aria2.changeGlobalOption", []any{opts}, nil); err != nil {
		writeErr(w, fmt.Errorf("应用设置失败: %v", err))
		return
	}
	a.kickTasks()
	writeJSON(w, 200, map[string]any{"ok": true, "config": c})
}

type ariaStatus struct {
	GID        string   `json:"gid"`
	Status     string   `json:"status"`
	Dir        string   `json:"dir"`
	InfoHash   string   `json:"infoHash"`
	Following  string   `json:"following"`
	FollowedBy []string `json:"followedBy"`
	Files      []struct {
		Path string `json:"path"`
	} `json:"files"`
	Bittorrent *struct {
		Info *struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GIDs        []string `json:"gids"`
		DeleteFiles bool     `json:"deleteFiles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	var errs []string
	for _, gid := range req.GIDs {
		if err := a.deleteOne(gid, req.DeleteFiles); err != nil {
			errs = append(errs, err.Error())
		}
	}
	_ = a.aria.Call("aria2.saveSession", nil, nil)
	if len(errs) > 0 {
		writeErr(w, fmt.Errorf("%s", strings.Join(errs, "；")))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) deleteOne(gid string, deleteFiles bool) error {
	if a.deleteParked(gid) {
		return nil
	}
	var st ariaStatus
	if err := a.aria.Call("aria2.tellStatus", []any{gid}, &st); err != nil {
		return err
	}
	// 先记墓碑：aria2 --force-save 会把已删除但结果未清掉的任务写回会话，下次启动「复活」。
	a.markRemoved(gid)
	defer a.saveTaskMeta()
	if st.Status == "active" || st.Status == "waiting" || st.Status == "paused" {
		if err := a.aria.Call("aria2.forceRemove", []any{gid}, nil); err != nil {
			return err
		}
		for i := 0; i < 50; i++ {
			var s2 ariaStatus
			if err := a.aria.Call("aria2.tellStatus", []any{gid, []string{"status"}}, &s2); err != nil || s2.Status == "removed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for i := 0; i < 20; i++ { // 刚 forceRemove 的任务可能还在「removing」，结果要稍后才能清掉
		if err := a.aria.Call("aria2.removeDownloadResult", []any{gid}, nil); err == nil {
			break
		}
		var s2 ariaStatus
		if err := a.aria.Call("aria2.tellStatus", []any{gid, []string{"status"}}, &s2); err != nil {
			break // 已不存在
		}
		time.Sleep(150 * time.Millisecond)
	}
	st.GID = gid
	a.cleanupArtifacts(&st)
	if st.Following != "" {
		_ = a.aria.Call("aria2.removeDownloadResult", []any{st.Following}, nil)
	}
	if !deleteFiles {
		return nil
	}
	dir := filepath.Clean(st.Dir)
	var targets []string
	for _, f := range st.Files {
		if f.Path == "" || strings.HasPrefix(f.Path, "[METADATA]") {
			continue
		}
		targets = append(targets, filepath.Clean(f.Path), filepath.Clean(f.Path)+".aria2")
	}
	if st.Bittorrent != nil && st.Bittorrent.Info != nil && st.Bittorrent.Info.Name != "" {
		targets = append(targets, filepath.Join(dir, st.Bittorrent.Info.Name)+".aria2")
	}
	var failed []string
	parents := map[string]bool{}
	for _, p := range targets {
		if !within(dir, p) {
			log.Println("跳过下载目录之外的路径:", p)
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			failed = append(failed, filepath.Base(p))
		}
		parents[filepath.Dir(p)] = true
	}
	// 清理因此变空的子目录（不会删除下载目录本身）
	for d := range parents {
		for within(dir, d) && d != dir {
			if os.Remove(d) != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("部分文件删除失败: %s", strings.Join(failed, ", "))
	}
	return nil
}

func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func friendlyAriaErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "being downloaded by other command") ||
		strings.Contains(low, "file already in download") ||
		strings.Contains(msg, "另一个下载任务") {
		return "目标文件名正被另一个任务占用（常见于同时添加同一文件的 HTTP 链接和 .torrent）。请先暂停或删除其中一个任务，或换一个保存目录后再试。"
	}
	return msg
}

type retryStatus struct {
	GID          string   `json:"gid"`
	Status       string   `json:"status"`
	Dir          string   `json:"dir"`
	InfoHash     string   `json:"infoHash"`
	ErrorMessage string   `json:"errorMessage"`
	FollowedBy   []string `json:"followedBy"`
	Following    string   `json:"following"`
	Files        []struct {
		Path     string `json:"path"`
		Selected string `json:"selected"`
		Uris     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"files"`
	Bittorrent *struct {
		Info *struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
}

func (a *App) handleRetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GIDs []string `json:"gids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.GIDs) == 0 {
		writeErr(w, fmt.Errorf("请选择要重新开始的失败任务"))
		return
	}
	var okN int
	var errs []string
	for _, gid := range req.GIDs {
		if err := a.retryOne(gid); err != nil {
			errs = append(errs, friendlyAriaErr(err))
			continue
		}
		okN++
	}
	_ = a.aria.Call("aria2.saveSession", nil, nil)
	if okN == 0 {
		writeErr(w, fmt.Errorf("%s", strings.Join(errs, "；")))
		return
	}
	out := map[string]any{"ok": true, "retried": okN}
	if len(errs) > 0 {
		out["warning"] = strings.Join(errs, "；")
	}
	writeJSON(w, 200, out)
}

func (a *App) retryOne(gid string) error {
	if a.bt != nil && a.bt.Has(gid) {
		// 重新 unpause / 若 error 则无法从快照完美恢复时提示用户重新添加
		return a.bt.Unpause(gid)
	}
	if a.yt != nil && a.yt.Has(gid) {
		_, err := a.yt.Retry(gid)
		return err
	}
	if _, ok, err := a.retryParked(gid); ok {
		return err
	}
	var st retryStatus
	if err := a.aria.Call("aria2.tellStatus", []any{gid}, &st); err != nil {
		return fmt.Errorf("读取任务失败: %v", err)
	}
	forced := a.clearForcedError(gid)
	if st.Status != "error" && st.Status != "removed" && !forced {
		return fmt.Errorf("任务不是失败状态（当前：%s）", st.Status)
	}
	dir := st.Dir
	if dir == "" {
		a.mu.Lock()
		dir = a.cfg.DownloadDir
		a.mu.Unlock()
	}
	opt := map[string]string{
		"dir":                dir,
		"continue":           "true",
		"allow-overwrite":    "true", // 允许从同名部分文件续传
		"auto-file-renaming": "false",
	}
	var oldOpt map[string]string
	if err := a.aria.Call("aria2.getOption", []any{gid}, &oldOpt); err == nil {
		if sf := oldOpt["select-file"]; sf != "" {
			opt["select-file"] = sf
		}
	}

	kept := a.art.take(gid) // 先摘掉旧映射，种子文件本身先不删
	var newGID string
	isBT := st.Bittorrent != nil || st.InfoHash != ""
	var err error
	if isBT {
		newGID, err = a.readdTorrentOrMagnet(&st, opt, kept)
	} else {
		uris := collectURIs(&st)
		if len(uris) == 0 {
			// 失败任务有时 files 为空，再问一次 getUris
			var raw []struct {
				URI string `json:"uri"`
			}
			_ = a.aria.Call("aria2.getUris", []any{gid}, &raw)
			for _, u := range raw {
				u.URI = strings.TrimSpace(u.URI)
				if u.URI != "" {
					uris = append(uris, u.URI)
				}
			}
		}
		if len(uris) == 0 {
			// 恢复映射以免丢失
			for _, p := range kept {
				a.art.add(gid, p)
			}
			return fmt.Errorf("找不到原始下载链接，无法重试")
		}
		err = a.aria.Call("aria2.addUri", []any{uris, opt}, &newGID)
	}
	if err != nil {
		for _, p := range kept {
			a.art.add(gid, p)
		}
		return err
	}
	for _, p := range kept {
		a.art.add(newGID, p)
	}
	_ = a.aria.Call("aria2.removeDownloadResult", []any{gid}, nil)
	return nil
}

func collectURIs(st *retryStatus) []string {
	seen := map[string]bool{}
	var uris []string
	for _, f := range st.Files {
		for _, u := range f.Uris {
			u.URI = strings.TrimSpace(u.URI)
			if u.URI == "" || seen[u.URI] {
				continue
			}
			seen[u.URI] = true
			uris = append(uris, u.URI)
		}
	}
	return uris
}

func (a *App) readdTorrentOrMagnet(st *retryStatus, opt map[string]string, kept []string) (string, error) {
	var candidates []string
	seen := map[string]bool{}
	addCand := func(p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." || seen[p] {
			return
		}
		seen[p] = true
		candidates = append(candidates, p)
	}
	for _, p := range kept {
		addCand(p)
	}
	if st.InfoHash != "" {
		if st.Dir != "" {
			addCand(filepath.Join(st.Dir, st.InfoHash+".torrent"))
		}
		a.mu.Lock()
		dd := a.cfg.DownloadDir
		a.mu.Unlock()
		if dd != "" {
			addCand(filepath.Join(dd, st.InfoHash+".torrent"))
		}
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil || len(b) < 16 {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(b)
		var gid string
		if err := a.aria.Call("aria2.addTorrent", []any{b64, []string{}, opt}, &gid); err != nil {
			return "", err
		}
		return gid, nil
	}
	uris := collectURIs(st)
	if len(uris) == 0 {
		var raw []struct {
			URI string `json:"uri"`
		}
		_ = a.aria.Call("aria2.getUris", []any{st.GID}, &raw)
		for _, u := range raw {
			u.URI = strings.TrimSpace(u.URI)
			if u.URI != "" {
				uris = append(uris, u.URI)
			}
		}
	}
	if len(uris) == 0 && st.InfoHash != "" {
		uris = []string{"magnet:?xt=urn:btih:" + strings.ToLower(st.InfoHash)}
	}
	if len(uris) == 0 {
		return "", fmt.Errorf("找不到种子文件或磁力链接，无法重试")
	}
	var gid string
	if err := a.aria.Call("aria2.addUri", []any{uris, opt}, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

// pollKeys 不含完整 files，避免大种子每秒把界面卡死。
var pollKeys = []string{
	"gid", "status", "totalLength", "completedLength", "uploadLength",
	"downloadSpeed", "uploadSpeed", "connections", "numSeeders", "seeder",
	"errorCode", "errorMessage", "dir", "bittorrent", "infoHash",
	"followedBy", "following", "verifiedLength", "verifyIntegrityPending",
}

// fetchTasks 合并各引擎任务列表（只在后台缓存协程里调用）。
// aria2 短暂失败时仍返回 BT / yt-dlp 任务，避免整表空白。
func (a *App) fetchTasks() (any, error) {
	raw, err := a.aria.MulticallTimeout([]rpcCall{
		{Method: "aria2.tellActive", Params: []any{pollKeys}},
		{Method: "aria2.tellWaiting", Params: []any{0, 500, pollKeys}},
		{Method: "aria2.tellStopped", Params: []any{0, 500, pollKeys}},
		{Method: "aria2.getGlobalStat", Params: []any{}},
	}, ariaRPCTimeout)
	var active, waiting, stopped []map[string]any
	var stat map[string]any
	if err != nil {
		active, waiting, stopped = nil, nil, nil
		stat = map[string]any{}
		// 若其它引擎也没有任务，才把错误抛给缓存层
		if (a.bt == nil || len(a.bt.ListSnapshots()) == 0) && (a.yt == nil || len(a.yt.ListSnapshots()) == 0) {
			return nil, err
		}
		log.Printf("aria2 任务列表暂时失败，仍合并其它引擎: %v", err)
	} else {
		// 再为缺少名称的 HTTP 任务补一份精简 files（仅首个文件）
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(unwrapMulti(parts, 0), &active)
		_ = json.Unmarshal(unwrapMulti(parts, 1), &waiting)
		_ = json.Unmarshal(unwrapMulti(parts, 2), &stopped)
		active = a.applyAria2Overrides(active)
		waiting = a.applyAria2Overrides(waiting)
		stopped = a.applyAria2Overrides(stopped)
		all := append(append(active, waiting...), stopped...)
		need := make([]string, 0)
		for _, t := range all {
			if t["bittorrent"] != nil {
				continue
			}
			need = append(need, fmt.Sprint(t["gid"]))
		}
		fileMap := map[string]any{}
		if len(need) > 0 && len(need) <= 40 {
			var calls []rpcCall
			for _, g := range need {
				calls = append(calls, rpcCall{Method: "aria2.getFiles", Params: []any{g}})
			}
			if fr, err := a.aria.MulticallTimeout(calls, ariaRPCTimeout); err == nil {
				var fres []json.RawMessage
				if json.Unmarshal(fr, &fres) == nil {
					for i, g := range need {
						if i >= len(fres) {
							break
						}
						var files []map[string]any
						b := unwrapMulti(fres, i)
						if json.Unmarshal(b, &files) == nil && len(files) > 0 {
							fileMap[g] = []any{slimFile(files[0])}
						}
					}
				}
			}
		}
		for _, t := range all {
			g := fmt.Sprint(t["gid"])
			if f, ok := fileMap[g]; ok {
				t["files"] = f
			}
			t["engine"] = EngineAria2
			t["engineLabel"] = engineLabel(EngineAria2)
		}
		_ = json.Unmarshal(unwrapMulti(parts, 3), &stat)
		if stat == nil {
			stat = map[string]any{}
		}
	} // end aria2 ok
	// v1.5.4：已从 aria2 移走的失败任务（HTML 网页）照常显示为失败
	stopped = append(stopped, a.parkedSnapshots()...)
	if a.bt != nil {
		for _, t := range a.bt.ListSnapshots() {
			switch fmt.Sprint(t["status"]) {
			case "active":
				active = append(active, t)
			case "waiting", "paused":
				waiting = append(waiting, t)
			default:
				stopped = append(stopped, t)
			}
		}
	}
	if a.yt != nil {
		for _, t := range a.yt.ListSnapshots() {
			switch fmt.Sprint(t["status"]) {
			case "active":
				active = append(active, t)
			case "waiting", "paused":
				waiting = append(waiting, t)
			default:
				stopped = append(stopped, t)
			}
		}
	}
	if stat == nil {
		stat = map[string]any{}
	}
	var extraDL, extraUL int64
	var extraActive, extraWaiting int
	for _, t := range active {
		if fmt.Sprint(t["engine"]) == EngineAria2 {
			continue
		}
		extraActive++
		extraDL += parseInt64(t["downloadSpeed"])
	}
	// 上传：内置 BT 下完后仍可能在做种（状态是「已完成」），也要计入顶栏 ↑。
	for _, l := range [][]map[string]any{active, waiting, stopped} {
		for _, t := range l {
			if fmt.Sprint(t["engine"]) != EngineAria2 {
				extraUL += parseInt64(t["uploadSpeed"])
			}
		}
	}
	for _, t := range waiting {
		if fmt.Sprint(t["engine"]) == EngineAria2 {
			continue
		}
		extraWaiting++
	}
	stat["downloadSpeed"] = fmt.Sprintf("%d", parseInt64(stat["downloadSpeed"])+extraDL)
	stat["uploadSpeed"] = fmt.Sprintf("%d", parseInt64(stat["uploadSpeed"])+extraUL)
	stat["numActive"] = fmt.Sprintf("%d", parseInt64(stat["numActive"])+int64(extraActive))
	stat["numWaiting"] = fmt.Sprintf("%d", parseInt64(stat["numWaiting"])+int64(extraWaiting))
	a.annotateSources(active, waiting, stopped)
	return []any{active, waiting, stopped, stat}, nil
}

func parseInt64(v any) int64 {
	switch x := v.(type) {
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

func unwrapMulti(parts []json.RawMessage, i int) []byte {
	if i >= len(parts) {
		return []byte("null")
	}
	// system.multicall 每项是 [result] 或 {fault}
	var arr []json.RawMessage
	if json.Unmarshal(parts[i], &arr) == nil && len(arr) > 0 {
		return arr[0]
	}
	return parts[i]
}

func slimFile(f map[string]any) map[string]any {
	out := map[string]any{"path": f["path"], "length": f["length"], "selected": f["selected"], "index": f["index"]}
	if uris, ok := f["uris"].([]any); ok && len(uris) > 0 {
		out["uris"] = uris[:1]
	}
	return out
}

func (a *App) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items := a.takeInbox()
		if items == nil {
			items = []inboxItem{}
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	var req struct {
		Items []inboxItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	a.pushInbox(req.Items)
	writeJSON(w, 200, map[string]any{"ok": true, "n": len(req.Items)})
}

func (a *App) handleAssoc(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		m, t := associationsStatus()
		writeJSON(w, 200, map[string]any{"magnet": m, "torrent": t, "supported": runtimeIsWindows})
		return
	}
	var req struct {
		Action string `json:"action"` // register | unregister
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	var err error
	switch req.Action {
	case "register":
		err = registerAssociations()
	case "unregister":
		err = unregisterAssociations()
	default:
		writeErr(w, fmt.Errorf("未知操作"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	m, t := associationsStatus()
	writeJSON(w, 200, map[string]any{"ok": true, "magnet": m, "torrent": t})
}

func (a *App) handleReadTorrent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	p := filepath.Clean(req.Path)
	b, err := os.ReadFile(p)
	if err != nil {
		writeErr(w, fmt.Errorf("无法读取种子文件: %v", err))
		return
	}
	if len(b) > 32<<20 {
		writeErr(w, fmt.Errorf("种子文件过大"))
		return
	}
	writeJSON(w, 200, map[string]any{"name": filepath.Base(p), "b64": base64.StdEncoding.EncodeToString(b)})
}

// handleFirewall：GET 查询规则状态；POST {action: add|remove, public} 弹 UAC 写规则。
// POST 会等用户在 UAC 里确认（最长约 2 分钟），界面侧用长超时异步等待。
func (a *App) handleFirewall(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	btPort := a.cfg.BTPort
	a.mu.Unlock()
	if r.Method == http.MethodGet {
		exists, pathOK := firewallStatus()
		writeJSON(w, 200, map[string]any{"supported": firewallSupported, "exists": exists, "pathOK": pathOK, "btPort": btPort})
		return
	}
	var req struct {
		Action string `json:"action"` // add | remove
		Public bool   `json:"public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Action != "add" && req.Action != "remove" {
		writeErr(w, fmt.Errorf("未知操作"))
		return
	}
	aria2 := ""
	if a.aria != nil && a.aria.cmd != nil {
		aria2 = a.aria.cmd.Path
	}
	if err := requestFirewallRules(req.Action, req.Public, aria2); err != nil {
		log.Println("防火墙规则:", err)
		writeErr(w, err)
		return
	}
	exists, pathOK := firewallStatus()
	log.Printf("防火墙规则 %s 完成（public=%t，exists=%t）", req.Action, req.Public, exists)
	writeJSON(w, 200, map[string]any{"ok": true, "exists": exists, "pathOK": pathOK, "btPort": btPort})
}

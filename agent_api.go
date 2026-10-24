package main

// 面向自动化 / 编程助手的稳定本地 API（v1）。界面内部用的 /api/tasks 等格式可能随界面变化，
// 脚本与 agent 请使用这里的 /api/v1/*，字段保持向后兼容。详见 docs/AGENT_API.md。
//
// 鉴权：Host 必须是 127.0.0.1/localhost（防 DNS 重绑定），并带
//   X-Token: <%APPDATA%\HornetDownloader\api-token>   或   Authorization: Bearer <同上>
// 浏览器跨站请求无法携带自定义头（预检会被 405 拒绝），因此网页无法驱动本接口。

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AppVersion 是发布版本号（与 README / 发布 zip 一致）。
const AppVersion = "1.5.3"

const (
	apiTokenFile = "api-token"
	apiPortFile  = "api-port"
)

// loadOrCreateAPIToken 读取（或首次生成）持久的本地 API 令牌。
func loadOrCreateAPIToken(dataDir string) string {
	p := filepath.Join(dataDir, apiTokenFile)
	if b, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			return t
		}
	}
	t := randHex(24)
	_ = os.WriteFile(p, []byte(t), 0600)
	return t
}

// tokenOK：界面令牌（每次启动随机）或持久 API 令牌均可。
func (a *App) tokenOK(r *http.Request) bool {
	tok := r.Header.Get("X-Token")
	if tok == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
	}
	if tok == "" {
		return false
	}
	eq := func(s string) bool { return s != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s)) == 1 }
	return eq(a.uiToken) || eq(a.apiToken)
}

// agentTask 是 v1 API 的任务视图。
type agentTask struct {
	ID         string   `json:"id"`
	Engine     string   `json:"engine"`
	Status     string   `json:"status"` // waiting | active | paused | complete | error | removed
	Done       bool     `json:"done"`   // 终态：complete / error / removed
	OK         bool     `json:"ok"`     // status == complete
	Name       string   `json:"name,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Files      []string `json:"files"`
	TotalBytes int64    `json:"totalBytes"`
	DoneBytes  int64    `json:"doneBytes"`
	Progress   float64  `json:"progress"` // 0..1
	SpeedBps   int64    `json:"speedBps"`
	Peers      int64    `json:"peers"`   // 连接数（BT 为对等节点数）
	Seeders    int64    `json:"seeders"` // BT 做种者
	Error      string   `json:"error,omitempty"`
	FollowedBy string   `json:"followedBy,omitempty"` // aria2 磁力：元数据任务完成后真正的下载任务 ID
	Source     string   `json:"source,omitempty"`
	AddedBy    string   `json:"addedBy,omitempty"` // agent | user
}

func toAgentTask(t map[string]any) agentTask {
	s := func(k string) string {
		if v, ok := t[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	at := agentTask{
		ID: s("gid"), Engine: s("engine"), Status: s("status"), Dir: s("dir"),
		TotalBytes: parseInt64(t["totalLength"]), DoneBytes: parseInt64(t["completedLength"]),
		SpeedBps: parseInt64(t["downloadSpeed"]), Error: s("errorMessage"), Files: []string{},
		AddedBy: s("addedBy"), Peers: parseInt64(t["connections"]), Seeders: parseInt64(t["numSeeders"]),
	}
	if at.Status != "active" {
		at.SpeedBps = 0 // aria2 暂停后的一两秒里仍报告上次的速度
	}
	if at.Engine == "" {
		at.Engine = EngineAria2
	}
	if at.Status == "" {
		at.Status = "waiting"
	}
	if at.TotalBytes > 0 {
		at.Progress = float64(at.DoneBytes) / float64(at.TotalBytes)
	}
	if fb, ok := t["followedBy"].([]any); ok && len(fb) > 0 {
		at.FollowedBy = fmt.Sprint(fb[len(fb)-1])
	}
	switch files := t["files"].(type) {
	case []any:
		for _, f := range files {
			if m, ok := f.(map[string]any); ok {
				at.addFile(m)
			}
		}
	case []map[string]any:
		for _, m := range files {
			at.addFile(m)
		}
	}
	if bt, ok := t["bittorrent"].(map[string]any); ok {
		if info, ok := bt["info"].(map[string]any); ok {
			at.Name = fmt.Sprint(info["name"])
		}
	}
	if at.Name == "" && len(at.Files) > 0 {
		at.Name = filepath.Base(at.Files[0])
	}
	at.Done = at.Status == "complete" || at.Status == "error" || at.Status == "removed"
	at.OK = at.Status == "complete"
	return at
}

func (at *agentTask) addFile(m map[string]any) {
	if sel, ok := m["selected"]; ok && fmt.Sprint(sel) == "false" {
		return
	}
	if at.Source == "" {
		switch uris := m["uris"].(type) {
		case []any:
			if len(uris) > 0 {
				if u, ok := uris[0].(map[string]any); ok {
					at.Source = fmt.Sprint(u["uri"])
				}
			}
		case []map[string]any:
			if len(uris) > 0 {
				at.Source = fmt.Sprint(uris[0]["uri"])
			}
		}
	}
	p := strings.TrimSpace(fmt.Sprint(m["path"]))
	if p == "" || p == "<nil>" {
		return
	}
	at.Files = append(at.Files, filepath.FromSlash(p))
}

// cachedTasks 返回轮询缓存里的全部任务（active + waiting + stopped）。
func (a *App) cachedTasks() []map[string]any {
	a.res.tcMu.Lock()
	res := a.res.tcResult
	a.res.tcMu.Unlock()
	parts, ok := res.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for i := 0; i < 3 && i < len(parts); i++ {
		if list, ok := parts[i].([]map[string]any); ok {
			out = append(out, list...)
		}
	}
	return out
}

func (a *App) agentTaskByID(id string) (agentTask, bool) {
	id = a.resolveAlias(id) // 403 换 UA 重试后任务 ID 会变
	for _, t := range a.cachedTasks() {
		if fmt.Sprint(t["gid"]) != id {
			continue
		}
		at := toAgentTask(t)
		// aria2 列表缓存只带首个文件；单任务查询时补全文件路径（慢调用池，不影响界面）。
		if at.Engine == EngineAria2 && a.aria != nil {
			if raw, err := a.aria.CallRawTimeout("aria2.getFiles", []any{id}, 10*time.Second); err == nil {
				var files []map[string]any
				if json.Unmarshal(raw, &files) == nil && len(files) > 0 {
					at.Files = []string{}
					for _, f := range files {
						at.addFile(f)
					}
				}
			}
		}
		return at, true
	}
	return agentTask{}, false
}

// GET /api/v1/ping：探活（不计入界面心跳）。
func (a *App) handleV1Ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "app": appName, "version": AppVersion, "pid": os.Getpid()})
}

// GET /api/v1/tasks
func (a *App) handleV1Tasks(w http.ResponseWriter, r *http.Request) {
	list := []agentTask{}
	for _, t := range a.cachedTasks() {
		list = append(list, toAgentTask(t))
	}
	writeJSON(w, 200, map[string]any{"tasks": list})
}

// GET /api/v1/task?id=<id>
func (a *App) handleV1Task(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, 400, map[string]any{"error": "missing id"})
		return
	}
	at, ok := a.agentTaskByID(id)
	if !ok {
		a.kickTasks()
		writeJSON(w, 404, map[string]any{"error": "task not found (it may appear within ~1s after add)", "id": id})
		return
	}
	writeJSON(w, 200, at)
}

// POST /api/v1/add {url | torrentB64+name, dir, engine, selectFiles}
func (a *App) handleV1Add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL         string `json:"url"`
		TorrentB64  string `json:"torrentB64"`
		Name        string `json:"name"`
		Dir         string `json:"dir"`
		Engine      string `json:"engine"`
		SelectFiles bool   `json:"selectFiles"` // true = 和界面一样先暂停等待选文件；默认 false 下载全部
		Force       bool   `json:"force"`       // true = 即使同一链接已在同一目录下载过也新建任务
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad json: " + err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" && req.TorrentB64 == "" {
		writeJSON(w, 400, map[string]any{"error": "url or torrentB64 required"})
		return
	}
	eng := strings.ToLower(strings.TrimSpace(req.Engine))
	if eng == "" {
		eng = EngineAuto
	}
	switch eng {
	case EngineAuto, EngineAria2, EngineBT, EngineYTDLP:
	default:
		writeJSON(w, 400, map[string]any{"error": "engine must be auto|aria2|bt|ytdlp"})
		return
	}
	a.mu.Lock()
	pref := a.cfg.DefaultEngine
	dir := a.cfg.DownloadDir
	a.mu.Unlock()
	if d := strings.TrimSpace(req.Dir); d != "" {
		if !filepath.IsAbs(d) {
			writeJSON(w, 400, map[string]any{"error": "dir must be an absolute path"})
			return
		}
		dir = filepath.Clean(d)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeJSON(w, 400, map[string]any{"error": "cannot create dir: " + err.Error()})
		return
	}
	// 幂等：同一链接 + 同一目录已有未失败的任务（完成的需文件仍在）时直接返回它
	if req.URL != "" && !req.Force {
		if at, ok := a.findExistingByURL(req.URL, dir); ok {
			writeJSON(w, 200, map[string]any{"id": at.ID, "engine": at.Engine, "existing": true, "dir": dir, "status": at.Status})
			return
		}
	}
	var o addOutcome
	var err error
	if req.URL != "" {
		o, err = routeAdd(eng, req.URL, pref, func(e string) (string, error) {
			return a.addOneURL(e, req.URL, dir, req.SelectFiles)
		})
	} else {
		name := req.Name
		if name == "" {
			name = "task.torrent"
		}
		ref := name
		if !strings.HasSuffix(strings.ToLower(ref), ".torrent") {
			ref += ".torrent"
		}
		o, err = routeAdd(eng, ref, pref, func(e string) (string, error) {
			return a.addOneTorrent(e, req.TorrentB64, name, dir, req.SelectFiles)
		})
	}
	if err != nil {
		writeJSON(w, 422, map[string]any{"error": friendlyAriaErr(err), "engine": o.Engine, "recommended": o.Recommended})
		return
	}
	a.noteCreated(o.GID)
	a.markAgentTask(o.GID)
	a.kickTasks()
	writeJSON(w, 200, map[string]any{
		"id": o.GID, "engine": o.Engine, "recommended": o.Recommended,
		"existing": o.Existing, "fallback": o.Fallback, "dir": dir,
	})
}

func (a *App) findExistingByURL(url, dir string) (agentTask, bool) {
	for _, t := range a.cachedTasks() {
		at := toAgentTask(t)
		if at.Source != url || at.Status == "error" || at.Status == "removed" {
			continue
		}
		if !strings.EqualFold(filepath.Clean(at.Dir), filepath.Clean(dir)) {
			continue
		}
		if at.Status == "complete" {
			if len(at.Files) == 0 {
				continue
			}
			if _, err := os.Stat(at.Files[0]); err != nil {
				continue
			}
		}
		return at, true
	}
	return agentTask{}, false
}

// POST /api/v1/remove {id, deleteFiles}  /api/v1/pause {id}  /api/v1/resume {id}
func (a *App) handleV1Control(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		DeleteFiles bool   `json:"deleteFiles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeJSON(w, 400, map[string]any{"error": "id required"})
		return
	}
	req.ID = a.resolveAlias(strings.TrimSpace(req.ID))
	if _, ok := a.agentTaskByID(req.ID); !ok {
		writeJSON(w, 404, map[string]any{"error": "task not found", "id": req.ID})
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/v1/remove":
		a.dropFiles(req.ID)
		err = a.deleteRouted(req.ID, req.DeleteFiles)
		if a.aria != nil {
			_ = a.aria.Call("aria2.saveSession", nil, nil)
		}
	case "/api/v1/pause":
		err = a.pauseOne(req.ID)
	case "/api/v1/resume":
		err = a.unpauseOne(req.ID)
	}
	a.kickTasks()
	if err != nil {
		writeJSON(w, 422, map[string]any{"error": err.Error(), "id": req.ID})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": req.ID})
}

// writeAPIPort 记录实际端口，CLI 据此找到正在运行的实例（16801 被占用时会换端口）。
func writeAPIPort(dataDir string, port int) {
	_ = os.WriteFile(filepath.Join(dataDir, apiPortFile), []byte(strconv.Itoa(port)), 0600)
}

// ---------- 任务来源（agent / 用户） ----------

const taskSourceFile = "task-source.json"

// markAgentTask 记录由自动化接口添加的任务，界面可按来源筛选。
func (a *App) markAgentTask(gid string) {
	if gid == "" {
		return
	}
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	a.loadTaskSourceLocked()
	a.agentGIDs[gid] = time.Now().Unix()
	if len(a.agentGIDs) > 2000 { // 只保留最近的
		var oldest string
		var ot int64 = 1 << 62
		for g, ts := range a.agentGIDs {
			if ts < ot {
				oldest, ot = g, ts
			}
		}
		delete(a.agentGIDs, oldest)
	}
	a.saveTaskSourceLocked()
}

func (a *App) saveTaskSourceLocked() {
	if a.dataDir != "" {
		b, _ := json.Marshal(a.agentGIDs)
		tmp := filepath.Join(a.dataDir, taskSourceFile+".tmp")
		if os.WriteFile(tmp, b, 0600) == nil {
			_ = os.Rename(tmp, filepath.Join(a.dataDir, taskSourceFile))
		}
	}
}

func (a *App) loadTaskSourceLocked() {
	if a.agentGIDs != nil {
		return
	}
	a.agentGIDs = map[string]int64{}
	if a.dataDir != "" {
		if b, err := os.ReadFile(filepath.Join(a.dataDir, taskSourceFile)); err == nil {
			_ = json.Unmarshal(b, &a.agentGIDs)
		}
	}
}

// annotateSources 给任务打上 addedBy = "agent" | "user"（aria2 磁力的后续任务继承来源）。
func (a *App) annotateSources(lists ...[]map[string]any) {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	a.loadTaskSourceLocked()
	for _, l := range lists {
		for _, t := range l {
			g := fmt.Sprint(t["gid"])
			by := "user"
			if _, ok := a.agentGIDs[g]; ok {
				by = "agent"
			} else if f, ok := t["following"]; ok && f != nil {
				if _, ok := a.agentGIDs[fmt.Sprint(f)]; ok {
					by = "agent"
				}
			}
			t["addedBy"] = by
		}
	}
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// 多引擎协调：aria2（HTTP/备用 BT）+ BitTorrent(anacrolix) + yt-dlp。

func (a *App) trackerSlice() []string {
	s := a.trackers()
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (a *App) engineForGID(gid string) string {
	if a.bt != nil && a.bt.Owns(gid) && a.bt.Has(gid) {
		return EngineBT
	}
	if a.yt != nil && a.yt.Owns(gid) && a.yt.Has(gid) {
		return EngineYTDLP
	}
	if strings.HasPrefix(gid, "bt-") {
		return EngineBT
	}
	if strings.HasPrefix(gid, "yt-") {
		return EngineYTDLP
	}
	return EngineAria2
}

type addReqTorrent struct {
	Name string `json:"name"`
	B64  string `json:"b64"`
}

func (a *App) handleRecommend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		Engine string `json:"engine"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	eng, hint := RecommendEngine(req.URL)
	a.mu.Lock()
	pref := a.cfg.DefaultEngine
	a.mu.Unlock()
	sel := req.Engine
	if sel == "" {
		sel = EngineAuto
	}
	resolved, rerr := resolveEngine(sel, req.URL, pref)
	out := map[string]any{
		"recommended":   eng,
		"hint":          hint,
		"resolved":      resolved,
		"resolvedLabel": engineLabel(resolved),
		"label":         engineLabel(eng),
		"defaultEngine": pref,
	}
	if rerr != nil {
		out["warning"] = rerr.Error()
	}
	writeJSON(w, 200, out)
}

// addOutcome 是一次添加的结果（便于测试）。
type addOutcome struct {
	GID         string `json:"gid"`
	Engine      string `json:"engine"`
	EngineLabel string `json:"engineLabel"`
	Requested   string `json:"requested"`
	Recommended string `json:"recommended"`
	Hint        string `json:"hint"`
	Fallback    string `json:"fallback,omitempty"` // 非空：自动模式下首选引擎失败，已改用 aria2 的原因
	Existing    bool   `json:"existing,omitempty"` // 相同磁力/种子已在列表，返回已有任务
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
}

// routeAdd：所有入口（新建对话框、剪贴板、关联、命令行、拖放、种子上传）的唯一路由点。
// add 实际执行添加（eng 已确定）。
//   - 手动选择的引擎失败：直接报错，绝不偷偷换成 aria2。
//   - 自动/默认：BitTorrent 因「重复任务」失败直接报错；其它错误才回退 aria2，并把原因带回界面。
func routeAdd(requested, ref, pref string, add func(eng string) (string, error)) (addOutcome, error) {
	rec, hint := RecommendEngine(ref)
	out := addOutcome{Requested: normEngine(requested), Recommended: rec, Hint: hint}
	eng, err := resolveEngine(requested, ref, pref)
	if err != nil {
		return out, err
	}
	gid, err := add(eng)
	if err != nil {
		// 同一 infohash 已在列表：返回已有任务，绝不偷偷改用 aria2（这是「总是 aria2」的根因之一）
		var dup *btDuplicateError
		if errors.As(err, &dup) && dup.GID != "" {
			out.GID, out.Engine, out.EngineLabel = dup.GID, eng, engineLabel(eng)
			out.Existing = true
			return out, nil
		}
		manual := out.Requested != EngineAuto && out.Requested != EngineDefault
		if manual || eng == EngineAria2 || !engineSupports(EngineAria2, ref) {
			return out, err
		}
		log.Printf("%s 引擎添加失败，自动模式回退 aria2: %v", engineLabel(eng), err)
		reason := engineLabel(eng) + " 失败（" + err.Error() + "），已改用 aria2"
		gid2, err2 := add(EngineAria2)
		if err2 != nil {
			return out, err
		}
		out.Fallback = reason
		gid, eng = gid2, EngineAria2
	}
	out.GID, out.Engine, out.EngineLabel = gid, eng, engineLabel(eng)
	return out, nil
}

func (a *App) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Engine   string          `json:"engine"`
		Dir      string          `json:"dir"`
		URL      string          `json:"url"` // 单个链接（自动化用；等同 urls:[url]）
		URLs     []string        `json:"urls"`
		Torrents []addReqTorrent `json:"torrents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	a.mu.Lock()
	pref := a.cfg.DefaultEngine
	selectBT := a.cfg.SelectBTFiles
	defDir := a.cfg.DownloadDir
	a.mu.Unlock()
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = defDir
	}
	if u := strings.TrimSpace(req.URL); u != "" {
		req.URLs = append([]string{u}, req.URLs...)
	}
	added := []addOutcome{}
	var errs []string

	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		o, err := routeAdd(req.Engine, u, pref, func(eng string) (string, error) {
			return a.addOneURL(eng, u, dir, selectBT)
		})
		if err != nil {
			errs = append(errs, truncate(u, 60)+"："+friendlyAriaErr(err))
			continue
		}
		o.URL = u
		a.noteCreated(o.GID)
		added = append(added, o)
	}
	for _, t := range req.Torrents {
		t := t
		ref := t.Name
		if !strings.HasSuffix(strings.ToLower(ref), ".torrent") {
			ref += ".torrent"
		}
		o, err := routeAdd(req.Engine, ref, pref, func(eng string) (string, error) {
			return a.addOneTorrent(eng, t.B64, t.Name, dir, selectBT)
		})
		if err != nil {
			errs = append(errs, t.Name+"："+friendlyAriaErr(err))
			continue
		}
		o.Name = t.Name
		a.noteCreated(o.GID)
		added = append(added, o)
	}

	a.kickTasks()
	out := map[string]any{"ok": len(errs) == 0 || len(added) > 0, "added": added}
	if len(added) > 0 {
		out["id"] = added[0].GID
	}
	if len(errs) > 0 {
		out["errors"] = errs
		if len(added) == 0 {
			writeJSON(w, 200, map[string]any{"error": strings.Join(errs, "；"), "added": added, "errors": errs})
			return
		}
	}
	writeJSON(w, 200, out)
}

func isMagnetOrTorrentURL(u string) bool {
	low := strings.ToLower(u)
	return strings.HasPrefix(low, "magnet:") || strings.HasSuffix(low, ".torrent")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (a *App) addOneURL(eng, uri, dir string, selectBT bool) (string, error) {
	if a.addURLHook != nil {
		return a.addURLHook(eng, uri, dir, selectBT)
	}
	switch eng {
	case EngineBT:
		if a.bt == nil {
			return "", fmt.Errorf("BitTorrent 引擎未启动")
		}
		if isMagnet(uri) {
			return a.bt.AddMagnet(uri, dir, selectBT)
		}
		if isHTTPURL(uri) && isTorrentRef(uri) {
			return a.bt.AddTorrentURL(uri, dir, selectBT)
		}
		return "", fmt.Errorf("BitTorrent 引擎仅支持磁力或种子文件")
	case EngineYTDLP:
		if a.yt == nil {
			return "", fmt.Errorf("yt-dlp 引擎未启动")
		}
		return a.yt.AddURL(uri, dir)
	case EngineAria2:
		return a.addAria2URI(uri, dir, selectBT)
	}
	return "", fmt.Errorf("未知下载引擎：%s", eng)
}

func (a *App) addOneTorrent(eng, b64, name, dir string, selectBT bool) (string, error) {
	if a.addTorrentHook != nil {
		return a.addTorrentHook(eng, b64, name, dir, selectBT)
	}
	switch eng {
	case EngineBT:
		if a.bt == nil {
			return "", fmt.Errorf("BitTorrent 引擎未启动")
		}
		return a.bt.AddTorrentB64(b64, name, dir, selectBT)
	case EngineAria2:
		return a.addAria2Torrent(b64, dir, selectBT)
	}
	return "", fmt.Errorf("%s 引擎不能处理种子文件", engineLabel(eng))
}

// noteCreated 记录任务创建时间（任务详情「概览」显示）。
func (a *App) noteCreated(gid string) {
	if gid == "" {
		return
	}
	a.createdMu.Lock()
	if a.created == nil {
		a.created = map[string]time.Time{}
	}
	a.created[gid] = time.Now()
	a.createdMu.Unlock()
}

func (a *App) createdAt(gid string) time.Time {
	a.createdMu.Lock()
	defer a.createdMu.Unlock()
	return a.created[gid]
}

func (a *App) addAria2URI(uri, dir string, selectBT bool) (string, error) {
	opt := map[string]string{}
	if dir != "" {
		opt["dir"] = dir
	}
	var gid string
	err := a.aria.Call("aria2.addUri", []any{[]string{uri}, opt}, &gid)
	return gid, err
}

func (a *App) addAria2Torrent(b64, dir string, selectBT bool) (string, error) {
	opt := map[string]string{}
	if dir != "" {
		opt["dir"] = dir
	}
	if selectBT {
		opt["pause"] = "true"
	}
	var gid string
	err := a.aria.Call("aria2.addTorrent", []any{b64, []string{}, opt}, &gid)
	return gid, err
}

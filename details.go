package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleReselect：任意时刻重选 BT/aria2 多文件任务的下载文件（下载中/暂停/已完成均可）。
// 立即返回 queued；aria2 走后台队列 pause→changeOption→unpause，避免界面卡住。
func (a *App) handleReselect(w http.ResponseWriter, r *http.Request) {
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
	// v1.5.3：以前只看界面「选择文件」对话框留下的缓存；从任务详情（尤其是已完成的任务）
	// 直接重选时缓存不存在，总是报「文件列表尚未读取完成」。现在按需读取（BT 立即返回）。
	fl, ferr := a.readyFileList(req.GID)
	if ferr != nil && !req.Skip {
		writeErr(w, ferr)
		return
	}
	var sel []int
	selectFile := ""
	count := -1
	if req.Skip {
		if fl != nil {
			for _, e := range fl.items {
				sel = append(sel, e.Index)
			}
			count = len(sel)
		}
	} else {
		ex := make(map[int]bool, len(req.Except))
		for _, i := range req.Except {
			ex[i] = true
		}
		for _, e := range fl.items {
			def := e.Selected
			switch req.Base {
			case "all":
				def = true
			case "none":
				def = false
			}
			if def != ex[e.Index] {
				sel = append(sel, e.Index)
			}
		}
		if len(sel) == 0 {
			writeErr(w, fmt.Errorf("至少选择一个文件"))
			return
		}
		count = len(sel)
		if fl != nil && (len(sel) != len(fl.items) || fl.selCount != len(fl.items)) {
			selectFile = compressIndexes(sel)
		} else if fl != nil && len(sel) == len(fl.items) {
			selectFile = compressIndexes(sel) // 全选也显式写入，便于 aria2 恢复被取消的文件
		}
	}
	gid := req.GID
	selCopy := append([]int(nil), sel...)
	sf := selectFile
	ok := a.enqueue(func() {
		t0 := time.Now()
		if a.bt != nil && a.bt.Has(gid) {
			m := map[int]bool{}
			if req.Skip {
				// 全选：传空 map 让 applySelection 走 DownloadAll；这里显式全选更清晰
				name, items, err := a.bt.FileEntries(gid)
				_ = name
				if err != nil {
					a.notify("error", "重选文件失败："+err.Error())
					return
				}
				for _, e := range items {
					m[e.Index] = true
				}
			} else {
				for _, i := range selCopy {
					m[i] = true
				}
			}
			if err := a.bt.SetSelection(gid, m); err != nil {
				a.notify("error", "重选文件失败："+err.Error())
				return
			}
			a.dropFiles(gid)
			log.Printf("重选文件 %s（BitTorrent，%d 个），耗时 %v", gid, len(m), time.Since(t0).Round(time.Millisecond))
			a.notify("info", "已更新文件选择（"+strconv.Itoa(len(m))+" 个）")
			return
		}
		// aria2：暂停 → changeOption select-file → 恢复（已完成任务只改选项）
		var st struct {
			Status string `json:"status"`
		}
		_ = a.aria.Call("aria2.tellStatus", []any{gid, []string{"status"}}, &st)
		wasActive := st.Status == "active" || st.Status == "waiting"
		wasComplete := st.Status == "complete" || st.Status == "error"
		if wasActive {
			_, _ = a.aria.CallRawTimeout("aria2.forcePause", []any{gid}, 30*time.Second)
		}
		if sf != "" {
			if _, err := a.aria.CallRawTimeout("aria2.changeOption", []any{gid, map[string]string{"select-file": sf}}, startBTTimeout); err != nil {
				a.notify("error", "设置下载文件失败："+err.Error())
				if wasActive {
					_, _ = a.aria.CallRawTimeout("aria2.unpause", []any{gid}, startBTTimeout)
				}
				return
			}
		}
		// 原先在下 / 已完成改选：恢复下载；暂停中只改选择保持暂停
		if wasActive || wasComplete {
			_, _ = a.aria.CallRawTimeout("aria2.unpause", []any{gid}, startBTTimeout)
		}
		a.dropFiles(gid)
		what := "全部文件"
		if count >= 0 {
			what = strconv.Itoa(count) + " 个文件"
		}
		log.Printf("重选文件 %s（aria2，%s），耗时 %v", gid, what, time.Since(t0).Round(time.Millisecond))
		a.notify("info", "已更新文件选择（"+what+"）")
	})
	if !ok {
		writeErr(w, fmt.Errorf("操作太多，请稍后再试"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "queued": true, "count": count})
}

// handleDetails：任务详情（概览/文件/Tracker/节点），短超时，失败时降级返回。
func (a *App) handleDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GID string `json:"gid"`
		Tab string `json:"tab"` // overview | files | trackers | peers
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GID == "" {
		writeErr(w, fmt.Errorf("参数错误"))
		return
	}
	tab := strings.ToLower(strings.TrimSpace(req.Tab))
	if tab == "" {
		tab = "overview"
	}
	eng := a.engineForGID(req.GID)
	out := map[string]any{"gid": req.GID, "engine": eng, "engineLabel": engineLabel(eng), "tab": tab}

	switch tab {
	case "overview":
		out["overview"] = a.detailsOverview(req.GID, eng)
	case "files":
		files, err := a.detailsFiles(req.GID, eng)
		if err != nil {
			out["warning"] = err.Error()
			out["files"] = []any{}
		} else {
			out["files"] = files
		}
	case "trackers":
		tr, err := a.detailsTrackers(req.GID, eng)
		if err != nil {
			out["warning"] = err.Error()
			out["trackers"] = []any{}
		} else {
			out["trackers"] = tr
		}
	case "peers":
		peers, err := a.detailsPeers(req.GID, eng)
		if err != nil {
			out["warning"] = err.Error()
			out["peers"] = []any{}
		} else {
			out["peers"] = peers
		}
	default:
		writeErr(w, fmt.Errorf("未知页签"))
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) detailsOverview(gid, eng string) map[string]any {
	ov := map[string]any{"gid": gid, "engine": eng, "engineLabel": engineLabel(eng)}
	if created := a.createdAt(gid); !created.IsZero() {
		ov["createdAt"] = created.UnixMilli()
	}
	switch eng {
	case EngineBT:
		if a.bt == nil {
			return ov
		}
		for _, t := range a.bt.ListSnapshots() {
			if fmt.Sprint(t["gid"]) != gid {
				continue
			}
			ov["name"] = ""
			if bt, ok := t["bittorrent"].(map[string]any); ok {
				if info, ok := bt["info"].(map[string]any); ok {
					ov["name"] = info["name"]
				}
			}
			ov["status"] = t["status"]
			ov["totalLength"] = t["totalLength"]
			ov["completedLength"] = t["completedLength"]
			ov["downloadSpeed"] = t["downloadSpeed"]
			ov["uploadSpeed"] = t["uploadSpeed"]
			ov["connections"] = t["connections"]
			ov["numSeeders"] = t["numSeeders"]
			ov["dir"] = t["dir"]
			ov["infoHash"] = t["infoHash"]
			ov["errorMessage"] = t["errorMessage"]
			ov["btListenPort"] = t["btListenPort"]
			ov["link"] = a.bt.URIOf(gid)
			if at := a.bt.AddedAt(gid); !at.IsZero() {
				ov["createdAt"] = at.UnixMilli()
			}
			return ov
		}
	case EngineYTDLP:
		if a.yt == nil {
			return ov
		}
		for _, t := range a.yt.ListSnapshots() {
			if fmt.Sprint(t["gid"]) != gid {
				continue
			}
			ov["name"] = ""
			if files, ok := t["files"].([]map[string]any); ok && len(files) > 0 {
				ov["name"] = files[0]["path"]
			}
			if title, ok := t["bittorrent"]; ok {
				_ = title
			}
			ov["status"] = t["status"]
			ov["totalLength"] = t["totalLength"]
			ov["completedLength"] = t["completedLength"]
			ov["downloadSpeed"] = t["downloadSpeed"]
			ov["dir"] = t["dir"]
			if files, ok := t["files"].([]any); ok && len(files) > 0 {
				if fm, ok := files[0].(map[string]any); ok {
					if uris, ok := fm["uris"].([]any); ok && len(uris) > 0 {
						if u, ok := uris[0].(map[string]any); ok {
							ov["link"] = u["uri"]
						}
					}
					if ov["name"] == "" || ov["name"] == nil {
						ov["name"] = fm["path"]
					}
				}
			}
			return ov
		}
	default:
		var st map[string]any
		if p, ok := a.parked(gid); ok {
			st = p.snapshot(gid)
			ov["status"] = st["status"]
			ov["totalLength"] = st["totalLength"]
			ov["completedLength"] = "0"
			ov["downloadSpeed"] = "0"
			ov["uploadSpeed"] = "0"
			ov["connections"] = "0"
			ov["dir"] = p.Dir
			ov["errorMessage"] = p.Msg
			ov["name"] = p.Path
			if len(p.URIs) > 0 {
				ov["link"] = p.URIs[0]
			}
			return ov
		}
		raw, err := a.aria.CallRawTimeout("aria2.tellStatus", []any{gid}, 4*time.Second)
		if err != nil {
			ov["warning"] = err.Error()
			return ov
		}
		_ = json.Unmarshal(raw, &st)
		ov["status"] = st["status"]
		ov["totalLength"] = st["totalLength"]
		ov["completedLength"] = st["completedLength"]
		ov["downloadSpeed"] = st["downloadSpeed"]
		ov["uploadSpeed"] = st["uploadSpeed"]
		ov["connections"] = st["connections"]
		ov["numSeeders"] = st["numSeeders"]
		ov["dir"] = st["dir"]
		ov["infoHash"] = st["infoHash"]
		ov["errorMessage"] = st["errorMessage"]
		if bt, ok := st["bittorrent"].(map[string]any); ok {
			if info, ok := bt["info"].(map[string]any); ok {
				ov["name"] = info["name"]
			}
		}
		if files, ok := st["files"].([]any); ok && len(files) > 0 {
			if fm, ok := files[0].(map[string]any); ok {
				if ov["name"] == nil || ov["name"] == "" {
					ov["name"] = fm["path"]
				}
				if uris, ok := fm["uris"].([]any); ok && len(uris) > 0 {
					if u, ok := uris[0].(map[string]any); ok {
						ov["link"] = u["uri"]
					}
				}
			}
		}
	}
	return ov
}

func (a *App) detailsFiles(gid, eng string) ([]map[string]any, error) {
	if eng == EngineBT && a.bt != nil && a.bt.Has(gid) {
		_, items, err := a.bt.FileProgress(gid)
		return items, err
	}
	if eng == EngineYTDLP {
		return nil, fmt.Errorf("yt-dlp 任务无多文件列表")
	}
	raw, err := a.aria.CallRawTimeout("aria2.getFiles", []any{gid}, 6*time.Second)
	if err != nil {
		return nil, err
	}
	var files []struct {
		Index           string `json:"index"`
		Path            string `json:"path"`
		Length          string `json:"length"`
		CompletedLength string `json:"completedLength"`
		Selected        string `json:"selected"`
	}
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		idx, _ := strconv.Atoi(f.Index)
		ln, _ := strconv.ParseInt(f.Length, 10, 64)
		done, _ := strconv.ParseInt(f.CompletedLength, 10, 64)
		out = append(out, map[string]any{
			"i": idx, "p": f.Path, "s": ln, "done": done, "sel": f.Selected == "true",
		})
	}
	return out, nil
}

func (a *App) detailsTrackers(gid, eng string) ([]map[string]any, error) {
	if eng == EngineBT && a.bt != nil && a.bt.Has(gid) {
		return a.bt.TrackerSnapshot(gid)
	}
	if eng == EngineYTDLP {
		return nil, nil
	}
	raw, err := a.aria.CallRawTimeout("aria2.tellStatus", []any{gid, []string{"bittorrent"}}, 4*time.Second)
	if err != nil {
		return nil, err
	}
	var st struct {
		Bittorrent *struct {
			AnnounceList [][]string `json:"announceList"`
		} `json:"bittorrent"`
	}
	_ = json.Unmarshal(raw, &st)
	var out []map[string]any
	if st.Bittorrent != nil {
		seen := map[string]bool{}
		for _, tier := range st.Bittorrent.AnnounceList {
			for _, u := range tier {
				u = strings.TrimSpace(u)
				if u == "" || seen[u] {
					continue
				}
				seen[u] = true
				out = append(out, map[string]any{"url": u, "status": ""})
			}
		}
	}
	return out, nil
}

func (a *App) detailsPeers(gid, eng string) ([]map[string]any, error) {
	if eng == EngineBT && a.bt != nil && a.bt.Has(gid) {
		return a.bt.PeerSnapshot(gid)
	}
	if eng == EngineYTDLP {
		return nil, nil
	}
	// BT via aria2
	peersRaw, err1 := a.aria.CallRawTimeout("aria2.getPeers", []any{gid}, 4*time.Second)
	serversRaw, err2 := a.aria.CallRawTimeout("aria2.getServers", []any{gid}, 4*time.Second)
	urisRaw, err3 := a.aria.CallRawTimeout("aria2.getUris", []any{gid}, 4*time.Second)
	var out []map[string]any
	if err1 == nil {
		var peers []map[string]any
		_ = json.Unmarshal(peersRaw, &peers)
		for _, p := range peers {
			out = append(out, map[string]any{
				"addr":     fmt.Sprintf("%v:%v", p["ip"], p["port"]),
				"client":   p["peerId"],
				"dl":       parseInt64(p["downloadSpeed"]),
				"ul":       parseInt64(p["uploadSpeed"]),
				"progress": 0,
				"kind":     "peer",
			})
		}
	}
	if err2 == nil {
		var servers []struct {
			Index   string `json:"index"`
			Servers []struct {
				URI           string `json:"uri"`
				CurrentURI    string `json:"currentUri"`
				DownloadSpeed string `json:"downloadSpeed"`
			} `json:"servers"`
		}
		_ = json.Unmarshal(serversRaw, &servers)
		for _, s := range servers {
			for _, x := range s.Servers {
				out = append(out, map[string]any{
					"addr": x.CurrentURI, "client": "HTTP",
					"dl": parseInt64(x.DownloadSpeed), "ul": 0, "kind": "server",
				})
			}
		}
	}
	if err3 == nil && len(out) == 0 {
		var uris []map[string]any
		_ = json.Unmarshal(urisRaw, &uris)
		for _, u := range uris {
			out = append(out, map[string]any{"addr": u["uri"], "client": u["status"], "kind": "uri"})
		}
	}
	if err1 != nil && err2 != nil && len(out) == 0 {
		return out, err1
	}
	return out, nil
}

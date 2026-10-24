package main

// 「停放」的失败任务（v1.5.4）：下载到 HTML 网页而判为失败的 aria2 任务。
//
// aria2 开着 --force-save，会把这种任务写进会话，下次启动时按原链接再下一遍网页
// （v1.5.3 只是下完再删）。现在判失败后把它从 aria2 里移走，任务信息存到 task-meta.json
// 的 parked 里；任务列表照常显示为「失败」，用户仍可删除或「重新开始」（重新开始 = 用原链接新建 aria2 任务）。

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

type parkedTask struct {
	URIs  []string `json:"uris"`
	Dir   string   `json:"dir,omitempty"`
	Path  string   `json:"path,omitempty"`
	Total int64    `json:"total,omitempty"`
	Msg   string   `json:"msg,omitempty"`
	At    int64    `json:"at,omitempty"` // unix 秒
}

func parkedFromGuard(st *guardStatus, msg string) parkedTask {
	p := parkedTask{Dir: st.Dir, Msg: msg, At: time.Now().Unix()}
	p.Total, _ = strconv.ParseInt(st.TotalLength, 10, 64)
	seen := map[string]bool{}
	for i, f := range st.Files {
		if i == 0 {
			p.Path = f.Path
		}
		for _, u := range f.Uris {
			if u.URI != "" && !seen[u.URI] {
				seen[u.URI] = true
				p.URIs = append(p.URIs, u.URI)
			}
		}
	}
	return p
}

func (a *App) setParked(gid string, p parkedTask) {
	m := a.metaStore()
	m.mu.Lock()
	if m.d.Parked == nil {
		m.d.Parked = map[string]parkedTask{}
	}
	m.d.Parked[gid] = p
	m.dirty = true
	m.mu.Unlock()
}

func (a *App) parked(gid string) (parkedTask, bool) {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.d.Parked[gid]
	return p, ok
}

func (a *App) unpark(gid string) bool {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.d.Parked[gid]; !ok {
		return false
	}
	delete(m.d.Parked, gid)
	delete(m.d.Errors, gid)
	delete(m.d.ErrAt, gid)
	m.dirty = true
	return true
}

// parkFailedHTTP 记下任务并把它从 aria2 里移走（不再写入会话、重启不再下载）。
func (a *App) parkFailedHTTP(st *guardStatus, msg string) {
	a.setParked(st.GID, parkedFromGuard(st, msg))
	a.saveTaskMeta()
	a.dropFromAria2(st.GID)
}

func (a *App) dropFromAria2(gid string) {
	if a.aria == nil {
		return
	}
	for i := 0; i < 20; i++ {
		if err := a.aria.Call("aria2.removeDownloadResult", []any{gid}, nil); err == nil {
			break
		}
		var s2 ariaStatus
		if err := a.aria.Call("aria2.tellStatus", []any{gid, []string{"status"}}, &s2); err != nil {
			break // 已不在 aria2 里
		}
		if s2.Status == "active" || s2.Status == "waiting" || s2.Status == "paused" {
			_ = a.aria.Call("aria2.forceRemove", []any{gid}, nil)
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = a.aria.Call("aria2.saveSession", nil, nil)
}

func (p parkedTask) snapshot(gid string) map[string]any {
	var uris []any
	for _, u := range p.URIs {
		uris = append(uris, map[string]any{"uri": u, "status": "used"})
	}
	total := strconv.FormatInt(p.Total, 10)
	file := map[string]any{"path": p.Path, "length": total, "completedLength": "0", "selected": "true", "index": "1"}
	if len(uris) > 0 {
		file["uris"] = uris[:1]
	}
	return map[string]any{
		"gid": gid, "status": "error", "errorCode": "html", "errorMessage": p.Msg,
		"totalLength": total, "completedLength": "0", "uploadLength": "0",
		"downloadSpeed": "0", "uploadSpeed": "0", "connections": "0",
		"dir": p.Dir, "files": []any{file},
		"engine": EngineAria2, "engineLabel": engineLabel(EngineAria2),
	}
}

func (a *App) parkedSnapshots() []map[string]any {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.d.Parked))
	for gid, p := range m.d.Parked {
		if _, removed := m.d.Removed[gid]; removed {
			continue
		}
		out = append(out, p.snapshot(gid))
	}
	return out
}

// deleteParked：删除停放的失败任务（网页文件早已删掉；残留的 .aria2 一并清理）。
func (a *App) deleteParked(gid string) bool {
	p, ok := a.parked(gid)
	if !ok {
		return false
	}
	a.unpark(gid)
	a.markRemoved(gid)
	if p.Path != "" {
		_ = os.Remove(p.Path + ".aria2")
	}
	a.saveTaskMeta()
	return true
}

// retryParked：用原链接新建 aria2 任务；旧 ID 作为别名指向新任务（agent 按旧 ID 仍可查询）。
func (a *App) retryParked(gid string) (string, bool, error) {
	p, ok := a.parked(gid)
	if !ok {
		return "", false, nil
	}
	if a.aria == nil {
		return "", true, fmt.Errorf("aria2 不可用")
	}
	if len(p.URIs) == 0 {
		return "", true, fmt.Errorf("找不到原始下载链接，无法重试")
	}
	dir := p.Dir
	if dir == "" {
		a.mu.Lock()
		dir = a.cfg.DownloadDir
		a.mu.Unlock()
	}
	opt := map[string]string{"dir": dir, "continue": "true", "allow-overwrite": "true", "auto-file-renaming": "false"}
	var newGID string
	if err := a.aria.Call("aria2.addUri", []any{p.URIs, opt}, &newGID); err != nil {
		return "", true, err
	}
	a.unpark(gid)
	a.setAlias(gid, newGID)
	a.srcMu.Lock()
	a.loadTaskSourceLocked()
	if ts, ok := a.agentGIDs[gid]; ok {
		a.agentGIDs[newGID] = ts
		a.saveTaskSourceLocked()
	}
	a.srcMu.Unlock()
	a.saveTaskMeta()
	_ = a.aria.Call("aria2.saveSession", nil, nil)
	log.Printf("重新开始失败任务 %s → %s", gid, newGID)
	return newGID, true, nil
}

package main

// task-meta.json：跨重启保存的任务元数据（v1.5.3）。
//   - created：创建时间（以前只在内存里，重启后详情页的「创建时间」消失）
//   - removed：用户删除的 aria2 任务墓碑。aria2 开着 --force-save，删除后若在下次
//     saveSession 之前退出/崩溃，会话文件里还留着它，下次启动「复活」；墓碑让它再被删掉。
//   - errors：程序判定的失败（例如下载到的是 HTML 网页而不是文件），aria2 自己认为是完成。
//   - alias：403/418 换 User-Agent 重试后，旧任务 ID → 新任务 ID（agent 按旧 ID 查询仍能找到）。
//   - uaRetried：已经换过 UA 的任务，不会再重试第二次。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const taskMetaFileName = "task-meta.json"

type taskMetaData struct {
	Created   map[string]int64  `json:"created,omitempty"` // unix 毫秒
	Removed   map[string]int64  `json:"removed,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
	Alias     map[string]string `json:"alias,omitempty"`
	UARetried map[string]int64  `json:"uaRetried,omitempty"`
	ErrAt     map[string]int64  `json:"errAt,omitempty"`
	// v1.5.4：判为失败（HTML）后已从 aria2 移走的任务，见 parked.go
	Parked map[string]parkedTask `json:"parked,omitempty"`
}

type taskMetaStore struct {
	mu    sync.Mutex
	d     taskMetaData
	dirty bool
	path  string
}

func (m *taskMetaStore) init() {
	if m.d.Created == nil {
		m.d.Created = map[string]int64{}
	}
	if m.d.Removed == nil {
		m.d.Removed = map[string]int64{}
	}
	if m.d.Errors == nil {
		m.d.Errors = map[string]string{}
	}
	if m.d.Alias == nil {
		m.d.Alias = map[string]string{}
	}
	if m.d.UARetried == nil {
		m.d.UARetried = map[string]int64{}
	}
	if m.d.ErrAt == nil {
		m.d.ErrAt = map[string]int64{}
	}
	if m.d.Parked == nil {
		m.d.Parked = map[string]parkedTask{}
	}
}

func (a *App) metaStore() *taskMetaStore {
	a.metaOnce.Do(func() {
		a.meta = &taskMetaStore{}
		if a.dataDir != "" {
			a.meta.path = filepath.Join(a.dataDir, taskMetaFileName)
		}
		a.meta.init()
	})
	return a.meta
}

func (a *App) loadTaskMeta() {
	m := a.metaStore()
	if m.path == "" {
		return
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var d taskMetaData
	if json.Unmarshal(b, &d) == nil {
		m.d = d
	}
	m.init()
}

func (a *App) saveTaskMeta() {
	m := a.metaStore()
	m.mu.Lock()
	if !m.dirty || m.path == "" {
		m.mu.Unlock()
		return
	}
	b, err := json.Marshal(m.d)
	m.dirty = false
	m.mu.Unlock()
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, b, 0600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}

// noteCreated 记录任务创建时间（任务详情「概览」显示）；已有记录（例如重复添加返回已有任务）不覆盖。
func (a *App) noteCreated(gid string) {
	a.noteCreatedAt(gid, time.Now())
}

func (a *App) noteCreatedAt(gid string, at time.Time) {
	if gid == "" || at.IsZero() {
		return
	}
	m := a.metaStore()
	m.mu.Lock()
	if _, ok := m.d.Created[gid]; !ok {
		m.d.Created[gid] = at.UnixMilli()
		m.dirty = true
	}
	m.mu.Unlock()
}

func (a *App) createdAt(gid string) time.Time {
	m := a.metaStore()
	m.mu.Lock()
	ms, ok := m.d.Created[gid]
	m.mu.Unlock()
	if ok {
		return time.UnixMilli(ms)
	}
	if a.bt != nil {
		return a.bt.AddedAt(gid)
	}
	return time.Time{}
}

func (a *App) markRemoved(gid string) {
	m := a.metaStore()
	m.mu.Lock()
	m.d.Removed[gid] = time.Now().Unix()
	delete(m.d.Parked, gid)
	delete(m.d.Errors, gid)
	delete(m.d.ErrAt, gid)
	m.dirty = true
	m.mu.Unlock()
}

func (a *App) isRemoved(gid string) bool {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.d.Removed[gid]
	return ok
}

func (a *App) setForcedError(gid, msg string) {
	m := a.metaStore()
	m.mu.Lock()
	m.d.Errors[gid] = msg
	m.d.ErrAt[gid] = time.Now().Unix()
	m.dirty = true
	m.mu.Unlock()
}

func (a *App) clearForcedError(gid string) bool {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.d.Errors[gid]; !ok {
		return false
	}
	delete(m.d.Errors, gid)
	delete(m.d.ErrAt, gid)
	m.dirty = true
	return true
}

func (a *App) forcedError(gid string) (string, bool) {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.d.Errors[gid]
	return s, ok
}

func (a *App) setAlias(oldGID, newGID string) {
	m := a.metaStore()
	m.mu.Lock()
	m.d.Alias[oldGID] = newGID
	if ms, ok := m.d.Created[oldGID]; ok {
		if _, ok2 := m.d.Created[newGID]; !ok2 {
			m.d.Created[newGID] = ms
		}
	}
	m.dirty = true
	m.mu.Unlock()
}

// resolveAlias 跟随「换 UA 重试」留下的旧→新 ID 链；没有别名时原样返回。
func (a *App) resolveAlias(gid string) string {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < 8; i++ {
		n, ok := m.d.Alias[gid]
		if !ok || n == "" {
			break
		}
		gid = n
	}
	return gid
}

func (a *App) uaRetried(gid string) bool {
	m := a.metaStore()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.d.UARetried[gid]
	return ok
}

func (a *App) markUARetried(gid string) {
	m := a.metaStore()
	m.mu.Lock()
	m.d.UARetried[gid] = time.Now().Unix()
	m.dirty = true
	m.mu.Unlock()
}

// pruneTaskMeta 丢弃已经不在任务列表里的记录（live 必须是完整列表）。
// 刚添加、轮询还没看到的任务有 10 分钟宽限；墓碑保留 30 天（aria2 会话里的残留需要它）。
func (a *App) pruneTaskMeta(live map[string]bool) int {
	m := a.metaStore()
	now := time.Now()
	n := 0
	m.mu.Lock()
	for g, ms := range m.d.Created {
		if !live[g] && now.Sub(time.UnixMilli(ms)) > 10*time.Minute {
			delete(m.d.Created, g)
			n++
		}
	}
	for g := range m.d.Errors {
		if !live[g] && now.Sub(time.Unix(m.d.ErrAt[g], 0)) > 10*time.Minute {
			delete(m.d.Errors, g)
			delete(m.d.ErrAt, g)
			n++
		}
	}
	for g, ts := range m.d.UARetried {
		if !live[g] && now.Sub(time.Unix(ts, 0)) > 10*time.Minute {
			delete(m.d.UARetried, g)
			n++
		}
	}
	for old, nw := range m.d.Alias {
		if !live[nw] {
			if _, ok := m.d.Created[nw]; !ok {
				delete(m.d.Alias, old)
				n++
			}
		}
	}
	for g, ts := range m.d.Removed {
		if now.Sub(time.Unix(ts, 0)) > 30*24*time.Hour {
			delete(m.d.Removed, g)
			n++
		}
	}
	if n > 0 {
		m.dirty = true
	}
	m.mu.Unlock()

	// task-source.json（agent 标记）同样清理
	a.srcMu.Lock()
	a.loadTaskSourceLocked()
	ns := 0
	for g, ts := range a.agentGIDs {
		if !live[g] && now.Sub(time.Unix(ts, 0)) > 10*time.Minute {
			delete(a.agentGIDs, g)
			ns++
		}
	}
	if ns > 0 {
		a.saveTaskSourceLocked()
	}
	a.srcMu.Unlock()
	return n + ns
}

func (m *taskMetaStore) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("created=%d removed=%d errors=%d alias=%d", len(m.d.Created), len(m.d.Removed), len(m.d.Errors), len(m.d.Alias))
}

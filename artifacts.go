package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// aria2 会把通过 RPC 上传的种子保存为 <dir>/<sha1(种子文件)>.torrent（用于重启后恢复任务），
// 磁力链接的元数据保存为 <dir>/<infohash>.torrent。任务删除或完成后由这里负责清理，
// 避免下载目录里堆满十六进制命名的 .torrent 文件。
type artifacts struct {
	mu   sync.Mutex
	path string
	m    map[string][]string // gid -> 文件
}

func newArtifacts(dataDir string) *artifacts {
	a := &artifacts{path: filepath.Join(dataDir, "uploads.json"), m: map[string][]string{}}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &a.m)
	}
	return a
}

func (a *artifacts) save() {
	b, _ := json.Marshal(a.m)
	_ = os.WriteFile(a.path, b, 0600)
}

func (a *artifacts) add(gid, file string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[gid] = append(a.m[gid], file)
	a.save()
}

func (a *artifacts) paths(gid string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.m[gid]
	if f == nil {
		return nil
	}
	out := make([]string, len(f))
	copy(out, f)
	return out
}

func (a *artifacts) take(gid string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.m[gid]
	if f != nil {
		delete(a.m, gid)
		a.save()
	}
	return f
}

func uploadedTorrentName(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	h := sha1.Sum(raw)
	return hex.EncodeToString(h[:]) + ".torrent"
}

// cleanupArtifacts 删除某个任务对应的辅助 .torrent 文件。
func (app *App) cleanupArtifacts(st *ariaStatus) {
	files := app.art.take(st.GID)
	if st.InfoHash != "" && st.Dir != "" {
		files = append(files, filepath.Join(st.Dir, st.InfoHash+".torrent"))
	}
	for _, f := range files {
		if err := os.Remove(f); err == nil {
			log.Println("已清理", f)
		}
	}
}

// janitor 定期清理已完成 BT 任务的辅助文件。
func (app *App) janitor() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	done := map[string]bool{}
	lastPrune := time.Now()
	for {
		select {
		case <-app.quit:
			return
		case <-t.C:
		case <-app.guardKick:
			time.Sleep(300 * time.Millisecond)
		}
		app.guardPass()
		if time.Since(lastPrune) > 10*time.Minute {
			lastPrune = time.Now()
			if live, ok := app.liveGIDs(); ok {
				if n := app.pruneTaskMeta(live); n > 0 {
					log.Printf("已清理 %d 条过期的任务元数据/来源记录", n)
				}
			}
		}
		app.saveTaskMeta()
		var list []ariaStatus
		if err := app.aria.Call("aria2.tellStopped", []any{0, 1000, []string{"gid", "status", "dir", "infoHash", "followedBy", "bittorrent"}}, &list); err != nil {
			continue
		}
		for i := range list {
			st := &list[i]
			if st.Status != "complete" || len(st.FollowedBy) > 0 || done[st.GID] {
				continue
			}
			done[st.GID] = true
			app.cleanupArtifacts(st)
			// BT 任务完成（做种结束）后 aria2 会保留控制文件，这里一并清理
			if st.Bittorrent != nil && st.Bittorrent.Info != nil && st.Bittorrent.Info.Name != "" && st.Dir != "" {
				_ = os.Remove(filepath.Join(st.Dir, st.Bittorrent.Info.Name) + ".aria2")
			}
		}
	}
}

// liveGIDs：轮询缓存里的全部任务 ID（含 aria2 磁力的前后任务）。缓存不新鲜或可能被截断时返回 false。
func (app *App) liveGIDs() (map[string]bool, bool) {
	app.res.tcMu.Lock()
	at, errStr, res := app.res.tcAt, app.res.tcErr, app.res.tcResult
	app.res.tcMu.Unlock()
	if errStr != "" || res == nil || time.Since(at) > 10*time.Second {
		return nil, false
	}
	live := map[string]bool{}
	n := 0
	for _, t := range app.cachedTasks() {
		n++
		live[fmt.Sprint(t["gid"])] = true
		if f, ok := t["following"]; ok && f != nil {
			live[fmt.Sprint(f)] = true
		}
		if fb, ok := t["followedBy"].([]any); ok {
			for _, x := range fb {
				live[fmt.Sprint(x)] = true
			}
		}
	}
	if n >= 900 { // tellWaiting/tellStopped 各取 500 条，接近上限时列表可能不完整
		return nil, false
	}
	return live, true
}

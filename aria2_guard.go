package main

// aria2 任务守护（v1.5.3）：
//   - HTTP 403/418：用备用 User-Agent 自动重试一次（部分镜像按 UA 拦截）。
//   - 文件直链却下载到 HTML 网页：判为失败并给出提示，删除这个假文件与 .aria2。
//   - 已删除任务的墓碑：aria2 会话里残留的已删除任务启动后再被删掉。
//   - 已完成 HTTP 任务残留的 .aria2 控制文件（--force-save 会保留）一并清理。

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const htmlTrapMsg = "服务器返回的是网页（HTML），不是文件本身，已删除这个网页文件。视频/媒体页面请改用 yt-dlp 引擎；也可能是镜像要求登录或拦截了下载。"

var uaBlockedRe = regexp.MustCompile(`(?i)status=(403|418)\b`)

func isUABlockedMsg(msg string) bool { return uaBlockedRe.MatchString(msg) }

type guardState struct {
	mu       sync.Mutex
	checked  map[string]bool // 已做过 HTML/控制文件检查的完成任务
	tombDone map[string]bool // 本次运行已清理过的墓碑
	uaBusy   map[string]bool
}

var guards = guardState{checked: map[string]bool{}, tombDone: map[string]bool{}, uaBusy: map[string]bool{}}

func (a *App) kickGuard() {
	if a.guardKick == nil {
		return
	}
	select {
	case a.guardKick <- struct{}{}:
	default:
	}
}

func (a *App) uaFallbackUsable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	fb := strings.TrimSpace(a.cfg.UserAgentFallback)
	return fb != "" && fb != a.cfg.effectiveUserAgent()
}

// applyAria2Overrides 在 fetchTasks 里调用（仅 aria2 任务）：去掉墓碑任务、套用程序判定的失败、
// 把等待换 UA 重试的任务显示为「等待中」（agent 的 hdl wait 不会把它当成最终失败）。
func (a *App) applyAria2Overrides(list []map[string]any) []map[string]any {
	out := list[:0]
	uaOK := false
	uaChecked := false
	for _, t := range list {
		g := fmt.Sprint(t["gid"])
		if a.isRemoved(g) {
			a.kickGuard()
			continue
		}
		if _, ok := a.parked(g); ok {
			// 已停放（列表里由 parkedSnapshots 显示）；aria2 里还在说明旧会话又加载了它，交给 guardPass 移走
			a.kickGuard()
			continue
		}
		if fmt.Sprint(t["status"]) == "complete" && t["bittorrent"] == nil && !guardChecked(g) {
			// 刚完成的 HTTP 任务先就地做 HTML 检查再报告「完成」，否则 hdl get / wait
			// 会在后台检查（最多 10 秒后）判失败之前就以成功退出。
			a.checkCompletedNow(g)
		}
		if msg, ok := a.forcedError(g); ok {
			t["status"] = "error"
			t["errorMessage"] = msg
			t["errorCode"] = "html"
		} else if fmt.Sprint(t["status"]) == "error" && t["bittorrent"] == nil && isUABlockedMsg(fmt.Sprint(t["errorMessage"])) && !a.uaRetried(g) {
			if !uaChecked {
				uaOK, uaChecked = a.uaFallbackUsable(), true
			}
			if uaOK {
				t["status"] = "waiting"
				t["errorMessage"] = "服务器拒绝了当前 User-Agent（HTTP 403/418），正在换备用 User-Agent 重试…"
				a.kickGuard()
			}
		}
		out = append(out, t)
	}
	return out
}

type guardStatus struct {
	GID          string `json:"gid"`
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	ErrorMessage string `json:"errorMessage"`
	TotalLength  string `json:"totalLength"`
	Completed    string `json:"completedLength"`
	Bittorrent   any    `json:"bittorrent"`
	Files        []struct {
		Path string `json:"path"`
		Uris []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"files"`
}

// guardPass 由 janitor 每 10 秒（或被 kickGuard 唤醒）执行一次。
func (a *App) guardPass() {
	if a.aria == nil {
		return
	}
	var list []guardStatus
	keys := []string{"gid", "status", "dir", "errorMessage", "totalLength", "completedLength", "bittorrent", "files"}
	if err := a.aria.Call("aria2.tellStopped", []any{0, 1000, keys}, &list); err != nil {
		return
	}
	var active []guardStatus
	_ = a.aria.Call("aria2.tellActive", []any{[]string{"gid", "status"}}, &active)
	var waiting []guardStatus
	_ = a.aria.Call("aria2.tellWaiting", []any{0, 1000, []string{"gid", "status"}}, &waiting)
	for _, st := range append(active, waiting...) {
		if a.isRemoved(st.GID) {
			a.reapTombstone(st.GID)
		}
	}
	for i := range list {
		st := &list[i]
		if a.isRemoved(st.GID) {
			a.reapTombstone(st.GID)
			continue
		}
		if st.Bittorrent != nil {
			continue
		}
		if _, ok := a.parked(st.GID); ok && st.Status != "complete" {
			// 旧会话又加载了停放的任务（还没下完或出错）：直接移走；下完的走 checkCompletedHTTP 删网页
			a.dropFromAria2(st.GID)
			continue
		}
		switch st.Status {
		case "error":
			if isUABlockedMsg(st.ErrorMessage) && !a.uaRetried(st.GID) && a.uaFallbackUsable() {
				a.retryWithFallbackUA(st)
			}
		case "complete":
			a.checkCompletedHTTP(st)
		}
	}
}

func (a *App) reapTombstone(gid string) {
	guards.mu.Lock()
	done := guards.tombDone[gid]
	guards.tombDone[gid] = true
	guards.mu.Unlock()
	if done {
		return
	}
	_ = a.aria.Call("aria2.forceRemove", []any{gid}, nil)
	for i := 0; i < 20; i++ {
		if err := a.aria.Call("aria2.removeDownloadResult", []any{gid}, nil); err == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = a.aria.Call("aria2.saveSession", nil, nil)
	log.Printf("已再次移除被删除过的任务 %s（来自旧会话）", gid)
}

func (a *App) retryWithFallbackUA(st *guardStatus) {
	guards.mu.Lock()
	if guards.uaBusy[st.GID] {
		guards.mu.Unlock()
		return
	}
	guards.uaBusy[st.GID] = true
	guards.mu.Unlock()
	defer func() {
		guards.mu.Lock()
		delete(guards.uaBusy, st.GID)
		guards.mu.Unlock()
	}()
	seen := map[string]bool{}
	var uris []string
	for _, f := range st.Files {
		for _, u := range f.Uris {
			if s := strings.TrimSpace(u.URI); s != "" && !seen[s] {
				seen[s] = true
				uris = append(uris, s)
			}
		}
	}
	if len(uris) == 0 {
		var raw []struct {
			URI string `json:"uri"`
		}
		_ = a.aria.Call("aria2.getUris", []any{st.GID}, &raw)
		for _, u := range raw {
			if s := strings.TrimSpace(u.URI); s != "" && !seen[s] {
				seen[s] = true
				uris = append(uris, s)
			}
		}
	}
	a.markUARetried(st.GID)
	if len(uris) == 0 {
		return
	}
	a.mu.Lock()
	fb := strings.TrimSpace(a.cfg.UserAgentFallback)
	dir := st.Dir
	if dir == "" {
		dir = a.cfg.DownloadDir
	}
	a.mu.Unlock()
	opt := map[string]string{
		"dir": dir, "user-agent": fb, "continue": "true",
		"allow-overwrite": "true", "auto-file-renaming": "false",
	}
	var newGID string
	if err := a.aria.Call("aria2.addUri", []any{uris, opt}, &newGID); err != nil {
		log.Printf("换 User-Agent 重试 %s 失败: %v", st.GID, err)
		return
	}
	a.markUARetried(newGID)
	a.setAlias(st.GID, newGID)
	a.srcMu.Lock()
	a.loadTaskSourceLocked()
	if ts, ok := a.agentGIDs[st.GID]; ok {
		a.agentGIDs[newGID] = ts
		a.saveTaskSourceLocked()
	}
	a.srcMu.Unlock()
	_ = a.aria.Call("aria2.removeDownloadResult", []any{st.GID}, nil)
	_ = a.aria.Call("aria2.saveSession", nil, nil)
	a.saveTaskMeta()
	log.Printf("HTTP 403/418：%s 已用备用 User-Agent %q 重试，新任务 %s", st.GID, fb, newGID)
	a.notify("info", "服务器拒绝了默认 User-Agent，已自动换用备用 User-Agent 重试")
	a.kickTasks()
}

// fileLikeURL：链接看起来指向一个文件（常见下载后缀），或是 Wikimedia Commons 的文件页。
func fileLikeURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	if isCommonsFilePage(u) {
		return true
	}
	ext := strings.ToLower(path.Ext(u.Path))
	return directFileExts[ext]
}

func looksLikeHTML(head []byte) bool {
	b := bytes.TrimLeft(head, "\xef\xbb\xbf \t\r\n")
	low := bytes.ToLower(b)
	if bytes.HasPrefix(low, []byte("<!doctype html")) || bytes.HasPrefix(low, []byte("<html")) {
		return true
	}
	return strings.HasPrefix(http.DetectContentType(head), "text/html")
}

func guardChecked(gid string) bool {
	guards.mu.Lock()
	defer guards.mu.Unlock()
	return guards.checked[gid]
}

// checkCompletedNow 读取单个已完成任务的文件信息并立即做完成检查（每个任务只做一次）。
func (a *App) checkCompletedNow(gid string) {
	if a.aria == nil {
		return
	}
	var st guardStatus
	keys := []string{"gid", "status", "dir", "errorMessage", "totalLength", "completedLength", "bittorrent", "files"}
	if err := a.aria.Call("aria2.tellStatus", []any{gid, keys}, &st); err != nil || st.Status != "complete" || st.Bittorrent != nil {
		return
	}
	a.checkCompletedHTTP(&st)
}

func (a *App) checkCompletedHTTP(st *guardStatus) {
	guards.mu.Lock()
	if guards.checked[st.GID] {
		guards.mu.Unlock()
		return
	}
	guards.checked[st.GID] = true
	guards.mu.Unlock()
	if len(st.Files) == 0 || st.Files[0].Path == "" {
		return
	}
	p := filepath.Clean(st.Files[0].Path)
	// --force-save 会给已完成的 HTTP 任务保留 .aria2 控制文件，完成后没有用处
	if st.TotalLength != "" && st.TotalLength == st.Completed {
		if err := os.Remove(p + ".aria2"); err == nil {
			log.Println("已清理完成任务残留的控制文件", p+".aria2")
		}
	}
	_, already := a.forcedError(st.GID)
	src := ""
	if len(st.Files[0].Uris) > 0 {
		src = st.Files[0].Uris[0].URI
	}
	if !fileLikeURL(src) {
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	if ext == ".html" || ext == ".htm" {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	fi, _ := f.Stat()
	f.Close()
	if n == 0 || !looksLikeHTML(head[:n]) {
		return
	}
	if already {
		// 已判过失败的任务：aria2 开着 --force-save，重启后会按旧会话把这个网页再下一遍，
		// 这里再删掉，不让假文件留在下载目录里。
		if fi != nil && fi.Size() < 16<<20 {
			_ = os.Remove(p)
		}
		_ = os.Remove(p + ".aria2")
		log.Printf("%s：重新出现的 HTML 假文件已删除（%s）", st.GID, p)
		// v1.5.3 留下的失败任务：这次顺便停放，以后重启不再下载
		msg, _ := a.forcedError(st.GID)
		if _, ok := a.parked(st.GID); !ok {
			a.parkFailedHTTP(st, msg)
		} else {
			a.dropFromAria2(st.GID)
		}
		return
	}
	a.setForcedError(st.GID, htmlTrapMsg)
	if fi != nil && fi.Size() < 16<<20 {
		_ = os.Remove(p)
	}
	_ = os.Remove(p + ".aria2")
	a.parkFailedHTTP(st, htmlTrapMsg)
	log.Printf("%s 下载到的是 HTML 网页而不是文件（%s），已判为失败", st.GID, src)
	a.kickTasks()
}

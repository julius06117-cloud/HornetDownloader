package main

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

var clipURLRe = regexp.MustCompile(`(?i)(?:https?|ftp|sftp)://[^\s<>"']+|magnet:\?[^\s<>"']+`)

func extractDownloadURLs(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	// 整段就是一条链接
	low := strings.ToLower(text)
	if strings.HasPrefix(low, "magnet:?") || strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "ftp://") {
		if !strings.ContainsAny(text, "\r\n\t ") || strings.HasPrefix(low, "magnet:?") {
			return []string{strings.TrimSpace(text)}
		}
	}
	ms := clipURLRe.FindAllString(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range ms {
		m = strings.TrimRight(m, ".,;)]}>\"'")
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// clipSeenSet：剪贴板去重。只对「新出现」的下载链接询问一次；同一链接再次复制、
// 或程序启动时剪贴板里已有的旧内容都不再弹窗。
type clipSeenSet struct {
	mu     sync.Mutex
	seen   map[string]bool
	order  []string
	primed bool
	last   string
}

const clipSeenMax = 500

func (s *clipSeenSet) markLocked(u string) bool {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[u] {
		return false
	}
	s.seen[u] = true
	s.order = append(s.order, u)
	if len(s.order) > clipSeenMax {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// reset：关闭监视后再打开时调用，重新以当时的剪贴板为基线。
func (s *clipSeenSet) reset() {
	s.mu.Lock()
	s.primed = false
	s.last = ""
	s.mu.Unlock()
}

// downloadableURL：只对看起来是下载目标的链接弹窗（磁力、种子、常见文件后缀、视频页），
// 复制普通网页链接不打扰。
func downloadableURL(u string) bool {
	if isMagnet(u) || isTorrentRef(u) || isVideoSiteURL(u) {
		return true
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return false
	}
	if isCommonsFilePage(pu) {
		return true
	}
	return directFileExts[strings.ToLower(path.Ext(pu.Path))]
}

// clipboardCandidates 返回需要询问的新链接；首次调用只记录基线。
func (a *App) clipboardCandidates(text string) []string {
	s := &a.clipSeen
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.primed && text == s.last {
		return nil
	}
	s.last = text
	var cand []string
	for _, u := range extractDownloadURLs(text) {
		if downloadableURL(u) {
			cand = append(cand, u)
		}
	}
	if !s.primed { // 启动（或重新打开监视）时剪贴板里已有的内容只作为基线
		s.primed = true
		for _, u := range cand {
			s.markLocked(u)
		}
		return nil
	}
	var out []string
	for _, u := range cand {
		if s.markLocked(u) {
			out = append(out, u)
		}
	}
	return out
}

// enqueueClipboardText：平台监视循环读到剪贴板文本后调用。
func (a *App) enqueueClipboardText(text string) {
	urls := a.clipboardCandidates(text)
	if len(urls) == 0 {
		return
	}
	known := a.knownTaskURLs()
	now := time.Now().Unix()
	var items []inboxItem
	for _, u := range urls {
		if known[u] {
			continue
		}
		items = append(items, inboxItem{Kind: "url", Value: u, Source: "clipboard", AddedAt: now})
	}
	if len(items) > 0 {
		a.pushInbox(items)
	}
}

func (a *App) clipboardEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.ClipboardWatch
}

// knownTaskURLs：已在任务列表里的链接（不再为它们弹窗）。
func (a *App) knownTaskURLs() map[string]bool {
	out := map[string]bool{}
	for _, t := range a.cachedTasks() {
		at := toAgentTask(t)
		if at.Source != "" {
			out[at.Source] = true
		}
	}
	if a.bt != nil {
		for _, u := range a.bt.URIs() {
			out[u] = true
		}
	}
	return out
}

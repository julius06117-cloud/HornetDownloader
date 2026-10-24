package main

import (
	"regexp"
	"strings"
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

// watchClipboard 平台实现在 clipboard_*.go；公共节流逻辑。
func (a *App) enqueueClipboardURLs(urls []string, last *string) {
	if len(urls) == 0 {
		return
	}
	key := strings.Join(urls, "\n")
	if key == *last {
		return
	}
	*last = key
	now := time.Now().Unix()
	var items []inboxItem
	for _, u := range urls {
		items = append(items, inboxItem{Kind: "url", Value: u, Source: "clipboard", AddedAt: now})
	}
	a.pushInbox(items)
}

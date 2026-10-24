package main

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

const (
	EngineAuto  = "auto"
	EngineAria2 = "aria2"
	EngineBT    = "bt"    // anacrolix/torrent，界面显示为 BitTorrent
	EngineYTDLP = "ytdlp"
)

// 已知视频站点主机名（含常见短链）。匹配时推荐 yt-dlp。
var videoHosts = []string{
	"youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be", "music.youtube.com",
	"bilibili.com", "www.bilibili.com", "m.bilibili.com", "b23.tv", "bili2233.cn",
	"v.qq.com", "www.iqiyi.com", "iqiyi.com", "v.youku.com", "youku.com",
	"www.douyin.com", "douyin.com", "www.tiktok.com", "tiktok.com", "vm.tiktok.com",
	"www.xiaohongshu.com", "xhslink.com", "www.ixigua.com", "ixigua.com",
	"www.acfun.cn", "acfun.cn", "www.twitch.tv", "twitch.tv",
	"vimeo.com", "www.vimeo.com", "www.nicovideo.jp", "nicovideo.jp",
}

func engineLabel(id string) string {
	switch id {
	case EngineAria2:
		return "aria2"
	case EngineBT:
		return "BitTorrent"
	case EngineYTDLP:
		return "yt-dlp"
	case EngineAuto:
		return "自动"
	default:
		return id
	}
}

// RecommendEngine 根据链接给出推荐引擎与中文说明（不强制）。
func RecommendEngine(raw string) (engine, hint string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return EngineAria2, "粘贴链接后会根据类型给出推荐"
	}
	low := strings.ToLower(raw)
	if strings.HasPrefix(low, "magnet:") {
		return EngineBT, "磁力链接推荐 BitTorrent 引擎（大文件列表更稳，纯 Go 内嵌）"
	}
	if strings.HasSuffix(low, ".torrent") || strings.Contains(low, ".torrent?") {
		return EngineBT, "种子文件推荐 BitTorrent 引擎"
	}
	if isVideoSiteURL(raw) {
		return EngineYTDLP, "视频站点页面推荐 yt-dlp（解析真实媒体地址）"
	}
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
		strings.HasPrefix(low, "ftp://") || strings.HasPrefix(low, "sftp://") {
		// 直链常见后缀：走 aria2 多线程
		u, err := url.Parse(raw)
		if err == nil {
			ext := strings.ToLower(path.Ext(u.Path))
			switch ext {
			case ".iso", ".zip", ".7z", ".rar", ".exe", ".msi", ".dmg", ".pkg",
				".apk", ".deb", ".rpm", ".gz", ".xz", ".bz2", ".tar", ".img",
				".mp4", ".mkv", ".avi", ".mov", ".flv", ".webm", ".mp3", ".flac",
				".pdf", ".epub", ".bin", ".wim":
				return EngineAria2, "文件直链推荐 aria2（多线程 + 断点续传）"
			}
		}
		return EngineAria2, "HTTP/FTP 链接默认推荐 aria2"
	}
	return EngineAria2, "将使用 aria2"
}

func isVideoSiteURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	for _, h := range videoHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// EngineDefault 表示「调用方没有指定」：新建对话框以外的入口（剪贴板、关联、命令行、
// 拖放、二次启动转交）使用它，由后端按设置里的默认引擎处理。
const EngineDefault = "default"

func normEngine(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", EngineDefault:
		return EngineDefault
	case "bittorrent", "libtorrent":
		return EngineBT
	case "yt-dlp":
		return EngineYTDLP
	}
	return s
}

func isMagnet(uri string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(uri)), "magnet:")
}

func isTorrentRef(uri string) bool {
	low := strings.ToLower(strings.TrimSpace(uri))
	if u, err := url.Parse(low); err == nil && u.Path != "" && (u.Scheme == "http" || u.Scheme == "https") {
		return strings.HasSuffix(u.Path, ".torrent")
	}
	return strings.HasSuffix(low, ".torrent")
}

func isHTTPURL(uri string) bool {
	low := strings.ToLower(strings.TrimSpace(uri))
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}

// engineSupports：引擎是否能处理这个链接（uri 为种子上传时传 "名称.torrent"）。
func engineSupports(eng, uri string) bool {
	switch eng {
	case EngineAria2:
		return true
	case EngineBT:
		return isMagnet(uri) || isTorrentRef(uri)
	case EngineYTDLP:
		return isHTTPURL(uri) && !isTorrentRef(uri)
	}
	return false
}

// resolveEngine 决定实际使用的引擎。优先级：
//  1. 手动选择 aria2/bt/ytdlp：照办；引擎不支持该链接则报错（不偷偷换引擎）。
//  2. auto：始终按链接推荐，设置里的默认引擎不参与（旧版本存的 "aria2" 不会覆盖自动）。
//  3. default（未指定，来自剪贴板/关联/拖放等入口）：设置的默认引擎若非 auto 且支持该链接则用它，否则按推荐。
func resolveEngine(requested, uri, defaultPref string) (string, error) {
	req := normEngine(requested)
	rec, _ := RecommendEngine(uri)
	switch req {
	case EngineAuto:
		return rec, nil
	case EngineDefault:
		pref := normEngine(defaultPref)
		if pref == EngineAria2 || pref == EngineBT || pref == EngineYTDLP {
			if engineSupports(pref, uri) {
				return pref, nil
			}
		}
		return rec, nil
	case EngineAria2, EngineBT, EngineYTDLP:
		if !engineSupports(req, uri) {
			return "", fmt.Errorf("%s 引擎不支持这个链接（推荐 %s）", engineLabel(req), engineLabel(rec))
		}
		return req, nil
	}
	return "", fmt.Errorf("未知下载引擎：%s", requested)
}

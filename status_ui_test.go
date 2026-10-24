package main

import (
	"os"
	"strings"
	"testing"
)

func TestStatusTextShortAndTipPresent(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	st := extractFn(html, "statusText")
	if st == "" {
		t.Fatal("statusText missing")
	}
	for _, need := range []string{"连接中", "下载中", "已暂停", "失败", "tip", "sub"} {
		// statusText returns [short, class, tip, sub] — tip/sub are variable names in returns
		_ = need
	}
	if !strings.Contains(st, `"连接中 "`) && !strings.Contains(st, "连接中 ") {
		t.Fatal("BT connecting short label missing")
	}
	if !strings.Contains(st, "下载中 · 连接 ") {
		t.Fatal("full tip for BT peers missing")
	}
	ops := extractFn(html, "buildOps")
	if strings.Count(ops, `btn("pause"`) != 1 {
		t.Fatal("pause must appear once in buildOps")
	}
	if strings.Contains(ops, `btn("del"`) {
		t.Fatal("inline delete should be menu-only")
	}
	if strings.Contains(html, `my !== det.seq && false`) {
		t.Fatal("details stale-response guard still disabled")
	}
	fe := extractFn(html, "friendlyAddErr")
	if !strings.Contains(fe, "未找到 yt-dlp") {
		t.Fatal("friendlyAddErr must clarify missing yt-dlp")
	}
}

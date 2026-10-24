package main

import (
	"os"
	"strings"
	"testing"
)

func extractFn(src, name string) string {
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		return ""
	}
	i := strings.Index(src[start:], "{")
	if i < 0 {
		return ""
	}
	i += start
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : j+1]
			}
		}
	}
	return ""
}

func TestUIBuildOpsNoDuplicatePause(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	m := extractFn(html, "buildOps")
	if m == "" {
		t.Fatal("buildOps not found")
	}
	n := strings.Count(m, `btn("pause"`)
	if n != 1 {
		t.Fatalf("buildOps should add pause exactly once, got %d\n%s", n, m)
	}
	if strings.Count(m, `btn("resume"`) < 1 {
		t.Fatal("buildOps missing resume")
	}
	// Old bug: pause appended before AND after the paused branch.
	if strings.Contains(html, `if (t.status === "active" || t.status === "waiting") ops += btn("pause"`) &&
		strings.Contains(html, `else if (t.status === "active" || t.status === "waiting") ops += btn("pause"`) {
		t.Fatal("duplicate pause append pattern still present")
	}
	if !strings.Contains(html, `td.stcell`) {
		t.Fatal("missing stcell style for status wrapping")
	}
	if !strings.Contains(html, `stsub`) {
		t.Fatal("missing stsub for peer subtitle")
	}
	st := extractFn(html, "statusText")
	if !strings.Contains(st, "连接中") && !strings.Contains(st, "下载中") {
		t.Fatal("statusText missing short BT labels")
	}
}

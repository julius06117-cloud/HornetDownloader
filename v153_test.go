package main

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultUserAgentHonest(t *testing.T) {
	ua := defaultUserAgent()
	if ua != "HornetDownloader/"+AppVersion {
		t.Fatalf("default UA = %q", ua)
	}
	if strings.Contains(ua, "Chrome") || strings.Contains(ua, "Mozilla") {
		t.Fatalf("default UA must not impersonate a browser: %q", ua)
	}
	c := defaultConfig()
	if c.effectiveUserAgent() != ua {
		t.Fatalf("empty config UA should fall back to default, got %q", c.effectiveUserAgent())
	}
	c.UserAgent = "  Custom/1.0\r\n--header=evil "
	c.UserAgent = sanitizeUA(c.UserAgent)
	if c.UserAgent != "Custom/1.0--header=evil" || strings.ContainsAny(c.effectiveUserAgent(), "\r\n") {
		t.Fatalf("sanitizeUA left control chars: %q", c.UserAgent)
	}
	found := false
	app := &App{cfg: c, dataDir: t.TempDir()}
	for _, a := range app.ariaArgs() {
		if strings.HasPrefix(a, "--user-agent=") {
			found = true
		}
	}
	if !found {
		t.Fatal("aria2 args missing --user-agent")
	}
}

func TestUABlockedAndHTMLDetection(t *testing.T) {
	for _, m := range []string{"The response status is not successful. status=403", "status=418"} {
		if !isUABlockedMsg(m) {
			t.Errorf("should be UA-blocked: %q", m)
		}
	}
	for _, m := range []string{"status=404", "status=4031", "Timeout.", ""} {
		if isUABlockedMsg(m) {
			t.Errorf("should not be UA-blocked: %q", m)
		}
	}
	if !looksLikeHTML([]byte("\xef\xbb\xbf  <!DOCTYPE html><html>")) || !looksLikeHTML([]byte("<html lang=en>")) {
		t.Error("HTML not detected")
	}
	if looksLikeHTML([]byte("\x89PNG\r\n")) || looksLikeHTML([]byte("PK\x03\x04")) {
		t.Error("binary detected as HTML")
	}
}

func TestCommonsFilePageRouting(t *testing.T) {
	pages := []string{
		"https://commons.wikimedia.org/wiki/File:Big_Buck_Bunny_4K.webm",
		"https://commons.wikimedia.org/w/index.php?title=File:Sintel_movie_4K.webm",
	}
	for _, u := range pages {
		if eng, hint := RecommendEngine(u); eng != EngineYTDLP || hint == "" {
			t.Errorf("%s -> %s (%q), want ytdlp with hint", u, eng, hint)
		}
		if !fileLikeURL(u) {
			t.Errorf("%s should be file-like (HTML guard)", u)
		}
	}
	direct := "https://upload.wikimedia.org/wikipedia/commons/c/c0/Big_Buck_Bunny_4K.webm"
	if eng, _ := RecommendEngine(direct); eng != EngineAria2 {
		t.Errorf("direct upload.wikimedia.org file should stay aria2, got %s", eng)
	}
	if fileLikeURL("https://example.com/download?id=3") {
		t.Error("query-only URL is not file-like")
	}
	if !fileLikeURL("https://mirrors.nju.edu.cn/debian-cd/current/amd64/iso-cd/debian.iso") {
		t.Error(".iso should be file-like")
	}
}

func TestClipboardDedupe(t *testing.T) {
	a := &App{}
	iso := "https://mirrors.nju.edu.cn/debian-cd/current/amd64/iso-cd/debian-13.iso"
	// 启动时剪贴板已有的链接只作为基线
	if got := a.clipboardCandidates(iso); len(got) != 0 {
		t.Fatalf("baseline offered: %v", got)
	}
	if got := a.clipboardCandidates("hello"); len(got) != 0 {
		t.Fatalf("plain text offered: %v", got)
	}
	if got := a.clipboardCandidates(iso); len(got) != 0 {
		t.Fatalf("re-copied baseline URL offered again: %v", got)
	}
	nu := "magnet:?xt=urn:btih:7ACF8FB590B2060DD9C3146EF770169D593433B0"
	if got := a.clipboardCandidates(nu); len(got) != 1 || got[0] != nu {
		t.Fatalf("new magnet not offered: %v", got)
	}
	a.clipboardCandidates("x")
	if got := a.clipboardCandidates(nu); len(got) != 0 {
		t.Fatalf("same magnet offered twice: %v", got)
	}
	if got := a.clipboardCandidates("https://news.example.com/article/123"); len(got) != 0 {
		t.Fatalf("ordinary web page offered: %v", got)
	}
	// 空剪贴板启动：第一次复制的链接也要提示
	b := &App{}
	b.clipboardCandidates("")
	if got := b.clipboardCandidates(iso); len(got) != 1 {
		t.Fatalf("first copy after empty start not offered: %v", got)
	}
}

func TestTaskMetaPersistAndPrune(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	a.noteCreatedAt("g1", time.Now().Add(-time.Hour))
	first := a.createdAt("g1")
	a.noteCreated("g1") // 重复添加不能覆盖创建时间
	if !a.createdAt("g1").Equal(first) {
		t.Fatal("created time overwritten")
	}
	a.markRemoved("dead")
	a.setForcedError("g2", "html")
	a.setAlias("g1", "g9")
	a.saveTaskMeta()

	b := &App{dataDir: dir}
	b.loadTaskMeta()
	if !b.isRemoved("dead") || b.resolveAlias("g1") != "g9" {
		t.Fatal("meta not persisted")
	}
	if msg, ok := b.forcedError("g2"); !ok || msg == "" {
		t.Fatal("forced error not persisted")
	}
	if b.createdAt("g9").IsZero() {
		t.Fatal("alias should carry created time")
	}
	// task-source.json 里的过期 agent 标记被清理
	os.WriteFile(filepath.Join(dir, "task-source.json"), []byte(`{"old":1,"g9":1}`), 0600)
	n := b.pruneTaskMeta(map[string]bool{"g9": true})
	if n == 0 {
		t.Fatal("nothing pruned")
	}
	if !b.createdAt("g1").IsZero() && b.metaStore().d.Created["g1"] != 0 {
		t.Fatal("stale created entry kept")
	}
	if !b.isRemoved("dead") {
		t.Fatal("tombstone must survive prune")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "task-source.json"))
	if strings.Contains(string(raw), `"old"`) {
		t.Fatalf("task-source.json not pruned: %s", raw)
	}
}

func TestBTGIDStableAndFiniteRate(t *testing.T) {
	ih := "7ACF8FB590B2060DD9C3146EF770169D593433B0"
	if g := btGIDFor(ih); g != "bt-7acf8fb590b2060d" {
		t.Fatalf("gid = %s", g)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5, 1e300} {
		if r := finiteRate(v); r != 0 {
			t.Fatalf("finiteRate(%v) = %d", v, r)
		}
	}
	if finiteRate(1234.9) != 1234 {
		t.Fatal("finiteRate normal value")
	}
}

func TestPprofLoopbackOnly(t *testing.T) {
	if pprofAddr("") != "" {
		t.Fatal("pprof must be off by default")
	}
	if pprofAddr("0.0.0.0:6060") != "" || pprofAddr(":6060") != "" {
		t.Fatal("pprof must refuse non-loopback")
	}
	if pprofAddr("127.0.0.1:6061") == "" {
		t.Fatal("loopback pprof refused")
	}
}

// go1.26.0 / go1.26.1 在 Windows 上做重叠 UDP 收发时会写坏 goroutine 栈（golang/go#77975），
// 表现为 BT 下载时程序无声退出（0xc0000005，高 32 位被改成小整数）。禁止用这两个版本发布。
func TestToolchainNotAffectedByWindowsUDPStackBug(t *testing.T) {
	if badWindowsToolchain(runtime.Version()) {
		t.Fatalf("%s 有 Windows UDP 栈损坏缺陷（golang/go#77975），请用 go1.26.2 及以上构建（go.mod 的 toolchain 行）", runtime.Version())
	}
	for v, bad := range map[string]bool{"go1.26.0": true, "go1.26.1": true, "go1.26rc2": true, "go1.26.2": false, "go1.26.10": false, "go1.27.3": false, "go1.25.7": false} {
		if got := badWindowsToolchain(v); got != bad {
			t.Errorf("badWindowsToolchain(%q)=%v want %v", v, got, bad)
		}
	}
}

func TestBTLogFilterDropsHashNoise(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(btLogFilter{slog.NewTextHandler(&buf, nil)})
	l.Warn("finished hashing piece", "piece", 1, "correct", true, "err", errors.New("short write"))
	l.Warn("finished hashing piece", "piece", 2, "correct", false, "err", errors.New("short write"))
	l.Warn("finished hashing piece", "piece", 3, "correct", false)
	l.Warn("something else", "err", errors.New("short write"))
	out := buf.String()
	if strings.Contains(out, "piece=1") || strings.Contains(out, "piece=2") {
		t.Fatalf("hash noise not filtered:\n%s", out)
	}
	if !strings.Contains(out, "piece=3") || !strings.Contains(out, "something else") {
		t.Fatalf("real warnings dropped:\n%s", out)
	}
}

func TestBTLogFilterRateLimitsWebseedErrors(t *testing.T) {
	btWebseedLogAt.Store(0)
	var buf bytes.Buffer
	l := slog.New(btLogFilter{slog.NewTextHandler(&buf, nil)})
	for i := 0; i < 50; i++ {
		l.Warn("webseed request error", "n", i)
	}
	if c := strings.Count(buf.String(), "webseed request error"); c != 1 {
		t.Fatalf("want 1 webseed line, got %d", c)
	}
}

func TestHTMLGuardDeletesAgainAfterRestart(t *testing.T) {
	dir := t.TempDir()
	dl := t.TempDir()
	p := filepath.Join(dl, "File%3AMovie.ogv")
	html := []byte("<!DOCTYPE html><html><body>wiki page</body></html>")
	mk := func(gid string) *guardStatus {
		st := &guardStatus{GID: gid, Status: "complete", TotalLength: "50", Completed: "50"}
		st.Files = append(st.Files, struct {
			Path string `json:"path"`
			Uris []struct {
				URI string `json:"uri"`
			} `json:"uris"`
		}{Path: p})
		st.Files[0].Uris = append(st.Files[0].Uris, struct {
			URI string `json:"uri"`
		}{URI: "https://commons.wikimedia.org/wiki/File:Movie.ogv"})
		return st
	}
	a := &App{dataDir: dir, res: newResilience()}
	a.loadTaskMeta()
	if err := os.WriteFile(p, html, 0o644); err != nil {
		t.Fatal(err)
	}
	a.checkCompletedHTTP(mk("h1"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("html file not deleted on first detection")
	}
	if _, ok := a.forcedError("h1"); !ok {
		t.Fatal("task not marked as error")
	}
	// 重启：新进程、guards.checked 清空，aria2 按旧会话又把网页下了一遍
	guards.mu.Lock()
	delete(guards.checked, "h1")
	guards.mu.Unlock()
	b := &App{dataDir: dir, res: newResilience()}
	b.loadTaskMeta()
	if err := os.WriteFile(p, html, 0o644); err != nil {
		t.Fatal(err)
	}
	b.checkCompletedHTTP(mk("h1"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("re-downloaded html file not deleted after restart")
	}
}

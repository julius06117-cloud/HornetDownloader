package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startAgentAPI 在 127.0.0.1 随机端口上跑真实的 routes()（含 Host / 令牌校验）。
func startAgentAPI(t *testing.T) (*App, string) {
	t.Helper()
	a, _ := newTestApp("auto", nil)
	a.uiToken = "ui-token-0123456789"
	a.apiToken = loadOrCreateAPIToken(t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: a.routes(port)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return a, fmt.Sprintf("http://127.0.0.1:%d", port)
}

func doReq(t *testing.T, method, url, tok string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPITokenPersistent(t *testing.T) {
	d := t.TempDir()
	a, b := loadOrCreateAPIToken(d), loadOrCreateAPIToken(d)
	if a == "" || a != b || len(a) < 32 {
		t.Fatalf("token not persistent: %q %q", a, b)
	}
	if st, err := os.Stat(filepath.Join(d, apiTokenFile)); err != nil || st.Size() == 0 {
		t.Fatalf("token file missing: %v", err)
	}
}

func TestAgentAPIAuthAndAdd(t *testing.T) {
	a, base := startAgentAPI(t)
	if code, _ := doReq(t, "GET", base+"/api/v1/tasks", "", nil); code != 401 {
		t.Fatalf("no token: got %d, want 401", code)
	}
	if code, _ := doReq(t, "GET", base+"/api/v1/tasks", "wrong", nil); code != 401 {
		t.Fatalf("bad token: got %d", code)
	}
	if code, out := doReq(t, "GET", base+"/api/v1/ping", a.apiToken, nil); code != 200 || out["app"] != appName {
		t.Fatalf("ping: %d %v", code, out)
	}
	if code, _ := doReq(t, "OPTIONS", base+"/api/v1/add", a.apiToken, nil); code == 200 {
		t.Fatal("OPTIONS (CORS preflight) must not succeed")
	}
	dir := t.TempDir()
	code, out := doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/file.iso", "dir": dir})
	if code != 200 || out["id"] != "aria2-gid" || out["engine"] != EngineAria2 {
		t.Fatalf("add: %d %v", code, out)
	}
	code, out = doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": testMagnet, "dir": dir})
	if code != 200 || out["engine"] != EngineBT {
		t.Fatalf("magnet add: %d %v", code, out)
	}
	if code, _ := doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/x", "dir": "relative/dir"}); code != 400 {
		t.Fatalf("relative dir: got %d, want 400", code)
	}
	if code, _ := doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/x", "dir": dir, "engine": "nope"}); code != 400 {
		t.Fatalf("bad engine: got %d, want 400", code)
	}
	// 旧接口也接受单个 url 并返回 id
	code, out = doReq(t, "POST", base+"/api/add", a.uiToken, map[string]any{"url": "https://example.com/y.zip", "dir": dir})
	if code != 200 || out["id"] != "aria2-gid" {
		t.Fatalf("/api/add url: %d %v", code, out)
	}
}

func TestAgentAPIRejectsForeignHost(t *testing.T) {
	a, base := startAgentAPI(t)
	req, _ := http.NewRequest("GET", base+"/api/v1/tasks", nil)
	req.Host = "evil.example:80"
	req.Header.Set("X-Token", a.apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign Host: got %d, want 403", resp.StatusCode)
	}
}

func TestAgentAPITaskView(t *testing.T) {
	a, base := startAgentAPI(t)
	a.res.tcResult = []any{
		[]map[string]any{{"gid": "bt-1", "engine": EngineBT, "status": "active", "totalLength": "200", "completedLength": "50",
			"downloadSpeed": "10", "dir": "D:/dl", "bittorrent": map[string]any{"info": map[string]any{"name": "pack"}},
			"files": []map[string]any{{"path": "D:/dl/pack/a.bin", "selected": "true"}, {"path": "D:/dl/pack/b.bin", "selected": "false"}}}},
		[]map[string]any{},
		[]map[string]any{{"gid": "abc123", "status": "error", "errorMessage": "404", "totalLength": "0", "completedLength": "0"}},
		map[string]any{},
	}
	code, out := doReq(t, "GET", base+"/api/v1/task?id=bt-1", a.apiToken, nil)
	if code != 200 || out["status"] != "active" || out["done"] != false || out["name"] != "pack" || out["progress"].(float64) != 0.25 {
		t.Fatalf("bt-1: %d %v", code, out)
	}
	if files := out["files"].([]any); len(files) != 1 || !strings.HasSuffix(filepath.ToSlash(files[0].(string)), "pack/a.bin") {
		t.Fatalf("files should list only selected: %v", files)
	}
	code, out = doReq(t, "GET", base+"/api/v1/task?id=abc123", a.apiToken, nil)
	if code != 200 || out["done"] != true || out["ok"] != false || out["error"] != "404" || out["engine"] != EngineAria2 {
		t.Fatalf("abc123: %d %v", code, out)
	}
	if code, _ := doReq(t, "GET", base+"/api/v1/task?id=missing", a.apiToken, nil); code != 404 {
		t.Fatalf("missing: got %d, want 404", code)
	}
	code, out = doReq(t, "GET", base+"/api/v1/tasks", a.apiToken, nil)
	if code != 200 || len(out["tasks"].([]any)) != 2 {
		t.Fatalf("tasks: %d %v", code, out)
	}
}

func TestAgentAPIIdempotentAddAndSource(t *testing.T) {
	a, base := startAgentAPI(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "a.bin")
	_ = os.WriteFile(f, []byte("x"), 0600)
	a.res.tcResult = []any{[]map[string]any{}, []map[string]any{},
		[]map[string]any{{"gid": "done1", "status": "complete", "dir": dir, "totalLength": "1", "completedLength": "1",
			"files": []any{map[string]any{"path": f, "uris": []any{map[string]any{"uri": "https://example.com/a.bin"}}}}}},
		map[string]any{}}
	code, out := doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/a.bin", "dir": dir})
	if code != 200 || out["id"] != "done1" || out["existing"] != true {
		t.Fatalf("re-get should return existing: %d %v", code, out)
	}
	code, out = doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/a.bin", "dir": dir, "force": true})
	if code != 200 || out["id"] != "aria2-gid" {
		t.Fatalf("force should add new: %d %v", code, out)
	}
	_ = os.Remove(f) // 文件没了：重新下载
	if code, out = doReq(t, "POST", base+"/api/v1/add", a.apiToken, map[string]any{"url": "https://example.com/a.bin", "dir": dir}); out["existing"] == true {
		t.Fatalf("missing file must not count as existing: %d %v", code, out)
	}
	a.annotateSources([]map[string]any{{"gid": "aria2-gid"}})
	lst := []map[string]any{{"gid": "aria2-gid"}, {"gid": "other"}, {"gid": "next", "following": "aria2-gid"}}
	a.annotateSources(lst)
	if lst[0]["addedBy"] != "agent" || lst[1]["addedBy"] != "user" || lst[2]["addedBy"] != "agent" {
		t.Fatalf("addedBy: %v", lst)
	}
	if code, _ := doReq(t, "POST", base+"/api/v1/remove", a.apiToken, map[string]any{"id": "nope"}); code != 404 {
		t.Fatalf("remove unknown: %d", code)
	}
}

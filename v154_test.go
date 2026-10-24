package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// fakeAria2 记录收到的 RPC 方法；addUri 返回固定 gid，其它返回 "OK"。
type fakeAria2 struct {
	mu      sync.Mutex
	methods []string
	params  [][]any
}

func (f *fakeAria2) start(t *testing.T) *Aria2 {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		f.params = append(f.params, req.Params)
		f.mu.Unlock()
		var res any = "OK"
		switch req.Method {
		case "aria2.addUri":
			res = "00000000000000aa"
		case "aria2.tellStatus":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": 1, "message": "not found"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": res})
	}))
	t.Cleanup(srv.Close)
	_, ps, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(ps)
	return &Aria2{port: port, client: newRPCClient(4), slowClient: newRPCClient(2), done: make(chan struct{})}
}

func (f *fakeAria2) called(m string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.methods {
		if x == m {
			n++
		}
	}
	return n
}

func htmlGuardStatus(gid, p, uri string) *guardStatus {
	st := &guardStatus{GID: gid, Status: "complete", TotalLength: "50", Completed: "50", Dir: filepath.Dir(p)}
	st.Files = append(st.Files, struct {
		Path string `json:"path"`
		Uris []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	}{Path: p})
	st.Files[0].Uris = append(st.Files[0].Uris, struct {
		URI string `json:"uri"`
	}{URI: uri})
	return st
}

func resetGuard(gid string) {
	guards.mu.Lock()
	delete(guards.checked, gid)
	guards.mu.Unlock()
}

// v1.5.4 修复 2：下载到网页判失败后，任务从 aria2 移走（重启不再重新下载），
// 但列表里仍显示为失败，可删除、可重新开始。
func TestHTMLFailedTaskParkedAndRemovedFromAria2(t *testing.T) {
	dir, dl := t.TempDir(), t.TempDir()
	p := filepath.Join(dl, "File%3AMovie.ogv")
	_ = os.WriteFile(p, []byte("<!DOCTYPE html><html><body>wiki</body></html>"), 0o644)
	resetGuard("p1")
	fa := &fakeAria2{}
	a := &App{dataDir: dir, res: newResilience(), aria: fa.start(t)}
	a.loadTaskMeta()
	a.checkCompletedHTTP(htmlGuardStatus("p1", p, "https://commons.wikimedia.org/wiki/File:Movie.ogv"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("html file not deleted")
	}
	if fa.called("aria2.removeDownloadResult") == 0 || fa.called("aria2.saveSession") == 0 {
		t.Fatalf("task not removed from aria2 session: %v", fa.methods)
	}
	// 重启后（新进程）任务仍在，状态为失败
	b := &App{dataDir: dir, res: newResilience()}
	b.loadTaskMeta()
	pt, ok := b.parked("p1")
	if !ok || len(pt.URIs) != 1 || pt.Dir != dl {
		t.Fatalf("parked task not persisted: %+v %v", pt, ok)
	}
	snaps := b.parkedSnapshots()
	if len(snaps) != 1 || snaps[0]["gid"] != "p1" || snaps[0]["status"] != "error" {
		t.Fatalf("snapshot: %v", snaps)
	}
	if err := b.pauseOne("p1"); err == nil {
		t.Fatal("pausing a parked task should fail with a hint")
	}
	// 删除
	if err := b.deleteOne("p1", true); err != nil {
		t.Fatal(err)
	}
	c := &App{dataDir: dir, res: newResilience()}
	c.loadTaskMeta()
	if _, ok := c.parked("p1"); ok || len(c.parkedSnapshots()) != 0 {
		t.Fatal("parked task still listed after delete")
	}
}

func TestParkedTaskRetryReaddsToAria2(t *testing.T) {
	dir, dl := t.TempDir(), t.TempDir()
	p := filepath.Join(dl, "page.iso")
	fa := &fakeAria2{}
	a := &App{dataDir: dir, res: newResilience(), aria: fa.start(t)}
	a.loadTaskMeta()
	a.setParked("p2", parkedTask{URIs: []string{"https://example.org/page.iso"}, Dir: dl, Path: p, Msg: "网页"})
	a.setForcedError("p2", "网页")
	if err := a.retryOne("p2"); err != nil {
		t.Fatal(err)
	}
	if fa.called("aria2.addUri") != 1 {
		t.Fatalf("retry did not add uri: %v", fa.methods)
	}
	if _, ok := a.parked("p2"); ok {
		t.Fatal("still parked after retry")
	}
	if _, ok := a.forcedError("p2"); ok {
		t.Fatal("forced error not cleared after retry")
	}
	if got := a.resolveAlias("p2"); got != "00000000000000aa" {
		t.Fatalf("alias = %q", got)
	}
}

// v1.5.3 已判失败（只记在 errors 里）的任务：下次又被 aria2 下完时自动转为停放并移出 aria2。
func TestHTMLFailedLegacyTaskMigratesToParked(t *testing.T) {
	dir, dl := t.TempDir(), t.TempDir()
	p := filepath.Join(dl, "File%3AOld.ogv")
	_ = os.WriteFile(p, []byte("<!DOCTYPE html><html></html>"), 0o644)
	resetGuard("p3")
	fa := &fakeAria2{}
	a := &App{dataDir: dir, res: newResilience(), aria: fa.start(t)}
	a.loadTaskMeta()
	a.setForcedError("p3", htmlTrapMsg)
	a.saveTaskMeta()
	a.checkCompletedHTTP(htmlGuardStatus("p3", p, "https://commons.wikimedia.org/wiki/File:Old.ogv"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("re-downloaded html not deleted")
	}
	if _, ok := a.parked("p3"); !ok {
		t.Fatal("legacy failed task not parked")
	}
	if fa.called("aria2.removeDownloadResult") == 0 {
		t.Fatal("legacy failed task not removed from aria2")
	}
}

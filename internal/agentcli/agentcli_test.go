package agentcli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeApp 模拟 HornetDownloader 的 /api/v1（仅监听 127.0.0.1）。
type fakeApp struct {
	mu     sync.Mutex
	polls  int
	final  Task
	added  map[string]any
	dir    string
	server *http.Server
}

func startFake(t *testing.T, final Task) *fakeApp {
	t.Helper()
	f := &fakeApp{final: final, dir: t.TempDir()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(filepath.Join(f.dir, "api-token"), []byte("tok-0123456789abcdef0123456789abcdef"), 0600)
	_ = os.WriteFile(filepath.Join(f.dir, "api-port"), []byte(strconv.Itoa(port)), 0600)
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != "tok-0123456789abcdef0123456789abcdef" {
				http.Error(w, "unauthorized", 401)
				return
			}
			h(w, r)
		}
	}
	js := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/v1/ping", auth(func(w http.ResponseWriter, r *http.Request) {
		js(w, 200, map[string]any{"ok": true, "app": "HornetDownloader"})
	}))
	mux.HandleFunc("/api/v1/add", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&f.added)
		f.mu.Unlock()
		js(w, 200, map[string]any{"id": "g1", "engine": "aria2"})
	}))
	mux.HandleFunc("/api/v1/task", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Query().Get("id") != "g1" {
			js(w, 404, map[string]any{"error": "task not found"})
			return
		}
		f.polls++
		if f.polls < 2 {
			js(w, 200, Task{ID: "g1", Engine: "aria2", Status: "active", Files: []string{}})
			return
		}
		js(w, 200, f.final)
	}))
	mux.HandleFunc("/api/v1/tasks", auth(func(w http.ResponseWriter, r *http.Request) {
		js(w, 200, map[string]any{"tasks": []Task{f.final}})
	}))
	f.server = &http.Server{Handler: mux}
	go f.server.Serve(ln)
	t.Cleanup(func() { f.server.Close() })
	return f
}

func run(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := Main(args, Options{Stdout: &o, Stderr: &e, AppExe: "does-not-exist.exe"})
	return code, o.String(), e.String()
}

func TestGetPrintsFilesAndExitsZero(t *testing.T) {
	f := startFake(t, Task{ID: "g1", Engine: "aria2", Status: "complete", Done: true, OK: true, Files: []string{"/dl/a.bin"}})
	code, out, errOut := run("get", "https://example.com/a.bin", "--data-dir", f.dir, "--dir", "rel", "--quiet")
	if code != ExitOK || strings.TrimSpace(out) != "/dl/a.bin" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if d, _ := f.added["dir"].(string); !filepath.IsAbs(d) {
		t.Fatalf("--dir must be sent as an absolute path, got %q", d)
	}
}

func TestAddWaitJSONFailure(t *testing.T) {
	f := startFake(t, Task{ID: "g1", Engine: "aria2", Status: "error", Done: true, Error: "404 Not Found", Files: []string{}})
	code, out, _ := run("add", "https://example.com/missing", "--wait", "--json", "--data-dir", f.dir, "--quiet")
	if code != ExitTaskFailed {
		t.Fatalf("code=%d out=%q", code, out)
	}
	var task Task
	if err := json.Unmarshal([]byte(out), &task); err != nil || task.Status != "error" || task.Error == "" {
		t.Fatalf("bad json %q: %v", out, err)
	}
}

func TestAddWithoutWaitPrintsID(t *testing.T) {
	f := startFake(t, Task{})
	code, out, _ := run("add", "magnet:?xt=urn:btih:0000000000000000000000000000000000000000", "--data-dir", f.dir)
	if code != ExitOK || strings.TrimSpace(out) != "g1" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestStatusAndList(t *testing.T) {
	f := startFake(t, Task{ID: "g1", Status: "complete", Done: true, OK: true, Files: []string{"/x"}})
	if code, out, _ := run("status", "nope", "--json", "--data-dir", f.dir); code != ExitTaskFailed || !strings.Contains(out, `"ok":false`) {
		t.Fatalf("status missing: %d %q", code, out)
	}
	code, out, _ := run("list", "--json", "--data-dir", f.dir)
	var l struct{ Tasks []Task }
	if code != ExitOK || json.Unmarshal([]byte(out), &l) != nil || len(l.Tasks) != 1 {
		t.Fatalf("list: %d %q", code, out)
	}
}

func TestExitCodes(t *testing.T) {
	if code, _, _ := run("add"); code != ExitUsage {
		t.Fatalf("missing arg: %d", code)
	}
	if code, _, _ := run("add", "not-a-url", "--data-dir", t.TempDir(), "--no-start"); code != ExitUnavailable {
		t.Fatalf("no app + --no-start: %d", code)
	}
	if code, _, _ := run("list", "--data-dir", t.TempDir()); code != ExitUnavailable {
		t.Fatalf("list without app: %d", code)
	}
	f := startFake(t, Task{})
	if code, _, _ := run("add", "not-a-url", "--data-dir", f.dir); code != ExitUsage {
		t.Fatalf("bad ref: %d", code)
	}
	if code, _, _ := run("frobnicate"); code != ExitUsage {
		t.Fatalf("unknown command: %d", code)
	}
}

func TestHelpJSON(t *testing.T) {
	code, out, _ := run("help", "--json")
	var h map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &h) != nil || h["exitCodes"] == nil {
		t.Fatalf("help --json: %d %q", code, out)
	}
}

func TestWaitTimeout(t *testing.T) {
	f := startFake(t, Task{ID: "g1", Status: "active", Files: []string{}})
	code, out, _ := run("get", "https://example.com/big", "--data-dir", f.dir, "--timeout", "1500ms", "--json", "--quiet")
	if code != ExitTimeout || !strings.Contains(out, "timeout") {
		t.Fatalf("timeout: %d %q", code, out)
	}
}

func TestVersionNotRunning(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := run("version", "--data-dir", dir, "--port", "1")
	if code != ExitOK || !strings.Contains(out, "hdl     "+Version) || !strings.Contains(out, "go      go") ||
		!strings.Contains(out, "commit  ") || !strings.Contains(out, "not running") {
		t.Fatalf("version plain: %d %q", code, out)
	}
	code, out, _ = run("version", "--json", "--data-dir", dir)
	var v VersionInfo
	if code != ExitOK || json.Unmarshal([]byte(out), &v) != nil || v.HDL != Version || v.App.Running ||
		!strings.HasPrefix(v.Build.Go, "go") || v.Build.Commit == "" {
		t.Fatalf("version --json: %d %q", code, out)
	}
	if code, out, _ = run("--version", "--data-dir", dir); code != ExitOK || !strings.Contains(out, Version) {
		t.Fatalf("--version: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "api-port")); !os.IsNotExist(err) {
		t.Fatal("version must not start the app")
	}
}

func TestVersionReportsRunningApp(t *testing.T) {
	f := startFake(t, Task{ID: "g1", Status: "complete", Done: true, OK: true, Files: []string{}})
	code, out, _ := run("version", "--json", "--data-dir", f.dir)
	var v VersionInfo
	if code != ExitOK || json.Unmarshal([]byte(out), &v) != nil || !v.App.Running || v.App.Version == "" || v.App.Port == 0 {
		t.Fatalf("version --json with app: %d %q", code, out)
	}
	code, out, _ = run("version", "--data-dir", f.dir)
	if code != ExitOK || !strings.Contains(out, "(running") {
		t.Fatalf("version plain with app: %d %q", code, out)
	}
}

func TestHelpListsVersion(t *testing.T) {
	_, out, _ := run("help", "--json")
	if !strings.Contains(out, `"name": "version"`) {
		t.Fatal("help --json does not list version")
	}
	_, out, _ = run("help")
	if !strings.Contains(out, "version [--json]") {
		t.Fatal("usage does not list version")
	}
	if !IsCommand("version") {
		t.Fatal("version not routed as a CLI command")
	}
}

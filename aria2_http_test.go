package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 用系统 aria2c 拉一个本地 HTTP 小文件，验证多连接下载链路可用。
func TestAria2HTTPLocalDownload(t *testing.T) {
	aria2, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not in PATH")
	}
	root := t.TempDir()
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i * 3)
	}
	src := filepath.Join(root, "src.bin")
	if err := os.WriteFile(src, payload, 0644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/src.bin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, src)
	})
	go http.Serve(ln, mux)
	url := fmt.Sprintf("http://%s/src.bin", ln.Addr().String())

	dataDir := filepath.Join(root, "aria")
	dlDir := filepath.Join(root, "dl")
	_ = os.MkdirAll(dataDir, 0700)
	_ = os.MkdirAll(dlDir, 0755)

	a, err := StartAria2(aria2, dataDir, []string{
		"--enable-rpc", "--rpc-listen-all=false", "--rpc-allow-origin-all=true",
		"--dir=" + dlDir, "--file-allocation=none",
		"--max-connection-per-server=4", "--split=4",
		"--check-certificate=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(3*time.Second)

	var gid string
	if err := a.Call("aria2.addUri", []any{[]string{url}, map[string]string{
		"dir": dlDir, "out": "got.bin", "split": "4", "max-connection-per-server": "4",
	}}, &gid); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var st map[string]any
		if err := a.Call("aria2.tellStatus", []any{gid, []string{"status", "completedLength", "totalLength"}}, &st); err == nil {
			if fmt.Sprint(st["status"]) == "complete" {
				got, err := os.ReadFile(filepath.Join(dlDir, "got.bin"))
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(payload) {
					t.Fatalf("size %d want %d", len(got), len(payload))
				}
				return
			}
			if fmt.Sprint(st["status"]) == "error" {
				t.Fatalf("aria2 error: %#v", st)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("aria2 HTTP download timed out")
}

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// 删除任务并删除文件：未完成的 .part 文件、程序留下的 .torrent 副本和空目录都要清掉，其它文件不能动。
func TestBTEngineRemoveDeletesPartFiles(t *testing.T) {
	root := t.TempDir()
	seedDir := filepath.Join(root, "seed")
	dlDir := filepath.Join(root, "dl")
	_ = os.MkdirAll(filepath.Join(seedDir, "pack"), 0755)
	_ = os.MkdirAll(dlDir, 0755)
	for _, name := range []string{"a.bin", "b.bin"} {
		if err := os.WriteFile(filepath.Join(seedDir, "pack", name), make([]byte, 32*1024), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var info metainfo.Info
	info.Name = "pack"
	info.PieceLength = 16 * 1024
	if err := info.BuildFromFilePath(filepath.Join(seedDir, "pack")); err != nil {
		t.Fatal(err)
	}
	ib, _ := bencode.Marshal(&info)
	mi := metainfo.MetaInfo{InfoBytes: ib}
	tf := filepath.Join(root, "pack.torrent")
	w, _ := os.Create(tf)
	_ = mi.Write(w)
	w.Close()
	torBuf, _ := os.ReadFile(tf)
	keepUser := filepath.Join(dlDir, "user-file.txt")
	_ = os.WriteFile(keepUser, []byte("keep"), 0644)

	e, err := StartBTEngine(filepath.Join(root, "app"), func() Config {
		return Config{DownloadDir: dlDir, SeedMinutes: 0}
	}, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	gid, err := e.AddTorrentB64(base64.StdEncoding.EncodeToString(torBuf), "pack.torrent", dlDir, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		ok := e.tasks[gid] != nil && e.tasks[gid].metaOK
		e.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// v1.5.4：下载目录里不能再出现 .torrent 副本，原文件存在数据目录 meta\ 下
	if _, err := os.Stat(filepath.Join(dlDir, "pack.torrent")); !os.IsNotExist(err) {
		t.Fatalf("torrent copy left in download dir: %v", err)
	}
	ih := mi.HashInfoBytes().HexString()
	stored := e.metaPath(ih)
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("torrent not stored in data dir: %v", err)
	}
	e.mu.Lock()
	uri := e.tasks[gid].uri
	e.mu.Unlock()
	if uri != "file://"+stored {
		t.Fatalf("task uri = %q, want data-dir copy", uri)
	}
	// 模拟下载到一半：part files
	_ = os.MkdirAll(filepath.Join(dlDir, "pack"), 0755)
	for _, name := range []string{"a.bin.part", "b.bin.part"} {
		_ = os.WriteFile(filepath.Join(dlDir, "pack", name), []byte("partial"), 0644)
	}
	if err := e.Remove(gid, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dlDir, "pack"), filepath.Join(dlDir, "pack.torrent")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after remove --delete-files", p)
		}
	}
	if _, err := os.Stat(keepUser); err != nil {
		t.Errorf("unrelated file was deleted: %v", err)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Errorf("data-dir torrent copy not removed with task")
	}
}

func TestBTPendingDeletes(t *testing.T) {
	data := t.TempDir()
	dl := t.TempDir()
	sub := filepath.Join(dl, "pack")
	_ = os.MkdirAll(sub, 0755)
	f1 := filepath.Join(sub, "a.bin.part")
	_ = os.WriteFile(f1, []byte("x"), 0644)
	addPendingDeletes(data, btPendingDelete{Files: []string{f1}, Dirs: [][2]string{{sub, dl}}})
	if _, err := os.Stat(pendingDeletePath(data)); err != nil {
		t.Fatal("pending list not written")
	}
	// 记录之后又被重新写过的文件不能删
	f2 := filepath.Join(dl, "redownloaded.iso")
	_ = os.WriteFile(f2, []byte("old"), 0644)
	addPendingDeletes(data, btPendingDelete{Files: []string{f2}})
	time.Sleep(20 * time.Millisecond)
	_ = os.WriteFile(f2, []byte("new content"), 0644)
	processPendingDeletes(data)
	if _, err := os.Stat(f2); err != nil {
		t.Fatal("file rewritten after being queued must not be deleted")
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("pending file/dir not removed")
	}
	if _, err := os.Stat(pendingDeletePath(data)); !os.IsNotExist(err) {
		t.Fatal("pending list not cleared")
	}
	if _, err := os.Stat(dl); err != nil {
		t.Fatal("stop dir must survive")
	}
}

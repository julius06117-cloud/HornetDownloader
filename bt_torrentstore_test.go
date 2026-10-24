package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// v1.5.4：v1.5.3 留在下载目录里的种子副本——重启后改用数据目录里的副本，旧副本不动；
// 「删除任务和文件」时旧副本按以前的规则一起删掉。
func TestBTLegacyTorrentCopyMigrates(t *testing.T) {
	root := t.TempDir()
	seedDir := filepath.Join(root, "seed")
	dlDir := filepath.Join(root, "dl")
	appDir := filepath.Join(root, "app")
	_ = os.MkdirAll(filepath.Join(seedDir, "pack"), 0755)
	_ = os.MkdirAll(dlDir, 0755)
	_ = os.MkdirAll(appDir, 0755)
	_ = os.WriteFile(filepath.Join(seedDir, "pack", "a.bin"), make([]byte, 32*1024), 0644)
	var info metainfo.Info
	info.Name = "pack"
	info.PieceLength = 16 * 1024
	if err := info.BuildFromFilePath(filepath.Join(seedDir, "pack")); err != nil {
		t.Fatal(err)
	}
	ib, _ := bencode.Marshal(&info)
	mi := metainfo.MetaInfo{InfoBytes: ib}
	legacy := filepath.Join(dlDir, "pack.torrent")
	w, _ := os.Create(legacy)
	_ = mi.Write(w)
	w.Close()
	ih := strings.ToLower(mi.HashInfoBytes().HexString())
	gid := "bt-" + ih[:16]
	sess := btSessionFile{Tasks: []btSessionItem{{GID: gid, URI: "file://" + legacy, Dir: dlDir, InfoHash: ih, Paused: true,
		AddedAt: time.Now().UnixMilli()}}}
	b, _ := json.Marshal(sess)
	_ = os.WriteFile(filepath.Join(appDir, "bt-session.json"), b, 0600)

	e, err := StartBTEngine(appDir, func() Config { return Config{DownloadDir: dlDir} }, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.mu.Lock()
	task := e.tasks[gid]
	var uri, lc string
	if task != nil {
		uri, lc = task.uri, task.legacyCopy
	}
	e.mu.Unlock()
	if task == nil {
		t.Fatal("legacy task not restored")
	}
	if uri != "file://"+e.metaPath(ih) || lc != legacy {
		t.Fatalf("uri=%q legacy=%q", uri, lc)
	}
	if _, err := os.Stat(e.metaPath(ih)); err != nil {
		t.Fatal("torrent not migrated into data dir")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("legacy copy must not be deleted on migration")
	}
	e.mu.Lock()
	e.saveSessionLocked()
	e.mu.Unlock()
	raw, _ := os.ReadFile(filepath.Join(appDir, "bt-session.json"))
	if !strings.Contains(string(raw), "legacyCopy") {
		t.Fatalf("session does not remember legacy copy: %s", raw)
	}
	if err := e.Remove(gid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy copy not removed with remove --delete-files")
	}
}

// 删除任务但保留文件：旧副本不动。
func TestLegacyTorrentCopyPath(t *testing.T) {
	dl := filepath.Join(t.TempDir(), "dl")
	if got := legacyTorrentCopy("file://"+filepath.Join(dl, "x.torrent"), dl); got != filepath.Join(dl, "x.torrent") {
		t.Fatalf("got %q", got)
	}
	if got := legacyTorrentCopy("file://"+filepath.Join(t.TempDir(), "meta", "x.torrent"), dl); got != "" {
		t.Fatalf("data-dir copy treated as legacy: %q", got)
	}
	if got := legacyTorrentCopy("magnet:?xt=urn:btih:abc", dl); got != "" {
		t.Fatal("magnet treated as legacy")
	}
}

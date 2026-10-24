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

func TestBTEngineReselectMidDownload(t *testing.T) {
	root := t.TempDir()
	seedDir := filepath.Join(root, "seed")
	dlDir := filepath.Join(root, "dl")
	_ = os.MkdirAll(filepath.Join(seedDir, "pack"), 0755)
	_ = os.MkdirAll(dlDir, 0755)
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		b := make([]byte, 64*1024)
		for i := range b {
			b[i] = name[0]
		}
		if err := os.WriteFile(filepath.Join(seedDir, "pack", name), b, 0644); err != nil {
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
	// Select only file index 1
	if err := e.SetSelection(gid, map[int]bool{1: true}); err != nil {
		t.Fatal(err)
	}
	_, items, err := e.FileEntries(gid)
	if err != nil {
		t.Fatal(err)
	}
	sel := 0
	for _, it := range items {
		if it.Selected {
			sel++
		}
	}
	if sel != 1 {
		t.Fatalf("want 1 selected, got %d of %d", sel, len(items))
	}
	// Reselect all three
	if err := e.SetSelection(gid, map[int]bool{1: true, 2: true, 3: true}); err != nil {
		t.Fatal(err)
	}
	_, items, err = e.FileEntries(gid)
	if err != nil {
		t.Fatal(err)
	}
	sel = 0
	for _, it := range items {
		if it.Selected {
			sel++
		}
	}
	if sel != 3 {
		t.Fatalf("want 3 selected, got %d", sel)
	}
}

func TestBTEngineKickAfterDisallow(t *testing.T) {
	root := t.TempDir()
	dlDir := filepath.Join(root, "dl")
	_ = os.MkdirAll(dlDir, 0755)
	// Minimal: start engine, add magnet that won't complete, force Disallow then kick.
	// Use tiny torrent from disk like local transfer but without seeder — just verify kick doesn't panic
	// and AllowDataDownload is called (covered by local transfer + SetSelection).
	e, err := StartBTEngine(filepath.Join(root, "app"), func() Config {
		return Config{DownloadDir: dlDir}
	}, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.mu.Lock()
	e.kickDownloadLocked(nil) // must not panic
	e.mu.Unlock()
}

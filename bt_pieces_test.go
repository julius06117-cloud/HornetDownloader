package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestPartFileOpenDoesNotClearCompleteBits(t *testing.T) {
	dir := t.TempDir()
	store := newBTPieceStore(filepath.Join(dir, "pieces"))
	var ih metainfo.Hash
	ih[0] = 0xab
	pc := store.completion()
	for i := 0; i < 4; i++ {
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, true); err != nil {
			t.Fatal(err)
		}
	}
	dl := filepath.Join(dir, "dl")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	// 只有 .part，最终文件还不存在：anacrolix 会试图把这些块标成未完成。
	if err := os.WriteFile(filepath.Join(dl, "movie.bin.part"), make([]byte, 256*1024), 0644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{
		Name:        "movie.bin",
		Length:      256 * 1024,
		PieceLength: 32 * 1024,
		Pieces:      make([]byte, 20*8),
	}
	impl := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dl,
		UsePartFiles:    g.Some(true),
		PieceCompletion: pc,
	})
	if _, err := impl.OpenTorrent(context.Background(), &info, ih); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		known, complete := store.get(ih.HexString(), i)
		if !known || !complete {
			t.Fatalf("piece %d was cleared when opening part files: known=%v complete=%v", i, known, complete)
		}
	}
}

func TestFreshCompleteBitWaitsForForceSave(t *testing.T) {
	dir := t.TempDir()
	s := newBTPieceStore(dir)
	var ih metainfo.Hash
	ih[0] = 9
	pc := s.completion()
	if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: 0}, true); err != nil {
		t.Fatal(err)
	}
	if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: 1}, false); err != nil {
		t.Fatal(err)
	}
	s.save(false)
	early := newBTPieceStore(dir)
	if known, complete := early.get(ih.HexString(), 0); known || complete {
		t.Fatalf("fresh complete bit persisted too soon: known=%v complete=%v", known, complete)
	}
	if known, complete := early.get(ih.HexString(), 1); !known || complete {
		t.Fatalf("incomplete bit should persist immediately: known=%v complete=%v", known, complete)
	}
	s.save(true)
	later := newBTPieceStore(dir)
	if known, complete := later.get(ih.HexString(), 0); !known || !complete {
		t.Fatalf("force save dropped complete bit: known=%v complete=%v", known, complete)
	}
}

func TestReconcileClearsPiecesWhenFileChanges(t *testing.T) {
	dir := t.TempDir()
	s := newBTPieceStore(dir)
	var ih metainfo.Hash
	ih[0] = 3
	pc := s.completion()
	for i := 0; i < 4; i++ {
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, true); err != nil {
			t.Fatal(err)
		}
	}
	dl := filepath.Join(dir, "dl")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(dl, "movie.bin.part")
	if err := os.WriteFile(part, make([]byte, 128*1024), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	// 大小不变，修改时间故意对不上（有的文件系统在同一秒内重写不会改 mtime）。
	s.setFileStamps(ih.HexString(), []btFileStamp{{
		Rel: "movie.bin", Size: st.Size(), Mtime: 1, Part: true,
	}})
	buf := make([]byte, 128*1024)
	buf[0] = 1
	if err := os.WriteFile(part, buf, 0644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{Name: "movie.bin", Length: 128 * 1024, PieceLength: 32 * 1024, Pieces: make([]byte, 20*4)}
	n := s.reconcile(ih.HexString(), dl, &info)
	if n == 0 {
		t.Fatal("changed file should invalidate saved pieces")
	}
	for i := 0; i < 4; i++ {
		known, complete := s.get(ih.HexString(), i)
		if known || complete {
			t.Fatalf("piece %d still trusted after file change: known=%v complete=%v", i, known, complete)
		}
	}
}

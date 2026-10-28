package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func movieTorrent(t *testing.T, payload []byte) (raw []byte, ih metainfo.Hash, name string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "movie.bin")
	if err := os.WriteFile(src, payload, 0644); err != nil {
		t.Fatal(err)
	}
	var info metainfo.Info
	info.PieceLength = 32 * 1024
	if err := info.BuildFromFilePath(src); err != nil {
		t.Fatal(err)
	}
	ib, err := bencode.Marshal(&info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: ib}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mi.HashInfoBytes(), info.BestName()
}

func startResumeEngine(t *testing.T, app, dl string) *BTEngine {
	t.Helper()
	e, err := StartBTEngine(app, func() Config { return Config{DownloadDir: dl, SeedMinutes: 0} }, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func waitBTReady(t *testing.T, e *BTEngine) *btTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		var task *btTask
		for _, task = range e.tasks {
		}
		ready := task != nil && task.metaOK && task.t != nil
		e.mu.Unlock()
		if ready {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("metadata not ready")
	return nil
}

func TestResumeCheckSnapshotCopy(t *testing.T) {
	e := &BTEngine{}
	task := &btTask{
		gid: "bt-abc", status: "active", checking: true, sawData: true,
		knownDone: 5 << 20, checkGoal: 100, checkLeft: 40,
	}
	m := e.snapshotLocked(task)
	if m["btChecking"] != "true" {
		t.Fatalf("btChecking=%v", m["btChecking"])
	}
	if m["errorMessage"] != btRecheckMessage {
		t.Fatalf("errorMessage=%v", m["errorMessage"])
	}
	if m["btCheckPercent"] != "60" {
		t.Fatalf("btCheckPercent=%v", m["btCheckPercent"])
	}
	if m["btResumeBytes"] != fmt.Sprintf("%d", 5<<20) {
		t.Fatalf("btResumeBytes=%v", m["btResumeBytes"])
	}
	at := toAgentTask(m)
	if !at.Checking || at.ResumeBytes != 5<<20 || at.Status != "active" || at.Done {
		t.Fatalf("agent view = %+v", at)
	}
}

// 有分块完成记录时，退出再打开不应把已完成的块重新哈希（进度保持，而不是从 0 校验上来）。
func TestBTRestartKeepsSavedPieces(t *testing.T) {
	const piece = 32 * 1024
	payload := make([]byte, 8*piece)
	for i := range payload {
		payload[i] = byte(i)
	}
	raw, ih, name := movieTorrent(t, payload)
	root := t.TempDir()
	app := filepath.Join(root, "app")
	dl := filepath.Join(root, "dl")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(dl, name+".part")
	if err := os.WriteFile(part, payload, 0644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}

	e := startResumeEngine(t, app, dl)
	pc := e.pieces.completion()
	for i := 0; i < 8; i++ {
		// 前 4 块已完成，后 4 块已知未完成：若被重新哈希，有效数据会把后 4 块也标成完成。
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, i < 4); err != nil {
			t.Fatal(err)
		}
	}
	e.pieces.setFileStamps(ih.HexString(), []btFileStamp{{
		Rel: name, Size: st.Size(), Mtime: st.ModTime().UnixNano(), Part: true,
	}})
	e.pieces.save(true)

	if _, err := e.AddTorrentB64(base64.StdEncoding.EncodeToString(raw), name+".torrent", dl, false); err != nil {
		t.Fatal(err)
	}
	task := waitBTReady(t, e)
	assertHalfSaved(t, task, 4*piece)
	e.Close()

	e2, err := StartBTEngine(app, func() Config { return Config{DownloadDir: dl, SeedMinutes: 0} }, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	task = waitBTReady(t, e2)
	assertHalfSaved(t, task, 4*piece)
	snaps := e2.ListSnapshots()
	if len(snaps) != 1 {
		t.Fatalf("snapshots=%d", len(snaps))
	}
	if snaps[0]["btChecking"] == "true" {
		t.Fatalf("restored task looks like it is rechecking: %+v", snaps[0])
	}
	if got := parseInt64(snaps[0]["completedLength"]); got != 4*piece {
		t.Fatalf("completedLength=%d, want %d", got, 4*piece)
	}
}

func assertHalfSaved(t *testing.T, task *btTask, want int64) {
	t.Helper()
	time.Sleep(400 * time.Millisecond)
	if got := task.t.BytesCompleted(); got != want {
		t.Fatalf("BytesCompleted=%d, want %d (a full rehash would finish the valid .part)", got, want)
	}
	if n := torrentHashPending(task.t); n != 0 {
		t.Fatalf("hash pending=%d, saved pieces should skip the recheck", n)
	}
	for i := 0; i < 8; i++ {
		ps := task.t.PieceState(i)
		if ps.Checking || ps.QueuedForHash || ps.Hashing {
			t.Fatalf("piece %d still checking: %+v", i, ps)
		}
		if i < 4 && !ps.Complete {
			t.Fatalf("piece %d lost its saved completion", i)
		}
		if i >= 4 && ps.Complete {
			t.Fatalf("piece %d was rehashed to complete", i)
		}
	}
}

// 文件被改过（修改时间变化）时不能沿用完成位，损坏的块必须重新哈希并标成未完成。
func TestBTChangedFileIsRechecked(t *testing.T) {
	const piece = 32 * 1024
	payload := make([]byte, 8*piece)
	for i := range payload {
		payload[i] = byte(i * 3)
	}
	raw, ih, name := movieTorrent(t, payload)
	root := t.TempDir()
	app := filepath.Join(root, "app")
	dl := filepath.Join(root, "dl")
	if err := os.MkdirAll(dl, 0755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(dl, name+".part")
	if err := os.WriteFile(part, payload, 0644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] ^= 0xff
	if err := os.WriteFile(part, payload, 0644); err != nil {
		t.Fatal(err)
	}

	e := startResumeEngine(t, app, dl)
	pc := e.pieces.completion()
	for i := 0; i < 8; i++ {
		if err := pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, true); err != nil {
			t.Fatal(err)
		}
	}
	// 大小与改写前相同，修改时间故意对不上：应判定文件被动过，不能沿用完成位。
	e.pieces.setFileStamps(ih.HexString(), []btFileStamp{{
		Rel: name, Size: st.Size(), Mtime: 1, Part: true,
	}})
	e.pieces.save(true)
	if _, err := e.AddTorrentB64(base64.StdEncoding.EncodeToString(raw), name+".torrent", dl, false); err != nil {
		t.Fatal(err)
	}
	task := waitBTReady(t, e)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && torrentHashPending(task.t) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if torrentHashPending(task.t) > 0 {
		t.Fatal("recheck did not finish")
	}
	ps := task.t.PieceState(0)
	if ps.Complete {
		t.Fatal("corrupted piece stayed complete")
	}
	if got := task.t.BytesCompleted(); got >= int64(len(payload)) {
		t.Fatalf("changed file still counts as fully complete: %d", got)
	}
	// 只坏了第 0 块，其余块哈希通过后应恢复为已完成。
	if got := task.t.BytesCompleted(); got != int64(len(payload)-piece) {
		t.Fatalf("BytesCompleted=%d, want %d", got, len(payload)-piece)
	}
}

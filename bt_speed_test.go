package main

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// 本地做种 → 本引擎下载合成多文件种子，验证有 peer 时能写出字节。
func TestBTEngineLocalTransfer(t *testing.T) {
	root := t.TempDir()
	// The seeder is a plain anacrolix client whose default mmap file IO keeps
	// complete files mapped until the process exits (anacrolix v1.61:
	// "the store never relinquishes its extra ref"). On Windows mapped files
	// cannot be deleted, so seed data lives outside t.TempDir and is removed
	// best-effort. The download side (our engine) must release its files; that
	// is asserted below via dlDir.
	seedDir, err := os.MkdirTemp("", "hdl-seed-*")
	if err != nil {
		t.Fatal(err)
	}
	dlDir := filepath.Join(root, "dl")
	_ = os.MkdirAll(filepath.Join(seedDir, "pack"), 0755)
	_ = os.MkdirAll(dlDir, 0755)
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "pack", "a.bin"), payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "pack", "b.bin"), payload[:128*1024], 0644); err != nil {
		t.Fatal(err)
	}

	var info metainfo.Info
	info.Name = "pack"
	info.PieceLength = 32 * 1024
	if err := info.BuildFromFilePath(filepath.Join(seedDir, "pack")); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(&info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	var torBuf []byte
	{
		f := filepath.Join(root, "pack.torrent")
		w, err := os.Create(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := mi.Write(w); err != nil {
			t.Fatal(err)
		}
		w.Close()
		torBuf, err = os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
	}

	scfg := torrent.NewDefaultClientConfig()
	scfg.ListenPort = 0
	scfg.NoDefaultPortForwarding = true
	// Loopback only: no Windows Firewall prompt, no DHT/IPv6 wildcard sockets.
	scfg.ListenHost = func(string) string { return "127.0.0.1" }
	scfg.NoDHT = true
	scfg.DisableIPv6 = true
	scfg.Seed = true
	scfg.DataDir = seedDir
	// Map-based piece completion: the default bolt DB (.torrent.bolt.db) stays
	// locked on Windows until the storage is closed, which breaks t.TempDir cleanup.
	seedStore := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   seedDir,
		PieceCompletion: storage.NewMapPieceCompletion(),
	})
	scfg.DefaultStorage = seedStore
	seedCl, err := torrent.NewClient(scfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := seedCl.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	<-st.GotInfo()
	_ = st.VerifyData()
	st.DownloadAll()

	addrs := seedCl.ListenAddrs()
	if len(addrs) == 0 {
		t.Fatal("seeder has no listen addr")
	}
	seedAddr := addrs[0].String()
	for _, a := range addrs {
		if ta, ok := a.(*net.TCPAddr); ok {
			host := "127.0.0.1"
			if ta.IP != nil && !ta.IP.IsUnspecified() && ta.IP.To4() != nil {
				host = ta.IP.String()
			}
			seedAddr = net.JoinHostPort(host, itoa(ta.Port))
			break
		}
	}
	// 把 0.0.0.0 / :: 换成回环，否则 AddPeers 无法拨号
	if host, port, err := net.SplitHostPort(seedAddr); err == nil {
		if host == "0.0.0.0" || host == "::" || host == "" {
			seedAddr = net.JoinHostPort("127.0.0.1", port)
		}
	}

	e, err := StartBTEngine(filepath.Join(root, "app"), func() Config {
		return Config{DownloadDir: dlDir, SeedMinutes: 0}
	}, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.Close()
		st.Drop()
		seedCl.Close()
		<-seedCl.Closed()
		// A caller-supplied DefaultStorage is not closed by Client.Close.
		_ = seedStore.Close()
		if err := os.RemoveAll(seedDir); err != nil {
			t.Logf("seed dir not removed (test seeder's mmap, released at exit): %v", err)
		}
		// Downloaded files must not stay locked after the engine closes, otherwise
		// users could not move/delete finished downloads on Windows. Allow a short
		// grace period for in-flight piece I/O to finish.
		start := time.Now()
		var err error
		for time.Since(start) < 3*time.Second {
			if err = os.RemoveAll(dlDir); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Errorf("downloaded files still locked %v after engine close: %v", time.Since(start), err)
		}
	})

	gid, err := e.AddTorrentB64(base64.StdEncoding.EncodeToString(torBuf), "pack.torrent", dlDir, false)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var taskT *torrent.Torrent
	for time.Now().Before(deadline) {
		e.mu.Lock()
		task := e.tasks[gid]
		ready := task != nil && task.metaOK
		if ready {
			taskT = task.t
		}
		e.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if taskT == nil {
		t.Fatal("metadata not ready")
	}

	var peers []torrent.PeerInfo
	if tcpAddr, err := net.ResolveTCPAddr("tcp", seedAddr); err == nil {
		peers = []torrent.PeerInfo{{Addr: tcpAddr, Source: torrent.PeerSourceDirect, Trusted: true}}
	} else {
		peers = []torrent.PeerInfo{{Addr: strAddr(seedAddr), Source: torrent.PeerSourceDirect, Trusted: true}}
	}
	taskT.AddPeers(peers)
	// 反向：做种端主动连下载端（家宽/容器里拨号方向常决定成败）
	leecherPort := e.listen
	st.AddPeers([]torrent.PeerInfo{{
		Addr:   &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: leecherPort},
		Source: torrent.PeerSourceDirect, Trusted: true,
	}})
	t.Logf("seeder=%s leecherPort=%d", seedAddr, leecherPort)

	deadline = time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		snaps := e.ListSnapshots()
		if len(snaps) == 1 {
			done := parseInt64(snaps[0]["completedLength"])
			total := parseInt64(snaps[0]["totalLength"])
			spd := parseInt64(snaps[0]["downloadSpeed"])
			if done > 32*1024 || (total > 0 && done >= total) {
				t.Logf("ok done=%d total=%d speed=%d peers=%v", done, total, spd, snaps[0]["connections"])
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no download progress: %#v", e.ListSnapshots())
}

type strAddr string

func (s strAddr) Network() string { return "tcp" }
func (s strAddr) String() string  { return string(s) }

func itoa(n int) string { return strconv.Itoa(n) }

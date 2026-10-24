package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBTEngineStartAndMagnet(t *testing.T) {
	dir := t.TempDir()
	e, err := StartBTEngine(dir, func() Config {
		return Config{DownloadDir: filepath.Join(dir, "dl")}
	}, func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_ = os.MkdirAll(filepath.Join(dir, "dl"), 0755)
	// well-known empty-ish infohash magnet (will wait for metadata)
	gid, err := e.AddMagnet("magnet:?xt=urn:btih:7ACF8FB590B2060DD9C3146EF770169D593433B0&dn=debian", filepath.Join(dir, "dl"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Has(gid) {
		t.Fatal("missing task")
	}
	// give it a moment; should be waiting or paused
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snaps := e.ListSnapshots()
		if len(snaps) == 1 {
			st := snaps[0]["status"]
			if st == "waiting" || st == "paused" || st == "active" {
				t.Logf("status=%v engine=%v", st, snaps[0]["engine"])
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("unexpected snaps: %#v", e.ListSnapshots())
}

package main

import (
	"os"
	"reflect"
	"testing"
	"unsafe"

	_ "github.com/anacrolix/torrent/storage"
)

// ifaceWords mirrors the two machine words of a non-empty interface value.
type ifaceWords struct {
	tab  unsafe.Pointer // *itab: {inter *interfacetype, _type *_type, ...}
	data unsafe.Pointer
}

//go:linkname storageDefaultFileIo github.com/anacrolix/torrent/storage.defaultFileIo
var storageDefaultFileIo func() ifaceWords

// TestTorrentStorageUsesClassicIO 确认 earlyenv 在 anacrolix storage 的 init() 之前生效：
// 默认的 mmap 文件 IO 会让工作集随做种/读写持续增长，并在映射页 IO 出错时直接崩溃。
func TestTorrentStorageUsesClassicIO(t *testing.T) {
	switch v := os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO"); v {
	case "":
		t.Fatal("TORRENT_STORAGE_DEFAULT_FILE_IO 未设置：package main 必须导入 hornetdl/internal/earlyenv")
	case "classic":
	default:
		t.Skipf("TORRENT_STORAGE_DEFAULT_FILE_IO preset to %q by the environment", v)
	}
	w := storageDefaultFileIo()
	if w.tab == nil {
		t.Fatal("defaultFileIo returned nil")
	}
	typ := *(*unsafe.Pointer)(unsafe.Add(w.tab, unsafe.Sizeof(uintptr(0))))
	e := struct{ typ, data unsafe.Pointer }{typ, w.data}
	got := reflect.TypeOf(*(*any)(unsafe.Pointer(&e))).String()
	if got != "storage.classicFileIo" {
		t.Fatalf("anacrolix storage default file IO = %s, want storage.classicFileIo", got)
	}
}

// Package earlyenv sets process environment that third-party packages read in
// their own init() functions. It must stay dependency-free (only "os") and be
// imported by package main.
//
// Go initialises packages one at a time, always picking the first package in
// import-path order whose imports are all initialised. earlyenv becomes ready
// right after "os"; github.com/anacrolix/torrent/storage also needs
// "path/filepath" (which itself needs "os" and sorts after "hornetdl/..."), so
// earlyenv always runs first. earlyenv_test.go in package main checks the
// result at run time.
package earlyenv

import "os"

// FileIOEnv is read once by anacrolix/torrent/storage in its init().
const FileIOEnv = "TORRENT_STORAGE_DEFAULT_FILE_IO"

func init() {
	// anacrolix/torrent v1.61 defaults to memory-mapped file IO on Windows. Mappings
	// are never unmapped while the torrent is open, so the process working set
	// grows with every byte read or written (seeding a 4 GB DVD maps 4 GB), files
	// stay locked until exit, and an I/O error on a mapped page (disk full,
	// removable/AV-locked file) is an access violation that kills the whole
	// process with no Go panic. Classic ReadAt/WriteAt IO avoids all of that.
	if _, ok := os.LookupEnv(FileIOEnv); !ok {
		_ = os.Setenv(FileIOEnv, "classic")
	}
}

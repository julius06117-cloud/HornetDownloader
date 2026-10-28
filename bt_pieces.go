package main

import (
	"encoding/base64"
	"encoding/json"
	"iter"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types/infohash"
)

// btRecheckMessage 是校验已有数据时给界面和任务说明的文案。
// 必须写明「断点续传」，避免退出再开时被看成从头下载。
const btRecheckMessage = "正在校验已下载的数据（断点续传，不是重新下载）"

// 分块刚标记完成时，anacrolix 先 Set(true) 再 fsync。晚一点再落盘，
// 崩溃最多丢掉这一小段记录（下次哈希这几块），不会把还没写稳的块当成已完成。
const btPieceDurableAfter = time.Second

// 单种子分块数上限，避免坏数据把内存撑爆。约 800 万块 × 16 KiB ≈ 128 TiB。
const btMaxPieces = 8_000_000

// btFileStamp 是某个数据文件在保存分块记录时的大小和修改时间。
// 下次启动若对不上（被改短、删掉、或内容被改过），对应分块改回「未知」并重新哈希，
// 而不是继续相信旧的完成位。
type btFileStamp struct {
	Rel   string `json:"rel"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // UnixNano
	Part  bool   `json:"part,omitempty"`
}

type btPieceDisk struct {
	V        int           `json:"v"`
	Pieces   int           `json:"pieces"`
	Complete string        `json:"complete"`
	Known    string        `json:"known"`
	Files    []btFileStamp `json:"files,omitempty"`
}

// btIHPieces 是一个 infohash 的分块完成位。
// known 位为 1：状态已知；complete 位为 1：已通过哈希。
// 已知且未完成的块不会在启动时重哈希（整块重新下载）；未知的块会哈希。
type btIHPieces struct {
	pieces   int
	complete []byte
	known    []byte
	files    []btFileStamp
	fresh    map[int]time.Time // 最近 Set(true) 的块，推迟落盘
	dirty    bool
	loaded   bool
}

// btPieceStore 把分块完成状态放在数据目录 bt/pieces/<infohash>.json。
// 不用下载目录里的 bolt DB：那个文件在 Windows 上会一直锁着，
// 一旦打不开，anacrolix 会整棵种子 DisallowDataDownload（有节点也永远 0 速度）。
type btPieceStore struct {
	dir string
	mu  sync.Mutex
	ih  map[string]*btIHPieces
}

func newBTPieceStore(dir string) *btPieceStore {
	_ = os.MkdirAll(dir, 0700)
	return &btPieceStore{dir: dir, ih: map[string]*btIHPieces{}}
}

func (s *btPieceStore) completion() storage.PieceCompletion {
	return &btPieceCompletion{store: s}
}

type btPieceCompletion struct {
	store *btPieceStore
}

func (p *btPieceCompletion) Persistent() bool { return true }

func (p *btPieceCompletion) Close() error {
	if p == nil || p.store == nil {
		return nil
	}
	// 单个种子的 storage 关闭时只落已过静默期的块。
	// 进程退出时引擎会再 force 保存，把最后几块也写上。
	p.store.save(false)
	return nil
}

func (p *btPieceCompletion) Get(pk metainfo.PieceKey) (storage.Completion, error) {
	if p == nil || p.store == nil {
		return storage.Completion{}, nil
	}
	known, complete := p.store.get(pk.InfoHash.HexString(), pk.Index)
	if !known {
		return storage.Completion{}, nil
	}
	return storage.Completion{Ok: true, Complete: complete}, nil
}

func (p *btPieceCompletion) Set(pk metainfo.PieceKey, complete bool) error {
	if p == nil || p.store == nil {
		return nil
	}
	// part files 打开种子时会把「最终文件还不存在」的块全部标成未完成。
	// 未下完的任务本来就只有 .part，若照做会清掉已保存的完成位，重启后又整盘重哈希，
	// 甚至被标成未完成而不校验、直接重下。这里忽略这一轮，以我们的记录为准。
	// 文件变短时的纠正发生在 Completion() 的长度检查里，不走这个函数。
	if btSetFromPartFilePass() {
		return nil
	}
	p.store.set(pk.InfoHash.HexString(), pk.Index, complete, time.Now())
	return nil
}

func (p *btPieceCompletion) GetRange(ih infohash.T, begin, end int) iter.Seq[storage.Completion] {
	return func(yield func(storage.Completion) bool) {
		for i := begin; i < end; i++ {
			c, _ := p.Get(metainfo.PieceKey{InfoHash: ih, Index: i})
			if !yield(c) {
				return
			}
		}
	}
}

var _ interface {
	storage.PieceCompletion
	storage.PieceCompletionGetRanger
	storage.PieceCompletionPersistenter
} = (*btPieceCompletion)(nil)

func btSetFromPartFilePass() bool {
	var pcs [24]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		fr, more := frames.Next()
		if strings.Contains(fr.Function, "setCompletionFromPartFiles") {
			return true
		}
		if !more {
			return false
		}
	}
}

func normalizeIH(ih string) string {
	ih = strings.ToLower(strings.TrimSpace(ih))
	if len(ih) != 40 {
		return ""
	}
	for _, c := range ih {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return ""
		}
	}
	return ih
}

func (s *btPieceStore) path(ih string) string {
	return filepath.Join(s.dir, ih+".json")
}

func (s *btPieceStore) stateLocked(ih string) *btIHPieces {
	ih = normalizeIH(ih)
	if ih == "" {
		return nil
	}
	st := s.ih[ih]
	if st == nil {
		st = &btIHPieces{fresh: map[int]time.Time{}}
		s.ih[ih] = st
	}
	if st.fresh == nil {
		st.fresh = map[int]time.Time{}
	}
	if !st.loaded {
		s.loadLocked(ih, st)
		st.loaded = true
	}
	return st
}

func (s *btPieceStore) loadLocked(ih string, st *btIHPieces) {
	b, err := os.ReadFile(s.path(ih))
	if err != nil {
		return
	}
	var disk btPieceDisk
	if json.Unmarshal(b, &disk) != nil || disk.V != 1 {
		log.Printf("[bt] 分块记录无法读取，将重新校验 %s", ih)
		return
	}
	complete, err1 := base64.StdEncoding.DecodeString(disk.Complete)
	known, err2 := base64.StdEncoding.DecodeString(disk.Known)
	if err1 != nil || err2 != nil {
		log.Printf("[bt] 分块记录损坏，将重新校验 %s", ih)
		return
	}
	st.pieces = disk.Pieces
	st.complete = complete
	st.known = known
	st.files = disk.Files
}

func (s *btPieceStore) get(ih string, index int) (known, complete bool) {
	if index < 0 || index >= btMaxPieces {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(ih)
	if st == nil {
		return false, false
	}
	return btBitGet(st.known, index), btBitGet(st.complete, index)
}

func (s *btPieceStore) set(ih string, index int, complete bool, now time.Time) {
	if index < 0 || index >= btMaxPieces {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(ih)
	if st == nil {
		return
	}
	st.known = btBitPut(st.known, index, true)
	st.complete = btBitPut(st.complete, index, complete)
	if index+1 > st.pieces {
		st.pieces = index + 1
	}
	if complete {
		st.fresh[index] = now
	} else {
		delete(st.fresh, index)
	}
	st.dirty = true
}

func (s *btPieceStore) completeCount(ih string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(ih)
	if st == nil {
		return 0
	}
	n := 0
	limit := st.pieces
	if limit > btMaxPieces {
		limit = btMaxPieces
	}
	for i := 0; i < limit; i++ {
		if btBitGet(st.known, i) && btBitGet(st.complete, i) {
			n++
		}
	}
	return n
}

// reconcile 在打开存储之前调用：文件大小或修改时间和记录不一致时，
// 把涉及到的分块改回未知，让引擎重新哈希，而不是沿用过期的完成位。
func (s *btPieceStore) reconcile(ihHex, dir string, info *metainfo.Info) int {
	if s == nil || info == nil || dir == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(ihHex)
	if st == nil || len(st.files) == 0 {
		return 0
	}
	ranges := btFilePieceRanges(info)
	cleared := 0
	for _, stamp := range st.files {
		path := filepath.Join(dir, filepath.FromSlash(stamp.Rel))
		if stamp.Part {
			path += ".part"
		}
		fi, err := os.Stat(path)
		if err == nil && fi.Size() == stamp.Size && !fi.IsDir() && fi.ModTime().UnixNano() == stamp.Mtime {
			continue
		}
		rg, ok := ranges[stamp.Rel]
		if !ok {
			cleared += clearAllPieceBits(st)
			log.Printf("[bt] 分块记录里的文件路径对不上，将重新校验 %s", normalizeIH(ihHex))
			break
		}
		for i := rg[0]; i < rg[1]; i++ {
			if btBitGet(st.known, i) || btBitGet(st.complete, i) {
				st.known = btBitPut(st.known, i, false)
				st.complete = btBitPut(st.complete, i, false)
				delete(st.fresh, i)
				cleared++
			}
		}
	}
	if cleared > 0 {
		st.dirty = true
		log.Printf("[bt] 已下载文件有变动，%d 个分块将重新校验（不是重新下载）", cleared)
	}
	return cleared
}

func clearAllPieceBits(st *btIHPieces) int {
	n := 0
	for i := 0; i < st.pieces; i++ {
		if btBitGet(st.known, i) || btBitGet(st.complete, i) {
			n++
		}
	}
	st.known = nil
	st.complete = nil
	st.fresh = map[int]time.Time{}
	st.dirty = true
	return n
}

func btFilePieceRanges(info *metainfo.Info) map[string][2]int {
	out := map[string][2]int{}
	if info == nil || info.PieceLength <= 0 {
		return out
	}
	pl := info.PieceLength
	var off int64
	for _, fi := range info.UpvertedFiles() {
		rel := strings.Join(append([]string{info.BestName()}, fi.BestPath()...), "/")
		if fi.Length > 0 {
			begin := int(off / pl)
			end := int((off + fi.Length + pl - 1) / pl)
			out[rel] = [2]int{begin, end}
		}
		off += fi.Length
		if info.FilesArePieceAligned() {
			off = (off + pl - 1) / pl * pl
		}
	}
	return out
}

type btDataFile struct {
	IH  string
	Dir string
	Rel string
}

// writeStamps 记录当前数据文件的大小和修改时间，供下次启动对照。
func (s *btPieceStore) writeStamps(files []btDataFile) {
	if s == nil || len(files) == 0 {
		return
	}
	grouped := map[string][]btFileStamp{}
	for _, f := range files {
		ih := normalizeIH(f.IH)
		if ih == "" || f.Rel == "" {
			continue
		}
		full := filepath.Join(f.Dir, filepath.FromSlash(f.Rel))
		part := full + ".part"
		if st, err := os.Stat(part); err == nil && !st.IsDir() {
			grouped[ih] = append(grouped[ih], btFileStamp{Rel: f.Rel, Size: st.Size(), Mtime: st.ModTime().UnixNano(), Part: true})
			continue
		}
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			grouped[ih] = append(grouped[ih], btFileStamp{Rel: f.Rel, Size: st.Size(), Mtime: st.ModTime().UnixNano()})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ih, stamps := range grouped {
		st := s.stateLocked(ih)
		if st == nil {
			continue
		}
		st.files = stamps
		st.dirty = true
	}
}

func (s *btPieceStore) setFileStamps(ih string, stamps []btFileStamp) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(ih)
	if st == nil {
		return
	}
	st.files = append([]btFileStamp(nil), stamps...)
	st.dirty = true
}

func (s *btPieceStore) forget(ih string) {
	if s == nil {
		return
	}
	ih = normalizeIH(ih)
	if ih == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ih, ih)
	_ = os.Remove(s.path(ih))
}

// save 把内存中的分块记录写到磁盘。force 为 false 时，刚标记完成、
// 可能还没 fsync 的块先不写，避免崩溃后把半截数据当成已完成。
func (s *btPieceStore) save(force bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for ih, st := range s.ih {
		if st == nil || !st.dirty {
			continue
		}
		complete, known := st.durableBits(now, force)
		disk := btPieceDisk{
			V:        1,
			Pieces:   st.pieces,
			Complete: base64.StdEncoding.EncodeToString(complete),
			Known:    base64.StdEncoding.EncodeToString(known),
			Files:    st.files,
		}
		b, err := json.Marshal(disk)
		if err != nil {
			continue
		}
		if err := s.writeAtomLocked(ih, b); err != nil {
			log.Printf("[bt] 保存分块记录失败 %s: %v", ih, err)
			continue
		}
		st.dirty = false
		if force {
			st.fresh = map[int]time.Time{}
			continue
		}
		for idx, at := range st.fresh {
			if now.Sub(at) >= btPieceDurableAfter {
				delete(st.fresh, idx)
			} else {
				// 还有未落盘的新完成位，保持 dirty，下次再写。
				st.dirty = true
			}
		}
	}
}

func (st *btIHPieces) durableBits(now time.Time, force bool) (complete, known []byte) {
	complete = append([]byte(nil), st.complete...)
	known = append([]byte(nil), st.known...)
	if force {
		return complete, known
	}
	for idx, at := range st.fresh {
		if now.Sub(at) < btPieceDurableAfter {
			complete = btBitPut(complete, idx, false)
			known = btBitPut(known, idx, false)
		}
	}
	return complete, known
}

func (s *btPieceStore) writeAtomLocked(ih string, b []byte) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	tmp := s.path(ih) + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(ih))
}

func btBitGet(b []byte, i int) bool {
	if i < 0 || i/8 >= len(b) {
		return false
	}
	return b[i/8]&(1<<uint(i%8)) != 0
}

func btBitPut(b []byte, i int, on bool) []byte {
	if i < 0 {
		return b
	}
	need := i/8 + 1
	if len(b) < need {
		nb := make([]byte, need)
		copy(nb, b)
		b = nb
	}
	bit := byte(1 << uint(i%8))
	if on {
		b[i/8] |= bit
	} else {
		b[i/8] &^= bit
	}
	return b
}

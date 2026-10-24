package main

// 诊断：崩溃输出文件、goroutine panic 记录、内存心跳、可选的本机 pprof。
// v1.5.3 起：程序以 -H windowsgui 构建，没有 stderr，Go 运行时的 fatal error /
// 未捕获 panic 以前会直接丢失，表现为「窗口静默消失」。

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	pprofrt "runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 软内存上限：仅让 GC 在接近时更积极（不会因此 OOM 退出）。GOMEMLIMIT 环境变量优先。
const defaultMemoryLimit = 768 << 20

// 超过该常驻内存（工作集）时写一次堆快照并记日志，便于事后分析。
const memWarnBytes = 1 << 30

var (
	crashMu   sync.Mutex
	crashFile *os.File
	crashPath string
)

// setupCrashOutput 让 Go 运行时把 fatal error / 未恢复 panic 的完整堆栈写入
// <dataDir>/crash-<时间>.log（debug.SetCrashOutput）。正常退出时删除空文件；
// 下次启动发现空文件 = 上次进程被外部结束或在 Go 之外崩溃（例如访问违例、被杀）。
func setupCrashOutput(dataDir string) {
	reportPreviousCrashes(dataDir)
	name := filepath.Join(dataDir, "crash-"+time.Now().Format("20060102-150405")+".log")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Println("创建崩溃日志失败:", err)
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		log.Println("SetCrashOutput 失败:", err)
		f.Close()
		_ = os.Remove(name)
		return
	}
	crashMu.Lock()
	crashFile, crashPath = f, name
	crashMu.Unlock()
}

// closeCrashOutput：正常退出时调用；没有内容的崩溃日志直接删除。
func closeCrashOutput() {
	crashMu.Lock()
	defer crashMu.Unlock()
	if crashFile == nil {
		return
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	st, err := crashFile.Stat()
	crashFile.Close()
	if err == nil && st.Size() == 0 {
		_ = os.Remove(crashPath)
	}
	crashFile = nil
}

// writeCrashNote 把已恢复的 panic 也写进崩溃日志（app.log 之外再留一份）。
func writeCrashNote(s string) {
	crashMu.Lock()
	defer crashMu.Unlock()
	if crashFile != nil {
		_, _ = crashFile.WriteString(time.Now().Format(time.RFC3339) + " " + s + "\n")
	}
}

func reportPreviousCrashes(dataDir string) {
	list, _ := filepath.Glob(filepath.Join(dataDir, "crash-*.log"))
	sort.Strings(list)
	var kept []string
	for _, p := range list {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.Size() == 0 {
			log.Printf("[diag] 上次运行（%s）没有正常退出，也没有 Go 崩溃输出：进程可能被外部结束、系统关机，或在 Go 运行时之外崩溃", strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "crash-"), ".log"))
			_ = os.Remove(p)
			continue
		}
		kept = append(kept, p)
		if time.Since(st.ModTime()) < 7*24*time.Hour {
			log.Printf("[diag] 发现崩溃日志 %s（%d 字节）", p, st.Size())
		}
	}
	// 只保留最近 10 个
	for len(kept) > 10 {
		_ = os.Remove(kept[0])
		kept = kept[1:]
	}
}

// openAppLog 打开 app.log；上一次的日志保留为 app.prev.log（以前每次启动都截断，
// 程序静默退出后重新打开就看不到退出前发生了什么）。
func openAppLog(dataDir string) (*os.File, error) {
	p := filepath.Join(dataDir, "app.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		_ = os.Remove(filepath.Join(dataDir, "app.prev.log"))
		_ = os.Rename(p, filepath.Join(dataDir, "app.prev.log"))
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
}

// capWriter：app.log 超过上限后停止写入正文，只写一次提示，避免长期运行撑满磁盘。
type capWriter struct {
	f    *os.File
	n    atomic.Int64
	max  int64
	over atomic.Bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.n.Add(int64(len(p))) > w.max {
		if w.over.CompareAndSwap(false, true) {
			_, _ = w.f.WriteString("…… app.log 已达上限，后续日志不再写入（重启程序会重新开始）\n")
		}
		return len(p), nil
	}
	return w.f.Write(p)
}

// goSafe 启动一个 goroutine；panic 时记录堆栈而不是带崩整个进程。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("[panic] %s: %v\n%s", name, r, debug.Stack())
				log.Print(msg)
				writeCrashNote(msg)
			}
		}()
		fn()
	}()
}

// goLoop 用于长期循环（轮询、清理、心跳）：panic 后记录并在 2 秒后重启，正常返回则结束。
func goLoop(name string, quit <-chan struct{}, fn func()) {
	go func() {
		for i := 0; ; i++ {
			panicked := func() (p bool) {
				defer func() {
					if r := recover(); r != nil {
						msg := fmt.Sprintf("[panic] %s（第 %d 次）: %v\n%s", name, i+1, r, debug.Stack())
						log.Print(msg)
						writeCrashNote(msg)
						p = true
					}
				}()
				fn()
				return false
			}()
			if !panicked {
				return
			}
			select {
			case <-quit:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

func applyMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	debug.SetMemoryLimit(defaultMemoryLimit)
}

type memSample struct {
	At          string `json:"at"`
	HeapAlloc   uint64 `json:"heapAlloc"`
	HeapInuse   uint64 `json:"heapInuse"`
	HeapSys     uint64 `json:"heapSys"`
	HeapObjects uint64 `json:"heapObjects"`
	StackInuse  uint64 `json:"stackInuse"`
	Sys         uint64 `json:"sys"`
	NumGC       uint32 `json:"numGC"`
	Goroutines  int    `json:"goroutines"`
	WorkingSet  uint64 `json:"workingSet"`   // Windows：进程工作集（任务管理器「内存」附近的数）
	PrivateUse  uint64 `json:"privateBytes"` // Windows：提交的私有内存
	PeakWS      uint64 `json:"peakWorkingSet"`
	BTConns     int    `json:"btConns"`
	BTTasks     int    `json:"btTasks"`
}

func (a *App) memSnapshot() memSample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := memSample{
		At: time.Now().Format(time.RFC3339), HeapAlloc: ms.HeapAlloc, HeapInuse: ms.HeapInuse,
		HeapSys: ms.HeapSys, HeapObjects: ms.HeapObjects, StackInuse: ms.StackInuse,
		Sys: ms.Sys, NumGC: ms.NumGC, Goroutines: runtime.NumGoroutine(),
	}
	s.WorkingSet, s.PrivateUse, s.PeakWS = processMemory()
	if a != nil && a.bt != nil {
		s.BTTasks, s.BTConns = a.bt.connStats()
	}
	return s
}

func mb(v uint64) string { return fmt.Sprintf("%.1fMB", float64(v)/(1<<20)) }

// memHeartbeat 定期把内存与 goroutine 数写进 app.log（默认 5 分钟；HORNET_MEMLOG_SEC 可改）。
func (a *App) memHeartbeat() {
	every := 5 * time.Minute
	if v, err := strconv.Atoi(os.Getenv("HORNET_MEMLOG_SEC")); err == nil && v >= 5 {
		every = time.Duration(v) * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	warned := false
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
		}
		s := a.memSnapshot()
		log.Printf("[mem] ws=%s private=%s peakWS=%s heap=%s heapSys=%s sys=%s gc=%d goroutines=%d bt=%d/%d conns",
			mb(s.WorkingSet), mb(s.PrivateUse), mb(s.PeakWS), mb(s.HeapAlloc), mb(s.HeapSys), mb(s.Sys),
			s.NumGC, s.Goroutines, s.BTTasks, s.BTConns)
		if !warned && (s.WorkingSet > memWarnBytes || s.PrivateUse > memWarnBytes) {
			warned = true
			p := filepath.Join(a.dataDir, "heap-"+time.Now().Format("20060102-150405")+".pb.gz")
			if f, err := os.Create(p); err == nil {
				_ = pprofrt.WriteHeapProfile(f)
				f.Close()
				log.Printf("[mem] 内存超过 %s，已写堆快照 %s", mb(memWarnBytes), p)
			}
		}
	}
}

// pprofAddr：--pprof 或环境变量 HORNET_PPROF；只允许回环地址。
func pprofAddr(flagVal string) string {
	v := strings.TrimSpace(flagVal)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("HORNET_PPROF"))
	}
	if v == "" {
		return ""
	}
	if !strings.Contains(v, ":") {
		v = "127.0.0.1:" + v
	}
	host, _, err := net.SplitHostPort(v)
	if err != nil {
		log.Println("[diag] pprof 地址无效:", v)
		return ""
	}
	if host == "localhost" {
		return v
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		log.Println("[diag] pprof 只允许监听回环地址，已忽略:", v)
		return ""
	}
	return v
}

// startPprof 在独立的回环端口上提供 /debug/pprof/* 与 /debug/mem（JSON），不挂到界面/API 端口。
func (a *App) startPprof(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/mem", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.memSnapshot())
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Println("[diag] pprof 监听失败:", err)
		return
	}
	log.Println("[diag] pprof 已开启（仅本机）: http://" + ln.Addr().String() + "/debug/pprof/")
	goSafe("pprof", func() {
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		_ = srv.Serve(ln)
	})
}

func runtimeInfo() string {
	return fmt.Sprintf("go=%s %s/%s cpus=%d btFileIO=%s", runtime.Version(), runtime.GOOS, runtime.GOARCH,
		runtime.NumCPU(), os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO"))
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// YTDLPEngine：视频站点页面下载（yt-dlp 旁路进程）。
// 二进制不内嵌（体积大），首次使用时尝试下载到 %APPDATA%\HornetDownloader\bin\yt-dlp.exe；
// 也可由用户自行放入该路径。
type YTDLPEngine struct {
	binDir   string
	sessPath string
	cfgFn    func() Config

	mu    sync.Mutex
	tasks map[string]*ytTask
	exe   string // 解析到的 yt-dlp 路径，空=尚未就绪
}

type ytTask struct {
	gid     string
	url     string
	dir     string
	status  string
	errMsg  string
	title   string
	total   int64
	done    int64
	speed   int64
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	addedAt time.Time
	outPath string
}

func StartYTDLPEngine(appData string, cfgFn func() Config) *YTDLPEngine {
	e := &YTDLPEngine{
		binDir:   filepath.Join(appData, "bin"),
		sessPath: filepath.Join(appData, "ytdlp-session.json"),
		cfgFn:    cfgFn,
		tasks:    map[string]*ytTask{},
	}
	e.exe = e.findExe()
	if e.exe != "" {
		log.Printf("yt-dlp 引擎可用: %s", e.exe)
	} else {
		log.Printf("yt-dlp 尚未就绪（添加视频站任务时会尝试自动下载）")
	}
	return e
}

func (e *YTDLPEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range e.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
}

func (e *YTDLPEngine) ID() string    { return EngineYTDLP }
func (e *YTDLPEngine) Label() string { return engineLabel(EngineYTDLP) }
func (e *YTDLPEngine) Owns(gid string) bool {
	return strings.HasPrefix(gid, "yt-")
}
func (e *YTDLPEngine) Has(gid string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.tasks[gid]
	return ok
}

func (e *YTDLPEngine) findExe() string {
	name := "yt-dlp"
	if runtimeIsWindows {
		name = "yt-dlp.exe"
	}
	p := filepath.Join(e.binDir, name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	// PATH
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

func (e *YTDLPEngine) Ensure() (string, error) {
	if exe := e.findExe(); exe != "" {
		e.exe = exe
		return exe, nil
	}
	if !runtimeIsWindows {
		return "", fmt.Errorf("未找到 yt-dlp，请安装到 PATH 或放到 %s", e.binDir)
	}
	_ = os.MkdirAll(e.binDir, 0700)
	dest := filepath.Join(e.binDir, "yt-dlp.exe")
	log.Printf("正在下载 yt-dlp 到 %s …", dest)
	urls := []string{
		"https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe",
		"https://mirror.ghproxy.com/https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe",
	}
	var last error
	for _, u := range urls {
		if err := downloadFile(u, dest, 3*time.Minute); err != nil {
			last = err
			log.Printf("yt-dlp 下载失败（%s）: %v", u, err)
			continue
		}
		e.exe = dest
		log.Printf("yt-dlp 已就绪: %s", dest)
		return dest, nil
	}
	return "", fmt.Errorf("无法自动下载 yt-dlp（%v）。请手动将 yt-dlp.exe 放到 %s 后重试", last, e.binDir)
}

func downloadFile(url, dest string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "HornetDownloader")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 80<<20))
	cerr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	return os.Rename(tmp, dest)
}

func (e *YTDLPEngine) AddURL(uri, dir string) (string, error) {
	uri = strings.TrimSpace(uri)
	if dir == "" {
		dir = e.cfgFn().DownloadDir
	}
	exe, err := e.Ensure()
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(dir, 0755)
	gid := "yt-" + randHex(8)
	ctx, cancel := context.WithCancel(context.Background())
	task := &ytTask{
		gid: gid, url: uri, dir: dir, status: "active",
		title: uri, addedAt: time.Now(), cancel: cancel,
	}
	// 输出模板
	outTmpl := filepath.Join(dir, "%(title).200B [%(id)s].%(ext)s")
	args := []string{
		"--newline",
		"--no-playlist",
		"--no-color",
		"--encoding", "utf-8", // 否则在中文 Windows 上按 GBK 输出，错误信息会乱码
		"-o", outTmpl,
		"--progress-template", "download:{\"pct\":\"%(progress._percent_str)s\",\"spd\":\"%(progress._speed_str)s\",\"eta\":\"%(progress._eta_str)s\",\"total\":\"%(progress._total_bytes_str)s\",\"done\":\"%(progress._downloaded_bytes_str)s\"}",
		uri,
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	prepareChild(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("启动 yt-dlp 失败: %v", err)
	}
	attachChild(cmd)
	task.cmd = cmd
	e.mu.Lock()
	e.tasks[gid] = task
	e.mu.Unlock()
	go e.consume(task, io.MultiReader(stdout, stderr))
	go func() {
		err := cmd.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.tasks[gid] != task {
			return
		}
		if ctx.Err() != nil {
			if task.status != "paused" && task.status != "complete" {
				task.status = "error"
				task.errMsg = "已取消"
			}
			return
		}
		if err != nil {
			task.status = "error"
			if task.errMsg == "" {
				task.errMsg = err.Error()
			}
			return
		}
		task.status = "complete"
		task.done = task.total
		// 进度条用的是相对值 0..1000；完成后报告真实文件大小，agent 拿到的 totalBytes 才有意义
		if task.outPath != "" {
			if fi, err := os.Stat(task.outPath); err == nil && fi.Size() > 0 {
				task.total, task.done = fi.Size(), fi.Size()
			}
		}
		if task.total == 0 {
			task.total = 1
			task.done = 1
		}
	}()
	return gid, nil
}

var (
	rePct   = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)%`)
	reSpeed = regexp.MustCompile(`(?i)at\s+([0-9.]+[KMG]?i?B/s)`)
	reDest  = regexp.MustCompile(`(?i)\[download\] Destination:\s+(.+)`)
	reMerge = regexp.MustCompile(`(?i)\[Merger\] Merging formats into "(.+)"`)
)

func (e *YTDLPEngine) consume(task *ytTask, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		e.mu.Lock()
		if strings.HasPrefix(line, "download:{") {
			var m map[string]string
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "download:")), &m) == nil {
				if p := parsePercent(m["pct"]); p >= 0 {
					task.done = int64(p * 10) // 相对进度 0..1000
					task.total = 1000
				}
				task.speed = parseSpeed(m["spd"])
			}
		} else if m := reDest.FindStringSubmatch(line); len(m) == 2 {
			task.outPath = strings.TrimSpace(m[1])
			task.title = filepath.Base(task.outPath)
		} else if m := reMerge.FindStringSubmatch(line); len(m) == 2 {
			task.outPath = strings.Trim(m[1], `"`)
			task.title = filepath.Base(task.outPath)
		} else if strings.Contains(strings.ToLower(line), "error") {
			task.errMsg = trimErr(line)
		} else if p := rePct.FindStringSubmatch(line); len(p) == 2 {
			if v, err := strconv.ParseFloat(p[1], 64); err == nil {
				task.done = int64(v * 10)
				task.total = 1000
			}
		}
		e.mu.Unlock()
	}
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

func parseSpeed(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "NA" || s == "Unknown B/s" {
		return 0
	}
	re := regexp.MustCompile(`(?i)^([0-9.]+)\s*([KMG])?i?B`)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToUpper(m[2]) {
	case "K":
		v *= 1024
	case "M":
		v *= 1024 * 1024
	case "G":
		v *= 1024 * 1024 * 1024
	}
	return int64(v)
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	hint := ytdlpHint(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	if hint != "" {
		return hint + "（" + s + "）"
	}
	return s
}

// ytdlpHint 把常见的 yt-dlp 英文错误翻成一句中文说明（原文保留在括号里）。
func ytdlpHint(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "confirm you") && strings.Contains(l, "not a bot"),
		strings.Contains(l, "sign in to confirm"):
		return "网站要求登录验证（YouTube 风控），当前网络无法匿名下载，可稍后重试或换网络"
	case strings.Contains(l, "video unavailable"), strings.Contains(l, "private video"):
		return "视频不可用（已删除、私密或地区限制）"
	case strings.Contains(l, "unsupported url"):
		return "不支持这个链接"
	case strings.Contains(l, "http error 403"):
		return "服务器拒绝访问（403）"
	case strings.Contains(l, "http error 404"):
		return "视频不存在（404）"
	}
	return ""
}

func (e *YTDLPEngine) Pause(gid string) error {
	// yt-dlp 无真正暂停：停掉进程，保留任务；继续时重新拉起（带断点参数）。
	e.mu.Lock()
	defer e.mu.Unlock()
	task, ok := e.tasks[gid]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if task.cancel != nil {
		task.cancel()
		task.cancel = nil
	}
	task.cmd = nil
	if task.status != "complete" && task.status != "error" {
		task.status = "paused"
	}
	task.speed = 0
	return nil
}

func (e *YTDLPEngine) Unpause(gid string) error {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	url, dir := task.url, task.dir
	delete(e.tasks, gid)
	e.mu.Unlock()
	_, err := e.AddURL(url, dir)
	return err
}

func (e *YTDLPEngine) Remove(gid string, deleteFiles bool) error {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	out := task.outPath
	if task.cancel != nil {
		task.cancel()
	}
	delete(e.tasks, gid)
	e.mu.Unlock()
	if deleteFiles && out != "" {
		_ = os.Remove(out)
	}
	return nil
}

func (e *YTDLPEngine) Retry(gid string) (string, error) {
	e.mu.Lock()
	task, ok := e.tasks[gid]
	if !ok {
		e.mu.Unlock()
		return "", fmt.Errorf("任务不存在")
	}
	url, dir := task.url, task.dir
	e.mu.Unlock()
	_ = e.Remove(gid, false)
	return e.AddURL(url, dir)
}

func (e *YTDLPEngine) ListSnapshots() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]map[string]any, 0, len(e.tasks))
	for _, t := range e.tasks {
		title := t.title
		if title == "" {
			title = t.url
		}
		path := t.outPath
		if path == "" {
			path = filepath.Join(t.dir, title)
		}
		m := map[string]any{
			"gid": t.gid, "status": t.status,
			"totalLength": fmt.Sprintf("%d", t.total), "completedLength": fmt.Sprintf("%d", t.done),
			"uploadLength":  "0",
			"downloadSpeed": fmt.Sprintf("%d", t.speed), "uploadSpeed": "0",
			"connections": "1", "numSeeders": "0",
			"dir": t.dir, "infoHash": "",
			"bittorrent": nil,
			"files": []map[string]any{{
				"path":   path,
				"length": fmt.Sprintf("%d", t.total),
				"uris":   []map[string]any{{"uri": t.url}},
			}},
			"engine": EngineYTDLP, "engineLabel": e.Label(),
			"errorMessage": t.errMsg, "errorCode": "",
		}
		out = append(out, m)
	}
	return out
}

// Package agentcli 实现 HornetDownloader 的命令行（供脚本与编程助手使用）。
//
//	hdl get    <url|magnet|file.torrent> [--dir D] [--engine auto|aria2|bt|ytdlp] [--json] [--timeout 30m]
//	hdl add    <url|magnet|file.torrent> [--dir D] [--engine E] [--wait] [--json] [--timeout 30m]
//	hdl wait   <id> [--json] [--timeout 30m]
//	hdl status <id> [--json]
//	hdl list   [--json]
//	hdl remove <id> [--delete-files] [--json]   hdl pause <id>   hdl resume <id>
//	hdl version [--json]
//	hdl help   [--json]
//
// 退出码：0 成功；1 任务失败/被移除；2 用法错误；3 无法连接或启动程序；4 等待超时；5 添加被拒绝。
// 详见 docs/AGENT_API.md。
package agentcli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ExitOK          = 0
	ExitTaskFailed  = 1
	ExitUsage       = 2
	ExitUnavailable = 3
	ExitTimeout     = 4
	ExitRejected    = 5

	DefaultPort = 16801
)

// IsCommand 判断 os.Args[1] 是否为 CLI 子命令（否则按普通启动/打开链接处理）。
func IsCommand(s string) bool {
	switch s {
	case "get", "add", "wait", "status", "list", "remove", "pause", "resume", "version", "--version", "help", "--help", "-h":
		return true
	}
	return false
}

// Options 由调用方提供运行环境。
type Options struct {
	AppExe string    // HornetDownloader.exe 路径（需要时用来启动无窗口实例）
	Stdout io.Writer // 默认 os.Stdout
	Stderr io.Writer // 默认 os.Stderr
}

// Task 与服务端 /api/v1/task 的返回一致。
type Task struct {
	ID         string   `json:"id"`
	Engine     string   `json:"engine"`
	Status     string   `json:"status"`
	Done       bool     `json:"done"`
	OK         bool     `json:"ok"`
	Name       string   `json:"name,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Files      []string `json:"files"`
	TotalBytes int64    `json:"totalBytes"`
	DoneBytes  int64    `json:"doneBytes"`
	Progress   float64  `json:"progress"`
	SpeedBps   int64    `json:"speedBps"`
	Peers      int64    `json:"peers"`
	Seeders    int64    `json:"seeders"`
	Error      string   `json:"error,omitempty"`
	FollowedBy string   `json:"followedBy,omitempty"`
	Source     string   `json:"source,omitempty"`
	AddedBy    string   `json:"addedBy,omitempty"`
}

type cli struct {
	opt     Options
	dataDir string
	port    int
	noStart bool
	client  *http.Client
	base    string
	token   string
}

const usage = `HornetDownloader command line (agent / script friendly)

  get    <url|magnet|file.torrent> [--dir D] [--engine auto|aria2|bt|ytdlp] [--json] [--timeout 30m]
         add and wait until finished; prints final file path(s)
  add    <url|magnet|file.torrent> [--dir D] [--engine E] [--wait] [--json] [--timeout 30m]
         add and return the task id (with --wait: same as get)
  wait   <id> [--json] [--timeout 30m]
  status <id> [--json]
  list   [--json]
  remove <id> [--delete-files] [--json]
  pause  <id> [--json]
  resume <id> [--json]
  version [--json]
         hdl version, running app version (does not start it), Go toolchain, commit
  help   [--json]

add/get are idempotent: the same URL into the same dir returns the existing task
(a finished one only if its file still exists). Use --force to download again.

Common: --data-dir D (default %APPDATA%\HornetDownloader)  --port N  --no-start
If HornetDownloader is not running it is started headless (no window, no focus steal).
Exit codes: 0 ok, 1 task failed, 2 usage, 3 app unavailable, 4 timeout, 5 add rejected.
Docs: docs/AGENT_API.md
`

// Main 运行一个子命令并返回退出码。
func Main(args []string, opt Options) int {
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if len(args) == 0 {
		fmt.Fprint(opt.Stderr, usage)
		return ExitUsage
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		for _, a := range args[1:] {
			if a == "--json" || a == "-json" {
				b, _ := json.MarshalIndent(helpJSON(), "", "  ")
				fmt.Fprintln(opt.Stdout, string(b))
				return ExitOK
			}
		}
		fmt.Fprint(opt.Stdout, usage)
		return ExitOK
	}
	cmd := args[0]
	if cmd == "--version" {
		cmd = "version"
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(opt.Stderr)
	dir := fs.String("dir", "", "download directory (absolute; relative paths are resolved against the current directory)")
	engine := fs.String("engine", "auto", "auto|aria2|bt|ytdlp")
	wait := fs.Bool("wait", cmd == "get" || cmd == "wait", "block until the task finishes")
	asJSON := fs.Bool("json", false, "machine-readable JSON output")
	timeout := fs.Duration("timeout", 0, "max wait time (0 = no limit)")
	dataDir := fs.String("data-dir", "", "HornetDownloader data dir")
	port := fs.Int("port", 0, "API port (default: from data-dir/api-port, else 16801)")
	noStart := fs.Bool("no-start", false, "do not start HornetDownloader if it is not running")
	quiet := fs.Bool("quiet", false, "no progress lines on stderr")
	force := fs.Bool("force", false, "add/get: create a new task even if the same URL is already in the same dir")
	delFiles := fs.Bool("delete-files", false, "remove: also delete downloaded files")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return ExitUsage
	}
	c := &cli{opt: opt, dataDir: *dataDir, port: *port, noStart: *noStart,
		client: &http.Client{Timeout: 30 * time.Second}}
	if c.dataDir == "" {
		c.dataDir = DefaultDataDir()
	}
	need := 1
	if cmd == "list" || cmd == "version" {
		need = 0
	}
	if len(pos) != need {
		fmt.Fprintf(opt.Stderr, "%s: expected %d argument(s), got %d\n\n%s", cmd, need, len(pos), usage)
		return ExitUsage
	}
	switch cmd {
	case "version":
		return c.runVersion(*asJSON)
	case "get", "add":
		if err := c.connect(true); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		d := *dir
		if d != "" {
			if abs, err := filepath.Abs(d); err == nil {
				d = abs
			}
		}
		res, code, err := c.add(pos[0], d, *engine, *force)
		if err != nil {
			return c.fail(*asJSON, code, err)
		}
		if !*wait {
			if *asJSON {
				return c.printJSON(res, ExitOK)
			}
			fmt.Fprintln(opt.Stdout, res["id"])
			return ExitOK
		}
		return c.waitAndReport(fmt.Sprint(res["id"]), *timeout, *asJSON, *quiet)
	case "wait":
		if err := c.connect(false); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		return c.waitAndReport(pos[0], *timeout, *asJSON, *quiet)
	case "status":
		if err := c.connect(false); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		t, found, err := c.task(pos[0])
		if err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		if !found {
			return c.fail(*asJSON, ExitTaskFailed, fmt.Errorf("task %s not found", pos[0]))
		}
		code := ExitOK
		if t.Done && !t.OK {
			code = ExitTaskFailed
		}
		if *asJSON {
			return c.printJSON(t, code)
		}
		c.printTask(t)
		return code
	case "list":
		if err := c.connect(false); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		var out struct {
			Tasks []Task `json:"tasks"`
		}
		if _, err := c.call("GET", "/api/v1/tasks", nil, &out); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		if out.Tasks == nil {
			out.Tasks = []Task{}
		}
		if *asJSON {
			return c.printJSON(out, ExitOK)
		}
		for _, t := range out.Tasks {
			fmt.Fprintf(opt.Stdout, "%s\t%s\t%s\t%5.1f%%\t%s\n", t.ID, t.Engine, t.Status, t.Progress*100, firstNonEmpty(t.Name, strings.Join(t.Files, ";"), t.Source))
		}
		return ExitOK
	case "remove", "pause", "resume":
		if err := c.connect(false); err != nil {
			return c.fail(*asJSON, ExitUnavailable, err)
		}
		var res map[string]any
		code, err := c.call("POST", "/api/v1/"+cmd, map[string]any{"id": pos[0], "deleteFiles": *delFiles}, &res)
		if err != nil {
			ec := ExitTaskFailed
			if code == 0 {
				ec = ExitUnavailable
			}
			return c.fail(*asJSON, ec, err)
		}
		if *asJSON {
			return c.printJSON(res, ExitOK)
		}
		fmt.Fprintln(opt.Stdout, "ok")
		return ExitOK
	}
	fmt.Fprintf(opt.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return ExitUsage
}

// parseInterleaved 允许参数与选项交错（例如 add URL --wait --json）。
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// DefaultDataDir 与主程序一致：%APPDATA%\HornetDownloader。
func DefaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "HornetDownloader")
	}
	return "."
}

func (c *cli) readPort() int {
	if c.port > 0 {
		return c.port
	}
	if b, err := os.ReadFile(filepath.Join(c.dataDir, "api-port")); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return DefaultPort
}

func (c *cli) readToken() string {
	b, _ := os.ReadFile(filepath.Join(c.dataDir, "api-token"))
	return strings.TrimSpace(string(b))
}

func (c *cli) ping() bool {
	c.base = fmt.Sprintf("http://127.0.0.1:%d", c.readPort())
	c.token = c.readToken()
	if c.token == "" {
		return false
	}
	cl := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", c.base+"/api/v1/ping", nil)
	req.Header.Set("X-Token", c.token)
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var info struct {
		App string `json:"app"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&info)
	return resp.StatusCode == 200 && info.App == "HornetDownloader"
}

// connect 找到正在运行的实例；没有则（允许时）以无窗口模式启动并等待 API 就绪。
func (c *cli) connect(allowStart bool) error {
	if c.ping() {
		return nil
	}
	if !allowStart || c.noStart {
		return errors.New("HornetDownloader is not running (start it, or use add/get which start it headless)")
	}
	exe := c.opt.AppExe
	if exe == "" {
		exe = FindAppExe()
	}
	if exe == "" {
		return errors.New("HornetDownloader.exe not found (set HORNETDL_EXE or put hdl.exe next to it)")
	}
	args := []string{"--no-window"}
	if c.dataDir != DefaultDataDir() {
		args = append(args, "--data-dir", c.dataDir)
	}
	if c.port > 0 {
		args = append(args, "--port", strconv.Itoa(c.port))
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	detach(cmd) // 不继承本进程的 stdout 管道，避免 `| Out-String` 一直等待
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if c.ping() {
			return nil
		}
	}
	return errors.New("started HornetDownloader but its API did not become ready within 45s (see app.log in the data dir)")
}

// FindAppExe：HORNETDL_EXE → 与本程序同目录 → %LOCALAPPDATA%\Programs\HornetDownloader。
func FindAppExe() string {
	var cands []string
	if e := os.Getenv("HORNETDL_EXE"); e != "" {
		cands = append(cands, e)
	}
	if self, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(self), "HornetDownloader.exe"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		cands = append(cands, filepath.Join(la, "Programs", "HornetDownloader", "HornetDownloader.exe"))
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (c *cli) call(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return resp.StatusCode, errors.New(e.Error)
		}
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return 200, nil
}

func (c *cli) add(ref, dir, engine string, force bool) (map[string]any, int, error) {
	body := map[string]any{"dir": dir, "engine": engine, "force": force}
	low := strings.ToLower(ref)
	isURL := strings.Contains(low, "://") || strings.HasPrefix(low, "magnet:")
	if !isURL && strings.HasSuffix(low, ".torrent") {
		b, err := os.ReadFile(ref)
		if err != nil {
			return nil, ExitUsage, err
		}
		body["torrentB64"] = base64.StdEncoding.EncodeToString(b)
		body["name"] = filepath.Base(ref)
	} else if isURL {
		body["url"] = ref
	} else {
		return nil, ExitUsage, fmt.Errorf("not a URL, magnet link or .torrent file: %s", ref)
	}
	var res map[string]any
	code, err := c.call("POST", "/api/v1/add", body, &res)
	if err != nil {
		if code == 0 {
			return nil, ExitUnavailable, err
		}
		return nil, ExitRejected, err
	}
	return res, ExitOK, nil
}

func (c *cli) task(id string) (Task, bool, error) {
	var t Task
	code, err := c.call("GET", "/api/v1/task?id="+id, nil, &t)
	if code == 404 {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return t, true, nil
}

func (c *cli) waitAndReport(id string, timeout time.Duration, asJSON, quiet bool) int {
	start := time.Now()
	lastLine := time.Time{}
	missing := 0
	for {
		t, found, err := c.task(id)
		switch {
		case err != nil:
			// 程序重启或短暂无响应：重新连接（不主动启动新实例）
			if !c.ping() {
				missing++
			}
		case !found:
			missing++
		default:
			missing = 0
			if t.FollowedBy != "" && t.Status == "complete" {
				id = t.FollowedBy // aria2 磁力：元数据任务完成后转到真正的下载任务
				continue
			}
			if t.Done {
				code := ExitOK
				if !t.OK {
					code = ExitTaskFailed
				}
				if asJSON {
					return c.printJSON(t, code)
				}
				if t.OK {
					for _, f := range t.Files {
						fmt.Fprintln(c.opt.Stdout, f)
					}
					if len(t.Files) == 0 {
						fmt.Fprintln(c.opt.Stdout, t.Dir)
					}
				} else {
					fmt.Fprintf(c.opt.Stderr, "task %s %s: %s\n", t.ID, t.Status, t.Error)
				}
				return code
			}
			if !quiet && time.Since(lastLine) > 5*time.Second {
				lastLine = time.Now()
				fmt.Fprintf(c.opt.Stderr, "[%s] %s %5.1f%% %s/s\n", t.ID, t.Status, t.Progress*100, human(t.SpeedBps))
			}
		}
		if missing > 30 {
			return c.fail(asJSON, ExitTaskFailed, fmt.Errorf("task %s not found", id))
		}
		if timeout > 0 && time.Since(start) > timeout {
			err := fmt.Errorf("timeout after %s waiting for task %s (still running; use `wait %s` or `remove %s`)", timeout, id, id, id)
			if asJSON {
				return c.printJSON(map[string]any{"ok": false, "error": err.Error(), "exitCode": ExitTimeout, "id": id}, ExitTimeout)
			}
			fmt.Fprintln(c.opt.Stderr, "error:", err)
			fmt.Fprintln(c.opt.Stdout, id)
			return ExitTimeout
		}
		time.Sleep(time.Second)
	}
}

func (c *cli) printTask(t Task) {
	fmt.Fprintf(c.opt.Stdout, "id:       %s\nengine:   %s\nstatus:   %s\nprogress: %.1f%% (%d/%d bytes)\n", t.ID, t.Engine, t.Status, t.Progress*100, t.DoneBytes, t.TotalBytes)
	if t.Error != "" {
		fmt.Fprintf(c.opt.Stdout, "error:    %s\n", t.Error)
	}
	for _, f := range t.Files {
		fmt.Fprintf(c.opt.Stdout, "file:     %s\n", f)
	}
}

func (c *cli) printJSON(v any, code int) int {
	b, _ := json.Marshal(v)
	fmt.Fprintln(c.opt.Stdout, string(b))
	return code
}

func (c *cli) fail(asJSON bool, code int, err error) int {
	if asJSON {
		return c.printJSON(map[string]any{"ok": false, "error": err.Error(), "exitCode": code}, code)
	}
	fmt.Fprintln(c.opt.Stderr, "error:", err)
	return code
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 {
			return fmt.Sprintf("%.1f%s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1fTB", f)
}

func helpJSON() map[string]any {
	return map[string]any{
		"name":        "hdl / HornetDownloader.exe",
		"docs":        "docs/AGENT_API.md",
		"recommended": "Use hdl.exe (console subsystem) from scripts; HornetDownloader.exe <cmd> works when stdout is piped.",
		"commands": []map[string]any{
			{"name": "get", "args": "<url|magnet|file.torrent>", "flags": []string{"--dir", "--engine", "--json", "--timeout", "--force", "--quiet"}, "desc": "add and wait; prints final file path(s)"},
			{"name": "add", "args": "<url|magnet|file.torrent>", "flags": []string{"--dir", "--engine", "--wait", "--json", "--timeout", "--force"}, "desc": "add; prints task id (or waits with --wait)"},
			{"name": "wait", "args": "<id>", "flags": []string{"--json", "--timeout"}, "desc": "wait for a task to finish"},
			{"name": "status", "args": "<id>", "flags": []string{"--json"}, "desc": "one task"},
			{"name": "list", "args": "", "flags": []string{"--json"}, "desc": "all tasks"},
			{"name": "remove", "args": "<id>", "flags": []string{"--delete-files", "--json"}, "desc": "remove a task"},
			{"name": "pause", "args": "<id>", "flags": []string{"--json"}, "desc": "pause a task"},
			{"name": "resume", "args": "<id>", "flags": []string{"--json"}, "desc": "resume a task"},
			{"name": "version", "args": "", "flags": []string{"--json"}, "desc": "hdl version, running app version (never starts the app), Go toolchain, git commit; always exits 0"},
		},
		"version": Version,
		"taskFields": map[string]string{
			"checking":    "since 1.5.5: true while BitTorrent is hashing data already on disk (resume after restart, not a new download). status stays active; error is a note, not a failure",
			"resumeBytes": "since 1.5.5: last known completed bytes while checking; doneBytes is bytes confirmed so far and may be lower until the check finishes",
		},
		"commonFlags": []string{"--data-dir", "--port", "--no-start"},
		"engines":     []string{"auto", "aria2", "bt", "ytdlp"},
		"exitCodes": map[string]string{
			"0": "ok", "1": "task failed / not found", "2": "usage error", "3": "app unavailable",
			"4": "timeout", "5": "add rejected",
		},
		"api": []string{"GET /api/v1/ping", "POST /api/v1/add", "GET /api/v1/task?id=", "GET /api/v1/tasks",
			"POST /api/v1/remove", "POST /api/v1/pause", "POST /api/v1/resume"},
	}
}

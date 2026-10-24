package agentcli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// Version 是发布版本号（HornetDownloader.exe 与 hdl.exe 共用；与 README / 发布 zip 一致）。
const Version = "1.5.4"

// Build 描述当前可执行文件的构建信息（来自 Go 嵌入的构建信息）。
type Build struct {
	Go         string `json:"go"`                   // 编译所用 Go 工具链，例如 go1.26.10
	Commit     string `json:"commit"`               // git 提交（完整 SHA），未知时为 "unknown"
	CommitTime string `json:"commitTime,omitempty"` // 提交时间（RFC3339，UTC）
	Modified   bool   `json:"modified"`             // 构建时工作区有未提交的改动
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// BuildInfo 读取本程序的构建信息。go build 在 git 仓库内默认嵌入 vcs.revision（-trimpath 不影响）。
func BuildInfo() Build {
	b := Build{Go: runtime.Version(), Commit: "unknown", OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.GoVersion != "" {
			b.Go = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
			case "vcs.time":
				b.CommitTime = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			}
		}
	}
	return b
}

// VersionInfo 是 `hdl version --json` 的输出。
type VersionInfo struct {
	HDL   string  `json:"hdl"`
	Build Build   `json:"build"`
	App   AppInfo `json:"app"`
}

// AppInfo：正在运行的 HornetDownloader（version 不会启动程序）。
type AppInfo struct {
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	Go      string `json:"go,omitempty"`
	Commit  string `json:"commit,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (c *cli) appInfo() AppInfo {
	port := c.readPort()
	info := AppInfo{Port: port}
	c.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	c.token = c.readToken()
	if c.token == "" {
		info.Error = "not running (no api-token in data dir)"
		return info
	}
	cl := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", c.base+"/api/v1/ping", nil)
	req.Header.Set("X-Token", c.token)
	resp, err := cl.Do(req)
	if err != nil {
		info.Error = "not running"
		return info
	}
	defer resp.Body.Close()
	var p struct {
		App     string `json:"app"`
		Version string `json:"version"`
		Go      string `json:"go"`
		Commit  string `json:"commit"`
		PID     int    `json:"pid"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&p)
	if resp.StatusCode != 200 || p.App != "HornetDownloader" {
		info.Error = fmt.Sprintf("unexpected reply from port %d (HTTP %d)", port, resp.StatusCode)
		return info
	}
	info.Running = true
	info.Version, info.Go, info.Commit, info.PID = p.Version, p.Go, p.Commit, p.PID
	if info.Version == "" {
		info.Version = "unknown (older than 1.5.4?)"
	}
	return info
}

func shortCommit(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// runVersion：hdl version [--json]。只读，不会启动 HornetDownloader；总是返回 0。
func (c *cli) runVersion(asJSON bool) int {
	v := VersionInfo{HDL: Version, Build: BuildInfo(), App: c.appInfo()}
	if asJSON {
		return c.printJSON(v, ExitOK)
	}
	w := c.opt.Stdout
	fmt.Fprintf(w, "hdl     %s\n", v.HDL)
	if v.App.Running {
		fmt.Fprintf(w, "app     %s (running, pid %d, port %d)\n", v.App.Version, v.App.PID, v.App.Port)
	} else {
		fmt.Fprintf(w, "app     %s\n", v.App.Error)
	}
	fmt.Fprintf(w, "go      %s\n", v.Build.Go)
	commit := shortCommit(v.Build.Commit)
	if v.Build.Modified {
		commit += " (modified)"
	}
	if v.Build.CommitTime != "" {
		commit += " " + v.Build.CommitTime
	}
	fmt.Fprintf(w, "commit  %s\n", commit)
	fmt.Fprintf(w, "os      %s/%s\n", v.Build.OS, v.Build.Arch)
	return ExitOK
}

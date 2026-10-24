package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	ariaRPCTimeout     = 8 * time.Second  // 普通 RPC（列表/暂停等）
	ariaRPCLongTimeout = 45 * time.Second // 大种子 getFiles / addTorrent
)

type Aria2 struct {
	cmd    *exec.Cmd
	port   int
	secret string
	done   chan struct{}
	// exitErr：aria2c 进程退出时的结果（nil = 退出码 0），done 关闭后可读。
	exitErr error
	// 两个独立连接池：慢调用（大种子 getFiles / addTorrent / unpause）不占用
	// 轮询和暂停等快调用的连接，避免在 Go 侧排队。aria2 本身单线程，
	// 真正的卡顿由 health 记录、由后台缓存吸收，界面永不直接等待 aria2。
	client     *http.Client
	slowClient *http.Client
	seq        atomic.Int64

	lastOK    atomic.Int64 // unix ms：最近一次成功的 RPC
	failSince atomic.Int64 // unix ms：连续失败开始时间，0 = 健康
	lastErr   atomic.Value // string
	started   atomic.Bool
}

func newRPCClient(maxConns int) *http.Client {
	return &http.Client{
		Timeout: 0, // 超时由每次请求的 context 控制
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConns:        maxConns,
			MaxIdleConnsPerHost: maxConns,
			MaxConnsPerHost:     maxConns,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  true,
		},
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("aria2 错误 %d: %s", e.Code, e.Message) }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func StartAria2(exe, dataDir string, args []string) (*Aria2, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	a := &Aria2{
		port:       port,
		secret:     randHex(24),
		done:       make(chan struct{}),
		client:     newRPCClient(6),
		slowClient: newRPCClient(2),
	}
	full := append([]string{}, args...)
	conf := filepath.Join(dataDir, "aria2.rpc.conf")
	if err := os.WriteFile(conf, []byte("rpc-secret="+a.secret+"\n"), 0600); err != nil {
		return nil, err
	}
	full = append(full, "--conf-path="+conf, fmt.Sprintf("--rpc-listen-port=%d", port), fmt.Sprintf("--stop-with-process=%d", os.Getpid()))
	a.cmd = exec.Command(exe, full...)
	a.cmd.Dir = dataDir
	prepareChild(a.cmd)
	out := &prefixLogger{prefix: "[aria2c] "}
	a.cmd.Stdout = out
	a.cmd.Stderr = out
	if err := a.cmd.Start(); err != nil {
		return nil, err
	}
	attachChild(a.cmd)
	go func() {
		err := a.cmd.Wait()
		a.exitErr = err
		close(a.done)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-a.done:
			return nil, errors.New("aria2c 启动后立即退出，请查看数据目录中的 app.log / aria2.log")
		default:
		}
		var v map[string]any
		if err := a.Call("aria2.getVersion", nil, &v); err == nil {
			log.Printf("aria2 版本 %v", v["version"])
			a.started.Store(true)
			return a, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = a.cmd.Process.Kill()
	return nil, errors.New("等待 aria2c RPC 超时")
}

func (a *Aria2) Call(method string, params []any, result any) error {
	raw, err := a.CallRaw(method, params)
	if err != nil {
		return err
	}
	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}

func (a *Aria2) CallRaw(method string, params []any) (json.RawMessage, error) {
	return a.CallRawTimeout(method, params, ariaRPCTimeout)
}

func (a *Aria2) CallRawTimeout(method string, params []any, timeout time.Duration) (json.RawMessage, error) {
	cl := a.client
	if timeout > ariaRPCTimeout {
		cl = a.slowClient
	}
	start := time.Now()
	res, err := a.callWith(cl, method, params, timeout)
	a.noteHealth(err, start)
	return res, err
}

// noteHealth 记录 aria2 是否在响应（RPC 层错误码不算无响应）。
func (a *Aria2) noteHealth(err error, start time.Time) {
	if !a.started.Load() {
		return // 启动等待期间不计
	}
	now := time.Now().UnixMilli()
	var re *rpcError
	if err == nil || errors.As(err, &re) {
		a.lastOK.Store(now)
		if fs := a.failSince.Swap(0); fs != 0 {
			log.Printf("[watchdog] aria2 恢复响应，期间无响应约 %.1f 秒", float64(now-fs)/1000)
		}
		return
	}
	a.lastErr.Store(err.Error())
	// 从这次调用发出的时刻算起（超时类错误意味着 aria2 从那时起就没响应）
	if a.failSince.CompareAndSwap(0, start.UnixMilli()) {
		log.Printf("[watchdog] aria2 RPC 无响应/失败: %v", err)
	}
}

// Health 返回 aria2 是否健康，以及连续无响应的毫秒数和最近错误。
func (a *Aria2) Health() (ok bool, stalledMs int64, lastErr string) {
	fs := a.failSince.Load()
	if v, _ := a.lastErr.Load().(string); v != "" {
		lastErr = v
	}
	if fs == 0 {
		return true, 0, ""
	}
	return false, time.Now().UnixMilli() - fs, lastErr
}

func (a *Aria2) callWith(cl *http.Client, method string, params []any, timeout time.Duration) (json.RawMessage, error) {
	p := append([]any{"token:" + a.secret}, params...)
	if method == "system.multicall" || method == "system.listMethods" || method == "system.listNotifications" {
		p = params
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": a.seq.Add(1), "method": method, "params": p})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", a.port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("aria2 响应超时（%s）", timeout)
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("aria2 响应解析失败: %v", err)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

func (a *Aria2) Multicall(calls []rpcCall) (json.RawMessage, error) {
	return a.MulticallTimeout(calls, ariaRPCTimeout)
}

func (a *Aria2) MulticallTimeout(calls []rpcCall, timeout time.Duration) (json.RawMessage, error) {
	var list []map[string]any
	for _, c := range calls {
		list = append(list, map[string]any{"methodName": c.Method, "params": append([]any{"token:" + a.secret}, c.Params...)})
	}
	return a.CallRawTimeout("system.multicall", []any{list}, timeout)
}

type rpcCall struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
}

func (a *Aria2) Shutdown(timeout time.Duration) {
	select {
	case <-a.done:
		return
	default:
	}
	_ = a.Call("aria2.saveSession", nil, nil)
	_ = a.Call("aria2.shutdown", nil, nil)
	select {
	case <-a.done:
		return
	case <-time.After(timeout / 2):
	}
	_ = a.Call("aria2.forceShutdown", nil, nil)
	select {
	case <-a.done:
		return
	case <-time.After(timeout / 2):
	}
	log.Println("aria2c 未能按时退出，强制结束")
	_ = a.cmd.Process.Kill()
	<-a.done
}

type prefixLogger struct{ prefix string }

func (p *prefixLogger) Write(b []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimRight(b, "\r\n"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			log.Print(p.prefix + string(bytes.TrimRight(line, "\r")))
		}
	}
	return len(b), nil
}

// multiWriter：app.log（超过 max 字节后不再写入）+ stderr（控制台运行时可见）。
func multiWriter(f *os.File, max int64) io.Writer {
	return io.MultiWriter(&capWriter{f: f, max: max}, os.Stderr)
}

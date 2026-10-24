//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runtimeIsWindows = true
const canPickFolder = true

func init() { runtime.LockOSThread() } // WebView2 需要在主线程运行

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW      = user32.NewProc("FindWindowW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForegroundWnd = user32.NewProc("SetForegroundWindow")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	jobHandle            windows.Handle
)

func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// attachChild 把 aria2c 放进 Job 对象：本程序无论以何种方式退出，aria2c 都会被系统结束。
func attachChild(cmd *exec.Cmd) {
	if jobHandle == 0 {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			log.Println("CreateJobObject:", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			log.Println("SetInformationJobObject:", err)
			windows.CloseHandle(h)
			return
		}
		jobHandle = h
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		log.Println("OpenProcess:", err)
		return
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(jobHandle, ph); err != nil {
		log.Println("AssignProcessToJobObject:", err)
	}
}

func messageBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(appTitle)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
}

func fatal(msg string) {
	log.Println("错误:", msg)
	messageBox(msg)
	os.Exit(1)
}

// webview2Installed 检查系统是否装有 WebView2 运行时（Win11 自带，Win10 多数已随 Edge 安装）。
func webview2Installed() bool {
	const id = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	paths := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + id},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + id},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + id},
	}
	for _, p := range paths {
		k, err := registry.OpenKey(p.root, p.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			return true
		}
	}
	return false
}

func runWindow(app *App) {
	// 上次用 WebView2 启动失败（进程中途退出）时会留下这个标记，此后改用 Edge 应用窗口
	marker := filepath.Join(app.dataDir, "webview2.failed")
	if forceBrowser || !webview2Installed() {
		log.Println("使用 Edge 应用窗口模式")
		runBrowserWindow(app)
		return
	}
	if _, err := os.Stat(marker); err == nil {
		log.Println("WebView2 上次启动失败，改用 Edge 应用窗口（删除 webview2.failed 可重试）")
		runBrowserWindow(app)
		return
	}
	_ = os.WriteFile(marker, []byte("starting"), 0600)
	go func() { // 界面成功连上后撤销标记
		for i := 0; i < 600; i++ {
			app.pingMu.Lock()
			ok := !app.lastPing.IsZero()
			app.pingMu.Unlock()
			if ok {
				_ = os.Remove(marker)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(app.dataDir, "webview2"),
		WindowOptions: webview2.WindowOptions{
			Title: appTitle, Width: 1180, Height: 760, Center: true, IconId: 1,
		},
	})
	if w == nil {
		_ = os.Remove(marker)
		log.Println("WebView2 不可用，改用 Edge 应用窗口")
		runBrowserWindow(app)
		return
	}
	defer w.Destroy()
	host := &winHost{app: app}
	hwnd := windows.HWND(uintptr(w.Window()))
	host.attach(hwnd)
	defer func() {
		host.removeTray()
		activeHost.Store(nil)
	}()
	go func() {
		<-app.quit
		app.forceClose.Store(true)
		host.removeTray()
		w.Dispatch(func() { w.Terminate() })
	}()
	w.Navigate(app.uiURL)
	w.Run()
}

func findBrowser() string {
	var cands []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands,
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func launchAppWindow(url, dataDir string) error {
	if b := findBrowser(); b != "" {
		cmd := exec.Command(b, "--app="+url, "--user-data-dir="+filepath.Join(dataDir, "browser-profile"),
			"--window-size=1180,760", "--no-first-run", "--no-default-browser-check", "--disable-features=Translate")
		return cmd.Start()
	}
	// 没有 Edge/Chrome：用默认浏览器打开
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func runBrowserWindow(app *App) {
	host := &winHost{app: app}
	host.attachMessageOnly()
	defer func() {
		host.removeTray()
		activeHost.Store(nil)
	}()
	if err := launchAppWindow(app.uiURL, app.dataDir); err != nil {
		fatal("无法打开界面窗口: " + err.Error())
	}
	for {
		app.waitHeartbeat(60*time.Second, 12*time.Second)
		select {
		case <-app.quit:
			return
		default:
		}
		if app.forceClose.Load() || !host.closeToTrayEnabled() {
			return
		}
		log.Println("界面窗口已关闭，已保留托盘图标；点托盘可重新打开")
		select {
		case <-app.quit:
			return
		case <-host.showCh:
			// onShow 已重新启动 Edge；继续等待心跳
		}
	}
}

// openExternalWindow 用于程序已在运行时：把已有窗口切到前台。
func openExternalWindow(url, dataDir string) {
	if h := activeHost.Load(); h != nil && h.subclassed && h.hwnd != 0 {
		h.showMainWindow()
		return
	}
	title, _ := windows.UTF16PtrFromString(appTitle)
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd != 0 {
		procShowWindow.Call(hwnd, 9) // SW_RESTORE
		procSetForegroundWnd.Call(hwnd)
		return
	}
	_ = launchAppWindow(url, dataDir)
}

const pickScript = `Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$f = New-Object System.Windows.Forms.FolderBrowserDialog
$f.Description = $env:HD_TITLE
$f.ShowNewFolderButton = $true
if ($env:HD_INIT -and (Test-Path -LiteralPath $env:HD_INIT)) { $f.SelectedPath = $env:HD_INIT }
$o = New-Object System.Windows.Forms.Form
$o.TopMost = $true
$o.ShowInTaskbar = $false
if ($f.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $f.SelectedPath }`

func pickFolder(initial string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-Command", pickScript)
	cmd.Env = append(os.Environ(), "HD_INIT="+initial, "HD_TITLE=选择下载目录")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("打开文件夹选择框失败: %v", err)
	}
	p := strings.TrimSpace(strings.TrimPrefix(string(out), "\ufeff"))
	return p, nil // 空字符串表示用户取消
}

func revealInFolder(p string) error {
	p = filepath.Clean(p)
	var line string
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		line = fmt.Sprintf(`explorer.exe /select,"%s"`, p)
	} else if err == nil {
		line = fmt.Sprintf(`explorer.exe "%s"`, p)
	} else if _, err := os.Stat(filepath.Dir(p)); err == nil {
		line = fmt.Sprintf(`explorer.exe "%s"`, filepath.Dir(p))
	} else {
		return errors.New("文件或目录不存在")
	}
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return cmd.Start()
}

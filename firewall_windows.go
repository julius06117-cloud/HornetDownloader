//go:build windows

package main

// Windows 防火墙规则：BitTorrent 需要入站连接才能跑满速度。设置页「添加防火墙规则」
// 通过 UAC（ShellExecuteEx "runas"）以管理员身份再启动本程序：
//   HornetDownloader.exe --firewall-rules=add [--firewall-public] [--firewall-aria2=<path>]
// 提权进程只调用 netsh 写规则然后退出。规则名固定为 HornetDownloader，先删后加，可重复执行。

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32FW           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32FW.NewProc("ShellExecuteExW")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIconOrMon   uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHideFW              = 0
	errorCancelled        = 1223
)

var errFirewallCancelled = errors.New("已取消：没有获得管理员授权")

// netsh 以隐藏窗口运行；CmdLine 原样传给 netsh，program="..." 需要自己加引号。
func runNetsh(args string) (string, error) {
	sys32, _ := windows.GetSystemDirectory()
	netsh := filepath.Join(sys32, "netsh.exe")
	cmd := exec.Command(netsh)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		CmdLine:       `"` + netsh + `" ` + args,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// applyFirewallRules 在已提权的进程里执行（--firewall-rules）。
func applyFirewallRules(action string, public bool, aria2 string) error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	cmds, err := firewallNetshCommands(action, public, exe, aria2)
	if err != nil {
		return err
	}
	for i, c := range cmds {
		out, err := runNetsh(c)
		if i == 0 {
			continue // delete：规则不存在时失败属正常
		}
		if err != nil {
			return fmt.Errorf("netsh 添加规则失败: %v %s", err, strings.TrimSpace(out))
		}
	}
	return nil
}

// requestFirewallRules 从普通权限进程弹 UAC，以管理员身份运行本程序写规则并等待结束。
func requestFirewallRules(action string, public bool, aria2 string) error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	params := []string{"--firewall-rules=" + action}
	if public {
		params = append(params, "--firewall-public")
	}
	if aria2 != "" {
		params = append(params, "--firewall-aria2="+aria2)
	}
	for i, p := range params {
		params[i] = syscall.EscapeArg(p)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(strings.Join(params, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: args,
		lpDirectory:  dir,
		nShow:        swHideFW,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorCancelled {
			return errFirewallCancelled
		}
		return fmt.Errorf("无法以管理员身份运行: %v", callErr)
	}
	if sei.hProcess == 0 {
		return errors.New("未能启动提权进程")
	}
	defer windows.CloseHandle(sei.hProcess)
	ev, err := windows.WaitForSingleObject(sei.hProcess, 120*1000)
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		return errors.New("等待提权进程超时")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("写入防火墙规则失败（退出码 %d）", code)
	}
	return nil
}

// firewallStatus 无需管理员：规则是否存在、是否指向当前 exe 路径。
func firewallStatus() (exists, pathOK bool) {
	out, err := runNetsh(`advfirewall firewall show rule name="` + firewallRuleName + `" verbose`)
	if err != nil {
		return false, false
	}
	exe, err := exePath()
	if err != nil {
		return true, false
	}
	for _, r := range exe {
		if r > 127 {
			// netsh 输出为 OEM 代码页（中文系统 GBK），非 ASCII 路径无法可靠比对，按已存在处理。
			return true, true
		}
	}
	return true, strings.Contains(strings.ToLower(out), strings.ToLower(exe))
}

const firewallSupported = true

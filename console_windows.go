package main

import (
	"os"

	"golang.org/x/sys/windows"
)

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// attachParentConsole：本程序是 GUI 子系统；以命令行子命令运行时，
// 若 stdout 已被重定向（管道/文件，如 `| Out-String`）则直接使用，否则挂到父进程控制台。
// 注意：PowerShell 不会等待未重定向的 GUI 程序结束，脚本里请用控制台版 hdl.exe。
func attachParentConsole() {
	if h := windows.Handle(os.Stdout.Fd()); h != 0 && h != windows.InvalidHandle {
		if ft, err := windows.GetFileType(h); err == nil && ft != windows.FILE_TYPE_UNKNOWN && ft != windows.FILE_TYPE_CHAR {
			return
		}
	}
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS = (DWORD)-1
	if r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		if h := windows.Handle(os.Stderr.Fd()); h == 0 || h == windows.InvalidHandle {
			os.Stderr = f
		} else if ft, err := windows.GetFileType(h); err != nil || ft == windows.FILE_TYPE_UNKNOWN {
			os.Stderr = f
		}
	}
}

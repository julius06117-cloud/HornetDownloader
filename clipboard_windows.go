//go:build windows

package main

import (
	"log"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32Clip                     = windows.NewLazySystemDLL("user32.dll")
	procOpenClipboard              = user32Clip.NewProc("OpenClipboard")
	procCloseClipboard             = user32Clip.NewProc("CloseClipboard")
	procGetClipboardData           = user32Clip.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = user32Clip.NewProc("IsClipboardFormatAvailable")
	kernel32Clip                   = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalLock                 = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32Clip.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13

func readClipboardText() (string, bool) {
	r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText)
	if r == 0 {
		return "", false
	}
	r, _, _ = procOpenClipboard.Call(0)
	if r == 0 {
		return "", false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	// p is a uintptr from GlobalLock; reinterpret via its address so go vet's
	// unsafeptr check is satisfied (the memory is locked until GlobalUnlock).
	s := windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&p)))
	return s, true
}

func (a *App) watchClipboard() {
	enabled := a.clipboardEnabled()
	if enabled {
		log.Println("剪贴板监视已启动")
	} else {
		log.Println("剪贴板监视未开启（设置）")
	}
	t := time.NewTicker(900 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
			on := a.clipboardEnabled()
			if !on {
				if enabled {
					log.Println("剪贴板监视已关闭（设置）")
					a.clipSeen.reset()
				}
				enabled = false
				continue // 关闭时完全不读剪贴板
			}
			if !enabled {
				log.Println("剪贴板监视已打开（设置）")
				enabled = true
			}
			text, _ := readClipboardText() // 读不到（空/非文本/被占用）按空文本处理，首次也会建立基线
			a.enqueueClipboardText(text)
		}
	}
}

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
	log.Println("剪贴板监视已启动")
	var last string
	t := time.NewTicker(900 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
			text, ok := readClipboardText()
			if !ok || text == "" {
				continue
			}
			urls := extractDownloadURLs(text)
			a.enqueueClipboardURLs(urls, &last)
		}
	}
}

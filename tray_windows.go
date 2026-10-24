//go:build windows

package main

import (
	"log"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmApp           = 0x8000
	wmTrayIcon      = wmApp + 1
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	trayIconUID     = 1
	nimAdd          = 0x00000000
	nimDelete       = 0x00000002
	nifMessage      = 0x00000001
	nifIcon         = 0x00000002
	nifTip          = 0x00000004
	nifShowTip      = 0x00000080
	tpmRightButton  = 0x0002
	tpmReturnCmd    = 0x0100
	idTrayShow      = 1001
	idTrayExit      = 1002
	swHide          = 0
	swRestore       = 9
	swShow          = 5
	imageIcon       = 1
	lrDefaultSize   = 0x00000040
	lrShared        = 0x00008000
)

type notifyIconData struct {
	CbSize           uint32
	Hwnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GUIDItem         windows.GUID
	HBalloonIcon     windows.Handle
}

type point struct{ X, Y int32 }

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

var gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
)

// winHost 管理主窗口关闭拦截与系统托盘。
type winHost struct {
	app        *App
	hwnd       windows.HWND
	origProc   uintptr
	icon       windows.Handle
	trayAdded  atomic.Bool
	mu         sync.Mutex
	subclassed bool
	showCh     chan struct{} // 浏览器模式：托盘点「显示」时通知
}

var activeHost atomic.Pointer[winHost]

func (h *winHost) closeToTrayEnabled() bool {
	h.app.mu.Lock()
	v := h.app.cfg.CloseToTray
	h.app.mu.Unlock()
	return v
}

func loadAppIcon() windows.Handle {
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	r, _, _ := procLoadImageW.Call(uintptr(hinst), 1, imageIcon, 0, 0, lrShared|lrDefaultSize)
	if r != 0 {
		return windows.Handle(r)
	}
	r, _, _ = procLoadIconW.Call(0, 32512) // IDI_APPLICATION
	return windows.Handle(r)
}

func (h *winHost) attach(hwnd windows.HWND) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hwnd = hwnd
	h.icon = loadAppIcon()
	h.subclassed = true
	cb := windows.NewCallback(hostWndProc)
	prev, _, _ := procSetWindowLongPtr.Call(uintptr(hwnd), uintptr(gwlpWndProc), cb)
	h.origProc = prev
	activeHost.Store(h)
	h.addTray()
}

func (h *winHost) attachMessageOnly() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.icon = loadAppIcon()
	h.showCh = make(chan struct{}, 1)
	className, _ := windows.UTF16PtrFromString("HornetDownloaderTray")
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   windows.NewCallback(hostWndProc),
		HInstance:     hinst,
		HIcon:         h.icon,
		LpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const hwndMessage = ^windows.HWND(2) // HWND_MESSAGE
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		0,
		0,
		0, 0, 0, 0,
		uintptr(hwndMessage),
		0,
		uintptr(hinst),
		0,
	)
	if hwnd == 0 {
		log.Println("创建托盘消息窗口失败:", err)
		return
	}
	h.hwnd = windows.HWND(hwnd)
	h.subclassed = false
	h.origProc = 0
	activeHost.Store(h)
	h.addTray()
}

func (h *winHost) addTray() {
	if h.hwnd == 0 {
		return
	}
	nid := h.makeNid()
	r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(nid)))
	if r == 0 {
		log.Println("Shell_NotifyIcon ADD:", err)
		return
	}
	h.trayAdded.Store(true)
}

func (h *winHost) removeTray() {
	if !h.trayAdded.Swap(false) || h.hwnd == 0 {
		return
	}
	nid := h.makeNid()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(nid)))
}

func (h *winHost) makeNid() *notifyIconData {
	nid := &notifyIconData{
		Hwnd:             h.hwnd,
		UID:              trayIconUID,
		UFlags:           nifMessage | nifIcon | nifTip | nifShowTip,
		UCallbackMessage: wmTrayIcon,
		HIcon:            h.icon,
		UVersion:         4,
	}
	nid.CbSize = uint32(unsafe.Sizeof(*nid))
	tip, _ := windows.UTF16FromString(appTitle)
	copy(nid.SzTip[:], tip)
	return nid
}

func (h *winHost) hideWindow() {
	if h.hwnd == 0 || !h.subclassed {
		return
	}
	procShowWindow.Call(uintptr(h.hwnd), swHide)
}

func (h *winHost) showMainWindow() {
	if h.hwnd == 0 || !h.subclassed {
		return
	}
	procShowWindow.Call(uintptr(h.hwnd), swRestore)
	procShowWindow.Call(uintptr(h.hwnd), swShow)
	procSetForegroundWnd.Call(uintptr(h.hwnd))
}

func (h *winHost) requestQuit() {
	h.app.forceClose.Store(true)
	h.removeTray()
	h.app.Quit()
	if h.subclassed && h.hwnd != 0 {
		procDestroyWindow.Call(uintptr(h.hwnd))
	}
}

func (h *winHost) popupMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	showT, _ := windows.UTF16PtrFromString("显示主窗口")
	exitT, _ := windows.UTF16PtrFromString("退出")
	procAppendMenuW.Call(menu, 0, idTrayShow, uintptr(unsafe.Pointer(showT)))
	procAppendMenuW.Call(menu, 0, idTrayExit, uintptr(unsafe.Pointer(exitT)))
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWnd.Call(uintptr(h.hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(h.hwnd), 0)
	procPostMessageW.Call(uintptr(h.hwnd), wmNull, 0, 0)
	switch cmd {
	case idTrayShow:
		h.onShow()
	case idTrayExit:
		h.requestQuit()
	}
}

func (h *winHost) onShow() {
	if h.subclassed {
		h.showMainWindow()
		return
	}
	select {
	case h.showCh <- struct{}{}:
	default:
	}
	_ = launchAppWindow(h.app.uiURL, h.app.dataDir)
}

func hostWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	h := activeHost.Load()
	if h == nil {
		r, _, _ := procDefWindowProc.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmClose:
		if !h.app.forceClose.Load() && h.closeToTrayEnabled() {
			h.hideWindow()
			return 0
		}
		h.removeTray()
		if h.origProc != 0 {
			r, _, _ := procCallWindowProc.Call(h.origProc, hwnd, msg, wp, lp)
			return r
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmTrayIcon:
		switch lp {
		case wmLButtonUp, wmLButtonDblClk:
			h.onShow()
		case wmRButtonUp, wmContextMenu:
			h.popupMenu()
		}
		return 0
	}
	if h.origProc != 0 {
		r, _, _ := procCallWindowProc.Call(h.origProc, hwnd, msg, wp, lp)
		return r
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

package syswin

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 托盘常量
const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	wmDestroy       = 0x0002
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000
	trayCallbackMsg = wmApp + 1

	mfString    = 0x0000
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
)

const (
	trayCmdShow      = 1
	trayCmdQuit      = 2
	trayCmdAutoStart = 3
)

type notifyIconData struct {
	CbSize           uint32
	Hwnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     windows.Handle
}

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

type msgStruct struct {
	Hwnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	PtX      int32
	PtY      int32
	LPrivate uint32
}

type point struct {
	X int32
	Y int32
}

var (
	procRegisterClassExW       = moduser32.NewProc("RegisterClassExW")
	procCreateWindowExW        = moduser32.NewProc("CreateWindowExW")
	procDefWindowProcW         = moduser32.NewProc("DefWindowProcW")
	procDestroyWindow          = moduser32.NewProc("DestroyWindow")
	procGetMessageW            = moduser32.NewProc("GetMessageW")
	procTranslateMessage       = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW       = moduser32.NewProc("DispatchMessageW")
	procRegisterWindowMessageW = moduser32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu        = moduser32.NewProc("CreatePopupMenu")
	procAppendMenuW            = moduser32.NewProc("AppendMenuW")
	procTrackPopupMenu         = moduser32.NewProc("TrackPopupMenu")
	procDestroyMenu            = moduser32.NewProc("DestroyMenu")
	procGetCursorPos           = moduser32.NewProc("GetCursorPos")
	procPostMessageW           = moduser32.NewProc("PostMessageW")
	procShellNotifyIconW       = modshell32.NewProc("Shell_NotifyIconW")

	trayByHwnd   = map[windows.Handle]*Tray{}
	trayWndProc  = windows.NewCallback(rawTrayWndProc)
	trayClassSeq = 0
)

// Tray 是一个托盘图标 + 右键菜单，自带独立消息循环线程。
type Tray struct {
	hwnd              windows.Handle
	hicon             windows.Handle
	tip               [128]uint16 // szTip
	onShow            func()
	onQuit            func()
	autoStartOn       func() bool // 菜单打开时问一下开没开，决定打不打勾
	onToggleAutoStart func()      // 点了「开机自启动」就切一下
	className         string
	wmTaskbarCreated  uint32
}

type trayAction func()

// TrayOptions 是 NewTray 的参数包，省得以后加功能又改签名。
type TrayOptions struct {
	Icon         []byte
	Tooltip      string
	OnShow       trayAction  // 左键单击 / 菜单「显示主窗口」
	OnQuit       trayAction  // 菜单「退出程序」
	AutoStartOn  func() bool // 菜单里「开机自启动」当前状态
	OnToggleAuto func()      // 点了「开机自启动」
}

// NewTray 创建托盘图标与右键菜单。
func NewTray(o TrayOptions) (*Tray, error) {
	hicon, err := IconFromICO(o.Icon, 32)
	if err != nil {
		return nil, err
	}
	t := &Tray{
		hicon:             hicon,
		onShow:            o.OnShow,
		onQuit:            o.OnQuit,
		autoStartOn:       o.AutoStartOn,
		onToggleAutoStart: o.OnToggleAuto,
	}
	t.tip = toUTF16(o.Tooltip)
	trayClassSeq++
	t.className = fmt.Sprintf("WxMultiTrayWnd_%d", trayClassSeq)

	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		ready <- t.run()
		var msg msgStruct
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
	if err := <-ready; err != nil {
		DestroyIconHandle(hicon)
		return nil, err
	}
	return t, nil
}

func (t *Tray) run() error {
	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString(t.className)
	wc := wndClassEx{
		LpfnWndProc:   trayWndProc,
		HInstance:     windows.Handle(hInst),
		LpszClassName: className,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassEx 失败: %v", e)
	}
	title, _ := windows.UTF16PtrFromString("微信多开助手 托盘")
	hwnd, _, e := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0, 0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("创建托盘消息窗口失败: %v", e)
	}
	t.hwnd = windows.Handle(hwnd)

	tb, _ := windows.UTF16PtrFromString("TaskbarCreated")
	taskbarCreated, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tb)))
	t.wmTaskbarCreated = uint32(taskbarCreated)

	trayByHwnd[t.hwnd] = t
	if err := t.addIcon(); err != nil {
		return err
	}
	return nil
}

func toUTF16(s string) [128]uint16 {
	var out [128]uint16
	src, _ := windows.UTF16FromString(s)
	copy(out[:], src)
	return out
}

func (t *Tray) tipText() [128]uint16 { return t.tip }

func (t *Tray) nid(flags uint32) notifyIconData {
	nid := notifyIconData{
		Hwnd:             t.hwnd,
		UID:              1,
		UFlags:           flags,
		UCallbackMessage: trayCallbackMsg,
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	if flags&nifIcon != 0 {
		nid.HIcon = t.hicon
	}
	if flags&nifTip != 0 {
		nid.SzTip = t.tipText()
	}
	return nid
}

func (t *Tray) addIcon() error {
	nid := t.nid(nifMessage | nifIcon | nifTip)
	r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return fmt.Errorf("Shell_NotifyIcon(NIM_ADD) 失败: %v", e)
	}
	return nil
}

// Remove 移除托盘图标并销毁窗口。
func (t *Tray) Remove() {
	if t == nil || t.hwnd == 0 {
		return
	}
	nid := t.nid(0)
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	procDestroyWindow.Call(uintptr(t.hwnd))
	DestroyIconHandle(t.hicon)
	t.hwnd = 0
}

func (t *Tray) showMenu() {
	// 自绘菜单（白底 + 圆角 + 悬停高亮），见 traymenu.go
	items := []MenuItem{
		{ID: trayCmdShow, Label: "显示主窗口"},
		{ID: trayCmdAutoStart, Label: "开机自启动", Checked: t.autoStartOn != nil && t.autoStartOn()},
		{Separator: true},
		{ID: trayCmdQuit, Label: "退出程序"},
	}
	switch ShowMenu(items) {
	case trayCmdShow:
		t.fireShow()
	case trayCmdAutoStart:
		if t.onToggleAutoStart != nil {
			t.onToggleAutoStart()
		}
	case trayCmdQuit:
		if t.onQuit != nil {
			t.onQuit()
		}
	}
}

func (t *Tray) fireShow() {
	if t.onShow != nil {
		t.onShow()
	}
}

func rawTrayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	h := windows.Handle(hwnd)
	t, ok := trayByHwnd[h]
	if ok {
		switch uint32(msg) {
		case trayCallbackMsg:
			switch uint32(lparam) & 0xFFFF {
			case wmLButtonUp:
				// 回调里不能阻塞，交给 goroutine
				go t.fireShow()
			case wmRButtonUp:
				t.showMenu()
			case 0x0403: // WM_LBUTTONDBLCLK
				go t.fireShow()
			}
			return 0
		case wmDestroy:
			delete(trayByHwnd, h)
			return 0
		default:
			if t.wmTaskbarCreated != 0 && uint32(msg) == t.wmTaskbarCreated {
				_ = t.addIcon()
				return 0
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

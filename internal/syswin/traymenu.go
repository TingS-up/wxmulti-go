package syswin

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 托盘右键菜单：系统那套 TrackPopupMenu 画出来是直角 + 系统灰配色，改不动。
// 这里自己开一个 WS_POPUP 小窗自绘：白底、圆角（Win10 用 SetWindowRgn 裁，
// 和主窗口一个套路）、悬停高亮、勾选项前面画绿勾。

const (
	menuItemH    = 28 // 逻辑像素
	menuSepH     = 9
	menuPadY     = 5
	menuPadX     = 6
	menuCheckCol = 22 // 勾那一列的宽度（文字从这列右边开始，两行标签才对得齐）
	menuCheckW   = 16
	menuRadius   = 10

	csDropShadow = 0x00020000
	wsPopup      = 0x80000000
	wsExToolWnd  = 0x00000080
	wsExTopmost  = 0x00000008

	dtCenter     = 0x0001
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020
	dtCalcRect   = 0x0400

	transparentBkMode = 1
	fwNormal          = 400
	fwSemiBold        = 600

	wmMenuPaint  = 0x000F
	wmMenuLUp    = 0x0202
	wmMenuMove   = 0x0200
	wmMenuKey    = 0x0100
	wmMenuActive = 0x0006
	wmMenuDestro = 0x0002
	waMenuInact  = 0
	vkMenuEscape = 0x1B
)

// 颜色按 COLORREF（0x00BBGGRR）写，别写成 RGB 顺序
const (
	colWhite   = 0x00FFFFFF
	colHover   = 0x00F4F8F2 // #F2F8F4
	colSep     = 0x00E7ECE8 // #E8ECE7
	colText    = 0x00222A1B // #1B2A22
	colCheck   = 0x0060C107 // #07C160
	swShowNorm = 5
)

// MenuItem 是一条菜单项；Separator 为真时忽略其他字段，画成分隔线。
type MenuItem struct {
	ID        int
	Label     string
	Checked   bool
	Separator bool
}

type menuState struct {
	items  []MenuItem
	hover  int
	want   int // 被点中的 ID，0 表示没选
	font   windows.Handle
	fontB  windows.Handle
	dpi    int
	width  int
	height int
}

type menuPaintStruct struct {
	Hdc         windows.Handle
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

var (
	procGetDC        = moduser32.NewProc("GetDC")
	procReleaseDC    = moduser32.NewProc("ReleaseDC")
	procBeginPaint   = moduser32.NewProc("BeginPaint")
	procEndPaint     = moduser32.NewProc("EndPaint")
	procGetClientRec = moduser32.NewProc("GetClientRect")
	procInvalidateRc = moduser32.NewProc("InvalidateRect")
	procMonitorPt    = moduser32.NewProc("MonitorFromPoint")
	procCreateBrush  = modgdi32.NewProc("CreateSolidBrush")
	procCreatePenM   = modgdi32.NewProc("CreatePen")
	procCreateFontW  = modgdi32.NewProc("CreateFontW")
	procSelectObjM   = modgdi32.NewProc("SelectObject")
	procDeleteObjM   = modgdi32.NewProc("DeleteObject")
	procSetBkModeM   = modgdi32.NewProc("SetBkMode")
	procSetTextCol   = modgdi32.NewProc("SetTextColor")
	procRoundRectM   = modgdi32.NewProc("RoundRect")
	procDrawTextM    = moduser32.NewProc("DrawTextW")
	procFillRectM    = moduser32.NewProc("FillRect")
	procShowWindowM  = moduser32.NewProc("ShowWindow")
	procUpdateWindow = moduser32.NewProc("UpdateWindow")

	menuStates    = sync.Map{} // hwnd -> *menuState
	menuWndProcCB = windows.NewCallback(rawMenuWndProc)
)

// ShowMenu 在鼠标位置弹出自绘菜单，返回被点中的项 ID（0 = 没选 / 取消）。
// 阻塞到菜单关闭为止，只能在托盘那条已经跑着消息循环的线程里调。
func ShowMenu(items []MenuItem) int {
	if len(items) == 0 {
		return 0
	}
	st := &menuState{items: items, hover: -1, dpi: systemDPI()}
	st.loadFonts()
	defer st.freeFonts()

	// 量一下最宽的文字，定窗口宽度
	textW := 0
	if hdc, _, _ := procGetDC.Call(0); hdc != 0 {
		old, _, _ := procSelectObjM.Call(hdc, uintptr(st.font))
		for _, it := range items {
			if it.Separator {
				continue
			}
			if w := measureText(hdc, it.Label); w > textW {
				textW = w
			}
		}
		procSelectObjM.Call(hdc, old)
		procReleaseDC.Call(0, hdc)
	}
	st.width = st.dp(menuPadX*2 + menuCheckCol + textW + 18)

	totalH := menuPadY * 2
	for _, it := range items {
		if it.Separator {
			totalH += menuSepH
		} else {
			totalH += menuItemH
		}
	}
	st.height = st.dp(totalH)

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("WxMultiMenuWnd")
	wc := wndClassEx{
		LpfnWndProc:   menuWndProcCB,
		HInstance:     windows.Handle(hInst),
		LpszClassName: className,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.Style = csDropShadow // 圆角区域 + 系统投影，省得自己画阴影
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	x, y := int(pt.X), int(pt.Y)
	x, y = clampToWorkArea(x, y, st.width, st.height)

	title, _ := windows.UTF16PtrFromString("")
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExToolWnd|wsExTopmost,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		uintptr(x), uintptr(y), uintptr(st.width), uintptr(st.height),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return 0
	}
	h := windows.Handle(hwnd)
	menuStates.Store(h, st)

	// 圆角：Win10 自己裁区域；Win11 交给 DWM（主窗口也是这么分的）
	if !RoundedCornersSupported() {
		rgn, _, _ := procCreateRoundRectRgn.Call(0, 0,
			uintptr(st.width+1), uintptr(st.height+1),
			uintptr(st.dp(menuRadius*2)), uintptr(st.dp(menuRadius*2)))
		if rgn != 0 {
			procSetWindowRgn.Call(hwnd, rgn, 1)
		}
	}

	procShowWindowM.Call(hwnd, swShowNorm)
	procUpdateWindow.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)

	// 局部消息循环，直到窗口销毁（点中 / ESC / 失焦都会销毁）
	var msg msgStruct
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		if _, ok := menuStates.Load(h); !ok {
			break
		}
	}
	menuStates.Delete(h)
	destroyWindowIfAlive(h)
	return st.want
}

func destroyWindowIfAlive(h windows.Handle) {
	procDestroyWindow.Call(uintptr(h))
}

// 别弹出屏幕外：越界就往回收，不压任务栏
func clampToWorkArea(x, y, w, h int) (int, int) {
	mon, _, _ := procMonitorPt.Call(uintptr(uint64(uint32(x))|uint64(uint32(y))<<32), 2)
	if mon == 0 {
		return x, y
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return x, y
	}
	if x+w > int(mi.RcWork.Right) {
		x = int(mi.RcWork.Right) - w
	}
	if y+h > int(mi.RcWork.Bottom) {
		y -= h
	}
	if x < int(mi.RcWork.Left) {
		x = int(mi.RcWork.Left)
	}
	if y < int(mi.RcWork.Top) {
		y = int(mi.RcWork.Top)
	}
	return x, y
}

func (st *menuState) loadFonts() {
	name, _ := windows.UTF16PtrFromString("Microsoft YaHei UI")
	size := int32(-st.dp(13))
	st.font = createFontHandle(name, size, fwNormal)
	st.fontB = createFontHandle(name, size, fwSemiBold)
}

func (st *menuState) freeFonts() {
	if st.font != 0 {
		procDeleteObjM.Call(uintptr(st.font))
	}
	if st.fontB != 0 {
		procDeleteObjM.Call(uintptr(st.fontB))
	}
}

func createFontHandle(name *uint16, height int32, weight int32) windows.Handle {
	h, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0, uintptr(weight),
		0, 0, 0, 1, // DEFAULT_CHARSET
		0, 0, 5, 0, // CLEARTYPE_QUALITY
		uintptr(unsafe.Pointer(name)),
	)
	return windows.Handle(h)
}

func measureText(hdc uintptr, s string) int {
	u, err := windows.UTF16FromString(s)
	if err != nil || len(u) == 0 {
		return 0
	}
	r := menuRect{Right: 2000, Bottom: 100}
	procDrawTextM.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1),
		uintptr(unsafe.Pointer(&r)), dtCalcRect|dtSingleLine)
	return int(r.Right - r.Left)
}

func systemDPI() int {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 96
	}
	defer procReleaseDC.Call(0, hdc)
	const logPixelsY = 90
	d, _, _ := modgdi32.NewProc("GetDeviceCaps").Call(hdc, logPixelsY)
	if d == 0 {
		return 96
	}
	return int(d)
}

func (st *menuState) dp(v int) int { return v * st.dpi / 96 }

type menuRect struct {
	Left, Top, Right, Bottom int32
}

// 第 idx 项的矩形（客户区坐标）
func (st *menuState) itemRect(idx int) menuRect {
	y := st.dp(menuPadY)
	for i, it := range st.items {
		h := menuItemH
		if it.Separator {
			h = menuSepH
		}
		if i == idx {
			return menuRect{
				Left:   int32(st.dp(menuPadX)),
				Top:    int32(y),
				Right:  int32(st.width - st.dp(menuPadX)),
				Bottom: int32(y + st.dp(h)),
			}
		}
		y += st.dp(h)
	}
	return menuRect{}
}

// 光标 y 落在第几项；落在分隔线或空白上返回 -1
func (st *menuState) itemAt(y int) int {
	cur := st.dp(menuPadY)
	for i, it := range st.items {
		h := st.dp(menuItemH)
		if it.Separator {
			h = st.dp(menuSepH)
		}
		if y >= cur && y < cur+h {
			if it.Separator {
				return -1
			}
			return i
		}
		cur += h
	}
	return -1
}

func (st *menuState) close(h windows.Handle) {
	menuStates.Delete(h)
	procDestroyWindow.Call(uintptr(h))
}

func rawMenuWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	h := windows.Handle(hwnd)
	v, ok := menuStates.Load(h)
	if !ok {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	st := v.(*menuState)

	switch uint32(msg) {
	case wmMenuPaint:
		var ps menuPaintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		paintMenu(st, hdc)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmMenuMove:
		idx := st.itemAt(int(int32(uint32(lparam >> 16))))
		if idx != st.hover {
			st.hover = idx
			procInvalidateRc.Call(hwnd, 0, 0)
		}
		return 0

	case wmMenuLUp:
		if idx := st.itemAt(int(int32(uint32(lparam >> 16)))); idx >= 0 {
			st.want = st.items[idx].ID
		}
		st.close(h)
		return 0

	case wmMenuKey:
		if int(uint32(wparam)) == vkMenuEscape {
			st.close(h)
		}
		return 0

	case wmMenuActive:
		// 点到别处去了就收起来
		if int(uint32(wparam))&0xFFFF == waMenuInact {
			st.close(h)
		}
		return 0

	case wmMenuDestro:
		menuStates.Delete(h)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func paintMenu(st *menuState, hdc uintptr) {
	// 白底：窗口大小就是客户区大小，直接用
	white, _, _ := procCreateBrush.Call(colWhite)
	full := menuRect{0, 0, int32(st.width), int32(st.height)}
	procFillRectM.Call(hdc, uintptr(unsafe.Pointer(&full)), white)
	procDeleteObjM.Call(white)

	procSetBkModeM.Call(hdc, transparentBkMode)
	oldFont, _, _ := procSelectObjM.Call(hdc, uintptr(st.font))
	defer procSelectObjM.Call(hdc, oldFont)

	y := st.dp(menuPadY)
	for i, it := range st.items {
		hh := st.dp(menuItemH)
		if it.Separator {
			hh = st.dp(menuSepH)
			pen, _, _ := procCreatePenM.Call(0, 1, colSep)
			old, _, _ := procSelectObjM.Call(hdc, pen)
			lineY := uintptr(y + hh/2)
			procRoundRectM.Call(hdc,
				uintptr(st.dp(menuPadX+4)), lineY,
				uintptr(st.width-st.dp(menuPadX+4)), lineY+1, 0, 0)
			procSelectObjM.Call(hdc, old)
			procDeleteObjM.Call(pen)
			y += hh
			continue
		}

		r := st.itemRect(i)
		if i == st.hover {
			brush, _, _ := procCreateBrush.Call(colHover)
			pen, _, _ := procCreatePenM.Call(0, 1, colHover)
			oldBrush, _, _ := procSelectObjM.Call(hdc, brush)
			oldPen, _, _ := procSelectObjM.Call(hdc, pen)
			rr := uintptr(st.dp(8))
			procRoundRectM.Call(hdc, uintptr(r.Left), uintptr(r.Top),
				uintptr(r.Right), uintptr(r.Bottom), rr, rr)
			procSelectObjM.Call(hdc, oldBrush)
			procSelectObjM.Call(hdc, oldPen)
			procDeleteObjM.Call(brush)
			procDeleteObjM.Call(pen)
		}

		if it.Checked {
			procSelectObjM.Call(hdc, uintptr(st.fontB))
			procSetTextCol.Call(hdc, colCheck)
			mark, _ := windows.UTF16FromString("✓")
			cr := menuRect{
				Left:   int32(st.dp(menuPadX + 3)),
				Top:    r.Top,
				Right:  int32(st.dp(menuPadX + 3 + menuCheckW)),
				Bottom: r.Bottom,
			}
			procDrawTextM.Call(hdc, uintptr(unsafe.Pointer(&mark[0])), uintptr(len(mark)-1),
				uintptr(unsafe.Pointer(&cr)), dtCenter|dtVCenter|dtSingleLine)
			procSelectObjM.Call(hdc, uintptr(st.font))
		}

		procSetTextCol.Call(hdc, colText)
		label, _ := windows.UTF16FromString(it.Label)
		tr := menuRect{
			Left:   int32(st.dp(menuPadX + menuCheckCol)),
			Top:    r.Top,
			Right:  int32(st.width - st.dp(menuPadX+8)),
			Bottom: r.Bottom,
		}
		procDrawTextM.Call(hdc, uintptr(unsafe.Pointer(&label[0])), uintptr(len(label)-1),
			uintptr(unsafe.Pointer(&tr)), dtVCenter|dtSingleLine)
		y += hh
	}
}

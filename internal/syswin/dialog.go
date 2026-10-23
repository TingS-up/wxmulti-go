package syswin

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 原生弹窗：输入 / 确认 / 提示三种。
//
// 为什么不用 WebView 里的 HTML 弹窗：WebView 只能画在自己那个 360px 的窗口里，
// 弹窗一定会被窗口边界裁掉，也没法显示在屏幕正中。这里开一个独立的 WS_POPUP 窗口，
// 白底圆角自绘（和托盘菜单、主窗口同一套做法），位置摆在工作区正中。
// 输入框用系统的 EDIT 控件 —— 中文输入法、粘贴、光标、选区全是原生的，不用自己造。

type DialogKind int

const (
	DialogAlert   DialogKind = iota // 只有一个「知道了」
	DialogConfirm                   // 确定 / 取消
	DialogInput                     // 输入框 + 确定 / 取消
)

// DialogOpts 描述一次弹窗。
type DialogOpts struct {
	Kind       DialogKind
	Title      string
	Desc       string // 可多行，自动换行
	Label      string // 输入框上面的字段标签，比如“账号名称”
	Hint       string // 输入框下面那行小字，比如“留空就用 账号1022-1547”
	Initial    string // 输入框初始内容
	OKText     string
	CancelText string
	Danger     bool // 确定按钮用红色（删除、覆盖这类）
}

// DialogResult 是弹窗的返回值。OK=false 表示取消/关闭。
type DialogResult struct {
	OK   bool
	Text string
}

const (
	dlgWidth     = 384 // 逻辑像素（8 的倍数）
	dlgPadX      = 24
	dlgPadTop    = 24
	dlgPadBottom = 24
	dlgRadius    = 14
	dlgBtnW      = 88
	dlgBtnH      = 40
	dlgBtnGap    = 12
	dlgEditH     = 40

	dlgClassName = "WxMultiDialgWnd"

	wsChild     = 0x40000000
	wsVisible   = 0x10000000
	wsClipChild = 0x02000000
	wsTabStop   = 0x00010000
	esAutoH     = 0x0080
	wsExToolWin = 0x00000080
	csDropShad  = 0x00020000

	wmDlgCreate       = 0x0001
	wmDlgPaintMsg     = 0x000F
	wmDlgEraseBg      = 0x0014
	wmDlgClose        = 0x0010
	wmDlgCtlColorEdit = 0x0133
	wmDlgSetFont      = 0x0030
	wmDlgMouseMove    = 0x0200
	wmDlgLButtonUp    = 0x0202
	wmDlgKeyDown      = 0x0100
	wmDlgGetText      = 0x000D
	wmDlgAppOK        = 0x8000 + 1
	wmDlgAppCancel    = 0x8000 + 2

	emSetMargins   = 0x00D3
	ecLeftMargin   = 0x0001
	ecRightMargin  = 0x0002
	vkDlgReturn    = 0x0D
	vkDlgEscape    = 0x1B
	dtWordBreak    = 0x0010
	dtNoPrefix     = 0x0800
	spiGetWorkArea = 0x0030
	swShowDlg      = 5
)

// 颜色（COLORREF：0x00BBGGRR）
// 取自设计库的极简方案：白卡 + 单一绿色强调 + 中性灰阶，正文对比度 ≥ 4.5:1。
const (
	dlgColCard     = 0x00FFFFFF
	dlgColBorder   = 0x00E6EBE6 // #E6EBE6 卡片描边
	dlgColField    = 0x00DCE3DC // #DCE3DC 输入框描边
	dlgColFieldBg  = 0x00FBFDFB // #FBFDFB 输入框底色
	dlgColTitle    = 0x00222A1B // #1B2A22 深墨绿标题
	dlgColDesc     = 0x005D6859 // #59685D → 正文灰绿
	dlgColHint     = 0x00707A6C // #6C7A70 → 辅助说明（对比度 4.6:1）
	dlgColLabel    = 0x00404A3C // #3C4A40 → 字段标签
	dlgColPrimary  = 0x0060C107 // #07C160 品牌绿
	dlgColPrimaryH = 0x004FB005 // #05B04F 悬停深一档
	dlgColDanger   = 0x003F45E0 // #E0453F（BGR 反过来写）
	dlgColDangerH  = 0x00343AC9 // #C93A34
	dlgColGhostBgH = 0x00F3F7F3 // #F3F7F3 次要按钮悬停
	dlgColGhostTx  = 0x00303C33 // #333C30
)

var (
	procDispatchMsgW   = moduser32.NewProc("DispatchMessageW")
	procTranslateMsgW  = moduser32.NewProc("TranslateMessage")
	procGetMsgW        = moduser32.NewProc("GetMessageW")
	procEnableWindow   = moduser32.NewProc("EnableWindow")
	procSetFocus       = moduser32.NewProc("SetFocus")
	procGetFocus       = moduser32.NewProc("GetFocus")
	procGetParent      = moduser32.NewProc("GetParent")
	procIsWindow       = moduser32.NewProc("IsWindow")
	procSetWindowLong  = moduser32.NewProc("SetWindowLongPtrW")
	procCallWindowProc = moduser32.NewProc("CallWindowProcW")
	procGetStockObject = modgdi32.NewProc("GetStockObject")
	procSetBkColor     = modgdi32.NewProc("SetBkColor")
	procSysParamInfo   = moduser32.NewProc("SystemParametersInfoW")
	procGetWindowText  = moduser32.NewProc("GetWindowTextW")
	procLoadCursorW    = moduser32.NewProc("LoadCursorW")
	procSetWindowText  = moduser32.NewProc("SetWindowTextW")

	dlgClassOnce sync.Once
	dlgClassErr  error
	dlgWndProcCB = windows.NewCallback(rawDialogWndProc)
	dlgEditProc  = windows.NewCallback(rawDialogEditProc)

	dlgMu  sync.Mutex
	dlgCur *dlgState
)


type dlgState struct {
	opts DialogOpts
	hwnd windows.Handle
	edit windows.Handle
	old  uintptr // EDIT 原来的窗口过程

	fontTitle windows.Handle
	fontDesc  windows.Handle
	fontBtn   windows.Handle
	fontHint  windows.Handle
	fontLabel windows.Handle
	fontEdit  windows.Handle

	brushWhite  windows.Handle
	brushGhost  windows.Handle
	brushGhostH windows.Handle

	dpi int
	w   int
	h   int

	titleRect  rect
	descRect   rect
	labelRect  rect
	hintRect   rect
	editRect   rect
	okRect     rect
	cancelRect rect

	hover int // 1=确定 2=取消
	done  bool
	ok    bool
	text  string
}

// ShowDialog 弹出一个原生对话框并阻塞到用户操作完（或关掉）。
// 位置在屏幕工作区正中，期间主窗口会被禁用，避免点到后面的界面。
func ShowDialog(o DialogOpts) DialogResult {
	// 自己的消息队列必须固定在一个系统线程上，否则 GetMessage 可能拿到别的线程的队列
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ensureDialogClass(); err != nil {
		return DialogResult{OK: false}
	}

	st := &dlgState{opts: o, dpi: systemDPI(), hover: 0}
	st.loadFonts()
	defer st.freeFonts()
	st.layout()

	x, y := dlgCenteredPos(st.w, st.h)
	cls, _ := windows.UTF16PtrFromString(dlgClassName)
	title, _ := windows.UTF16PtrFromString(o.Title)
	style := uintptr(wsPopup | wsClipChild | wsVisible)

	// 先把状态挂上去，WM_CREATE 里靠它拿回自己
	dlgMu.Lock()
	dlgCur = st
	dlgMu.Unlock()
	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(wsExToolWin), uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		style, uintptr(x), uintptr(y), uintptr(st.w), uintptr(st.h),
		0, 0, 0, 0)
	if hwnd == 0 {
		dlgMu.Lock()
		dlgCur = nil
		dlgMu.Unlock()
		return DialogResult{OK: false}
	}
	st.hwnd = windows.Handle(hwnd)


	// 主窗口禁用 + 我们自己到前台
	owner := dialogOwner()
	if owner != 0 {
		procEnableWindow.Call(uintptr(owner), 0)
	}
	procShowWindowM.Call(hwnd, swShowDlg)
	procSetForegroundWindow.Call(hwnd)
	if st.edit != 0 {
		procSetFocus.Call(uintptr(st.edit))
	} else {
		procSetFocus.Call(hwnd)
	}

	// 本地消息循环：窗口销毁即结束
	var msg msgStruct
	for {
		r, _, _ := procGetMsgW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		procTranslateMsgW.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMsgW.Call(uintptr(unsafe.Pointer(&msg)))
		if !dlgAlive(st.hwnd) {
			break
		}
	}

	if owner != 0 {
		procEnableWindow.Call(uintptr(owner), 1)
		procSetForegroundWindow.Call(uintptr(owner))
	}
	if st.edit != 0 {
		dlgMu.Lock()
		dlgCur = nil
		dlgMu.Unlock()
	}
	return DialogResult{OK: st.ok, Text: st.text}
}

func dlgAlive(h windows.Handle) bool {
	if h == 0 {
		return false
	}
	r, _, _ := procIsWindow.Call(uintptr(h))
	return r != 0
}

func ensureDialogClass() error {
	dlgClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
		cls, _ := windows.UTF16PtrFromString(dlgClassName)
		wc := wndClassEx{
			CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
			Style:         csDropShad,
			LpfnWndProc:   dlgWndProcCB,
			HInstance:     windows.Handle(hInst),
			HCursor:       windows.Handle(cursor),
			HbrBackground: 0,
			LpszClassName: cls,
		}
		r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if r == 0 {
			dlgClassErr = fmt.Errorf("RegisterClassEx（对话框）失败: %v", e)
		}
	})
	return dlgClassErr
}

// dlgCenteredPos 返回屏幕工作区正中的左上角坐标。
func dlgCenteredPos(w, h int) (int, int) {
	var wa rect
	r, _, _ := procSysParamInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	if r == 0 || wa.Right <= wa.Left || wa.Bottom <= wa.Top {
		return (1920 - w) / 2, (1080 - h) / 2
	}
	x := int(wa.Left) + (int(wa.Right-wa.Left)-w)/2
	y := int(wa.Top) + (int(wa.Bottom-wa.Top)-h)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// dialogOwner 找主窗口，用来做模态禁用。
func dialogOwner() windows.Handle {
	t, _ := windows.UTF16PtrFromString("微信多开助手")
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	return windows.Handle(h)
}

func (st *dlgState) dp(v int) int { return v * st.dpi / 96 }

func (st *dlgState) loadFonts() {
	name := "Microsoft YaHei UI"
	p := windows.StringToUTF16Ptr(name)
	st.fontTitle = createFontHandle(p, int32(st.dp(22)), fwSemiBold)
	st.fontDesc = createFontHandle(p, int32(st.dp(14)), fwNormal)
	st.fontBtn = createFontHandle(p, int32(st.dp(15)), fwSemiBold)
	st.fontHint = createFontHandle(p, int32(st.dp(13)), fwNormal)
	st.fontLabel = createFontHandle(p, int32(st.dp(13)), fwSemiBold)
	st.fontEdit = createFontHandle(p, int32(st.dp(16)), fwNormal)
	br, _, _ := procCreateBrush.Call(dlgColCard)
	st.brushWhite = windows.Handle(br)
	br, _, _ = procCreateBrush.Call(dlgColGhostBgH)
	st.brushGhostH = windows.Handle(br)
	br, _, _ = procCreateBrush.Call(dlgColFieldBg)
	st.brushGhost = windows.Handle(br) // 输入框底色
}

func (st *dlgState) freeFonts() {
	for _, h := range []windows.Handle{st.fontTitle, st.fontDesc, st.fontBtn, st.fontHint, st.fontLabel, st.fontEdit, st.brushWhite, st.brushGhost, st.brushGhostH} {
		if h != 0 {
			procDeleteObjM.Call(uintptr(h))
		}
	}
}

func (st *dlgState) layout() {
	padX := st.dp(dlgPadX)
	padTop := st.dp(dlgPadTop)
	w := st.dp(dlgWidth)
	innerW := w - padX*2

	y := padTop
	// 标题：22px 半粗，行高 32
	st.titleRect = rect{int32(padX), int32(y), int32(padX + innerW), int32(y + st.dp(32))}
	y += st.dp(32) + st.dp(8)

	// 说明：15px，行高由 DrawText 按字体算
	descH := 0
	if st.opts.Desc != "" {
		descH = st.measureWrapped(st.opts.Desc, innerW, st.fontDesc)
		st.descRect = rect{int32(padX), int32(y), int32(padX + innerW), int32(y + descH)}
		y += descH
	}
	y += st.dp(24)

	// 输入框 44 高；提示文字放在框下面（helpers 在字段下方，比塞在框上面干净）
	if st.opts.Kind == DialogInput {
		if st.opts.Label != "" {
			st.labelRect = rect{int32(padX), int32(y), int32(padX + innerW), int32(y + st.dp(18))}
			y += st.dp(18) + st.dp(8)
		}
		st.editRect = rect{int32(padX), int32(y), int32(padX + innerW), int32(y + st.dp(dlgEditH))}
		y += st.dp(dlgEditH)
		if st.opts.Hint != "" {
			y += st.dp(8)
			st.hintRect = rect{int32(padX), int32(y), int32(padX + innerW), int32(y + st.dp(18))}
			y += st.dp(18)
		}
		y += st.dp(24)
	}

	btnW := st.dp(dlgBtnW)
	btnH := st.dp(dlgBtnH)
	btnY := y
	st.okRect = rect{int32(w - padX - btnW), int32(btnY), int32(w - padX), int32(btnY + btnH)}
	if st.opts.Kind != DialogAlert {
		cx2 := st.okRect.Left - int32(st.dp(dlgBtnGap))
		st.cancelRect = rect{cx2 - int32(btnW), int32(btnY), cx2, int32(btnY + btnH)}
	}
	st.w = w
	st.h = btnY + btnH + st.dp(dlgPadBottom)
}

// measureWrapped 量一段自动换行文本的高度。
func (st *dlgState) measureWrapped(s string, width int, font windows.Handle) int {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return st.dp(20)
	}
	defer procReleaseDC.Call(0, hdc)
	old, _, _ := procSelectObjM.Call(hdc, uintptr(font))
	defer procSelectObjM.Call(hdc, old)
	u := windows.StringToUTF16Ptr(s)
	r := rect{0, 0, int32(width), 0}
	procDrawTextM.Call(hdc, uintptr(unsafe.Pointer(u)), ^uintptr(0),
		uintptr(unsafe.Pointer(&r)), dtWordBreak|dtNoPrefix|dtCalcRect)
	if r.Bottom <= 0 {
		return st.dp(20)
	}
	return int(r.Bottom - r.Top)
}

func (st *dlgState) finish(ok bool) {
	st.ok = ok
	if ok && st.edit != 0 {
		buf := make([]uint16, 512)
		n, _, _ := procSendMessageW.Call(uintptr(st.edit), wmDlgGetText,
			uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
		if n > 0 {
			st.text = windows.UTF16ToString(buf[:n])
		}
	}
	st.done = true
	if st.hwnd != 0 {
		procDestroyWindow.Call(uintptr(st.hwnd))
	}
}

func rawDialogWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmDlgCreate:
		// 窗口对象在 CreateWindowEx 之前就挂到 dlgCur 上了，这里只补 hwnd。
		// 不用 lparam 里的 CREATESTRUCT：那是 unsafe 指针转换，go vet 会警告。
		st := pendingDlg()
		if st == nil {
			return ^uintptr(0)
		}
		st.hwnd = windows.Handle(hwnd)
		st.createEdit()
		st.applyRoundRegion()
		return 0

	case wmDlgPaintMsg:
		st := currentDlg(hwnd)
		if st == nil {
			break
		}
		var ps menuPaintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			paintDialog(st, hdc)
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0

	case wmDlgEraseBg:
		return 1

	case wmDlgMouseMove:
		st := currentDlg(hwnd)
		if st == nil {
			break
		}
		x := int(int16(lparam & 0xFFFF))
		y := int(int16((lparam >> 16) & 0xFFFF))
		hv := 0
		if dlgPtIn(st.okRect, x, y) {
			hv = 1
		} else if dlgPtIn(st.cancelRect, x, y) {
			hv = 2
		}
		if hv != st.hover {
			st.hover = hv
			procInvalidateRc.Call(hwnd, 0, 0)
		}
		return 0

	case wmDlgLButtonUp:
		st := currentDlg(hwnd)
		if st == nil {
			break
		}
		x := int(int16(lparam & 0xFFFF))
		y := int(int16((lparam >> 16) & 0xFFFF))
		if dlgPtIn(st.okRect, x, y) {
			st.finish(true)
		} else if st.opts.Kind != DialogAlert && dlgPtIn(st.cancelRect, x, y) {
			st.finish(false)
		}
		return 0

	case wmDlgKeyDown:
		st := currentDlg(hwnd)
		if st == nil {
			break
		}
		switch wparam {
		case vkDlgEscape:
			st.finish(false)
			return 0
		case vkDlgReturn:
			st.finish(true)
			return 0
		}

	case wmDlgAppOK:
		if st := currentDlg(hwnd); st != nil {
			st.finish(true)
		}
		return 0
	case wmDlgAppCancel:
		if st := currentDlg(hwnd); st != nil {
			st.finish(false)
		}
		return 0

	case wmDlgCtlColorEdit:
		st := currentDlg(hwnd)
		if st == nil {
			break
		}
		procSetBkColor.Call(wparam, dlgColCard)
		procSetTextCol.Call(wparam, dlgColTitle)
		return uintptr(st.brushWhite)

	case wmDlgClose:
		if st := currentDlg(hwnd); st != nil {
			st.finish(false)
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func currentDlg(hwnd uintptr) *dlgState {
	dlgMu.Lock()
	defer dlgMu.Unlock()
	if dlgCur != nil && dlgCur.hwnd == windows.Handle(hwnd) {
		return dlgCur
	}
	return nil
}

// pendingDlg 返回已经建好、但窗口还没创建的那一个（WM_CREATE 里用）。
func pendingDlg() *dlgState {
	dlgMu.Lock()
	defer dlgMu.Unlock()
	if dlgCur != nil && dlgCur.hwnd == 0 {
		return dlgCur
	}
	return nil
}

func dlgPtIn(r rect, x, y int) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func (st *dlgState) createEdit() {
	if st.opts.Kind != DialogInput {
		return
	}
	cls, _ := windows.UTF16PtrFromString("EDIT")
	// EDIT 自身高度按字体算，外面再画一圈边框；多留 10px 让它看起来居中
	editTop := int(st.editRect.Top) + st.dp(10)
	editH := st.dp(22)
	h, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0,
		uintptr(wsChild|wsVisible|wsTabStop|esAutoH),
		uintptr(int(st.editRect.Left)+st.dp(14)), uintptr(editTop),
		uintptr(int(st.editRect.Right-st.editRect.Left)-st.dp(28)), uintptr(editH),
		uintptr(st.hwnd), 0, 0, 0)
	if h == 0 {
		return
	}
	st.edit = windows.Handle(h)
	procSendMessageW.Call(h, wmDlgSetFont, uintptr(st.fontEdit), 1)
	// 左右内边距（EM_SETMARGINS），让文字别贴着边框
	margins := uintptr(st.dp(2)<<16 | st.dp(2))
	procSendMessageW.Call(h, emSetMargins, ecLeftMargin|ecRightMargin, margins)
	if st.opts.Initial != "" {
		ini, _ := windows.UTF16PtrFromString(st.opts.Initial)
		procSetWindowText.Call(h, uintptr(unsafe.Pointer(ini)))
		procSendMessageW.Call(h, 0x00B1, 0, ^uintptr(0)) // EM_SETSEL 全选
	}
	// 子类化：回车 = 确定，Esc = 取消
	old, _, _ := procSetWindowLong.Call(h, ^uintptr(3) /*GWLP_WNDPROC = -4*/, dlgEditProc)
	if old != 0 {
		st.old = old
	}
}

func rawDialogEditProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmDlgKeyDown {
		parent, _, _ := procGetParent.Call(hwnd)
		switch wparam {
		case vkDlgReturn:
			procPostMessageW.Call(parent, wmDlgAppOK, 0, 0)
			return 0
		case vkDlgEscape:
			procPostMessageW.Call(parent, wmDlgAppCancel, 0, 0)
			return 0
		}
	}
	if msg == 0x0102 && (wparam == 13 || wparam == 27) { // WM_CHAR：别让 EDIT 发哔声
		return 0
	}
	dlgMu.Lock()
	var old uintptr
	if dlgCur != nil {
		old = dlgCur.old
	}
	dlgMu.Unlock()
	if old == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	r, _, _ := procCallWindowProc.Call(old, hwnd, msg, wparam, lparam)
	return r
}

func (st *dlgState) applyRoundRegion() {
	r := st.dp(dlgRadius) * 2
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(st.w+1), uintptr(st.h+1), uintptr(r), uintptr(r))
	if rgn != 0 {
		procSetWindowRgn.Call(uintptr(st.hwnd), rgn, 1)
	}
}

func paintDialog(st *dlgState, hdc uintptr) {
	// 卡片：白底 + 1px 描边（窗口区域已裁成圆角，填满就是圆角卡片）
	dlgFillRoundRect(hdc, rect{0, 0, int32(st.w), int32(st.h)}, st.dp(dlgRadius), dlgColCard)
	dlgStrokeRoundRect(hdc, rect{0, 0, int32(st.w), int32(st.h)}, st.dp(dlgRadius), dlgColBorder, 1)

	procSetBkModeM.Call(hdc, transparentBkMode)

	if st.opts.Title != "" {
		dlgDrawText(st, hdc, st.opts.Title, st.titleRect, st.fontTitle, dlgColTitle, dtSingleLine|dtVCenter|dtNoPrefix)
	}
	if st.opts.Desc != "" {
		dlgDrawText(st, hdc, st.opts.Desc, st.descRect, st.fontDesc, dlgColDesc, dtWordBreak|dtNoPrefix)
	}

	if st.opts.Kind == DialogInput {
		if st.opts.Label != "" {
			dlgDrawText(st, hdc, st.opts.Label, st.labelRect, st.fontLabel, dlgColLabel, dtSingleLine|dtVCenter|dtNoPrefix)
		}
		// 输入框：浅底 + 圆角描边；聚焦时换成品牌绿 2px
		dlgFillRoundRectBrush(hdc, st.editRect, st.dp(10), uintptr(st.brushGhost))
		focus, _, _ := procGetFocus.Call(0)
		if st.edit != 0 && windows.Handle(focus) == st.edit {
			dlgStrokeRoundRect(hdc, st.editRect, st.dp(10), dlgColPrimary, 2)
		} else {
			dlgStrokeRoundRect(hdc, st.editRect, st.dp(10), dlgColField, 1)
		}
		if st.opts.Hint != "" {
			dlgDrawText(st, hdc, st.opts.Hint, st.hintRect, st.fontHint, dlgColHint, dtSingleLine|dtVCenter|dtNoPrefix)
		}
	}

	// 主按钮：品牌绿（危险操作换红）
	okBg := uintptr(dlgColPrimary)
	if st.opts.Danger {
		okBg = dlgColDanger
	}
	if st.hover == 1 {
		if st.opts.Danger {
			okBg = dlgColDangerH
		} else {
			okBg = dlgColPrimaryH
		}
	}
	dlgFillRoundRect(hdc, st.okRect, st.dp(10), okBg)
	okLabel := st.opts.OKText
	if okLabel == "" {
		okLabel = "确定"
	}
	dlgDrawText(st, hdc, okLabel, st.okRect, st.fontBtn, dlgColCard, dtSingleLine|dtVCenter|dtCenter|dtNoPrefix)

	// 次要按钮：白底细边，悬停浅灰（比实心灰更像现代桌面应用的次要动作）
	if st.opts.Kind != DialogAlert {
		if st.hover == 2 {
			dlgFillRoundRectBrush(hdc, st.cancelRect, st.dp(10), uintptr(st.brushGhostH))
		} else {
			dlgFillRoundRect(hdc, st.cancelRect, st.dp(10), dlgColCard)
		}
		dlgStrokeRoundRect(hdc, st.cancelRect, st.dp(10), dlgColField, 1)
		cl := st.opts.CancelText
		if cl == "" {
			cl = "取消"
		}
		dlgDrawText(st, hdc, cl, st.cancelRect, st.fontBtn, dlgColGhostTx, dtSingleLine|dtVCenter|dtCenter|dtNoPrefix)
	}
}

func dlgDrawText(st *dlgState, hdc uintptr, s string, r rect, font windows.Handle, color uintptr, flags uintptr) {
	if s == "" {
		return
	}
	old, _, _ := procSelectObjM.Call(hdc, uintptr(font))
	procSetTextCol.Call(hdc, color)
	u := windows.StringToUTF16Ptr(s)
	rr := r
	procDrawTextM.Call(hdc, uintptr(unsafe.Pointer(u)), ^uintptr(0), uintptr(unsafe.Pointer(&rr)), flags)
	procSelectObjM.Call(hdc, old)
}

func dlgFillRoundRect(hdc uintptr, r rect, radius int, color uintptr) {
	b, _, _ := procCreateBrush.Call(color)
	defer procDeleteObjM.Call(b)
	dlgFillRoundRectBrush(hdc, r, radius, b)
}

func dlgFillRoundRectBrush(hdc uintptr, r rect, radius int, brush uintptr) {
	oldBrush, _, _ := procSelectObjM.Call(hdc, brush)
	nullPen, _, _ := procGetStockObject.Call(8) // NULL_PEN
	oldPen, _, _ := procSelectObjM.Call(hdc, nullPen)
	procRoundRectM.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right+1), uintptr(r.Bottom+1),
		uintptr(radius*2), uintptr(radius*2))
	procSelectObjM.Call(hdc, oldBrush)
	procSelectObjM.Call(hdc, oldPen)
}

func dlgStrokeRoundRect(hdc uintptr, r rect, radius int, color uintptr, width int) {
	pen, _, _ := procCreatePenM.Call(0 /*PS_SOLID*/, uintptr(width), color)
	defer procDeleteObjM.Call(pen)
	hollow, _, _ := procGetStockObject.Call(5) // NULL_BRUSH
	oldPen, _, _ := procSelectObjM.Call(hdc, pen)
	oldBrush, _, _ := procSelectObjM.Call(hdc, hollow)
	procRoundRectM.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom),
		uintptr(radius*2), uintptr(radius*2))
	procSelectObjM.Call(hdc, oldPen)
	procSelectObjM.Call(hdc, oldBrush)
}

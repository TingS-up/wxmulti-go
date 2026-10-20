// Package syswin 提供本工具需要的原生 Win32 能力：
// 拒绝型互斥体（多开核心）、文件版本读取、ShellExecute 启动、ICO 图标加载。
package syswin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	moduser32   = windows.NewLazySystemDLL("user32.dll")
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	modshell32  = windows.NewLazySystemDLL("shell32.dll")
	modversion  = windows.NewLazySystemDLL("version.dll")
	modgdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procCreateMutexW             = modkernel32.NewProc("CreateMutexW")
	procGetModuleHandleW         = modkernel32.NewProc("GetModuleHandleW")
	procAllocateAndInitializeSid = modadvapi32.NewProc("AllocateAndInitializeSid")
	procInitializeAcl            = modadvapi32.NewProc("InitializeAcl")
	procAddAccessDeniedAce       = modadvapi32.NewProc("AddAccessDeniedAce")
	procSetSecurityInfo          = modadvapi32.NewProc("SetSecurityInfo")
	procFreeSid                  = modadvapi32.NewProc("FreeSid")
	procShellExecuteW            = modshell32.NewProc("ShellExecuteW")
	procGetFileVersionInfoSizeW  = modversion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW      = modversion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW           = modversion.NewProc("VerQueryValueW")
	procCreateIconFromResourceEx = moduser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = moduser32.NewProc("DestroyIcon")
	procFindWindowW              = moduser32.NewProc("FindWindowW")
	procShowWindow               = moduser32.NewProc("ShowWindow")
	procIsIconic                 = moduser32.NewProc("IsIconic")
	procSetForegroundWindow      = moduser32.NewProc("SetForegroundWindow")
	procMessageBoxW              = moduser32.NewProc("MessageBoxW")
	procSendMessageW             = moduser32.NewProc("SendMessageW")
	procGetWindowRect            = moduser32.NewProc("GetWindowRect")
	procSetWindowPos             = moduser32.NewProc("SetWindowPos")
	procMonitorFromWindow        = moduser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW          = moduser32.NewProc("GetMonitorInfoW")
	procGetDpiForWindow          = moduser32.NewProc("GetDpiForWindow")
	procSetWindowRgn             = moduser32.NewProc("SetWindowRgn")
	procCreateRoundRectRgn       = modgdi32.NewProc("CreateRoundRectRgn")
)

const (
	seKernelObject          = 6
	daclSecurityInformation = 4
	aclRevision             = 2
	// 原易语言代码用的访问掩码字面量 1048575 = 0xFFFFF
	accessMaskDenyAll = 0x000FFFFF
)

// ErrMutexOccupied 表示同名互斥体已经被别的进程用「拒绝所有人」的 DACL 占住了。
// 这不是失败：目标状态（名字被占 + 谁都打不开）本来就已经成立。
var ErrMutexOccupied = errors.New("互斥体已被拒绝型 DACL 占位")

// AcquireSingleton 给本程序自己用的普通命名互斥体。
// already=true 说明已经有实例在跑（句柄会被关掉，不泄漏）。
func AcquireSingleton(name string) (h windows.Handle, already bool, err error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, false, err
	}
	r, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if r == 0 {
		return 0, false, fmt.Errorf("CreateMutexW 失败: %v", callErr)
	}
	if callErr == windows.ERROR_ALREADY_EXISTS {
		// 已经有实例：拿到的句柄没用，关掉
		windows.CloseHandle(windows.Handle(r))
		return 0, true, nil
	}
	return windows.Handle(r), false, nil
}

// CreateDenyAllMutex 创建命名互斥体，并把 DACL 改成“拒绝所有人访问”。
//
// 这就是“多开”的全部秘密：微信 4.x 启动时会用
// CreateMutexW("XWeChat_App_Instance_Identity_Mutex_Name") / OpenMutexW 判断是否已有实例。
// 正常情况下第二个实例会因为 ERROR_ALREADY_EXISTS 而激活第一个窗口并退出；
// 当互斥体存在但被 DACL 拒绝访问时，微信拿到的是 ERROR_ACCESS_DENIED，
// 它把这种情况当成“互斥体不存在”，于是照常启动 —— 每个实例都能独立跑起来。
//
// 返回的句柄必须一直持有到进程退出，否则 DACL 会随最后一个句柄一起被销毁。
func CreateDenyAllMutex(name string) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		// ACCESS_DENIED 说明这个互斥体已经被别的进程（比如本程序的另一个实例、
		// 或者老版易语言工具）用拒绝型 DACL 占住了 —— 多开效果已经成立，不算失败。
		if callErr == windows.ERROR_ACCESS_DENIED {
			return 0, ErrMutexOccupied
		}
		return 0, fmt.Errorf("CreateMutexW 失败: %v", callErr)
	}
	fail := func(step string, e error) (windows.Handle, error) {
		windows.CloseHandle(windows.Handle(h))
		return 0, fmt.Errorf("%s 失败: %v", step, e)
	}

	// SECURITY_WORLD_SID_AUTHORITY = {0,0,0,0,0,1}
	auth := [6]byte{0, 0, 0, 0, 0, 1}
	var sid uintptr
	r, _, e := procAllocateAndInitializeSid.Call(
		uintptr(unsafe.Pointer(&auth)), 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&sid)),
	)
	if r == 0 {
		return fail("AllocateAndInitializeSid", e)
	}
	defer procFreeSid.Call(sid)

	acl := make([]byte, 1024)
	if r, _, e = procInitializeAcl.Call(uintptr(unsafe.Pointer(&acl[0])), uintptr(len(acl)), aclRevision); r == 0 {
		return fail("InitializeAcl", e)
	}
	if r, _, e = procAddAccessDeniedAce.Call(
		uintptr(unsafe.Pointer(&acl[0])), aclRevision, accessMaskDenyAll, sid,
	); r == 0 {
		return fail("AddAccessDeniedAce", e)
	}
	// SetSecurityInfo 返回 0 表示成功（它返回的是 Win32 错误码而不是 BOOL）
	if r, _, _ = procSetSecurityInfo.Call(
		h, seKernelObject, daclSecurityInformation, 0, 0,
		uintptr(unsafe.Pointer(&acl[0])), 0,
	); r != 0 {
		return fail("SetSecurityInfo", windows.Errno(r))
	}
	return windows.Handle(h), nil
}

// vsFixedFileInfo 对应 VS_FIXEDFILEINFO
type vsFixedFileInfo struct {
	Signature        uint32
	StrucVersion     uint32
	FileVersionMS    uint32
	FileVersionLS    uint32
	ProductVersionMS uint32
	ProductVersionLS uint32
	FileFlagsMask    uint32
	FileFlags        uint32
	FileOS           uint32
	FileType         uint32
	FileSubtype      uint32
	FileDateMS       uint32
	FileDateLS       uint32
}

// FileVersion 读取 PE 文件的固定版本信息，返回 (主版本号, 完整版本串)。
func FileVersion(path string) (int, string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, "", err
	}
	size, _, e := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p)), 0)
	if size == 0 {
		return 0, "", fmt.Errorf("取文件版本信息大小失败: %v", e)
	}
	buf := make([]byte, size)
	if r, _, e := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return 0, "", fmt.Errorf("取文件版本信息失败: %v", e)
	}
	root, _ := windows.UTF16PtrFromString("\\")
	var info unsafe.Pointer
	var infoLen uint32
	if r, _, e := procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&infoLen)),
	); r == 0 || info == nil {
		return 0, "", fmt.Errorf("解析版本信息失败: %v", e)
	}
	fi := (*vsFixedFileInfo)(info)
	if fi.Signature != 0xFEEF04BD {
		return 0, "", fmt.Errorf("版本信息签名不正确")
	}
	major := int(fi.FileVersionMS >> 16)
	minor := int(fi.FileVersionMS & 0xFFFF)
	build := int(fi.FileVersionLS >> 16)
	rev := int(fi.FileVersionLS & 0xFFFF)
	return major, fmt.Sprintf("%d.%d.%d.%d", major, minor, build, rev), nil
}

// ShellOpen 等价于易语言的 shell::执行(#打开文件, ...)，即“双击打开”。
func ShellOpen(file, workDir string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var dirPtr *uint16
	if workDir != "" {
		dirPtr, err = windows.UTF16PtrFromString(workDir)
		if err != nil {
			return err
		}
	}
	r, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(filePtr)),
		0,
		uintptr(unsafe.Pointer(dirPtr)),
		SW_SHOWNORMAL,
	)
	// ShellExecuteW 返回值 > 32 才算成功
	if r <= 32 {
		return fmt.Errorf("启动 %s 失败（ShellExecute 返回 %d）", file, r)
	}
	return nil
}

// SW_SHOWNORMAL / SW_RESTORE / SW_MINIMIZE
const (
	SW_SHOWNORMAL = 1
	SW_RESTORE    = 9
)

// IconFromICO 把内存中的 ICO 字节流里最接近 preferred 尺寸的一张图变成 HICON。
func IconFromICO(data []byte, preferred int) (windows.Handle, error) {
	if len(data) < 6 {
		return 0, fmt.Errorf("ICO 数据太短")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count <= 0 {
		return 0, fmt.Errorf("ICO 中没有图像")
	}
	type pick struct {
		size   int
		offset uint32
		length uint32
	}
	best := pick{size: -1}
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(data) {
			break
		}
		w := int(data[off])
		if w == 0 {
			w = 256
		}
		length := binary.LittleEndian.Uint32(data[off+8 : off+12])
		offset := binary.LittleEndian.Uint32(data[off+12 : off+16])
		if int(offset)+int(length) > len(data) {
			continue
		}
		// 先找 >= preferred 里最小的，找不到再退回最大的
		better := false
		if best.size < 0 {
			better = true
		} else if w >= preferred && (best.size < preferred || w < best.size) {
			better = true
		} else if best.size < preferred && w > best.size {
			better = true
		}
		if better {
			best = pick{size: w, offset: offset, length: length}
		}
	}
	if best.size < 0 {
		return 0, fmt.Errorf("ICO 中没有可用图像")
	}
	ptr := unsafe.Pointer(&data[best.offset])
	h, _, e := procCreateIconFromResourceEx.Call(
		uintptr(ptr), uintptr(best.length), 1 /*fIcon*/, 0x00030000,
		0, 0, 0,
	)
	if h == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx 失败: %v", e)
	}
	return windows.Handle(h), nil
}

// DestroyIconHandle 释放 HICON。
func DestroyIconHandle(h windows.Handle) {
	if h != 0 {
		procDestroyIcon.Call(uintptr(h))
	}
}

// ActivateExistingWindow 找到已运行实例的窗口并拉到前台（用于单实例）。
func ActivateExistingWindow(title, class string) bool {
	var hwnd uintptr
	if title != "" {
		t, err := windows.UTF16PtrFromString(title)
		if err == nil {
			hwnd, _, _ = procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
		}
	}
	if hwnd == 0 && class != "" {
		c, err := windows.UTF16PtrFromString(class)
		if err == nil {
			hwnd, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(c)), 0)
		}
	}
	if hwnd == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, SW_RESTORE)
	}
	procShowWindow.Call(hwnd, SW_SHOWNORMAL)
	procSetForegroundWindow.Call(hwnd)
	return true
}

// RoundedCornersSupported 判断系统自己会不会给无边框窗口加圆角。
// Windows 11（build >= 22000）由 DWM 自动处理，圆角带抗锯齿，比 SetWindowRgn 好看得多；
// Windows 10 不会，得自己裁。
func RoundedCornersSupported() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	build, _, err := k.GetStringValue("CurrentBuildNumber")
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(build)
	if err != nil {
		return false
	}
	return n >= 22000
}

// ApplyRoundedCorners 用窗口区域把无边框窗口裁成圆角（Windows 10 用）。
// radius 是逻辑像素（96 DPI 下的值），会按窗口 DPI 放大。
func ApplyRoundedCorners(title string, radius int) error {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return fmt.Errorf("找不到窗口：%s", title)
	}
	var wr rect
	if r, _, e := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); r == 0 {
		return fmt.Errorf("GetWindowRect 失败: %v", e)
	}
	w, h := int(wr.Right-wr.Left), int(wr.Bottom-wr.Top)
	if w <= 0 || h <= 0 {
		return fmt.Errorf("窗口尺寸异常: %dx%d", w, h)
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	r := radius * int(dpi) / 96
	if r < 2 {
		r = 2
	}
	// +1 让 1:1 边界刚好盖住整窗，避免右侧/底部露一条缝
	rgn, _, e := procCreateRoundRectRgn.Call(0, 0, uintptr(w+1), uintptr(h+1), uintptr(r*2), uintptr(r*2))
	if rgn == 0 {
		return fmt.Errorf("CreateRoundRectRgn 失败: %v", e)
	}
	// 成功时区域归系统所有，不能再 DeleteObject
	if r2, _, e2 := procSetWindowRgn.Call(hwnd, rgn, 1); r2 == 0 {
		return fmt.Errorf("SetWindowRgn 失败: %v", e2)
	}
	return nil
}

// monitorInfo 对应 MONITORINFO（只用到前两个 RECT）
type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

// SetWindowHeight 把标题匹配的窗口高度改成 h；如果底边会超出屏幕工作区，就往上挪。
// 用于「目录设置」展开/收起时让窗口自己长高、缩回。
func SetWindowHeight(title string, h int) error {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return fmt.Errorf("找不到窗口：%s", title)
	}
	var wr rect
	if r, _, e := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); r == 0 {
		return fmt.Errorf("GetWindowRect 失败: %v", e)
	}
	width := int(wr.Right - wr.Left)

	top := int(wr.Top)
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, 2 /*MONITOR_DEFAULTTONEAREST*/)
	if monitor != 0 {
		mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		if r, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); r != 0 {
			workTop := int(mi.RcWork.Top)
			workBottom := int(mi.RcWork.Bottom)
			if top+h > workBottom {
				top = workBottom - h
			}
			if top < workTop {
				top = workTop
			}
		}
	}
	const swpNoZOrder, swpNoActivate = 0x0004, 0x0010
	if r, _, e := procSetWindowPos.Call(
		hwnd, 0, uintptr(int(wr.Left)), uintptr(top), uintptr(width), uintptr(h),
		swpNoZOrder|swpNoActivate,
	); r == 0 {
		return fmt.Errorf("SetWindowPos 失败: %v", e)
	}
	return nil
}

// 窗口消息（设置图标用）
const (
	WM_SETICON = 0x0080
	ICON_SMALL = 0
	ICON_BIG   = 1
)

// SetWindowIcons 给窗口显式设置大小图标，并用 app.ico 里的多尺寸图。
// 不设的话无边框窗口根本没有窗口图标（WM_GETICON 返回 0），
// 任务栏、缩略图预览、Alt+Tab 就会各自去猜、还会吃到系统缓存里的旧图。
// 返回的 HICON 句柄在窗口销毁前不能释放，调用方负责持有。
func SetWindowIcons(title string, icoData []byte) (big, small windows.Handle, err error) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, 0, err
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return 0, 0, fmt.Errorf("找不到窗口：%s", title)
	}
	bigIcon, err := IconFromICO(icoData, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("大图标加载失败: %w", err)
	}
	smallIcon, err := IconFromICO(icoData, 16)
	if err != nil {
		DestroyIconHandle(bigIcon)
		return 0, 0, fmt.Errorf("小图标加载失败: %w", err)
	}
	if r, _, e := procSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, uintptr(bigIcon)); r != 0 {
		_ = e
	}
	procSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, uintptr(smallIcon))
	return bigIcon, smallIcon, nil
}

// NormalizeErr 把 syscall.Errno(0) 之类的“假错误”清掉。
func NormalizeErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "The operation completed successfully") {
		return nil
	}
	return err
}

// Fatal 用系统消息框弹一个致命错误（wails 还没启动时也能用）。
func Fatal(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	const mbOK, mbIconError = 0x00000000, 0x00000010
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError)
	_ = r
}

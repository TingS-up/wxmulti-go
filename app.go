package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"

	"wxmulti/internal/core"
	"wxmulti/internal/syswin"
)

// 前端可用的三种启动模式
const (
	ModeAll    = "all"    // 全部已保存账号
	ModeNew    = "new"    // 新开窗口（扫码登录）
	ModePicked = "picked" // 复登：前端多选出来的一批账号
)

// State 是给前端渲染的完整界面状态快照。
type State struct {
	AppName      string         `json:"appName"`
	AppVersion   string         `json:"appVersion"`
	ExeDir       string         `json:"exeDir"`
	DataDir      string         `json:"dataDir"`
	ConfigDir    string         `json:"configDir"`
	InstallDir   string         `json:"installDir"`
	CacheDir     string         `json:"cacheDir"`
	InstallOK    bool           `json:"installOk"`
	InstallErr   string         `json:"installErr"`
	WeixinVer    string         `json:"weixinVer"`
	CacheOK      bool           `json:"cacheOk"`
	CacheErr     string         `json:"cacheErr"`
	Accounts     []core.Account `json:"accounts"`
	MutexOK      bool           `json:"mutexOk"`
	MutexNote    string         `json:"mutexNote"`
	Ready        bool           `json:"ready"`
	AutoDetected []string       `json:"autoDetected"`
	TrayOK       bool           `json:"trayOk"`
}

// Result 是所有操作方法的统一返回体。
type Result struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
	State State  `json:"state"`
}

func okResult(msg string, st State) Result { return Result{OK: true, Code: "ok", Msg: msg, State: st} }
func errResult(code, msg string, st State) Result {
	return Result{OK: false, Code: code, Msg: msg, State: st}
}

// App 是绑定给前端的后端对象（前端通过 window.go.main.App.* 调用）。
type App struct {
	ctx   context.Context
	store *core.Store
	tray  *syswin.Tray

	mu        sync.Mutex
	mutex     windows.Handle // 拒绝型互斥体句柄，必须活到进程结束
	mutexOK   bool
	mutexNote string
	mutexEr   string
	quitting  bool
	launching bool
	detected  []string
	iconBig   windows.Handle // 窗口大图标，窗口活多久就得留多久
	iconSmall windows.Handle
}

// NewApp 构造后端，并在窗口出来之前完成所有初始化：
// 建拒绝型互斥体（多开核心）、建托盘图标、首次运行自动识别微信目录。
// 放在 wails.Run 之前是刻意的 —— OnStartup 跑在 goroutine 里，
// 会和前端第一次 GetState 抢时间，界面会拿到半成品状态。
func NewApp() (*App, error) {
	store, err := core.NewStore()
	if err != nil {
		return nil, err
	}
	a := &App{store: store}
	// 装在 Program Files 这类目录时，数据会退到 %LOCALAPPDATA%\<app>，日志里留一行免得用户找不到
	if want := filepath.Join(store.ExeDir, "data"); !strings.EqualFold(want, store.DataDir) {
		a.logf("运行目录写不了，数据改放：%s", store.DataDir)
	}

	if h, err := syswin.CreateDenyAllMutex("XWeChat_App_Instance_Identity_Mutex_Name"); err != nil {
		if errors.Is(err, syswin.ErrMutexOccupied) {
			// 已经被别的进程用拒绝型 DACL 占住了：多开效果成立，只是句柄不归我们
			a.mutexOK = true
			a.mutexNote = "多开守护已由其他进程占位"
			a.logf("多开互斥体已被其他进程拒绝型占位（多开仍然有效）")
		} else {
			a.mutexEr = err.Error()
			a.logf("多开互斥体创建失败: %v", err)
		}
	} else {
		a.mutex = h
		a.mutexOK = true
		a.logf("多开互斥体已就位（拒绝型 DACL）")
	}

	tray, trayErr := syswin.NewTray(syswin.TrayOptions{
		Icon:    appICO,
		Tooltip: core.AppName + " · 多开守护中",
		OnShow: func() {
			runtime.WindowShow(a.ctx)
			runtime.WindowUnminimise(a.ctx)
		},
		OnQuit: func() {
			a.Quit()
		},
		AutoStartOn: func() bool { return syswin.AutoStartEnabled(core.AppName) },
		OnToggleAuto: func() {
			on := !syswin.AutoStartEnabled(core.AppName)
			if err := syswin.SetAutoStart(core.AppName, on); err != nil {
				a.logf("设置开机自启动失败: %v", err)
				return
			}
			if on {
				a.logf("开机自启动已开启")
			} else {
				a.logf("开机自启动已关闭")
			}
		},
	})
	a.tray = tray
	if trayErr != nil {
		a.logf("托盘创建失败: %v", trayErr)
	} else {
		a.logf("托盘图标已创建")
	}

	// 首次运行自动识别目录，省得用户手动找
	cfg := store.ReadConfig()
	if cfg.InstallDir == "" {
		if dir := core.DetectInstallDir(); dir != "" {
			if err := store.SetInstallDir(dir); err == nil {
				a.detected = append(a.detected, "install")
				a.logf("自动识别安装目录: %s", dir)
			}
		}
	}
	if cfg.CacheDir == "" {
		if dir := core.DetectCacheDir(); dir != "" {
			if err := store.SetCacheDir(dir); err == nil {
				a.detected = append(a.detected, "cache")
				a.logf("自动识别缓存目录: %s", dir)
			}
		}
	}
	return a, nil
}

// OnStartup 窗口就绪：保存 ctx、设窗口图标、上圆角。
func (a *App) OnStartup(ctx context.Context) {
	a.ctx = ctx

	// 显式给窗口设图标：无边框窗口默认没有窗口图标，
	// 任务栏缩略图预览/Alt+Tab 会退回系统缓存里的旧图。
	if big, small, err := syswin.SetWindowIcons(core.AppName, appICO); err != nil {
		a.logf("设置窗口图标失败: %v", err)
	} else {
		a.iconBig, a.iconSmall = big, small
		a.logf("窗口图标已设置（大 %d / 小 %d）", big, small)
	}

	a.applyRoundedCorners()
	a.logf("主窗口已就绪")
}

// OnBeforeClose 用户点关闭（或 Alt+F4）时最小化到托盘，只有主动退出才真正结束。
// 托盘没建起来就没有“回家的路”，这种情况下直接退出。
func (a *App) OnBeforeClose(ctx context.Context) bool {
	if a.quitting || a.tray == nil {
		a.mu.Lock()
		a.quitting = true
		a.mu.Unlock()
		return false
	}
	runtime.WindowHide(ctx)
	return true
}

// OnShutdown 释放托盘与互斥体。
func (a *App) OnShutdown(ctx context.Context) {
	if a.tray != nil {
		a.tray.Remove()
		a.tray = nil
	}
	if a.mutex != 0 {
		windows.CloseHandle(a.mutex)
		a.mutex = 0
	}
	if a.iconBig != 0 {
		syswin.DestroyIconHandle(a.iconBig)
		a.iconBig = 0
	}
	if a.iconSmall != 0 {
		syswin.DestroyIconHandle(a.iconSmall)
		a.iconSmall = 0
	}
}

// onSecondInstance 重复运行时把已有窗口拉出来。
func (a *App) onSecondInstance(_ options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

// logf 写一行调试日志到 data\debug.log，出问题时方便定位。
func (a *App) logf(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05") + "  " + fmt.Sprintf(format, args...) + "\r\n"
	f, err := os.OpenFile(filepath.Join(a.store.DataDir, "debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// ---------------------------------------------------------------- 状态

// GetState 读取当前状态快照。
func (a *App) GetState() State {
	return a.snapshot()
}

func (a *App) snapshot() State {
	cfg := a.store.ReadConfig()
	st := State{
		AppName:      core.AppName,
		AppVersion:   core.AppVersion,
		ExeDir:       a.store.ExeDir,
		DataDir:      a.store.DataDir,
		ConfigDir:    a.store.ConfigDir,
		InstallDir:   cfg.InstallDir,
		CacheDir:     cfg.CacheDir,
		MutexOK:      a.mutexOK,
		AutoDetected: a.detected,
		TrayOK:       a.tray != nil,
	}
	if a.mutexOK {
		st.MutexNote = "多开守护已生效"
		if a.mutexNote != "" {
			st.MutexNote = a.mutexNote
		}
	} else {
		st.MutexNote = "多开守护创建失败：" + a.mutexEr
	}
	if cfg.InstallDir != "" {
		if ver, err := core.ValidateInstallDir(cfg.InstallDir); err != nil {
			st.InstallErr = err.Error()
		} else {
			st.InstallOK = true
			st.WeixinVer = ver
		}
	}
	if cfg.CacheDir != "" {
		if err := core.ValidateCacheDir(cfg.CacheDir); err != nil {
			st.CacheErr = err.Error()
		} else {
			st.CacheOK = true
		}
	}
	if accounts, err := a.store.Accounts(); err == nil {
		st.Accounts = accounts
	} else {
		st.Accounts = []core.Account{}
	}
	st.Ready = st.InstallOK && st.CacheOK
	return st
}

// ---------------------------------------------------------------- 目录设置

// ChooseInstall 打开选择框挑微信安装目录。
func (a *App) ChooseInstall() Result {
	st := a.snapshot()
	def := st.InstallDir
	if def == "" {
		def = `C:\Program Files\Tencent\Weixin`
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "选择微信安装目录（包含 Weixin.exe 的目录）",
		DefaultDirectory:     def,
		CanCreateDirectories: false,
	})
	if err != nil {
		return errResult("dialog", "打开选择框失败："+err.Error(), st)
	}
	if strings.TrimSpace(dir) == "" {
		return errResult("cancel", "已取消", st)
	}
	if err := a.store.SetInstallDir(dir); err != nil {
		return errResult("invalid", err.Error(), a.snapshot())
	}
	ver, _ := core.ValidateInstallDir(dir)
	return okResult(fmt.Sprintf("安装目录已设置（微信 %s）", ver), a.snapshot())
}

// ChooseCache 打开选择框挑微信缓存目录。
func (a *App) ChooseCache() Result {
	st := a.snapshot()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "选择微信缓存目录（包含 xwechat_files 的目录）",
		DefaultDirectory:     st.CacheDir,
		CanCreateDirectories: false,
	})
	if err != nil {
		return errResult("dialog", "打开选择框失败："+err.Error(), st)
	}
	if strings.TrimSpace(dir) == "" {
		return errResult("cancel", "已取消", st)
	}
	if err := a.store.SetCacheDir(dir); err != nil {
		return errResult("invalid", err.Error(), a.snapshot())
	}
	return okResult("缓存目录已设置", a.snapshot())
}

// OpenConfigDir 在资源管理器里打开配置目录（程序自己的 data 目录，debug.log 就在这儿）。
func (a *App) OpenConfigDir() Result {
	st := a.snapshot()
	if err := syswin.ShellOpen(st.DataDir, ""); err != nil {
		return errResult("shell", err.Error(), st)
	}
	return okResult("已打开配置目录", st)
}

// OpenAccountsDir 在资源管理器里打开账号目录（data\allUsersConfig，一个账号一个文件夹）。
func (a *App) OpenAccountsDir() Result {
	st := a.snapshot()
	if err := syswin.ShellOpen(st.ConfigDir, ""); err != nil {
		return errResult("shell", err.Error(), st)
	}
	return okResult("已打开账号目录", st)
}

// ---------------------------------------------------------------- 启动

// Launch 启动微信。
//
//	mode = "new"    新开窗口扫码登录
//	mode = "all"    依次启动全部已保存账号
//	mode = "picked" 依次启动 accounts 里勾选的那批账号
func (a *App) Launch(mode string, accounts []string) Result {
	st := a.snapshot()
	if !st.InstallOK {
		msg := st.InstallErr
		if msg == "" {
			msg = "请先设置微信安装目录"
		}
		return errResult("invalid", msg, st)
	}
	if !st.CacheOK {
		msg := st.CacheErr
		if msg == "" {
			msg = "请先设置微信缓存目录"
		}
		return errResult("invalid", msg, st)
	}
	if !a.beginLaunch() {
		return errResult("busy", "正在启动中，请稍候", st)
	}
	defer a.endLaunch()

	cfg := a.store.ReadConfig()

	type target struct {
		label   string
		account string
	}
	var targets []target
	switch mode {
	case ModeAll:
		for _, acc := range st.Accounts {
			if acc.Ready {
				targets = append(targets, target{acc.Name, acc.Name})
			}
		}
		if len(targets) == 0 {
			return errResult("empty", "还没有可用的已保存账号，请先扫码登录并保存", a.snapshot())
		}
	case ModePicked:
		if len(accounts) == 0 {
			return errResult("empty", "先勾选要登录的账号", a.snapshot())
		}
		var bad []string
		for _, name := range accounts {
			var hit *core.Account
			for i := range st.Accounts {
				if st.Accounts[i].Name == name {
					hit = &st.Accounts[i]
					break
				}
			}
			if hit == nil {
				bad = append(bad, name+"（不存在）")
				continue
			}
			if !hit.Ready {
				bad = append(bad, name+"（配置不完整）")
				continue
			}
			targets = append(targets, target{name, name})
		}
		if len(targets) == 0 {
			return errResult("invalid", "选中的账号都不能用："+strings.Join(bad, "、"), a.snapshot())
		}
		for _, b := range bad {
			a.logf("跳过 %s", b)
		}
	default:
		targets = append(targets, target{"新建扫码", ""})
	}

	started := 0
	var failures []string
	for i, t := range targets {
		runtime.EventsEmit(a.ctx, "launch:progress", map[string]any{
			"index": i + 1,
			"total": len(targets),
			"name":  t.label,
			"done":  false,
		})
		warns, err := a.store.Launch(cfg.InstallDir, cfg.CacheDir, t.account)
		// 清理旧会话被微信占用是多开时的常态，只记日志，不打扰界面
		for _, w := range warns {
			a.logf("启动 %s 提示（多开时清理失败属正常）: %s", t.label, w)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s：%s", t.label, err.Error()))
			continue
		}
		started++
	}
	runtime.EventsEmit(a.ctx, "launch:progress", map[string]any{
		"index": len(targets),
		"total": len(targets),
		"name":  "",
		"done":  true,
	})

	newState := a.snapshot()
	if started == 0 {
		detail := "启动失败"
		if len(failures) > 0 {
			detail += "：" + failures[0]
		}
		return errResult("failed", detail, newState)
	}
	msg := fmt.Sprintf("已启动 %d 个微信实例", started)
	if len(failures) > 0 {
		msg += fmt.Sprintf("，%d 个失败（%s）", len(failures), failures[0])
	}
	return okResult(msg, newState)
}

func (a *App) beginLaunch() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.launching {
		return false
	}
	a.launching = true
	return true
}

func (a *App) endLaunch() {
	a.mu.Lock()
	a.launching = false
	a.mu.Unlock()
}

// ---------------------------------------------------------------- 账号

// SaveAccount 把当前微信缓存目录里的会话保存为命名账号。
func (a *App) SaveAccount(name string, overwrite bool) Result {
	st := a.snapshot()
	if !st.CacheOK {
		msg := st.CacheErr
		if msg == "" {
			msg = "请先设置微信缓存目录"
		}
		return errResult("invalid", msg, st)
	}
	acc, sourceNote, err := a.store.SaveAccount(name, overwrite)
	if err != nil {
		if errors.Is(err, core.ErrAccountExists) {
			return errResult("exists", "已存在同名账号", a.snapshot())
		}
		a.logf("保存账号失败: %v", err)
		return errResult("failed", err.Error(), a.snapshot())
	}
	msg := "已保存账号：" + acc.Name
	if sourceNote != "" {
		msg += "（来源：" + sourceNote + "）"
		a.logf("保存账号 %s 用了备用来源: %s", name, sourceNote)
	}
	return okResult(msg, a.snapshot())
}

// DeleteAccount 删除已保存账号。
func (a *App) DeleteAccount(name string) Result {
	if err := a.store.DeleteAccount(name); err != nil {
		return errResult("failed", err.Error(), a.snapshot())
	}
	return okResult("已删除账号："+name, a.snapshot())
}

// ---------------------------------------------------------------- 窗口

// HideToTray 隐藏到托盘。
func (a *App) HideToTray() {
	runtime.WindowHide(a.ctx)
}

// Quit 真正退出程序。
//
// 托盘图标在调 runtime.Quit 之前就先摘掉：退出过程中 WebView 拆起来要一点时间，
// 等 OnShutdown 再删的话，进程都退了图标还可能留在通知区域里，要等鼠标划过才消失。
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	tray := a.tray
	a.tray = nil
	a.mu.Unlock()

	if tray != nil {
		tray.Remove()
		a.logf("托盘图标已移除")
	}
	runtime.Quit(a.ctx)
}

// FitHeight 把窗口高度调整到前端算出来的内容高度（目录展开、账号增减时都会调）。
// 夹在 [winHeightMin, winHeightMax] 之间，别在这里写死数字，改 main.go 那份常量就够；
// 窗口底边超出屏幕时 syswin 会把它往上挪。
func (a *App) FitHeight(h int) {
	req := h
	if h < winHeightMin {
		h = winHeightMin
	}
	if h > winHeightMax {
		h = winHeightMax
	}
	a.logf("FitHeight 请求=%d 实际=%d", req, h)
	if err := syswin.SetWindowHeight(core.AppName, h); err != nil {
		a.logf("调整窗口高度失败: %v", err)
		return
	}
	a.applyRoundedCorners()
}

// LogJS 给前端 debug 用：把 JS 里量出来的数字直接写进 data\debug.log，
// 省得为了看一眼数值去开 devtools。
func (a *App) LogJS(msg string) {
	a.logf("[js] %s", msg)
}

// applyRoundedCorners 给无边框窗口加圆角。
// Windows 11 上由 DWM 自动做（带抗锯齿），Windows 10 上只能自己用 SetWindowRgn 裁。
func (a *App) applyRoundedCorners() {
	if syswin.RoundedCornersSupported() {
		return
	}
	if err := syswin.ApplyRoundedCorners(core.AppName, winCornerRadius); err != nil {
		a.logf("窗口圆角处理失败: %v", err)
	}
}

// RevealInExplorer 打开某个路径（用于调试/定位）。
func (a *App) RevealInExplorer(path string) Result {
	st := a.snapshot()
	if path == "" {
		return errResult("invalid", "路径为空", st)
	}
	if err := exec.Command("explorer.exe", path).Start(); err != nil {
		return errResult("shell", err.Error(), st)
	}
	return okResult("已打开", st)
}

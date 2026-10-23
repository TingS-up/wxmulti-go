package main

import (
	"embed"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	win32 "golang.org/x/sys/windows"

	"wxmulti/internal/core"
	"wxmulti/internal/syswin"
)

//go:embed all:frontend
var assets embed.FS

//go:embed app.ico
var appICO []byte

const (
	// 360 = 原来的 480 减掉 1/4。窗口不允许缩放，宽高都靠前端量内容来定。
	winWidth          = 360
	winHeight         = 520 // 初始高度，之后前端按内容自适应
	winHeightExpanded = 664
	// 分段控件搬进标题栏后，「新开窗口」那页内容只有 220 上下，下限给 200 就够
	winHeightMin    = 200
	winHeightMax    = 900
	winCornerRadius = 14 // 逻辑像素，Win10 自己裁圆角时的半径
)

// singletonGuard 是本进程的单实例互斥体句柄，必须活到进程结束
var singletonGuard win32.Handle

func main() {
	// 单实例：要在 NewApp 之前判掉，否则第二个进程会白建一个托盘图标、
	// 还会因为拒绝型互斥体已被占而打出一条误导性的“创建失败”日志。
	guard, already, err := syswin.AcquireSingleton(core.AppTag + "-guard")
	if err != nil {
		syswin.Fatal(core.AppName, "单实例检查失败：\n"+err.Error())
		return
	}
	if already {
		syswin.ActivateExistingWindow(core.AppName, "wailsWindow")
		return
	}
	singletonGuard = guard

	app, err := NewApp()
	if err != nil {
		syswin.Fatal(core.AppName, "初始化失败：\n"+err.Error())
		return
	}

	// WebView2 的缓存默认落在 %APPDATA%\<exe 文件名>\ 下（70 MB 起，而且每换一次 exe 名字就多一份）。
	// 那是纯缓存、不是用户设置，不该塞进漫游目录，统一放到 %LOCALAPPDATA%\微信多开助手\webview2。
	webviewData := ""
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		webviewData = filepath.Join(base, core.AppName, "webview2")
	}

	// 光换目录只能挪走垃圾，垃圾本身还在：Chromium 的组件更新器会把 Widevine DRM(21.9 MB)、
	// 组件包缓存(21.5 MB)、广告过滤规则(12.3 MB)、语音识别(2.6 MB) 这些本工具一个都用不上的
	// 东西下进 user-data 目录。这里把它们关掉，目录从 70 MB 级别压到 10 MB 以内。
	applyWebviewArgs()

	err = wails.Run(&options.App{
		Title:            core.AppName,
		Width:            winWidth,
		Height:           winHeight,
		MinWidth:         winWidth,
		MaxWidth:         winWidth,
		MinHeight:        winHeightMin,
		MaxHeight:        winHeightMax,
		DisableResize:    true,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 244, G: 247, B: 245, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.OnStartup,
		OnBeforeClose:    app.OnBeforeClose,
		OnShutdown:       app.OnShutdown,
		Bind:             []interface{}{app},
		Windows: &windows.Options{
			Theme:                windows.SystemDefault,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			WebviewUserDataPath:  webviewData,
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               core.AppTag,
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
	})
	if err != nil {
		syswin.Fatal(core.AppName, "运行失败：\n"+err.Error())
		os.Exit(1)
	}
}

// applyWebviewArgs 关掉 WebView2 里跟本工具完全无关的后台行为。
// Wails v2 的 windows.Options 没有暴露浏览器参数，WebView2 认的是环境变量
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS，在创建环境之前设进本进程即可，浏览器子进程会继承。
func applyWebviewArgs() {
	const key = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
	args := "--disable-component-update " + // 不再下载 Widevine / 过滤规则 / 语音包 / 组件 crx
		"--disable-background-networking " + // 组件更新、指标上报、变体种子全部停掉
		"--disk-cache-size=33554432 " + // HTTP 缓存封顶 32 MB（页面全是本地资源，实际用不到 1 MB）
		"--no-first-run --no-default-browser-check" // 少写几个首次运行状态文件
	if old := os.Getenv(key); old != "" {
		args = old + " " + args
	}
	_ = os.Setenv(key, args)
}

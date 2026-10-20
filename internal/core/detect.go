package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// DetectInstallDir 找微信 4.x 的安装目录。装了系统目录（Program Files 之类）也要能认出来，
// 所以来源按可靠性铺了好几条，每条候选都要过 ValidateInstallDir 才算数：
//  1. 微信自己写的 InstallPath / InstallDir / InstallLocation（HKCU、HKLM，64/32 位视图都看）
//  2. App Paths\Weixin.exe 的默认值（安装包通常会给它注册 exe 全路径）
//  3. 卸载表：InstallLocation → DisplayIcon → UninstallString（装在系统目录时经常只有 DisplayIcon）
//  4. 常见根目录下的 Tencent\Weixin、Weixin 之类，直接 stat 一把
func DetectInstallDir() string {
	for _, dir := range installCandidates() {
		if _, err := ValidateInstallDir(dir); err == nil {
			return normalizeDir(dir)
		}
	}
	return ""
}

func installCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = normalizeDir(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	views := []uint32{0, registry.WOW64_32KEY}
	roots := []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE}

	// 1. 微信自己的键
	for _, root := range roots {
		for _, view := range views {
			k, err := registry.OpenKey(root, `Software\Tencent\Weixin`, registry.READ|view)
			if err != nil {
				continue
			}
			for _, name := range []string{"InstallPath", "InstallDir", "InstallLocation"} {
				if v, _, err := k.GetStringValue(name); err == nil && v != "" {
					add(pathToDir(v))
				}
			}
			k.Close()
		}
	}

	// 2. App Paths
	for _, root := range roots {
		for _, view := range views {
			k, err := registry.OpenKey(root,
				`Software\Microsoft\Windows\CurrentVersion\App Paths\Weixin.exe`, registry.READ|view)
			if err != nil {
				continue
			}
			if v, _, err := k.GetStringValue(""); err == nil {
				add(pathToDir(v))
			}
			k.Close()
		}
	}

	// 3. 卸载表
	const key64 = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	const key32 = `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`
	type uninstRoot struct {
		root registry.Key
		path string
	}
	for _, ur := range []uninstRoot{
		{registry.CURRENT_USER, key64},
		{registry.CURRENT_USER, key32},
		{registry.LOCAL_MACHINE, key64},
		{registry.LOCAL_MACHINE, key32},
	} {
		parent, err := registry.OpenKey(ur.root, ur.path, registry.READ)
		if err != nil {
			continue
		}
		names, err := parent.ReadSubKeyNames(-1)
		parent.Close()
		if err != nil {
			continue
		}
		for _, name := range names {
			sk, err := registry.OpenKey(ur.root, ur.path+`\`+name, registry.READ)
			if err != nil {
				continue
			}
			display, _, _ := sk.GetStringValue("DisplayName")
			loc, _, _ := sk.GetStringValue("InstallLocation")
			icon, _, _ := sk.GetStringValue("DisplayIcon")
			unins, _, _ := sk.GetStringValue("UninstallString")
			sk.Close()
			if !looksLikeWeixin(display) {
				continue
			}
			add(pathToDir(loc))
			add(pathToDir(icon))
			add(pathToDir(unins))
		}
	}

	// 4. 常见安装根目录
	for _, base := range commonInstallRoots() {
		for _, sub := range []string{`Tencent\Weixin`, `Weixin`, `Tencent\WeChat`, `WeChat`} {
			add(filepath.Join(base, sub))
		}
	}
	return out
}

// pathToDir 把注册表里五花八门的写法归成目录：
// 带引号、带图标序号（`"...\Weixin.exe",0`）、直接给 exe 路径，都要能处理。
func pathToDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, ","); i > 0 {
		if _, err := strconv.Atoi(strings.TrimSpace(p[i+1:])); err == nil {
			p = p[:i]
		}
	}
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if strings.EqualFold(filepath.Ext(p), ".exe") {
		return filepath.Dir(p)
	}
	return p
}

func commonInstallRoots() []string {
	var out []string
	for _, env := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if v := os.Getenv(env); v != "" {
			out = append(out, v)
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		out = append(out, filepath.Join(v, "Programs"))
	}
	return out
}

func looksLikeWeixin(display string) bool {
	if display == "" {
		return false
	}
	lower := strings.ToLower(display)
	return strings.Contains(display, "微信") ||
		strings.Contains(lower, "weixin") ||
		strings.Contains(lower, "wechat")
}

// DetectCacheDir 找微信的聊天记录目录，也就是包含 xwechat_files 的那个目录。
// 三个来源，按可靠性排：
//  1. %APPDATA%\Tencent\xwechat\config\*.ini —— 微信自己把数据根目录明文写在这（最准）
//  2. 注册表 HKCU\Software\Tencent\Weixin 里的 FileSavePath / DataPath / SavePath
//  3. 常见默认位置（Documents\xwechat_files 等）兜底
//
// 候选路径本身、它的父目录、祖父目录都会试一遍，因为不同版本记的层级不一样
// （有的记数据根目录，有的直接记到 xwechat_files）。
func DetectCacheDir() string {
	var candidates []string

	// 1. 微信写的 ini
	if appData := os.Getenv("APPDATA"); appData != "" {
		cfgDir := filepath.Join(appData, "Tencent", "xwechat", "config")
		if entries, err := os.ReadDir(cfgDir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".ini") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(cfgDir, e.Name()))
				if err != nil {
					continue
				}
				if p := strings.Trim(strings.TrimSpace(decodeText(raw)), `"`); p != "" {
					candidates = append(candidates, p)
				}
			}
		}
	}

	// 2. 注册表里的备选值
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Tencent\Weixin`, registry.READ); err == nil {
		for _, name := range []string{"FileSavePath", "DataPath", "SavePath", "FileSaveDir"} {
			if v, _, err := k.GetStringValue(name); err == nil && v != "" {
				candidates = append(candidates, strings.Trim(strings.TrimSpace(v), `"`))
			}
		}
		k.Close()
	}

	// 3. 默认位置兜底
	home, _ := os.UserHomeDir()
	for _, base := range []string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "文档"),
		home,
	} {
		candidates = append(candidates, filepath.Join(base, "xwechat_files"))
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		// 候选本身 / 父目录 / 祖父目录，都试一遍
		for i := 0; i < 3; i++ {
			if ValidateCacheDir(clean) == nil {
				return normalizeDir(clean)
			}
			parent := filepath.Dir(clean)
			if parent == clean {
				break
			}
			clean = parent
		}
	}
	return ""
}

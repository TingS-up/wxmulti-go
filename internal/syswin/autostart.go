package syswin

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// 开机自启动走 HKCU 的 Run 键：不需要管理员权限，也不影响别的用户。
const autoStartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// AutoStartEnabled 查开机自启动开着没有（HKCU Run 里有同名值就算开着）。
func AutoStartEnabled(appName string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartRunKey, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(appName)
	return err == nil && v != ""
}

// SetAutoStart 开/关开机自启动：写一个指向当前 exe 的带引号路径，关就是删掉这个值。
func SetAutoStart(appName string, on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autoStartRunKey,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 Run 注册表失败: %w", err)
	}
	defer k.Close()

	if !on {
		if err := k.DeleteValue(appName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除自启动项失败: %w", err)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取 exe 路径失败: %w", err)
	}
	if err := k.SetStringValue(appName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("写入自启动项失败: %w", err)
	}
	return nil
}

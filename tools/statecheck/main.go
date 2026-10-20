// statecheck 排障用：按指定 exe 目录打印后端状态（账号列表/配置/校验结果）。
// 用法： go run ./tools/statecheck "已编译成品"
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"wxmulti/internal/core"
)

func main() {
	dir := "已编译成品"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	s, err := core.NewStoreAt(dir)
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	cfg := s.ReadConfig()
	fmt.Printf("exeDir=%s\n", s.ExeDir)
	fmt.Printf("data=%s\n", s.DataDir)
	fmt.Printf("config.ini: install=%q cache=%q\n", cfg.InstallDir, cfg.CacheDir)
	if ver, err := core.ValidateInstallDir(cfg.InstallDir); err != nil {
		fmt.Println("install 校验失败:", err)
	} else {
		fmt.Println("install 校验通过，微信版本:", ver)
	}
	if err := core.ValidateCacheDir(cfg.CacheDir); err != nil {
		fmt.Println("cache 校验失败:", err)
	} else {
		fmt.Println("cache 校验通过:", core.WeixinConfigDir(cfg.CacheDir))
	}
	accs, err := s.Accounts()
	if err != nil {
		fmt.Println("枚举账号失败:", err)
		return
	}
	raw, _ := json.MarshalIndent(accs, "", "  ")
	fmt.Printf("accounts(%d)=\n%s\n", len(accs), raw)
}

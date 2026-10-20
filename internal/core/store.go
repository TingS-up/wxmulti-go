// Package core 是工具的业务内核：配置、账号仓库、启动流程。
// 行为与原版易语言源码 1.2.e 一一对应（见 REVERSE_NOTES.md）。
package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"wxmulti/internal/syswin"
)

const (
	// WeixinExe 微信 4.x 主程序名
	WeixinExe = "Weixin.exe"
	// MinWeixinVersion 原版要求主版本号 >= 4（只支持 xwechat 内核）
	MinWeixinVersion = 4

	globalConfigName = "global_config"
	globalConfigCRC  = "global_config.crc"
	accountMetaName  = "_meta.json"

	// LaunchSettleDelay 与原版「程序_延时(2,1)」等价：启动后等 2 秒再清理预置会话文件
	LaunchSettleDelay = 2 * time.Second
)

var (
	// ErrAccountExists 账号目录已存在且未允许覆盖
	ErrAccountExists = errors.New("账号已存在")
	// ErrNotConfigured 安装目录/缓存目录未配置或无效
	ErrNotConfigured = errors.New("微信目录未配置或无效")
	// ErrAccountNotReady 账号配置不完整（缺 global_config）
	ErrAccountNotReady = errors.New("账号配置不完整")
	// ErrNoSessionFile 缓存目录里还没有会话文件
	ErrNoSessionFile = errors.New("没有可保存的会话")
)

// Config 对应 data/config.ini 的内容
type Config struct {
	InstallDir string
	CacheDir   string
}

// Account 是一个已保存的登录会话
type Account struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Note    string `json:"note"`
	SavedAt string `json:"savedAt"`
	Size    int64  `json:"size"`
}

type accountMeta struct {
	Name     string `json:"name"`
	SavedAt  string `json:"savedAt"`
	Source   string `json:"source"`
	Weixin   string `json:"weixinVersion"`
	AppBuild string `json:"appBuild"`
}

// Store 管理 exe 同级的 data 目录
type Store struct {
	ExeDir    string // 运行目录（exe 所在目录），等价于易语言的「取运行目录」
	DataDir   string // <运行目录>\data
	ConfigDir string // <运行目录>\data\allUsersConfig
	IniPath   string // <运行目录>\data\config.ini
}

// NewStore 按 exe 所在目录初始化目录结构。
func NewStore() (*Store, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("取运行目录失败: %w", err)
	}
	return NewStoreAt(filepath.Dir(exe))
}

// NewStoreAt 用指定目录当「运行目录」（测试和排障用）。
//
// 装在 Program Files 这类没有写权限的目录时，exe 旁边建不出 data 目录 ——
// 这种情况退到 %LOCALAPPDATA%\<AppName>，功能一模一样，只是数据不放在 exe 旁边。
// ExeDir 仍然报真实的 exe 位置，界面上看的到还是那个目录。
func NewStoreAt(exeDir string) (*Store, error) {
	s := storeAt(exeDir)
	err := s.ensureDirs()
	if err == nil {
		return s, nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return nil, err
	}
	alt := storeAt(filepath.Join(base, AppName))
	if aerr := alt.ensureDirs(); aerr != nil {
		return nil, err
	}
	alt.ExeDir = exeDir
	return alt, nil
}

func storeAt(dir string) *Store {
	return &Store{
		ExeDir:    dir,
		DataDir:   filepath.Join(dir, "data"),
		ConfigDir: filepath.Join(dir, "data", "allUsersConfig"),
		IniPath:   filepath.Join(dir, "data", "config.ini"),
	}
}

func (s *Store) ensureDirs() error {
	for _, dir := range []string{s.DataDir, s.ConfigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// WeixinConfigDir 返回微信会话配置目录：<缓存目录>\xwechat_files\all_users\config
func WeixinConfigDir(cacheDir string) string {
	return filepath.Join(strings.TrimRight(cacheDir, `\/`), "xwechat_files", "all_users", "config")
}

// ---------------------------------------------------------------- config.ini

// ReadConfig 读取 config.ini，兼容老版易语言写出的 GBK 编码，
// 也兼容记事本另存为时加的 UTF-8/UTF-16 BOM。
func (s *Store) ReadConfig() Config {
	cfg := Config{}
	raw, err := os.ReadFile(s.IniPath)
	if err != nil {
		return cfg
	}
	text := decodeText(raw)
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.Trim(trimmed, "[]"))
			continue
		}
		if section != "path" {
			continue
		}
		k, v, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "install":
			cfg.InstallDir = normalizeDir(v)
		case "cache":
			cfg.CacheDir = normalizeDir(v)
		}
	}
	return cfg
}

func (s *Store) writeConfig(cfg Config) error {
	var b strings.Builder
	b.WriteString("[path]\r\n")
	b.WriteString("install=" + cfg.InstallDir + "\r\n")
	b.WriteString("cache=" + cfg.CacheDir + "\r\n")
	tmp := s.IniPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("写配置失败: %w", err)
	}
	if err := os.Rename(tmp, s.IniPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写配置失败: %w", err)
	}
	return nil
}

// SetInstallDir 校验并写入微信安装目录。
func (s *Store) SetInstallDir(dir string) error {
	dir = normalizeDir(dir)
	if _, err := ValidateInstallDir(dir); err != nil {
		return err
	}
	cfg := s.ReadConfig()
	cfg.InstallDir = dir
	return s.writeConfig(cfg)
}

// SetCacheDir 校验并写入微信缓存目录。
func (s *Store) SetCacheDir(dir string) error {
	dir = normalizeDir(dir)
	if err := ValidateCacheDir(dir); err != nil {
		return err
	}
	cfg := s.ReadConfig()
	cfg.CacheDir = dir
	return s.writeConfig(cfg)
}

func normalizeDir(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if !strings.HasSuffix(p, string(os.PathSeparator)) && !strings.HasSuffix(p, `\`) {
		p += string(os.PathSeparator)
	}
	return p
}

// decodeText 解码配置文本：先处理 BOM，再在 UTF-8 / GBK 之间取正确的那个。
func decodeText(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		raw = raw[3:]
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		units := make([]uint16, 0, len(raw)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
		}
		return string(utf16.Decode(units))
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		units := make([]uint16, 0, len(raw)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			units = append(units, binary.BigEndian.Uint16(raw[i:i+2]))
		}
		return string(utf16.Decode(units))
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().String(string(raw)); err == nil {
		return decoded
	}
	return string(raw)
}

// ValidateInstallDir 检查 Weixin.exe 存在且主版本号 >= 4，返回版本串。
func ValidateInstallDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", ErrNotConfigured
	}
	exe := filepath.Join(dir, WeixinExe)
	if !fileExists(exe) {
		return "", fmt.Errorf("目录中没有 %s：%s", WeixinExe, exe)
	}
	major, version, err := syswin.FileVersion(exe)
	if err != nil {
		return "", fmt.Errorf("读取 %s 版本失败: %w", WeixinExe, err)
	}
	if major < MinWeixinVersion {
		return version, fmt.Errorf("微信版本过低（%s），只支持 4.x", version)
	}
	return version, nil
}

// ValidateCacheDir 检查缓存目录里是否存在 xwechat_files\all_users\config
func ValidateCacheDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return ErrNotConfigured
	}
	cfgDir := WeixinConfigDir(dir)
	st, err := os.Stat(cfgDir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("目录中没有 xwechat_files\\all_users\\config：%s", cfgDir)
	}
	return nil
}

// ---------------------------------------------------------------- 账号仓库

// Accounts 枚举 data\allUsersConfig 下的账号目录（对应原版「多开_配置枚举」）。
func (s *Store) Accounts() ([]Account, error) {
	entries, err := os.ReadDir(s.ConfigDir)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, s.describe(e.Name()))
	}
	// 按保存时间倒序（最近保存的排最前），时间一样再按名字
	sort.Slice(out, func(i, j int) bool {
		if out[i].SavedAt != out[j].SavedAt {
			return out[i].SavedAt > out[j].SavedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) describe(name string) Account {
	dir := filepath.Join(s.ConfigDir, name)
	acc := Account{Name: name, Ready: true}
	var size int64
	for _, f := range []string{globalConfigName, globalConfigCRC} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil || st.IsDir() {
			acc.Ready = false
			acc.Note = "缺少 " + f
			return acc
		}
		size += st.Size()
	}
	acc.Size = size
	if raw, err := os.ReadFile(filepath.Join(dir, accountMetaName)); err == nil {
		var m accountMeta
		if json.Unmarshal(raw, &m) == nil {
			acc.SavedAt = m.SavedAt
			if m.Weixin != "" {
				acc.Note = "微信 " + m.Weixin
			}
		}
	}
	if acc.SavedAt == "" {
		if st, err := os.Stat(filepath.Join(dir, globalConfigName)); err == nil {
			acc.SavedAt = st.ModTime().Format("2006-01-02 15:04")
		}
	}
	return acc
}

// ValidateAccountName 校验账号名，非法字符与原版一致。
func ValidateAccountName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("账号名称不能为空")
	}
	if utf8.RuneCountInString(name) > 32 {
		return errors.New("账号名称不能超过 32 个字符")
	}
	if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
		return errors.New("账号名称不能以下划线或点开头")
	}
	for _, c := range []string{`/`, `\`, `:`, `*`, `?`, `<`, `>`, `|`, `"`} {
		if strings.Contains(name, c) {
			return fmt.Errorf("账号名称不能包含字符 %s", c)
		}
	}
	for _, c := range name {
		if c < 0x20 {
			return errors.New("账号名称包含控制字符")
		}
	}
	return nil
}

// sessionFilePair 是「会话文件」的一对来源候选。
// global_configX / global_configX.crc 是清理旧配置时改名留下的残骸，
// 内容就是当时那份 global_config —— 微信把原文件锁住删不掉时就会走到这个状态，
// 所以保存账号时把它当备选源，不然用户会一直报「找不到 global_config」。
type sessionFilePair struct {
	main  string
	crc   string
	label string
}

var sessionSources = []sessionFilePair{
	{globalConfigName, globalConfigCRC, ""},
	{"global_configX", "global_configX.crc", "global_configX（上次清理改名留下的副本）"},
}

// pickSessionSource 找当前可用的会话文件来源。
func pickSessionSource(dir string) (sessionFilePair, bool) {
	for _, c := range sessionSources {
		if fileExists(filepath.Join(dir, c.main)) && fileExists(filepath.Join(dir, c.crc)) {
			return c, true
		}
	}
	return sessionFilePair{}, false
}

// SaveAccount 把当前微信缓存目录里的会话保存为命名账号。
// overwrite=false 且目录已存在时返回 ErrAccountExists。
// 第二个返回值是实际使用的来源说明（用了备用源时非空），供界面提示。
func (s *Store) SaveAccount(name string, overwrite bool) (Account, string, error) {
	name = strings.TrimSpace(name)
	if err := ValidateAccountName(name); err != nil {
		return Account{}, "", err
	}
	cfg := s.ReadConfig()
	if err := ValidateCacheDir(cfg.CacheDir); err != nil {
		return Account{}, "", err
	}
	src := WeixinConfigDir(cfg.CacheDir)

	pair, ok := pickSessionSource(src)
	if !ok {
		return Account{}, "", fmt.Errorf("%w：缓存目录里没有 global_config —— "+
			"微信登录之后才写这个文件。先点「打开登录窗口」扫码登录一个微信号，再回来保存", ErrNoSessionFile)
	}

	target := filepath.Join(s.ConfigDir, name)
	if fileExists(target) {
		if !overwrite {
			return Account{}, "", ErrAccountExists
		}
		if err := os.RemoveAll(target); err != nil {
			return Account{}, "", fmt.Errorf("清理旧账号目录失败: %w", err)
		}
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return Account{}, "", fmt.Errorf("创建账号目录失败: %w", err)
	}
	pairs := []struct{ from, to string }{
		{filepath.Join(src, pair.main), filepath.Join(target, globalConfigName)},
		{filepath.Join(src, pair.crc), filepath.Join(target, globalConfigCRC)},
	}
	for _, p := range pairs {
		if err := copyFileRetry(p.from, p.to); err != nil {
			os.RemoveAll(target)
			return Account{}, "", fmt.Errorf("复制 %s 失败（微信是否正占着它？）: %w", filepath.Base(p.from), err)
		}
	}
	version, _ := ValidateInstallDir(cfg.InstallDir)
	meta := accountMeta{
		Name:     name,
		SavedAt:  time.Now().Format("2006-01-02 15:04"),
		Source:   filepath.Join(src, pair.main),
		Weixin:   version,
		AppBuild: AppBuild,
	}
	if raw, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(target, accountMetaName), raw, 0o644)
	}
	return s.describe(name), pair.label, nil
}

// DeleteAccount 删除已保存账号。
func (s *Store) DeleteAccount(name string) error {
	name = strings.TrimSpace(name)
	if err := ValidateAccountName(name); err != nil {
		return err
	}
	dir := filepath.Join(s.ConfigDir, name)
	if !fileExists(dir) {
		return fmt.Errorf("账号不存在: %s", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除账号失败: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- 启动流程

// Launch 复刻原版 微信_启动目标：
//  1. 清理微信缓存目录里的 global_config(+crc)
//  2. 若指定账号，则把保存的会话文件复制进去
//  3. ShellExecute 打开 Weixin.exe
//  4. 等 2 秒（微信已读取配置）后再清理一次，避免会话文件被别的实例复用
//
// 第一步是「尽力而为」：微信正开着的时候会独占 global_config.crc，
// 改名和删除都可能失败。原版对这两个操作的结果都没检查，清不掉照样启动；
// 这里也一样，清不掉的失败只作为 warning 返回，不拦启动。
func (s *Store) Launch(installDir, cacheDir, account string) (warnings []string, err error) {
	cfgDir := WeixinConfigDir(cacheDir)
	if st, statErr := os.Stat(cfgDir); statErr != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotConfigured, cfgDir)
	}
	if e := clearGlobalConfig(cfgDir); e != nil {
		warnings = append(warnings, "旧配置未能清理（微信可能正占着）："+e.Error())
	}
	if account != "" {
		src := filepath.Join(s.ConfigDir, account)
		for _, f := range []string{globalConfigName, globalConfigCRC} {
			if !fileExists(filepath.Join(src, f)) {
				return warnings, fmt.Errorf("%w: %s 缺少 %s", ErrAccountNotReady, account, f)
			}
		}
		for _, f := range []string{globalConfigName, globalConfigCRC} {
			if e := copyFileRetry(filepath.Join(src, f), filepath.Join(cfgDir, f)); e != nil {
				// 只把最里层的原因带给界面：os.Rename 的报错是一长串
				// 「rename A: rename B: Access is denied」，弹窗里没法看
				return warnings, fmt.Errorf("写入会话配置失败（微信正占着会话文件），先把正在运行的微信关掉再试：%s", rootCause(e))
			}
		}
	}
	exe := filepath.Join(installDir, WeixinExe)
	if !fileExists(exe) {
		return warnings, fmt.Errorf("找不到 %s", exe)
	}
	if e := syswin.ShellOpen(exe, strings.TrimRight(installDir, `\/`)); e != nil {
		return warnings, e
	}
	time.Sleep(LaunchSettleDelay)
	// 清理失败不影响启动结果：微信已经把配置读进内存了
	if e := clearGlobalConfig(cfgDir); e != nil {
		warnings = append(warnings, "启动后会话文件未能清理："+e.Error())
	}
	return warnings, nil
}

// clearGlobalConfig 对应原版 清理全局配置文件：先更名再删除。
// 微信会锁住 global_config，直接删除常常失败，改名通常还能成功。
// 每次调用都顺手清掉上次留下的 global_configX 残骸（能删就删，删不掉下次再试）。
func clearGlobalConfig(dir string) error {
	var firstErr error
	for _, orphan := range []string{"global_configX", "global_configX.crc"} {
		_ = os.Remove(filepath.Join(dir, orphan))
	}
	targets := []struct{ name, renamed string }{
		{globalConfigName, "global_configX"},
		{globalConfigCRC, "global_configX.crc"},
	}
	for _, t := range targets {
		p := filepath.Join(dir, t.name)
		if !fileExists(p) {
			continue
		}
		tmp := filepath.Join(dir, t.renamed)
		_ = os.Remove(tmp)
		renamed := false
		for i := 0; i < 5; i++ {
			if err := os.Rename(p, tmp); err == nil {
				renamed = true
				break
			}
			time.Sleep(60 * time.Millisecond)
		}
		if renamed {
			if err := os.Remove(tmp); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		// 改名也不行就硬删
		if err := os.Remove(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// copyFileRetry 带重试的文件复制（微信可能短暂占用目标文件）。
func copyFileRetry(src, dst string) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = copyFile(src, dst); err == nil {
			return nil
		}
		time.Sleep(time.Duration(80*(i+1)) * time.Millisecond)
	}
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = in.Stat(); err == nil {
		// 复制内容
	}
	buf := make([]byte, 256*1024)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
		}
		if rerr != nil {
			break
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// rootCause 取错误链最里层的那句话。
// os.Rename 之类会包成「rename A: rename B: Access is denied」，
// 界面弹窗只需要最后那句「Access is denied.」。
func rootCause(err error) string {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err.Error()
		}
		err = u
	}
}

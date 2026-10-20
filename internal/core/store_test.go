package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 装在 Program Files 这类没写权限的目录时，data 建不出来要退到 %LOCALAPPDATA%。
// 这里用「父级是文件」的路径模拟「建不出目录」，再把 LOCALAPPDATA 指到临时目录，
// 免得真去写用户目录。
func TestStoreFallbackToLocalAppData(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badExeDir := filepath.Join(blocker, "exe") // 父级是文件，MkdirAll 必然失败
	altBase := filepath.Join(tmp, "localappdata")
	t.Setenv("LOCALAPPDATA", altBase)

	s, err := NewStoreAt(badExeDir)
	if err != nil {
		t.Fatalf("应该退到 LOCALAPPDATA，却直接失败了: %v", err)
	}
	wantPrefix := filepath.Join(altBase, AppName)
	if !strings.HasPrefix(s.DataDir, wantPrefix) {
		t.Fatalf("DataDir = %s，期望落在 %s 下面", s.DataDir, wantPrefix)
	}
	if s.ExeDir != badExeDir {
		t.Fatalf("ExeDir 应保留真实路径，实际 %s", s.ExeDir)
	}
	if _, err := os.Stat(s.ConfigDir); err != nil {
		t.Fatalf("备用的账号目录没建出来: %v", err)
	}
}

// 正常情况下就该用 exe 旁边的 data，不该乱跑。
func TestStoreUsesExeDir(t *testing.T) {
	tmp := t.TempDir()
	s, err := NewStoreAt(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tmp, "data"); s.DataDir != want {
		t.Fatalf("DataDir = %s，期望 %s", s.DataDir, want)
	}
}

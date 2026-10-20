package core

import "testing"

func TestPathToDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"C:\Program Files\Tencent\Weixin"`, `C:\Program Files\Tencent\Weixin`},
		{`"C:\Program Files\Tencent\Weixin\Weixin.exe"`, `C:\Program Files\Tencent\Weixin`},
		{`"C:\Program Files\Tencent\Weixin\Weixin.exe",0`, `C:\Program Files\Tencent\Weixin`},
		{`C:\Program Files\Tencent\Weixin\Weixin.exe,0`, `C:\Program Files\Tencent\Weixin`},
		{`C:\Program Files\Tencent\Weixin`, `C:\Program Files\Tencent\Weixin`},
		{`  "C:\Program Files (x86)\Tencent\WeChat"  `, `C:\Program Files (x86)\Tencent\WeChat`},
		{``, ``},
	}
	for _, c := range cases {
		if got := pathToDir(c.in); got != c.want {
			t.Errorf("pathToDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectInstallDir(t *testing.T) {
	dir := DetectInstallDir()
	t.Logf("DetectInstallDir = %q", dir)
	if dir == "" {
		t.Skip("本机没识别到微信安装目录")
	}
	if _, err := ValidateInstallDir(dir); err != nil {
		t.Fatalf("识别出来的目录过不了校验: %v", err)
	}
}

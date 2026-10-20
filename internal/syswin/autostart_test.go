package syswin

import "testing"

// 开机自启动就是 HKCU Run 里的一个值：
// 打开 → 写入指向当前 exe 的带引号路径；关闭 → 删除。
// 测试用独立的值名，跑完把它删掉，不碰用户真实的启动项。
func TestAutoStartToggle(t *testing.T) {
	const name = "微信多开助手_单元测试"

	if AutoStartEnabled(name) {
		t.Fatalf("测试值 %q 一开始就存在，可能上次没清干净", name)
	}
	if err := SetAutoStart(name, true); err != nil {
		t.Fatalf("开启失败: %v", err)
	}
	if !AutoStartEnabled(name) {
		t.Fatalf("开启了但读回来是关的")
	}
	if err := SetAutoStart(name, false); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if AutoStartEnabled(name) {
		t.Fatalf("关闭了但读回来还是开的")
	}
	// 重复关闭不该报错
	if err := SetAutoStart(name, false); err != nil {
		t.Fatalf("重复关闭报错了: %v", err)
	}
}

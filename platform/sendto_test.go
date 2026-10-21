//go:build windows

package platform

// 右键「发送到」快捷方式的自愈行为：程序换目录（构建目录 → 安装目录）后，
// 旧的 .lnk 仍指着失效路径，右键点了毫无反应且不会自动修好。EnsureSendToLink
// 现在每次同步都核对目标，不一致就重建。
//
// 测试隔离：sendToDir() 读 %APPDATA%，这里把它指到临时目录，不碰真实的 SendTo。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSendToLinkSelfHealsTarget(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	dir := filepath.Join(tmp, "Microsoft", "Windows", "SendTo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 SendTo 目录: %v", err)
	}

	const dev = "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"
	if err := EnsureSendToLink("我的手机", dev); err != nil {
		t.Fatalf("首次创建快捷方式: %v", err)
	}
	lnk := filepath.Join(dir, "我的手机.lnk")
	if _, err := os.Stat(lnk); err != nil {
		t.Fatalf("快捷方式应已创建: %v", err)
	}

	// 目标正确时再调一次：不应报错（走跳过路径）
	if err := EnsureSendToLink("我的手机", dev); err != nil {
		t.Fatalf("重复调用应幂等: %v", err)
	}

	// 把 .lnk 改成指向一个不存在的 exe，模拟「程序换目录」后的坏链接
	broken := `
$ws = New-Object -ComObject WScript.Shell
$s = $ws.CreateShortcut('` + psQuote(lnk) + `')
$s.TargetPath = 'C:\nowhere\gone.exe'
$s.Arguments = 'sendto --to ` + dev + `'
$s.Save()`
	if err := runHiddenPowerShell(broken, 20*time.Second); err != nil {
		t.Fatalf("构造坏链接: %v", err)
	}

	// 再次同步：应识别目标失效并重建
	if err := EnsureSendToLink("我的手机", dev); err != nil {
		t.Fatalf("自愈重建: %v", err)
	}
	if !sendToLinkMatches(lnk, helperExeForTest(t), dev) {
		t.Fatalf("坏链接未被修复，仍指向旧目标")
	}
}

// helperExeForTest 取 EnsureSendToLink 会用的同一个目标（保持口径一致）。
func helperExeForTest(t *testing.T) string {
	t.Helper()
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	if exe == "" {
		t.Fatal("取不到当前 exe 路径")
	}
	return exe
}

// 设备 ID 变了（同一显示名换了设备）也必须重建，否则右键发到错误的对端。
func TestSendToLinkRebuildsOnDeviceChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	dir := filepath.Join(tmp, "Microsoft", "Windows", "SendTo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 SendTo 目录: %v", err)
	}

	const dev1 = "1111111111111111111111111111111111111111111111111111111111111111"
	const dev2 = "2222222222222222222222222222222222222222222222222222222222222222"
	if err := EnsureSendToLink("我的手机", dev1); err != nil {
		t.Fatalf("首次创建: %v", err)
	}
	if err := EnsureSendToLink("我的手机", dev2); err != nil {
		t.Fatalf("换设备后同步: %v", err)
	}
	lnk := filepath.Join(dir, "我的手机.lnk")
	if !sendToLinkMatches(lnk, helperExeForTest(t), dev2) {
		t.Fatal("设备 ID 变更后快捷方式应指向新设备")
	}
}

// 设备名里的非法字符必须被消毒，不能造成路径穿越。
func TestSendToLinkSanitizesName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	dir := filepath.Join(tmp, "Microsoft", "Windows", "SendTo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 SendTo 目录: %v", err)
	}
	const dev = "3333333333333333333333333333333333333333333333333333333333333333"
	// 带路径分隔符与上级引用的设备名
	if err := EnsureSendToLink(`..\..\evil`, dev); err != nil {
		t.Fatalf("创建: %v", err)
	}
	// 快捷方式必须落在 SendTo 目录内（不在上级目录）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 SendTo 目录: %v", err)
	}
	found := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lnk" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("应只有 1 个快捷方式且位于 SendTo 内, got %d", found)
	}
	if _, err := os.Stat(filepath.Join(tmp, "evil.lnk")); err == nil {
		t.Fatal("设备名造成了路径穿越")
	}
}

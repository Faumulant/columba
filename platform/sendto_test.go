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

	// 把 .lnk 改成指向一个不存在的 exe，模拟「程序换目录」后的坏链接。
	// 走与产品相同的安全路径：临时名建在 %TEMP% 根（ASCII），再 Move 到位——
	// 直接对含非 ANSI 字符的路径调 Save() 在英文 runner 上会失败。
	if err := writeShortcutViaTemp(lnk, `C:\nowhere\gone.exe`, "sendto --to "+dev); err != nil {
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

// writeShortcutViaTemp 在测试里造一个指向 target/args 的 .lnk。
//
// 不能直接对含非 ANSI 字符的路径调 Save()——那正是被测的缺陷本身。
// 这里走与产品相同的安全路径：临时名建在 %TEMP% 根（ASCII），再 Move 到位。
func writeShortcutViaTemp(lnk, target, args string) error {
	tmp := filepath.Join(os.TempDir(), "__columba_test_tmp.lnk")
	os.Remove(tmp)
	script := `
$tmp = '` + psQuote(tmp) + `'
$final = '` + psQuote(lnk) + `'
if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force }
$ws = New-Object -ComObject WScript.Shell
$s = $ws.CreateShortcut($tmp)
$s.TargetPath = '` + psQuote(target) + `'
$s.Arguments = '` + psQuote(args) + `'
$s.Save()
if (-not (Test-Path -LiteralPath $tmp)) { throw '临时快捷方式未能创建' }
if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Force }
Move-Item -LiteralPath $tmp -Destination $final -Force`
	return runHiddenPowerShell(script, 20*time.Second)
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

// 设备名含系统 ANSI 代码页表示不了的字符时，快捷方式仍必须创建成功。
//
// 这是回归测试：WScript.Shell.CreateShortcut(...).Save() 写 .lnk 走的是系统
// ANSI 代码页，且对**整条路径**做转换，非该页可表示的字符会**静默失败**
// （不建文件、不抛异常）。中文 Windows（ANSI=936）能表示中文，所以中文设备名
// 在中文开发机上不可见问题；英文 Windows（ANSI=1252）上中文设备名同样建不出来，
// 用户右键菜单里根本看不到该设备，也没有任何提示。
//
// 这里用 emoji 与韩文——它们在 936 与 1252 下都无法表示，因此本测试在
// 两种开发机上都能复现原缺陷，不依赖 CI 的语言环境。
func TestSendToLinkHandlesNonANSIName(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"我的手机", "中文（936 可表示，1252 不可）"},
		{"test😀", "emoji（936 与 1252 均不可表示）"},
		{"테스트", "韩文（936 与 1252 均不可表示）"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("APPDATA", tmp)
			dir := filepath.Join(tmp, "Microsoft", "Windows", "SendTo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("建 SendTo 目录: %v", err)
			}
			const dev = "4444444444444444444444444444444444444444444444444444444444444444"
			if err := EnsureSendToLink(c.name, dev); err != nil {
				t.Fatalf("创建快捷方式（%s）: %v", c.label, err)
			}
			lnk := filepath.Join(dir, c.name+".lnk")
			if _, err := os.Stat(lnk); err != nil {
				t.Fatalf("快捷方式未创建（%s）: %v", c.label, err)
			}
			// 内容也要正确：移到最终位置后 .lnk 仍应指向当前 exe 与该设备
			if !sendToLinkMatches(lnk, helperExeForTest(t), dev) {
				t.Fatalf("快捷方式内容不正确（%s）", c.label)
			}
		})
	}
}

// SendTo 目录本身含非 ANSI 字符时（中文用户名机器的真实形态：
// %APPDATA% 展开后是 C:\Users\张三\AppData\Roaming\...），快捷方式也必须建成功。
//
// 这条比上面的用例更严：Save() 对整条路径做 ANSI 转换，目录名含中文时
// 连纯 ASCII 的设备名都会失败。只在文件名上想办法是不够的。
func TestSendToLinkHandlesNonANSIDir(t *testing.T) {
	tmp := t.TempDir()
	// 用 emoji 命名目录，确保 936 与 1252 都无法表示（中文在 936 下可表示，
	// 无法在本机复现「目录不可表示」的情形）
	appdata := filepath.Join(tmp, "用户😀")
	t.Setenv("APPDATA", appdata)
	dir := filepath.Join(appdata, "Microsoft", "Windows", "SendTo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 SendTo 目录: %v", err)
	}
	const dev = "5555555555555555555555555555555555555555555555555555555555555555"
	// 纯 ASCII 设备名，把问题限定在目录路径上
	if err := EnsureSendToLink("Phone", dev); err != nil {
		t.Fatalf("目录含非 ANSI 字符时创建失败: %v", err)
	}
	lnk := filepath.Join(dir, "Phone.lnk")
	if _, err := os.Stat(lnk); err != nil {
		t.Fatalf("快捷方式未创建: %v", err)
	}
	if !sendToLinkMatches(lnk, helperExeForTest(t), dev) {
		t.Fatal("快捷方式内容不正确")
	}
	// 重复调用应幂等：读取路径同样要能正常工作，否则每次同步都会白重建
	if err := EnsureSendToLink("Phone", dev); err != nil {
		t.Fatalf("重复调用: %v", err)
	}
}

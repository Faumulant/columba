package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"drop/core/identity"
	"drop/core/ipc"
	"drop/platform"
)

// drop install —— 一次性系统安装。防火墙规则需要管理员权限，
// 其余（drop: 协议、开机自启）为当前用户级。
func cmdInstall() error {
	exe, _ := os.Executable()
	fmt.Println("安装 Drop 系统集成：" + exe)

	if err := platform.AllowFirewall(); err != nil {
		fmt.Println("✗ 防火墙入站规则（需要管理员权限）：" + firstLine(err.Error()))
		fmt.Println("  请在【管理员】终端重新运行 drop install，或手动执行：")
		fmt.Printf("  netsh advfirewall firewall add rule name=Drop dir=in action=allow program=\"%s\" enable=yes\n", exe)
	} else {
		fmt.Println("✓ 防火墙入站规则（自动发现依赖它）")
	}
	// Firefox 签名豁免（AutoConfig）：写入检测到的 Firefox 安装目录。
	// 需要管理员权限（Firefox 装在 Program Files）；Dev Edition 重启后
	// 即可加载本机登记的未签名扩展，免去 about:config 手动步骤。
	for _, dir := range firefoxInstallDirs() {
		if err := writeFirefoxAutoConfig(dir); err != nil {
			fmt.Println("（Firefox 签名豁免写入失败 " + dir + "：" + firstLine(err.Error()) + "）")
			continue
		}
		fmt.Println("✓ Firefox 签名豁免（AutoConfig）：" + dir)
	}
	if err := platform.RegisterProtocolHandler(); err != nil {
		return fmt.Errorf("注册 drop: 协议失败: %w", err)
	}
	fmt.Println("✓ drop: 协议回调（通知按钮依赖它）")
	if err := platform.RegisterNativeHost(); err != nil {
		return fmt.Errorf("注册浏览器扩展宿主失败: %w", err)
	}
	fmt.Println("✓ 浏览器扩展宿主（Firefox/Chrome/Edge Native Messaging）")
	if warn, err := platform.RegisterFirefoxExtension(ipc.FirefoxExtDir()); err == nil {
		fmt.Println("✓ Firefox 扩展登记（Dev Edition 重启后生效）")
		if warn != "" {
			fmt.Println("  " + warn)
		}
	} else {
		fmt.Println("（未登记 Firefox 扩展：" + firstLine(err.Error()) + "）")
	}

	if err := platform.SetAutostart(); err != nil {
		return fmt.Errorf("设置开机自启失败: %w", err)
	}
	fmt.Println("✓ 开机自启（登录后自动运行后台）")
	fmt.Println("\n完成。后台将以托盘图标运行：drop run（或注销重登录验证自启）。")
	fmt.Println("卸载：drop uninstall")
	return nil
}

// drop uninstall —— 反向清理（防火墙部分同样需要管理员）。
func cmdUninstall() error {
	var warns []string
	if err := platform.RemoveFirewall(); err != nil {
		warns = append(warns, "防火墙规则移除失败（需要管理员）")
	}
	if err := platform.UnregisterProtocolHandler(); err != nil {
		warns = append(warns, "drop: 协议移除失败")
	}
	if err := platform.ClearAutostart(); err != nil {
		warns = append(warns, "自启移除失败")
	}
	if err := platform.UnregisterNativeHost(); err != nil {
		warns = append(warns, "浏览器扩展宿主注册移除失败")
	}
	if err := platform.UnregisterFirefoxExtension(); err != nil {
		warns = append(warns, "Firefox 扩展登记移除失败")
	}
	// 清理每台已配对设备的"发送到"快捷方式
	if trust, err := identity.LoadTrust(); err == nil {
		for _, p := range trust.List() {
			platform.RemoveSendToLink(p.DisplayName())
		}
	}
	if len(warns) > 0 {
		fmt.Println("完成（有警告）：" + strings.Join(warns, "；"))
		return nil
	}
	fmt.Println("✓ 已移除防火墙规则、drop: 协议、开机自启、扩展宿主与 Firefox 扩展登记。")
	fmt.Println("数据（身份/信任库/历史）保留于 " + os.Getenv("LOCALAPPDATA") + "\\Drop")
	return nil
}

// firefoxInstallDirs 探测本机所有 Firefox 安装目录（含正式版/Dev/ESR）。
func firefoxInstallDirs() []string {
	var out []string
	seen := map[string]bool{}
	roots := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")}
	for _, root := range roots {
		if root == "" {
			continue
		}
		hits, _ := filepath.Glob(filepath.Join(root, "*irefox*"))
		for _, dir := range hits {
			fi, err := os.Stat(dir)
			if err != nil || !fi.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "firefox.exe")); err != nil {
				continue
			}
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	return out
}

// writeFirefoxAutoConfig 写入 AutoConfig 两个文件，豁免本机登记扩展的签名。
// 这是管理员供给机制，Firefox 官方支持；对 Dev/ESR/正式版均有效。
func writeFirefoxAutoConfig(dir string) error {
	pref := filepath.Join(dir, "defaults", "pref")
	if err := os.MkdirAll(pref, 0o755); err != nil {
		return err
	}
	autojs := "pref(\"general.config.filename\", \"mozilla.cfg\");\n" +
		"pref(\"general.config.obscure_value\", 0);\n"
	if err := os.WriteFile(filepath.Join(pref, "autoconfig.js"), []byte(autojs), 0o644); err != nil {
		return err
	}
	cfg := "// Drop: 允许加载本机登记的扩展（由 drop install 写入）\n" +
		"pref(\"xpinstall.signatures.required\", false);\n"
	return os.WriteFile(filepath.Join(dir, "mozilla.cfg"), []byte(cfg), 0o644)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

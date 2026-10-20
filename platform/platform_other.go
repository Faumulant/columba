//go:build !windows

// 非 Windows 平台的最小实现：保持核心可编译（如 Termux 上的接收端），
// 系统集成能力（托盘/通知/注册表）留待各平台原生客户端实现。
package platform

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func Notify(n Notif) error { return nil }

func OpenPath(path string) error {
	return exec.Command("xdg-open", path).Start()
}

func RevealPath(path string) error {
	return exec.Command("xdg-open", path).Start()
}

func SpawnDetached(exe string, args []string) error {
	return exec.Command(exe, args...).Start()
}

func AcquireSingleInstance() (bool, func(), error) {
	return false, func() {}, nil
}

func EnsureSendToLink(display, deviceID string) error { return nil }

// FreeDiskBytes 返回路径所在卷的可用空间。
func FreeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func RemoveSendToLink(display string) error                  { return nil }
func OpenPanelWindow(url string) error                       { return exec.Command("xdg-open", url).Start() }
func RegisterNativeHost() error                              { return nil }
func UnregisterNativeHost() error                            { return nil }
func PickFolder(desc string) (string, error)                 { return "", errors.New("当前平台不支持") }
func FirefoxExtensionRegistered() bool                       { return false }
func RegisterFirefoxExtension(extDir string) (string, error) { return "", nil }
func UnregisterFirefoxExtension() error                      { return nil }
func RegisterProtocolHandler() error                         { return nil }
func UnregisterProtocolHandler() error                       { return nil }
func AutostartEnabled() bool                                 { return false }
func SetAutostart() error                                    { return nil }
func ClearAutostart() error                                  { return nil }
func AllowFirewall() error                                   { return nil }
func RemoveFirewall() error                                  { return nil }

// SetPanelDark 面板深浅色：仅 Windows 的原生窗口有标题栏需要跟随，
// 其余平台是空实现。此前只有 Windows 版本，导致 GOOS=linux 构建 core/ipc 直接
// 失败（undefined: platform.SetPanelDark），CI 与 macOS 端都会撞上。
func SetPanelDark(dark bool) {}

// 桌面快捷方式：Windows 专属（.lnk + PowerShell）。其余平台暂不提供，
// 空实现让 cmd/drop 能跨平台编译；将来做 macOS 时在这里换 .app/别名实现。
func EnsureDesktopShortcut() error { return nil }
func RemoveDesktopShortcut()       {}

// RunTray 桩：无托盘，阻塞到 ctx 取消（作为无界面常驻接收端使用）。
func RunTray(ctx context.Context, tooltip string, items []TrayItem, onQuit func()) error {
	<-ctx.Done()
	return nil
}

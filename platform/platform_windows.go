//go:build windows

package platform

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"drop/core/identity"
)

// 通知归属 AUMID：借用 PowerShell 的 AppID，保证未注册自有 AUMID 时
// 通知也能可靠弹出（横幅标注为 PowerShell，M3 再优化）。
const toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

var xmlEsc = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// Notify 弹出系统 toast 通知（含按钮）。
func Notify(n Notif) error {
	launch := ""
	if len(n.Actions) > 0 {
		launch = n.Actions[0].URL
	}
	var b strings.Builder
	b.WriteString(`<toast activationType="protocol" launch="` + xmlEsc.Replace(launch) + `">`)
	b.WriteString(`<visual><binding template="ToastGeneric">`)
	b.WriteString(`<text>` + xmlEsc.Replace(n.Title) + `</text>`)
	b.WriteString(`<text>` + xmlEsc.Replace(n.Body) + `</text>`)
	b.WriteString(`</binding></visual><actions>`)
	for _, a := range n.Actions {
		b.WriteString(`<action content="` + xmlEsc.Replace(a.Label) +
			`" arguments="` + xmlEsc.Replace(a.URL) + `" activationType="protocol"/>`)
	}
	b.WriteString(`</actions></toast>`)

	script := `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml('` + b.String() + `')
$t = [Windows.UI.Notifications.ToastNotification]::new($x)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + toastAppID + `').Show($t)`
	return runHiddenPowerShell(script, 15*time.Second)
}

// psEncode PowerShell -EncodedCommand 需要的 UTF-16LE base64。
func psEncode(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func runHiddenPowerShell(script string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OpenPath 用系统默认方式打开文件/目录。
func OpenPath(path string) error {
	return shellExecute("open", path, "")
}

// RevealPath 在资源管理器中定位文件。
func RevealPath(path string) error {
	return shellExecute("open", "explorer.exe", `/select,"`+path+`"`)
}

func shellExecute(verb, file, args string) error {
	var argPtr *uint16
	if args != "" {
		argPtr = windows.StringToUTF16Ptr(args)
	}
	return windows.ShellExecute(0, windows.StringToUTF16Ptr(verb),
		windows.StringToUTF16Ptr(file), argPtr, nil, windows.SW_SHOWNORMAL)
}

// SpawnDetached 启动一个无窗口的分离子进程（不等待）。
func SpawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

// AcquireSingleInstance 保证后台 daemon 单实例。
func AcquireSingleInstance() (already bool, release func(), err error) {
	h, err := windows.CreateMutex(nil, true, windows.StringToUTF16Ptr(`Local\DropDaemon`))
	if h == 0 {
		return false, nil, err
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return true, nil, nil
	}
	return false, func() {
		windows.ReleaseMutex(h)
		windows.CloseHandle(h)
	}, nil
}

// --- Explorer "发送到" 快捷方式 ---

func sendToDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "SendTo")
}

// sanitizeFileName 剔除 Windows 文件名非法字符、路径分隔符与控制字符，
// 防止设备显示名（网络对端自报，不可信）被用于路径穿越或脚本注入。
func sanitizeFileName(s string) string {
	out := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\/:*?"<>|`, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	out = strings.Trim(out, " .")
	r := []rune(out)
	if len(r) > 60 {
		out = string(r[:60])
	}
	if out == "" {
		out = "device"
	}
	return out
}

// psQuote PowerShell 单引号字符串转义：每个单引号替换为两个单引号。
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// EnsureSendToLink 为一台已配对设备创建"发送到"快捷方式（已存在则跳过）。
// 目标指向无控制台的伴生 exe，右键发送不闪黑窗。
func EnsureSendToLink(display, deviceID string) error {
	name := filepath.Join(sendToDir(), sanitizeFileName(display)+".lnk")
	// 消毒后仍兜底校验：目标必须位于 SendTo 目录内
	if dir := filepath.Dir(name); !strings.EqualFold(dir, filepath.Clean(sendToDir())) {
		return fmt.Errorf("非法的快捷方式路径 %q", name)
	}
	if _, err := os.Stat(name); err == nil {
		return nil
	}
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	script := `
$ws = New-Object -ComObject WScript.Shell
$s = $ws.CreateShortcut('` + psQuote(name) + `')
$s.TargetPath = '` + psQuote(exe) + `'
$s.Arguments = 'sendto --to ` + psQuote(deviceID) + `'
$s.WindowStyle = 7
$s.Description = 'Drop'
$s.Save()`
	return runHiddenPowerShell(script, 20*time.Second)
}

// RemoveSendToLink 移除设备的"发送到"快捷方式。
func RemoveSendToLink(display string) error {
	name := filepath.Join(sendToDir(), sanitizeFileName(display)+".lnk")
	if dir := filepath.Dir(name); !strings.EqualFold(dir, filepath.Clean(sendToDir())) {
		return fmt.Errorf("非法的快捷方式路径 %q", name)
	}
	os.Remove(name)
	return nil
}

// OpenPanelWindow 用 Edge App 模式打开面板（独立应用窗口，无浏览器界面）；
// 找不到 Edge 时退回默认浏览器。
func OpenPanelWindow(url string) error {
	for _, p := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
	} {
		if p != "" && func() bool { _, err := os.Stat(p); return err == nil }() {
			return shellExecute("open", p, `--app="`+url+`" --window-size=920,680`)
		}
	}
	return shellExecute("open", url, "")
}

//go:embed icons/drop.ico
var dropIco []byte // 内嵌的官方图标（托盘用）。

// PickFolder 弹出系统原生的文件夹选择窗口（前台），返回所选路径。
func PickFolder(desc string) (string, error) {
	script := `
Add-Type -AssemblyName System.Windows.Forms | Out-Null
Add-Type -AssemblyName System.Drawing | Out-Null
# 可见的置顶小提示窗作为属主：其子弹窗（选择对话框）必然盖过面板
$f = New-Object System.Windows.Forms.Form
$f.TopMost = $true
$f.ShowInTaskbar = $false
$f.Text = 'Columba'
$f.Size = New-Object System.Drawing.Size(300, 100)
$f.StartPosition = 'CenterScreen'
$l = New-Object System.Windows.Forms.Label
$l.Text = '正在打开文件夹选择…'
$l.AutoSize = $true
$l.Location = New-Object System.Drawing.Point(30, 30)
$f.Controls.Add($l)
$f.Show()
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = '` + desc + `'
$d.ShowNewFolderButton = $true
if ($d.ShowDialog($f) -eq 'OK') { Write-Output $d.SelectedPath }
$f.Close()`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-STA", "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errors.New("未选择文件夹")
	}
	return p, nil
}

// FreeDiskBytes 返回路径所在卷的可用空间（字节）。
func FreeDiskBytes(path string) (uint64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	root := filepath.VolumeName(abs) + `\`
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(root), &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}

// --- 浏览器 Native Messaging 注册 ---

// RegisterNativeHost 写入宿主清单并注册到 Firefox/Chrome/Edge。
// 扩展经 Native Messaging 与本机 daemon 通信，凭据不出浏览器进程。
func RegisterNativeHost() error {
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	dir := filepath.Join(identity.AppDir(), "native")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	manifest := filepath.Join(dir, "drop-host.json")
	host := map[string]any{
		"name":               "drop.drop",
		"description":        "Drop 浏览器扩展本地宿主",
		"path":               exe,
		"type":               "stdio",
		"allowed_extensions": []string{"drop@drop.local"},
		"allowed_origins":    []string{"chrome-extension://pgihdeeaofkecmpdldpagfhnehmibgkf/"},
	}
	b, err := json.MarshalIndent(host, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifest, b, 0o600); err != nil {
		return err
	}
	for _, key := range []string{
		`Software\Google\Chrome\NativeMessagingHosts\drop.drop`,
		`Software\Microsoft\Edge\NativeMessagingHosts\drop.drop`,
		`Software\Mozilla\NativeMessagingHosts\drop.drop`,
	} {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue("", manifest)
		k.Close()
		if err != nil {
			return fmt.Errorf("注册 %s 失败: %w", key, err)
		}
	}
	return nil
}

func UnregisterNativeHost() error {
	for _, key := range []string{
		`Software\Google\Chrome\NativeMessagingHosts\drop.drop`,
		`Software\Microsoft\Edge\NativeMessagingHosts\drop.drop`,
		`Software\Mozilla\NativeMessagingHosts\drop.drop`,
	} {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RegisterFirefoxExtension 把扩展目录登记进 Firefox（HKCU，免管理员），
// 并尝试写入 Dev Edition 的签名豁免策略。Firefox 重启后自动加载扩展。
// 返回 warning 非空时表示策略写入失败（如注册表受保护），
// 需要用户在 about:config 手动设置 xpinstall.signatures.required = false。
// 签名豁免仅对 Developer Edition / Nightly / ESR 生效，正式版强制签名。
func RegisterFirefoxExtension(extDir string) (string, error) {
	extDir = filepath.Clean(extDir)
	if _, err := os.Stat(filepath.Join(extDir, "manifest.json")); err != nil {
		return "", errors.New("扩展目录无效（缺少 manifest.json）")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Mozilla\Firefox\Extensions\drop@drop.local`, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	if err := k.SetStringValue("", extDir); err != nil {
		return "", err
	}
	warning := ""
	pk, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Policies\Mozilla\Firefox\Preferences\xpinstall.signatures.required`, registry.SET_VALUE)
	if err != nil {
		warning = "未能自动写入签名豁免策略（本机注册表受限）。请手动：about:config → xpinstall.signatures.required → false"
	} else {
		defer pk.Close()
		if err := pk.SetDWordValue("", 0); err != nil {
			warning = "未能自动写入签名豁免策略。请手动：about:config → xpinstall.signatures.required → false"
		}
	}
	return warning, nil
}

func UnregisterFirefoxExtension() error {
	for _, key := range []string{
		`Software\Mozilla\Firefox\Extensions\drop@drop.local`,
		`Software\Policies\Mozilla\Firefox\Preferences\xpinstall.signatures.required`,
	} {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}

// FirefoxExtensionRegistered 查询扩展是否已登记。
func FirefoxExtensionRegistered() bool {
	_, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Mozilla\Firefox\Extensions\drop@drop.local`, registry.QUERY_VALUE)
	return err == nil
}

// --- 注册表：drop: 协议回调 + 开机自启 ---

// helperExe 返回无控制台的伴生 exe（dropw.exe，GUI 子系统构建），
// 供"系统触发"的入口（协议回调/自启/发送到垫片）使用，避免黑窗闪烁；
// 不存在时退回当前 exe。
func helperExe() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	w := filepath.Join(filepath.Dir(self), "dropw.exe")
	if _, err := os.Stat(w); err == nil {
		return w
	}
	return self
}

func RegisterProtocolHandler() error {
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\drop`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.SetStringValue("", "URL:Drop")
	k.SetStringValue("URL Protocol", "")
	k2, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\drop\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k2.Close()
	return k2.SetStringValue("", `"`+exe+`" handle "%1"`)
}

func UnregisterProtocolHandler() error {
	for _, sub := range []string{`Software\Classes\drop\shell\open\command`, `Software\Classes\drop\shell\open`, `Software\Classes\drop\shell`, `Software\Classes\drop`} {
		if err := registry.DeleteKey(registry.CURRENT_USER, sub); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}

// AutostartEnabled 查询开机自启是否已开启。
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Drop")
	return err == nil
}

func SetAutostart() error {
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("Drop", `"`+exe+`" run --hidden`)
}

func ClearAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue("Drop")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// --- 防火墙（需要管理员权限） ---

func AllowFirewall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=Drop").Run()
	out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name=Drop", "dir=in", "action=allow", "program="+exe, "enable=yes").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func RemoveFirewall() error {
	out, err := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=Drop").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- 托盘 ---

// RunTray 阻塞运行托盘图标，直到用户选择退出或 ctx 取消。
func RunTray(ctx context.Context, tooltip string, items []TrayItem, onQuit func()) error {
	onReady := func() {
		systray.SetIcon(iconBytes())
		systray.SetTitle("Drop")
		systray.SetTooltip(tooltip)
		for _, it := range items {
			m := systray.AddMenuItem(it.Label, it.Label)
			go func(it TrayItem, m *systray.MenuItem) {
				for range m.ClickedCh {
					it.OnClick()
				}
			}(it, m)
		}
		mQuit := systray.AddMenuItem("退出", "退出 Drop 后台")
		go func() {
			for range mQuit.ClickedCh {
				if onQuit != nil {
					onQuit()
				}
				systray.Quit()
			}
		}()
	}
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(onReady, nil)
	return nil
}

// iconBytes 返回内嵌的官方图标。
func iconBytes() []byte { return dropIco }

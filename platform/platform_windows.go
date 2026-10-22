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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"drop/core/identity"
	"drop/core/transfer"
)

// 通知归属 AUMID（App User Model ID）。
//
// 自有 AUMID 需要在 HKCU\Software\Classes\AppUserModelId\<ID> 下注册 DisplayName，
// 系统才会把横幅来源显示成这个名字（见 ensureToastAUMID）。注册失败时回退到
// 借用 PowerShell 的 AppID——宁可来源名显示错，也不能让通知弹不出来。
const (
	ownAUMID = "Faumulant.Columba"
	// 回退用的 PowerShell AppID（未注册自有 AUMID 时的保底）
	fallbackAUMID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`
)

var xmlEsc = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// toastAppID 返回当前生效的 AUMID。注册只在首次调用时做一次（sync.Once）：
// 每条通知都写注册表既慢又无意义。
func toastAppID() string {
	aumidOnce.Do(func() {
		aumidErr = ensureToastAUMID()
	})
	if aumidErr == nil {
		return ownAUMID
	}
	return fallbackAUMID
}

var (
	aumidOnce sync.Once
	aumidErr  error
)

// ensureToastAUMID 在 HKCU 注册自有 AUMID 的显示名。
//
// 实测结论（2026-10-21）：只要 HKCU\Software\Classes\AppUserModelId\<ID> 下
// 有 DisplayName，CreateToastNotifier(ID) 的 Setting 即为 Enabled、横幅来源
// 显示该名字；**不需要**再建带 AUMID 的开始菜单快捷方式（那是打包应用的常规
// 做法，对免安装 exe 非必需，而设 lnk 的 AppUserModelID 需 IShellLinkW +
// IPropertyStore 的 COM 调用，在本项目里得不偿失）。
func ensureToastAUMID() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Classes\AppUserModelId\`+ownAUMID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("DisplayName", "Columba（飞鸠）")
}

// Notify 弹出系统 toast 通知（含按钮）。
func Notify(n Notif) error {
	// 点击通知本体（非按钮）时的目标：优先 OpenURL，否则沿用第一个按钮的 URL
	// （保持原有「点通知等于点第一个按钮」的语义）。
	launch := n.OpenURL
	if launch == "" && len(n.Actions) > 0 {
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
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + toastAppID() + `').Show($t)`
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
	_, err := runHiddenPowerShellCapture(script, timeout)
	return err
}

// runHiddenPowerShellCapture 同 runHiddenPowerShell，但返回标准输出（去首尾空白）。
// 用于需要读回结果的场合（如核对既有 .lnk 的目标是否仍有效）。
//
// 必须加 -OutputFormat Text：-EncodedCommand 下 PowerShell 默认把进度流
// （COM 创建时的「正在准备使用模块」）序列化成 `#< CLIXML` 混进 stdout，
// 真正的输出反而被它淹没（实测踩到：读回的内容是整段 CLIXML，比对必然失败）。
func runHiddenPowerShellCapture(script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-OutputFormat", "Text",
		"-EncodedCommand", psEncode(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	// 兜底：万一 CLIXML 仍混进来，剔除 CLIXML 相关的行，只留纯文本。
	// 保留所有纯文本行（而非只取最后一行）——调用方可能输出多行标记，
	// 只取最后一行会把需要的行丢掉。
	if strings.Contains(s, "#< CLIXML") {
		var keep []string
		for _, ln := range strings.Split(s, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#<") || strings.HasPrefix(t, "<") {
				continue
			}
			keep = append(keep, t)
		}
		return strings.Join(keep, "\n"), nil
	}
	return s, nil
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
	// Windows 保留设备名不能作文件名（大小写不敏感，且 con.txt 这类带扩展的
	// 形式同样非法）：对端自报的显示名撞车时"发送到"快捷方式创建异常。
	// 加前缀而不是丢弃，保证 Ensure/Remove 两侧同一显示名算出同一 .lnk 路径。
	stem := out
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "CLOCK$", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		out = "_" + out
	}
	return out
}

// psQuote PowerShell 单引号字符串转义：每个单引号替换为两个单引号。
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// EnsureDesktopShortcut 创建（或修补）桌面 Columba 快捷方式。
// 图标落地为独立 .ico 再作 IconLocation：图标缓存绑定的是这个稳定文件，
// 开发期反复覆盖 exe 不再导致桌面图标变空白（经典图标缓存失效）。
// 已存在的快捷方式只更新图标，保留用户自己的目标配置。
//
// 桌面路径交给 PowerShell 解析（可能被 OneDrive 重定向），所以这里的
// $final 由脚本自己算，saveShortcut 只负责 ASCII 临时名与改名。
func EnsureDesktopShortcut() error {
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	if exe == "" {
		return fmt.Errorf("无法确定程序路径")
	}
	icoPath := filepath.Join(identity.AppDir(), "columba.ico")
	if err := os.WriteFile(icoPath, dropIco, 0o644); err != nil {
		return err
	}
	// 已存在的快捷方式只更新图标、保留用户自己的目标配置，所以这里先把既有
	// .lnk 复制到临时名再改，而不是无条件重建。$tmp 位于 %TEMP% 根（ASCII），
	// 因此复制与保存都不受代码页影响。
	script := `
$ws = New-Object -ComObject WScript.Shell
if (Test-Path -LiteralPath $final) { Copy-Item -LiteralPath $final -Destination $tmp -Force }
$s = $ws.CreateShortcut($tmp)
if (-not (Test-Path -LiteralPath $final)) {
  $s.TargetPath = '` + psQuote(exe) + `'
  $s.Arguments = 'panel'
  $s.WorkingDirectory = '` + psQuote(filepath.Dir(exe)) + `'
  $s.WindowStyle = 1
  $s.Description = 'Columba'
}
$s.IconLocation = '` + psQuote(icoPath) + `,0'
$s.Save()`
	return saveShortcut(
		`(Join-Path ([Environment]::GetFolderPath('Desktop')) 'Columba.lnk')`,
		script)
}

// RemoveDesktopShortcut 移除桌面快捷方式与落地的图标文件（卸载用，幂等）。
func RemoveDesktopShortcut() {
	os.Remove(filepath.Join(identity.AppDir(), "columba.ico"))
	script := `
$lnk = Join-Path ([Environment]::GetFolderPath('Desktop')) 'Columba.lnk'
if (Test-Path $lnk) { Remove-Item $lnk -Force }`
	_ = runHiddenPowerShell(script, 20*time.Second)
}

// EnsureSendToLink 为一台已配对设备创建"发送到"快捷方式。
// 目标指向无控制台的伴生 exe，右键发送不闪黑窗。
//
// 已存在时不是简单跳过，而是**校验并修复目标**：程序换目录（构建目录 → 安装目录、
// 或用户自己挪了位置）后，旧 .lnk 仍指着失效路径，右键「发送到」点了毫无反应，
// 而且没有任何机制会自己修好它。这里每次同步都核对一次目标 exe 是否与当前一致，
// 不一致就重建——成本只是读一个 .lnk，却能让「换目录 / 升级」自愈。
func EnsureSendToLink(display, deviceID string) error {
	name := filepath.Join(sendToDir(), sanitizeFileName(display)+".lnk")
	// 消毒后仍兜底校验：目标必须位于 SendTo 目录内
	if dir := filepath.Dir(name); !strings.EqualFold(dir, filepath.Clean(sendToDir())) {
		return fmt.Errorf("非法的快捷方式路径 %q", name)
	}
	exe := helperExe()
	if exe == "" {
		exe, _ = os.Executable()
	}
	// 已存在且目标正确（且参数里的设备 ID 也没变）就跳过
	if _, err := os.Stat(name); err == nil && sendToLinkMatches(name, exe, deviceID) {
		return nil
	}
	script := `
$ws = New-Object -ComObject WScript.Shell
$s = $ws.CreateShortcut($tmp)
$s.TargetPath = '` + psQuote(exe) + `'
$s.Arguments = 'sendto --to ` + psQuote(deviceID) + `'
$s.WindowStyle = 7
$s.Description = 'Drop'
$s.Save()`
	return saveShortcut(`'`+psQuote(name)+`'`, script)
}

// saveShortcut 执行创建快捷方式的 PowerShell 脚本，规避系统代码页限制。
//
// WScript.Shell.CreateShortcut(...).Save() 写 .lnk 时走的是**系统 ANSI 代码页**，
// 且对**整条路径**做转换——不只是文件名。实测（GBK 系统）：
//
//	中文设备名（GBK 可表示）           → 成功
//	emoji / 韩文（GBK 不可表示）        → 静默失败
//	ASCII 文件名 + 中文目录             → 静默失败（目录名被转换坏）
//
// 更糟的是失败**静默**：不创建文件、不抛异常，PowerShell 照常退出 0，
// 调用方以为成功。
//
// 后果有两层：英文版 Windows（ANSI=1252）上中文设备名建不出来；而**中文用户名
// 的机器**上，%APPDATA%\Microsoft\Windows\SendTo 这条路径本身含中文，连 ASCII
// 设备名都会失败。用户右键菜单里看不到设备，也没有任何提示。
//
// 修法：临时 .lnk 建在 %TEMP% 根目录（整条路径纯 ASCII，Save 必然成功），
// 再用 Move-Item 移到最终位置——Move 走 Win32 UTF-16 接口，不受代码页限制。
// 之所以全在 PowerShell 内完成而不是回 Go 侧 os.Rename：桌面目录可能被
// OneDrive 重定向，只有 [Environment]::GetFolderPath 取得准。
//
// 另加 Test-Path 断言：把「Save 静默失败」变成显式报错，避免同类问题再被
// 误判成测试的环境问题。
//
// finalExpr 是求值为最终 .lnk 绝对路径的 PowerShell 表达式；
// script 中通过 $tmp 引用临时路径（%TEMP% 根、纯 ASCII）。
func saveShortcut(finalExpr, script string) error {
	full := `
$final = ` + finalExpr + `
$tmp = Join-Path $env:TEMP '__columba_tmp.lnk'
if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force }
` + script + `
if (-not (Test-Path -LiteralPath $tmp)) {
  throw '快捷方式未能创建（可能受系统代码页限制）'
}
if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Force }
Move-Item -LiteralPath $tmp -Destination $final -Force`
	return runHiddenPowerShell(full, 20*time.Second)
}

// sendToLinkMatches 检查既有 .lnk 的目标与参数是否仍指向当前 exe / 该设备。
// 用 PowerShell 读（创建也走它，避免引入 COM 直调的另一套实现）。
// 任何读取失败都按「不匹配」处理 → 走重建路径，宁可重写一次也不要留坏链接。
//
// 读取同样受 ANSI 代码页限制，且同样作用于**整条路径**：CreateShortcut(含
// 非 ANSI 字符的路径) 不报错，只返回空对象，读到的 TargetPath/Arguments
// 都是空串——于是「不匹配」判定成立，每次同步都白重建一遍。
//
// 绕法：把 .lnk 复制到 %TEMP% 根下的 ASCII 临时名再读。必须用 %TEMP% 根，
// 不能放同目录——CreateShortcut 对整条路径做转换，同目录若含非 ANSI 字符
// （中文用户名机器上的 SendTo 就是）依然读不到，只会得到空对象。
// 输出带固定前缀，便于在 CLIXML 噪音里精确取回，不受行数影响。
func sendToLinkMatches(lnk, wantExe, wantDeviceID string) bool {
	script := `
$final = '` + psQuote(lnk) + `'
$tmp = Join-Path $env:TEMP '__columba_read.lnk'
if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force }
try {
  Copy-Item -LiteralPath $final -Destination $tmp -Force -ErrorAction Stop
  $ws = New-Object -ComObject WScript.Shell
  $s = $ws.CreateShortcut($tmp)
  Write-Output ('LNKDATA:' + $s.TargetPath + '|' + $s.Arguments)
} catch {
  Write-Output ('LNKERR:' + $_.Exception.Message)
} finally {
  if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
}`
	out, err := runHiddenPowerShellCapture(script, 10*time.Second)
	if err != nil {
		return false
	}
	// 从输出里精确取 LNKDATA: 那一行，避免被 CLIXML 噪音或其他行干扰
	data := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "LNKDATA:") {
			data = strings.TrimPrefix(line, "LNKDATA:")
			break
		}
	}
	if data == "" {
		return false
	}
	// 比对前统一路径形式：PowerShell 回显的 TargetPath 可能是写入时的 8.3 短名
	// （用户名超过 8 字符时，CI runner 上就是），而 wantExe 来自 os.Executable()
	// 是长名。两者指向同一文件，但字符串不同，直接比较会永远判不匹配。
	want := wantExe + `|sendto --to ` + wantDeviceID
	return strings.EqualFold(normalizeLnkData(data), normalizeLnkData(want))
}

// normalizeLnkData 把 "目标路径|参数" 里的目标路径展开为长名，消除 8.3 短名差异。
// 展开失败时保留原值——宁可判不匹配走重建，也不要因为规范化出错而误判匹配。
func normalizeLnkData(s string) string {
	i := strings.LastIndex(s, "|sendto --to ")
	if i < 0 {
		return s
	}
	return longPath(s[:i]) + s[i:]
}

// longPath 用 GetLongPathNameW 把 8.3 短名（PROGRA~1 之类）展开为长名。
// os.Executable() 返回长名，而 PowerShell 回显的 TargetPath 保留写入时的形式，
// 两者指向同一文件但字符串不同，直接比较会永远判不匹配。
func longPath(p string) string {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetLongPathName(ptr, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
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

// OpenPanelWindow 打开面板窗口：窗口生命周期完全自管——隐藏创建 →
// 设图标/深色标题栏/DWM → 嵌入 WebView2 → 导航 → 深色首绘完成后才显示。
// useLegacyPanel 切回库自管路径（回滚开关）。
const useLegacyPanel = false

// panelMu 串行化面板打开请求：goroutine-per-call + 无锁全局曾让两个
// 并发请求双双通过"已打开"检查，产出一黑（自管深色）一白（legacy 回退）
// 两个面板。
var panelMu sync.Mutex

func OpenPanelWindow(url string) error {
	if useLegacyPanel {
		logToDaemon("OpenPanelWindow: useLegacyPanel=true → legacy")
		return openPanelLegacy(url)
	}
	panelMu.Lock()
	defer panelMu.Unlock()
	if wakeExistingPanel() {
		return nil
	}
	go func() {
		// panic 防护网：面板窗口的任何崩溃只损失这一次打开，
		// daemon/托盘/传输不受影响；堆栈落盘（daemon.log）供定位
		defer func() {
			if r := recover(); r != nil {
				logPanelCrash(fmt.Sprintf("面板窗口 panic: %v\n%s", r, debug.Stack()))
				// v3 崩溃也必须给用户一个面板：降级 legacy（其内部还有
				// 浏览器兜底）。回退必须放在这里——panic 会跳过
				// panelWindowMain 内部 Embed 失败分支的一切后续。
				openPanelLegacy(url)
			}
		}()
		runtime.LockOSThread()
		panelWindowMain(url)
	}()
	return nil
}

// logPanelCrash 把面板窗口 goroutine 的 panic 堆栈追加进 daemon.log，
// 与 cmd_run 的 DebugLog 同款格式与 2MB 轮换。写日志失败静默返回：
// 崩溃处理路径上不能让日志问题再放大成二次 panic。
func logPanelCrash(msg string) {
	logToDaemon(msg)
}

// logToDaemon 追加一行到 daemon.log（2MB 轮换，与 cmd_run 的 DebugLog 同格式）。
// 写失败静默：崩溃/失败处理路径上不能让日志问题再放大成二次 panic。
func logToDaemon(msg string) {
	logPath := filepath.Join(identity.AppDir(), "daemon.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 2<<20 {
		os.Remove(logPath + ".old")
		os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, time.Now().Format("01-02 15:04:05.000 ")+msg)
}

// panelLogWriter 把标准 log 包的输出接到 daemon.log。
//
// 为什么需要：go-webview2 fork 在 WebView2 环境/controller 创建失败时用
// log.Printf 打 HRESULT，但 daemon 是 -H windowsgui 的 GUI 进程，stderr 没有
// 任何去处——这就是「v3 嵌入失败」长期只有我加的那句笼统提示、查不到具体
// HRESULT 的原因。init 里换掉 log 的默认 writer 即可接住。
type panelLogWriter struct{}

func (panelLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	if msg != "" {
		logToDaemon("[webview2] " + msg)
	}
	return len(p), nil
}

func init() {
	// 标准 log 包默认写 stderr，GUI 进程下不可见；改投 daemon.log。
	// 前缀留空：logToDaemon 自己加时间戳与 [webview2] 标签。
	log.SetFlags(0)
	log.SetOutput(panelLogWriter{})
}

// SetPanelDark 面板手动选择深浅色时同步原生标题栏：DWM 深色属性可对活窗口
// 随时翻转。auto 模式不调用（标题栏在窗口创建时已按系统设定）；legacy 路径
// 没有 panelHwnd，静默跳过（其标题栏由库自管，跟随系统）。
func SetPanelDark(on bool) {
	if panelHwnd != 0 {
		setNativeTitleBar(unsafe.Pointer(panelHwnd), on)
	}
}

// wakeExistingPanel 已有面板窗口时唤醒并聚焦。用 FindWindow 按窗口类名查，
// 跨进程同样有效——托盘的 daemon 进程与 drop panel CLI 是两个进程，
// 进程内全局 panelHwnd 防不住跨进程重复打开。
//
// 要查两个类名：v3 自管窗口用 panelWndClassName，而 legacy 回退路径用
// go-webview2 库自建的窗口（类名固定为 "webview"）。只查前者时，一旦 v3
// 嵌入失败回退 legacy，后续每次打开都找不到已有窗口 → 窗口越开越多
// （现象：每次打开或重启都会多出一个面板）。
func wakeExistingPanel() bool {
	for _, name := range []string{panelWndClassName, legacyPanelClassName} {
		cls, _ := windows.UTF16PtrFromString(name)
		hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
		if hwnd == 0 {
			continue
		}
		procShowWindow.Call(hwnd, 9 /*SW_RESTORE*/)
		procSetForegroundWindow.Call(hwnd)
		return true
	}
	return false
}

// legacyPanelClassName 是 third_party/go-webview2 内部 RegisterClassExW 用的
// 类名（库源码固定写死 "webview"）。legacy 面板靠它被 FindWindow 找到。
const legacyPanelClassName = "webview"

func openPanelLegacy(url string) error {
	go func() {
		runtime.LockOSThread()
		// 单实例兜底：openPanelLegacy 有 5 个调用点（含 v3 失败/panic 回退），
		// 其中一部分绕过了 OpenPanelWindow 的 panelMu。这里再查一次已有窗口，
		// 避免回退路径重复建窗（否则窗口会越开越多）。
		if wakeExistingPanel() {
			return
		}
		lw, lh := panelLogicalSize()
		scale := systemDpiScale()
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title:  "Columba",
				Width:  uint(float64(lw) * scale),
				Height: uint(float64(lh) * scale),
				IconId: 1, // go-winres 写入的首个图标资源
				Center: true,
			},
		})
		if w == nil {
			// WebView2 运行时缺失：退回 Edge 应用窗口（图标为默认地球）
			shellExecute("open", url, "")
			return
		}
		defer w.Destroy()
		w.Navigate(url)
		setWindowIcon(w.Window(), dropIco)
		setNativeTitleBar(w.Window(), appsUseDarkTheme())
		go trackPanelSize(w.Window(), scale) // 记录尺寸，下次按上次大小打开
		w.Run()
	}()
	return nil
}

const panelWndClassName = "ColumbaPanelWnd"

// panelBrowser 嵌入自管窗口的 WebView2 浏览器；窗口过程在
// WM_SIZE/WM_ACTIVATE/WM_MOVE 时联动（缩放/焦点/弹层跟随）。
var panelBrowser *edge.Chromium

// 面板库金丝雀：引用 fork 独有常量。构建环境丢失 go.work replace（如
// GOWORK 泄漏）链接回原版 go-webview2 时，此处直接编译失败——把「静默
// 用错库、面板运行期即崩」拦死在构建期。与 build.bat 开头的 GOWORK
// 清理互为双保险。
var _ = edge.ForkVersion

// panelPinner 固定浏览器对象及其 COM 回调结构：Go 1.27 起 GC 会移动堆对象，
// 而 WebView2 原生侧持有这些结构的裸指针（库源码注释自述违反 unsafe.Pointer
// 规则4），对象一旦被移动，回调参数即错乱（env=nil 崩溃）。
var panelPinner runtime.Pinner

// panelHwnd 当前面板窗口句柄；非 0 = 面板已打开（单实例：再次打开 = 唤醒）
var panelHwnd uintptr

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}

// panelWindowMain 自管窗口主流程：注册窗口类 → 隐藏创建 → 装饰（图标/
// 深色标题栏/DWM）→ 嵌入 WebView2 → 导航 → 深色首绘完成后才显示。
// 所有装饰在显示前完成，杜绝白闪与图标丢失的时序窗口。
func panelWindowMain(url string) {
	runtime.LockOSThread()
	// COM 必须在本线程初始化。go-webview2 的 edge 包在自己的 init() 里调过
	// CoInitializeEx，但那是 Go runtime 的启动线程；面板窗口跑在 LockOSThread
	// 锁定的另一个线程上，而 COM 的 STA 是线程级的。不补这一句，
	// CreateCoreWebView2EnvironmentWithOptions 直接返回 CO_E_NOTINITIALIZED
	// (0x800401F0)——这是 v3 嵌入长期失败的真根因（2026-10-21 靠 daemon.log
	// 里的 HRESULT 定位，此前只有笼统提示，一直查不出）。
	// COINIT_APARTMENTTHREADED = 2；S_FALSE(1) 表示本线程已初始化过，同样可用。
	if r, _, _ := procCoInitializeEx.Call(0, 2); int32(r) < 0 {
		logToDaemon(fmt.Sprintf("面板线程 CoInitializeEx 失败: 0x%08x，回退 legacy", uint32(r)))
		openPanelLegacy(url)
		return
	}
	defer procCoUninitialize.Call()
	// 单实例：面板已打开 → 唤醒并聚焦，不创建第二个窗口
	//（多窗口会与全局 webview 引用互相顶替，导致尺寸联动错乱）
	if wakeExistingPanel() {
		return
	}
	lw, lh := panelLogicalSize()
	scale := systemDpiScale()
	wpx, hpx := int(float64(lw)*scale), int(float64(lh)*scale)

	inst, _, _ := procGetModuleHandleW.Call(0)
	if inst == 0 || !registerPanelClass(inst) {
		logToDaemon(fmt.Sprintf("panelWindowMain: registerPanelClass 失败 inst=%d", inst))
		openPanelLegacy(url)
		return
	}

	// 居中位置（屏幕小于窗口时退 0，防无符号下溢）
	sw, _, _ := procGetSystemMetrics.Call(0)
	sh, _, _ := procGetSystemMetrics.Call(1)
	swI, shI := int(sw), int(sh)
	x, y := int32(0), int32(0)
	if swI > wpx {
		x = int32((swI - wpx) / 2)
	}
	if shI > hpx {
		y = int32((shI - hpx) / 2)
	}

	cls, _ := windows.UTF16PtrFromString(panelWndClassName)
	name, _ := windows.UTF16PtrFromString("Columba")
	// WS_OVERLAPPEDWINDOW，不带 WS_VISIBLE：隐藏创建，装饰完再显示
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(name)),
		0xCF0000, // WS_OVERLAPPEDWINDOW
		uintptr(x), uintptr(y), uintptr(wpx), uintptr(hpx),
		0, 0, inst, 0,
	)
	if hwnd == 0 {
		logToDaemon("panelWindowMain: CreateWindowExW 失败，回退 legacy")
		openPanelLegacy(url)
		return
	}

	setWindowIcon(unsafe.Pointer(hwnd), dropIco)
	setNativeTitleBar(unsafe.Pointer(hwnd), appsUseDarkTheme())

	// 直接嵌入 WebView2 到自管窗口。本版 go-webview2 的 NewWithOptions
	// 忽略 Window 参数（永远自建窗口并把浏览器嵌进去），会产出一空壳
	// 一内容两个面板；edge.Chromium.Embed 才是真嵌入（同步完成，失败可回退）。
	browser := edge.NewChromium()
	browser.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	// 导航结果诊断：v3 面板曾出现「窗口正常、标题正常、内容全白」，而同一 URL
	// 用 Edge App 模式打开完全正常——说明 HTML/JS 没问题，是嵌入路径的渲染没起来。
	// 这个回调能给出 IsSuccess 与 WebErrorStatus，是判断「页面到底加载成功没有」
	// 的唯一可靠依据（比看截图可靠）。
	browser.NavigationCompletedCallback = func(sender *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {
		logToDaemon(fmt.Sprintf("面板导航完成: isSuccess=%v webErrorStatus=%d",
			args.IsSuccess(), args.WebErrorStatus()))
	}
	panelPinner.Pin(browser) // 递归固定整棵回调对象树，防 GC 移动（见 panelPinner 注释）
	panelBrowser = browser   // 先挂上：Embed 内部会泵消息，窗口过程可能先于就绪被调（Resize/Focus 均 nil 安全）
	if !browser.Embed(hwnd) {
		panelPinner.Unpin()
		panelBrowser = nil
		procDestroyWindow.Call(hwnd)
		if transfer.DebugLog != nil {
			transfer.DebugLog("面板 v3 嵌入失败（WebView2 环境/controller 创建失败），回退 legacy 路径")
		}
		openPanelLegacy(url)
		return
	}
	panelHwnd = hwnd
	browser.Resize() // 初始边界 = 客户区（不自定会停在 0x0 → 白屏）
	browser.Navigate(url)
	// WebView2 的 controller 创建后 IsVisible 默认是 false，必须显式 Show()，
	// 否则窗口与标题栏都正常、内容区却是一片空白。
	// 这是 v3 自管窗口路径独有的坑：legacy 由 go-webview2 库自己处理显示，
	// 所以「legacy 正常、v3 白屏」曾经很难归因（同一 URL 用 Edge App 模式
	// 打开完全正常，说明 HTML/JS 没问题）。2026-10-21 用 NavigationCompleted
	// 确认 isSuccess=true 后才定位到这里——页面加载成功，只是不可见。
	if err := browser.Show(); err != nil {
		logToDaemon("面板 Show 失败: " + err.Error())
	}
	// 隐藏状态下给渲染引擎 300ms 完成深色首绘：显示瞬间即是最终形态，
	// 不再出现「先白后黑」的初始化闪断
	time.Sleep(300 * time.Millisecond)

	go trackPanelSize(unsafe.Pointer(hwnd), scale) // 记住上次窗口大小
	procShowWindow.Call(hwnd, 5 /*SW_SHOW*/)
	// 显示后再 Resize 一次：创建时是 WS_OVERLAPPEDWINDOW 不带 WS_VISIBLE，
	// 隐藏窗口的 GetClientRect 在部分环境返回 0×0，之前那次 Resize 等于把
	// WebView2 视口设成了 0 —— 面板能显示但内容区全白。
	// ShowWindow 后尺寸已确定，这一次才是有效值。
	if panelBrowser != nil {
		panelBrowser.Resize()
	}
	panelMessageLoop() // 消息泵：WM_DESTROY → PostQuitMessage 后返回
	panelPinner.Unpin()
	panelHwnd = 0
	panelBrowser = nil
}

func registerPanelClass(hinstance uintptr) bool {
	cls, _ := windows.UTF16PtrFromString(panelWndClassName)
	// cls 必须活到 RegisterClassExW 之后：中间的 LoadCursorW 是系统调用/GC 点，
	// 而存进结构体的裸指针不再被 Go 引用，Go 1.27 起堆会移动就成了悬垂指针
	//（类注册随机失败 → 面板静默降级 legacy 白窗）。pin 由面板关闭时的
	// panelPinner.Unpin 释放，每次开面板 pin 一次。
	panelPinner.Pin(unsafe.Pointer(cls))
	var wc wndClassExW
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = windows.NewCallback(panelWndProc)
	wc.HInstance = hinstance
	wc.LpszClassName = uintptr(unsafe.Pointer(cls))
	// .Call 的 err 来自 GetLastError，成功时不保证为零——按返回句柄判断
	if hcur, _, _ := procLoadCursorW.Call(0, 32512 /*IDC_ARROW*/); hcur != 0 {
		wc.HCursor = hcur
	}
	atom, _, errno := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	// ERROR_CLASS_ALREADY_EXISTS：窗口类随进程终身存在，第二次打开面板时
	// 类还在、窗口过程还是同一个，直接复用——绝不能因此降级 legacy 路径
	//（曾因此每次重开面板都静默退回白窗）。
	if atom == 0 && errno != windows.ERROR_CLASS_ALREADY_EXISTS {
		return false
	}
	return true
}

func panelWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	const (
		wmDestroy     = 0x0002
		wmMove        = 0x0003
		wmSize        = 0x0005
		wmActivate    = 0x0006
		wmMoving      = 0x0216
		sizeMinimized = 1
		waInactive    = 0
	)
	switch msg {
	case wmDestroy:
		panelHwnd = 0
		panelBrowser = nil
		procPostQuitMessage.Call(0)
		return 0
	case wmSize:
		// 最小化不处理；Resize 内部自读客户区，controller 未就绪时安全空转
		if wp != sizeMinimized && panelBrowser != nil {
			panelBrowser.Resize()
		}
	case wmActivate:
		// 窗口获焦时把键盘焦点交给 WebView2（等价库 autofocus 行为）
		if wp != waInactive && panelBrowser != nil {
			panelBrowser.Focus()
		}
	case wmMove, wmMoving:
		// 弹层（下拉框/提示）跟随窗口位置
		if panelBrowser != nil {
			panelBrowser.NotifyParentWindowPositionChanged()
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// winMsg GetMessageW 的消息结构。
type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// panelMessageLoop 面板窗口的消息泵：WM_QUIT（窗口过程 WM_DESTROY 投递）后返回。
// 即库 WebView.Run 的精简版——面板不走 Eval/Bind，无需 dispatch 队列。
func panelMessageLoop() {
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) { // 0 = WM_QUIT；-1 = 错误
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

var (
	procGetDpiForSystem = user32.NewProc("GetDpiForSystem")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	dwmapi              = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWinAttr   = dwmapi.NewProc("DwmSetWindowAttribute")

	// COM 初始化：面板线程必须在用 WebView2 之前自己 CoInitializeEx
	//（STA 是线程级的，库的 init() 只覆盖了 Go 启动线程）。
	ole32              = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")

	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
)

// appsUseDarkTheme 读系统「应用模式」深色设置（与面板内容的跟随信号一致）。
func appsUseDarkTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, _ := k.GetIntegerValue("AppsUseLightTheme")
	return v == 0
}

// setNativeTitleBar 深色模式下声明窗口标题栏为深色（根治「打开时白、
// 点击后变黑」）。曾尝试 DWMWA_CAPTION_COLOR/TEXT_COLOR/BORDER_COLOR
// 三属性做标题栏同色融合，但在 Win11 26200 上会导致任务栏/标题栏图标
// 消失（白块），故回滚仅保留深色声明。
func setNativeTitleBar(hwnd unsafe.Pointer, dark bool) {
	// 显式设 0/1：早return会让深色标题栏永远翻不回浅色（手动主题切换实锤）
	on := int32(0)
	if dark {
		on = 1
	}
	procDwmSetWinAttr.Call(uintptr(hwnd), 20 /*DWMWA_USE_IMMERSIVE_DARK_MODE*/, uintptr(unsafe.Pointer(&on)), 4)
}

type winRect struct{ L, T, R, B int32 }

// systemDpiScale 系统显示缩放比例（96dpi = 1.0）。
func systemDpiScale() float64 {
	d, _, _ := procGetDpiForSystem.Call()
	if d == 0 {
		return 1.0
	}
	s := float64(d) / 96.0
	if s < 1.0 {
		s = 1.0
	}
	return s
}

// panelLogicalSize 上次面板窗口的逻辑尺寸（跨会话记忆），无记录或越界回默认。
func panelLogicalSize() (int, int) {
	if b, err := os.ReadFile(filepath.Join(identity.AppDir(), "panel_window.json")); err == nil {
		var v struct {
			W int `json:"w"`
			H int `json:"h"`
		}
		if json.Unmarshal(b, &v) == nil && v.W >= 500 && v.H >= 400 && v.W <= 5000 && v.H <= 4000 {
			return v.W, v.H
		}
	}
	return 920, 680
}

// trackPanelSize 面板打开期间轮询窗口矩形，变化时以逻辑像素持久化；
// GetWindowRect 失败（窗口已销毁）即退出。
func trackPanelSize(hwnd unsafe.Pointer, scale float64) {
	last := ""
	for {
		time.Sleep(500 * time.Millisecond)
		var rc winRect
		if r, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rc))); r == 0 {
			return
		}
		w := int(float64(rc.R-rc.L) / scale)
		h := int(float64(rc.B-rc.T) / scale)
		key := fmt.Sprintf("%dx%d", w, h)
		if key == last {
			continue
		}
		last = key
		b, _ := json.Marshal(struct {
			W int `json:"w"`
			H int `json:"h"`
		}{w, h})
		_ = os.WriteFile(filepath.Join(identity.AppDir(), "panel_window.json"), b, 0o644)
	}
}

// panelIcons 图标句柄缓存，下标 = WM_SETICON 的 WPARAM（0 小图 / 1 大图）。
// WM_SETICON 不移交所有权：窗口销毁时系统不会销毁我们创建的 HICON，所以每次
// 开面板新建两个就是净泄漏 2 个 GDI 对象；而 ico 是编译期内嵌常量、句柄与
// 具体窗口无关，进程级复用才是正确生命周期。
var panelIcons [2]uintptr

// setWindowIcon 从内存 ICO 设置窗口的大/小图标（任务栏与标题栏）。
func setWindowIcon(hwnd unsafe.Pointer, ico []byte) {
	if len(ico) < 22 {
		return
	}
	h := windows.HWND(hwnd)
	for _, size := range []struct{ w, h, flag uintptr }{{16, 16, 0}, {48, 48, 1}} {
		if cached := panelIcons[size.flag]; cached != 0 {
			procSendMessageW.Call(uintptr(h), 0x0080 /*WM_SETICON*/, size.flag, cached)
			continue
		}
		off, _, _ := procLookupIconIdFromDirectoryEx.Call(
			uintptr(unsafe.Pointer(&ico[0])), 1,
			size.w, size.h, 0)
		if off == 0 {
			continue
		}
		if int(off)+8 > len(ico) {
			continue
		}
		hIcon, _, _ := procCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&ico[off])),
			uintptr(len(ico)-int(off)),
			1, 0x00030000, size.w, size.h, 0)
		if hIcon == 0 { // 成功时 GetLastError 不保证为零，只看句柄
			continue
		}
		panelIcons[size.flag] = hIcon
		procSendMessageW.Call(uintptr(h), 0x0080 /*WM_SETICON*/, size.flag, hIcon)
	}
}

var (
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procLookupIconIdFromDirectoryEx = user32.NewProc("LookupIconIdFromDirectoryEx")
	procCreateIconFromResourceEx    = user32.NewProc("CreateIconFromResourceEx")
	procSendMessageW                = user32.NewProc("SendMessageW")
)

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
		// Preferences 策略只认每个 pref 子项下的具面值 Value + Status，
		// 写成匿名默认值 Firefox 会整条忽略（注册表写成功但豁免不生效，
		// 面板也收不到 warning，因为写入本身没报错）。
		err = pk.SetDWordValue("Value", 0)
		if err == nil {
			err = pk.SetStringValue("Status", "default")
		}
		if err != nil {
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

// firewallRuleName 本程序注册的入站规则名。用固定名而不是 exe 名：
// Windows 在程序首次监听端口时会弹「允许访问」并自动建一条**以 exe 名命名、
// 按 exe 路径绑定**的规则（实测用户机器上累积了 6 条 dropw.exe 规则，
// 对应 TEMP / Columba / Programs\Columba 三个路径）。程序一换目录，旧规则
// 就指向不存在的路径且不会自动失效，于是又弹一次窗——
// 用户每次都要多点两下。用固定名 + 每次覆盖，规则只留一条。
const firewallRuleName = "Columba"

func AllowFirewall() error {
	// 规则必须绑**监听端口的那个 exe**，不是调用本函数的 exe。
	//
	// 实测踩到的坑：`drop install` / `drop firewall` 都由 drop.exe 发起，
	// 所以 os.Executable() 是 drop.exe；但真正监听 42707 的是 dropw.exe
	//（无窗口 daemon）。规则绑到 drop.exe 上等于没加——对端照样连不进来，
	// 而面板/日志都显示「规则已注册」，非常难查。
	// helperExe() 返回同目录的 dropw.exe，正是需要的那个。
	exe := helperExe()
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	// 1) 清掉本程序以前留下的**固定名**规则（历史上叫 Drop，现在叫 Columba）
	for _, n := range []string{"Drop", firewallRuleName} {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+n).Run()
	}
	// 2) 清掉 Windows 自动生成、且已指向失效路径的 dropw 规则。
	//
	// 只删「程序路径已不存在」的：那些必然是历史残留（本程序换过目录），
	// 留着既占规则表、又让用户以为还在生效。**不动**路径仍有效的规则——
	// 那可能是用户有意保留的，删掉等于擅自撤销授权。
	cleanupStaleDropwRules()
	// 3) 加固定名规则，指向监听端口的 dropw.exe
	out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+firewallRuleName, "dir=in", "action=allow", "program="+exe, "enable=yes").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cleanupStaleDropwRules 删除 DisplayName 形如 "TCP Query User{...}...\dropw.exe"
// 且其 Program 路径已不存在的入站规则。尽力而为，失败不影响主流程。
//
// 用 netsh 而不是 Win32 API：本文件其余网络操作都走 netsh（见 AllowFirewall），
// 保持单一实现路径；这里只需按名字删，不需要查询结构化信息。
func cleanupStaleDropwRules() {
	// netsh 能按 program 精确过滤，但拿不到「路径是否存在」；先用 PowerShell 枚举
	// 出失效规则的 Name，再交给 netsh 删。PowerShell 已在别处用于快捷方式操作
	//（见 EnsureSendToLink），不是新引入的依赖。
	const ps = `$ErrorActionPreference='SilentlyContinue'
Get-NetFirewallRule -Direction Inbound | Where-Object {
  $_.DisplayName -like '*dropw.exe*' -and $_.DisplayName -notlike 'Columba*'
} | ForEach-Object {
  $prog = (Get-NetFirewallApplicationFilter -AssociatedNetFirewallRule $_).Program
  if ($prog -and -not (Test-Path $prog)) { $_.Name }
} | ForEach-Object { netsh advfirewall firewall delete rule name="$_" | Out-Null }`
	_ = runHiddenPowerShell(ps, 20*time.Second)
}

func RemoveFirewall() error {
	var firstErr error
	for _, n := range []string{"Drop", firewallRuleName} {
		out, err := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+n).CombinedOutput()
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	cleanupStaleDropwRules()
	return firstErr
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

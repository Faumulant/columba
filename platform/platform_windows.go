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
	"runtime"
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

// EnsureDesktopShortcut 创建（或修补）桌面 Columba 快捷方式。
// 图标落地为独立 .ico 再作 IconLocation：图标缓存绑定的是这个稳定文件，
// 开发期反复覆盖 exe 不再导致桌面图标变空白（经典图标缓存失效）。
// 已存在的快捷方式只更新图标，保留用户自己的目标配置。
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
	script := `
$desktop = [Environment]::GetFolderPath('Desktop')
$lnk = Join-Path $desktop 'Columba.lnk'
$ws = New-Object -ComObject WScript.Shell
$s = $ws.CreateShortcut($lnk)
if (-not (Test-Path $lnk)) {
  $s.TargetPath = '` + psQuote(exe) + `'
  $s.Arguments = 'panel'
  $s.WorkingDirectory = '` + psQuote(filepath.Dir(exe)) + `'
  $s.WindowStyle = 1
  $s.Description = 'Columba'
}
$s.IconLocation = '` + psQuote(icoPath) + `,0'
$s.Save()`
	return runHiddenPowerShell(script, 20*time.Second)
}

// RemoveDesktopShortcut 移除桌面快捷方式与落地的图标文件（卸载用，幂等）。
func RemoveDesktopShortcut() {
	os.Remove(filepath.Join(identity.AppDir(), "columba.ico"))
	script := `
$lnk = Join-Path ([Environment]::GetFolderPath('Desktop')) 'Columba.lnk'
if (Test-Path $lnk) { Remove-Item $lnk -Force }`
	_ = runHiddenPowerShell(script, 20*time.Second)
}

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
		return openPanelLegacy(url)
	}
	panelMu.Lock()
	defer panelMu.Unlock()
	if wakeExistingPanel() {
		return nil
	}
	go func() {
		runtime.LockOSThread()
		panelWindowMain(url)
	}()
	return nil
}

// wakeExistingPanel 已有面板窗口时唤醒并聚焦。用 FindWindow 按窗口类名查，
// 跨进程同样有效——托盘的 daemon 进程与 drop panel CLI 是两个进程，
// 进程内全局 panelHwnd 防不住跨进程重复打开。
func wakeExistingPanel() bool {
	cls, _ := windows.UTF16PtrFromString(panelWndClassName)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if hwnd == 0 {
		return false
	}
	procShowWindow.Call(hwnd, 9 /*SW_RESTORE*/)
	procSetForegroundWindow.Call(hwnd)
	return true
}

func openPanelLegacy(url string) error {
	go func() {
		runtime.LockOSThread()
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
	panelPinner.Pin(browser) // 递归固定整棵回调对象树，防 GC 移动（见 panelPinner 注释）
	panelBrowser = browser   // 先挂上：Embed 内部会泵消息，窗口过程可能先于就绪被调（Resize/Focus 均 nil 安全）
	if !browser.Embed(hwnd) {
		panelPinner.Unpin()
		panelBrowser = nil
		procDestroyWindow.Call(hwnd)
		openPanelLegacy(url)
		return
	}
	panelHwnd = hwnd
	browser.Resize() // 初始边界 = 客户区（不自定会停在 0x0 → 白屏）
	browser.Navigate(url)
	// 隐藏状态下给渲染引擎 300ms 完成深色首绘：显示瞬间即是最终形态，
	// 不再出现「先白后黑」的初始化闪断
	time.Sleep(300 * time.Millisecond)

	go trackPanelSize(unsafe.Pointer(hwnd), scale) // 记住上次窗口大小
	procShowWindow.Call(hwnd, 5 /*SW_SHOW*/)
	panelMessageLoop() // 消息泵：WM_DESTROY → PostQuitMessage 后返回
	panelPinner.Unpin()
	panelHwnd = 0
	panelBrowser = nil
}

func registerPanelClass(hinstance uintptr) bool {
	cls, _ := windows.UTF16PtrFromString(panelWndClassName)
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
	if !dark {
		return
	}
	on := int32(1)
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

// setWindowIcon 从内存 ICO 设置窗口的大/小图标（任务栏与标题栏）。
func setWindowIcon(hwnd unsafe.Pointer, ico []byte) {
	if len(ico) < 22 {
		return
	}
	h := windows.HWND(hwnd)
	for _, size := range []struct{ w, h, flag uintptr }{{16, 16, 0}, {48, 48, 1}} {
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

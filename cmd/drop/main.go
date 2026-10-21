// drop 是唯一的二进制入口：最初为 CLI，后续版本在此之上增加
// 常驻 daemon、系统集成的宿主与 native messaging host。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const usageText = `drop — 仅局域网的文件投递工具

日常使用：
  drop run                             常驻后台（托盘图标，接收+通知）
  drop install                         一次性系统安装（防火墙规则需管理员）
  drop firewall                        仅注册防火墙规则（安装器调用，需管理员）
  drop uninstall                       移除系统集成
配对与设备：
  drop pair                            生成一次性配对码，等待对方加入
  drop pair <配对码> [-to 名称|ID]     携带对方配对码加入对方
  drop devices                         已配对设备与在线状态
  drop rename <名称>                   修改本机设备名（不影响设备 ID）
  drop alias <设备> <备注>             给对方设备起备注名
发送与接收：
  drop send [-to 名称|ID] [-addr HOST:PORT] FILE...
                                       发送文件/文件夹（前台，带进度）
  drop receive [-port 42707] [-dir DIR]
                                       前台接收（调试用，日常用 run）
  drop history [-n 20]                 查看最近传输
内部命令：
  drop sendto --to <设备ID> FILE...    Explorer 发送到入口（勿手动调用）
  drop handle <drop:地址>              通知按钮回调（勿手动调用）
  drop native-host                     浏览器扩展宿主（勿手动调用）
  drop discover [-wait 6s]             扫描局域网内正在接收的设备
`

// isNativeHostInvocation 判断本次启动是否是浏览器在拉起 Native Messaging 宿主。
//
// 两个条件必须同时成立：
//  1. exe 基名是 dropw（GUI 子系统伴生）。按基名精确匹配而不是子串：子串匹配下
//     任何名字含 "dropw" 的改名二进制（如 dist 产物）裸启都会去读 stdin，
//     表现为「无输出挂着」。
//  2. 无参数（手工调试）**或** 首个参数是浏览器 origin。
//
// 为什么必须认 origin 参数：Chrome/Edge/Firefox 拉起宿主时一定带它，形如
// `chrome-extension://<id>/`。早先只判「无参数」→ 真实调用全部落到命令分发、
// 打印「未知命令」后 exit 2，Chrome 报 `Error when communicating with the
// native messaging host`。而自测脚本恰好是无参数启动的，所以一直没测出来。
func isNativeHostInvocation() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe") != "dropw" {
		return false
	}
	if len(os.Args) < 2 {
		return true // 手工调试
	}
	return isBrowserOrigin(os.Args[1])
}

// isBrowserOrigin 首个参数是否是浏览器传来的调用方 origin。
// 三种浏览器的形状：
//
//	chrome-extension://<id>/    Chrome / Edge
//	moz-extension://<uuid>/     Firefox
//	<id>@<host>                 Firefox 的旧式扩展 ID（无 scheme）
//
// 只认这些前缀，避免把真正的命令行（如 `dropw run`）误判成宿主调用。
func isBrowserOrigin(arg string) bool {
	a := strings.TrimSpace(arg)
	if strings.HasPrefix(a, "chrome-extension://") || strings.HasPrefix(a, "moz-extension://") {
		return true
	}
	// Firefox 旧式 ID：形如 drop@example.com，不含空白与路径分隔符
	return strings.Contains(a, "@") &&
		!strings.ContainsAny(a, ` \/`) &&
		!strings.Contains(a, ":")
}

func main() {
	// ─── 浏览器 Native Messaging 宿主 ───────────────────────────
	//
	// Chrome/Edge/Firefox 拉起宿主时**一定带参数**：第一个参数是调用方 origin，
	// 形如 `chrome-extension://<id>/`。所以「无参数 = 宿主」这个判断是错的——
	// 真实调用永远有参数，会落到下面的命令分发里打印「未知命令」然后 exit 2，
	// Chrome 侧表现为 `Error when communicating with the native messaging host`。
	//
	// 判定改成「exe 名是 dropw 且（无参 或 首个参数形如浏览器 origin）」：
	//   · 无参仍兼容（手工调试 / 老版本调用）
	//   · 带 origin 参数才是浏览器的真实调用方式
	// 名字必须是 dropw：drop.exe 是控制台主程序，带 origin 参数时应当照常报未知命令。
	if isNativeHostInvocation() {
		if err := nativeHostLoop(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "id":
		err = cmdID()
	case "rename":
		err = cmdRename(os.Args[2:])
	case "pair":
		err = cmdPair(os.Args[2:])
	case "devices":
		err = cmdDevices(os.Args[2:])
	case "alias":
		err = cmdAlias(os.Args[2:])
	case "discover":
		err = cmdDiscover(os.Args[2:])
	case "receive":
		err = cmdReceive(os.Args[2:])
	case "send":
		err = cmdSend(os.Args[2:])
	case "sendto":
		err = cmdSendto(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "panel":
		err = cmdPanel()
	case "native-host":
		err = nativeHostLoop()
	case "install":
		err = cmdInstall()
	case "firewall":
		err = cmdFirewall()
	case "uninstall":
		err = cmdUninstall()
	case "handle":
		err = cmdHandle(os.Args[2:])
	case "history":
		err = cmdHistory(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

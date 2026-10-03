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

func main() {
	if len(os.Args) < 2 {
		// dropw.exe（GUI 子系统伴生）无参启动 = 浏览器 Native Messaging 宿主；
		// drop.exe（控制台主程序）无参显示用法。
		if exe, err := os.Executable(); err == nil &&
			strings.Contains(strings.ToLower(filepath.Base(exe)), "dropw") {
			if err := nativeHostLoop(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
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

//go:build windows

// paneltest 面板窗口冒烟测试：不启动 daemon，直接走 OpenPanelWindow 完整路径，
// 验证嵌入修复后只产生一个顶层窗口（连开两次验证唤醒逻辑）。
// 参数 "legacy" 时改走库自管窗口路径（对照实验）。测试后删除，不入库。
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/jchv/go-webview2"

	"drop/platform"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "legacy" {
		go func() {
			runtime.LockOSThread()
			w := webview2.NewWithOptions(webview2.WebViewOptions{
				WindowOptions: webview2.WindowOptions{Title: "paneltest-legacy"},
			})
			if w == nil {
				fmt.Println("LEGACY_NIL")
				return
			}
			defer w.Destroy()
			w.Navigate("https://example.com")
			w.Run()
		}()
		fmt.Println("READY")
		time.Sleep(60 * time.Second)
		return
	}
	if err := platform.OpenPanelWindow("https://example.com"); err != nil {
		fmt.Fprintln(os.Stderr, "open1:", err)
		os.Exit(1)
	}
	time.Sleep(3 * time.Second)
	// 第二次打开：应唤醒既有窗口而非新建
	if err := platform.OpenPanelWindow("https://example.com"); err != nil {
		fmt.Fprintln(os.Stderr, "open2:", err)
		os.Exit(1)
	}
	fmt.Println("READY")
	time.Sleep(60 * time.Second)
}

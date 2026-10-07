//go:build windows

// winenum 枚举指定 PID 的全部顶层窗口（类名/可见性/标题），冒烟测试辅助，用后删除。
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

var (
	u32                    = syscall.NewLazyDLL("user32.dll")
	procEnumWindows        = u32.NewProc("EnumWindows")
	procGetWindowThreadPID = u32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW      = u32.NewProc("GetClassNameW")
	procIsWindowVisible    = u32.NewProc("IsWindowVisible")
	procGetWindowTextW     = u32.NewProc("GetWindowTextW")
)

func main() {
	pid64, err := strconv.ParseUint(os.Args[1], 10, 32)
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: winenum <pid>")
		os.Exit(2)
	}
	target := uint32(pid64)
	cb := syscall.NewCallback(func(hwnd, l uintptr) uintptr {
		var pid uint32
		procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == target {
			cbuf := make([]uint16, 256)
			procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cbuf[0])), 256)
			tbuf := make([]uint16, 256)
			procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&tbuf[0])), 256)
			vis, _, _ := procIsWindowVisible.Call(hwnd)
			fmt.Printf("class=%q visible=%v title=%q\n",
				syscall.UTF16ToString(cbuf), vis != 0, syscall.UTF16ToString(tbuf))
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
}

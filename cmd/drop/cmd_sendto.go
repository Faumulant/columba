package main

import (
	"errors"
	"flag"
	"os"

	"drop/platform"
)

// drop sendto —— Explorer"发送到"快捷方式的瞬发垫片：
// 校验参数后以无窗口分离子进程执行真正的发送，自身立即退出，
// 保证资源管理器操作不闪黑窗。传输结果由系统通知汇报。
func cmdSendto(args []string) error {
	fs := flag.NewFlagSet("sendto", flag.ExitOnError)
	to := fs.String("to", "", "目标设备 ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	files := fs.Args()
	if *to == "" || len(files) == 0 {
		return errors.New("用法：drop sendto --to <设备ID> FILE...")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return platform.SpawnDetached(exe, append([]string{"send", "-notify", "--to", *to}, files...))
}

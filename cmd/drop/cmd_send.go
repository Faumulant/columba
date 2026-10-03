package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"drop/core/identity"
	"drop/core/protocol"
	"drop/core/transfer"
	"drop/platform"
)

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "目标设备名称或 ID（发现到多台设备时必填）")
	addr := fs.String("addr", "", "直连 HOST:PORT（跳过发现，调试用）")
	wait := fs.Duration("wait", 7*time.Second, "设备发现等待时长")
	notify := fs.Bool("notify", false, "完成后弹系统通知（sendto 后台模式使用）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("缺少要发送的文件或文件夹")
	}
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}
	var t transfer.Target
	var sent string
	run := func() error {
		if *addr != "" {
			t = transfer.Target{Addr: *addr, ID: *to}
		} else if ct, ok := transfer.CachedTarget(id, protocol.NormalizeID(*to)); ok {
			t = ct // 缓存直连：省去发现/探测
		} else {
			var derr error
			if t, derr = transfer.FindPeer(id, *to, *wait, trust); derr != nil {
				return derr
			}
		}
		name := t.Name
		if display := trust.Name(t.ID); display != "" {
			name = display // 优先用本机信任库的显示名（备注名优先）
		} else if name == "" {
			name = *addr
		}
		fmt.Fprintf(os.Stderr, "发送目标：%s\n", name)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		sent, err = transfer.Send(ctx, id, t, fs.Args())
		return err
	}
	err = run()
	if *notify {
		if err != nil {
			platform.Notify(platform.Notif{Title: "Columba 发送失败", Body: fmt.Sprintf("%v", err)})
		} else {
			platform.Notify(platform.Notif{Title: "Columba", Body: sent + " 已发送到 " + t.Name})
		}
		time.Sleep(3 * time.Second)
	}
	return err
}

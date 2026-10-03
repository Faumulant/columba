package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"drop/core/identity"
	"drop/core/transfer"
	"drop/platform"
)

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	to := fs.String("to", "", "目标设备名称或 ID（同时可见多台设备时必填）")
	addr := fs.String("addr", "", "直连 HOST:PORT（跳过发现）")
	wait := fs.Duration("wait", 7*time.Second, "设备发现等待时长")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}

	// drop pair —— 生成配对码，等待对方加入
	if fs.NArg() == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		r := transfer.NewReceiver(id, trust, transfer.ReceiveOptions{
			Port:        42707,
			Dir:         filepath.Join(home, "Downloads", "Drop"),
			ConfirmPeer: confirmPrompt,
			FreeDisk:    platform.FreeDiskBytes,
			ExitOnPair:  true,
		})
		code, err := r.StartPairing()
		if err != nil {
			return err
		}
		fmt.Printf("配对码：%s（5 分钟内有效，一次性）\n等待对方执行 drop pair %s …\n", code, code)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := r.Run(ctx); err != nil {
			return err
		}
		return nil
	}

	// drop pair <配对码> —— 携带对方的码加入对方
	code := fs.Arg(0)
	var t transfer.Target
	if *addr != "" {
		t = transfer.Target{Addr: *addr, ID: *to}
	} else {
		// 配对对象尚未入信任库，发现时不做信任过滤（trust 传 nil）
		if t, err = transfer.FindPeer(id, *to, *wait, nil); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "正在向 %s 发起配对，等待对方确认…\n", targetName(t))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	peerName, err := transfer.Pair(ctx, id, trust, t, code)
	if err != nil {
		return err
	}
	fmt.Printf("✓ 配对完成，%s 已加入可信设备\n", peerName)
	return nil
}

// targetName 直连时没有设备名，显示地址兜底。
func targetName(t transfer.Target) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Addr
}

// confirmPrompt 本机确认是否接受对方的配对请求。
func confirmPrompt(name, id string) bool {
	if os.Getenv("DROP_AUTO_CONFIRM") != "" {
		fmt.Fprintln(os.Stderr, "（DROP_AUTO_CONFIRM 已设置，自动确认）")
		return true
	}
	fmt.Fprint(os.Stderr, "   允许该设备加入？(y/n) ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

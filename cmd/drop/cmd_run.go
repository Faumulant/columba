package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"drop/core/discover"
	"drop/core/identity"
	"drop/core/ipc"
	"drop/core/protocol"
	"drop/core/transfer"
	"drop/platform"
)

// dropURL 生成通知按钮用的 drop: 协议地址。
func dropURL(action, path string) string {
	return "drop:" + action + "?path=" + url.QueryEscape(path)
}

func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// drop run —— 常驻后台：接收 + 宣告 + 托盘 + 面板 IPC + 收件通知。
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	hidden := fs.Bool("hidden", false, "由开机自启调用：以无窗口子进程方式重启自身")
	hiddenChild := fs.Bool("hidden-child", false, "内部使用：已在无窗口子进程中")
	port := fs.Int("port", 42707, "TCP 监听端口")
	dir := fs.String("dir", "", "默认接收目录（面板设置可覆盖）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hidden && !*hiddenChild {
		// Run 键启动控制台程序会带出黑窗：立即以无窗口方式转储子进程。
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return platform.SpawnDetached(exe, []string{"run", "--hidden-child"})
	}

	already, release, err := platform.AcquireSingleInstance()
	if err != nil {
		return err
	}
	if already {
		// 已有后台在跑：若其内嵌面板与本版本不同（即运行的是旧版），
		// 通知它退出并接管——双击快捷方式即完成"更新+重启"。
		if !tryUpgrade() {
			return errors.New("Columba 后台已在运行（托盘图标可退出）")
		}
		a2, r2, err2 := platform.AcquireSingleInstance()
		if err2 != nil {
			return err2
		}
		if a2 {
			return errors.New("Columba 后台已在运行（托盘图标可退出）")
		}
		release = r2
	}
	defer release()

	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		cancel()
	}()

	// 排障日志：发现/发送路径的关键事件落盘（transfer.DebugLog 钩子，
	// 手机端同款）。超过 2MB 轮换一次，避免无限增长。
	logPath := filepath.Join(identity.AppDir(), "daemon.log")
	transfer.DebugLog = func(format string, args ...any) {
		if st, err := os.Stat(logPath); err == nil && st.Size() > 2<<20 {
			os.Remove(logPath + ".old")
			os.Rename(logPath, logPath+".old")
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		fmt.Fprintln(f, time.Now().Format("01-02 15:04:05.000 ")+fmt.Sprintf(format, args...))
		f.Close()
	}
	transfer.DebugLog("daemon 启动 port=%d pid=%d", *port, os.Getpid())

	// 发现：宣告自己 + 收集他人宣告（面板在线状态用）
	peers := discover.NewTracker()
	disc, err := discover.New(id.ID())
	if err != nil {
		fmt.Fprintln(os.Stderr, "※ 设备发现不可用（直连仍可用）:", err)
	} else {
		defer disc.Close()
		disc.StartAnnouncing(protocol.Announce{Proto: protocol.Version, ID: id.ID(), Name: id.Name, Port: *port})
		go func() {
			for e := range disc.Events() {
				peers.Feed(e)
				// 对端改名自动同步进信任库（备注名优先，不受影响）
				trust.RefreshName(e.Announce.ID, e.Announce.Name, e.IP)
			}
		}()
	}

	// IPC（配置中的接收目录优先于默认值）
	s, err := ipc.New(id, trust, peers)
	if err != nil {
		return err
	}
	defaultDir := *dir
	if defaultDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		defaultDir = filepath.Join(home, "Downloads", "Drop")
	}

	recv := transfer.NewReceiver(id, trust, transfer.ReceiveOptions{
		Port:        *port,
		Dir:         defaultDir,
		DirFn:       s.RecvDir,
		ConfirmPeer: s.ConfirmPeer,
		FreeDisk:    platform.FreeDiskBytes,
		MaxDevices: func() int {
			if identity.IsUnlocked() {
				return 1 << 30
			}
			return 3
		},
		OnReceived: func(peer, name, path string) {
			platform.Notify(platform.Notif{
				Title: "Columba 已接收",
				Body:  name + "（来自 " + peer + "）",
				Actions: []platform.Action{
					{Label: "打开", URL: dropURL("open", path)},
					{Label: "打开所在位置", URL: dropURL("reveal", path)},
				},
			})
		},
		OnText: func(peer, text string) {
			platform.Notify(platform.Notif{
				Title: "Columba 收到文本",
				Body:  "来自 " + peer + "：" + firstChars(text, 40),
			})
		},
	})
	s.SetReceiver(recv)
	s.NotifyFile = func(dir, peer, name string) {
		if dir == "send" {
			platform.Notify(platform.Notif{Title: "Columba", Body: name + " 已发送到 " + peer})
		}
	}
	s.NotifyText = func(dir, peer, text string) {
		if dir == "send" {
			platform.Notify(platform.Notif{Title: "Columba", Body: "文本已发送到 " + peer})
		}
	}
	s.NotifyErr = func(title, msg string) {
		platform.Notify(platform.Notif{Title: title, Body: msg})
	}
	s.OnDeviceRemoved = func(display string) { platform.RemoveSendToLink(display) }

	go func() {
		if err := recv.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "接收端异常退出:", err)
			cancel()
		}
	}()
	go s.StartProber(ctx)
	go s.StartUpdateCheck(ctx)
	go syncSendToLoop(ctx, trust)

	panelURL, err := s.Start(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "后台就绪：%s\n面板 %s\n", id.Name, panelURL)

	items := []platform.TrayItem{
		{Label: "打开面板", OnClick: func() { platform.OpenPanelWindow(panelURL) }},
		{Label: "打开接收目录", OnClick: func() { platform.OpenPath(s.RecvDir()) }},
	}
	return platform.RunTray(ctx, "Columba — 局域网投递（接收中）", items, cancel)
}

// tryUpgrade 检测运行中的后台是否为旧版本：比对内嵌面板哈希，
// 不同则请求旧后台退出并轮询等锁释放。返回 false 表示版本相同或无法判断。
func tryUpgrade() bool {
	raw, err := os.ReadFile(filepath.Join(identity.AppDir(), "ipc.json"))
	if err != nil {
		return false
	}
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &info) != nil || info.Port == 0 {
		return false
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/panel?t=%s", info.Port, info.Token))
	if err != nil {
		return false // 后台没响应，交给正常报错路径
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) == ipc.PanelHash() {
		return false // 版本相同，不是升级场景
	}
	http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/shutdown?t=%s", info.Port, info.Token),
		"application/json", nil)
	// 轮询等旧进程退出释放单实例锁
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		a, rel, err := platform.AcquireSingleInstance()
		if err == nil && !a {
			rel()
			return true
		}
	}
	return false
}

// syncSendToLoop 保持"发送到"菜单与已配对设备一致：信任库有变动时
// 为每台设备补建快捷方式。
func syncSendToLoop(ctx context.Context, trust *identity.TrustStore) {
	last := time.Time{}
	synced := false
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if !synced || trustChanged(&last) {
			for _, p := range trust.List() {
				if err := platform.EnsureSendToLink(p.DisplayName(), p.ID); err != nil {
					fmt.Fprintln(os.Stderr, "发送到快捷方式创建失败:", err)
				}
			}
			synced = true
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func trustChanged(last *time.Time) bool {
	p := identity.AppDir() + string(os.PathSeparator) + "trust.json"
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	if st.ModTime().After(*last) {
		*last = st.ModTime()
		return true
	}
	return false
}

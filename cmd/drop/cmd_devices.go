package main

import (
	"flag"
	"fmt"
	"time"

	"drop/core/discover"
	"drop/core/identity"
	"drop/core/protocol"
	"drop/core/transfer"
)

// drop devices —— 列出已配对设备与在线状态。
// 在线判定：短时间内收到过该设备的组播宣告。
func cmdDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	wait := fs.Duration("wait", 3*time.Second, "探测在线状态的时长")
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
	peers := trust.List()
	if len(peers) == 0 {
		fmt.Println("还没有已配对设备：运行 drop pair 开始配对")
		return nil
	}

	tr := discover.NewTracker()
	disc, err := discover.New(id.ID())
	if err == nil {
		defer disc.Close()
		ch := disc.Events()
		deadline := time.After(*wait)
	loop:
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					break loop
				}
				tr.Feed(e)
			case <-deadline:
				break loop
			}
		}
	}
	online := map[string]bool{}
	for _, info := range tr.Snapshot() {
		online[protocol.NormalizeID(info.Announce.ID)] = true
	}
	// 组播收不到时（路由器/AP 不转发组播很常见）用子网探测补充在线状态
	if len(online) < len(peers) {
		if found, err := transfer.Probe(id); err == nil {
			for _, t := range found {
				online[protocol.NormalizeID(t.ID)] = true
			}
		}
	}
	for _, p := range peers {
		status := "离线"
		if online[p.ID] {
			status = "在线"
		}
		fmt.Printf("%s  %-16s %s\n", status, p.DisplayName(), protocol.DisplayID(p.ID))
	}
	return nil
}

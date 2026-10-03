package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"drop/core/discover"
	"drop/core/identity"
	"drop/core/protocol"
)

func cmdDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	wait := fs.Duration("wait", 6*time.Second, "扫描时长")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	disc, err := discover.New(id.ID())
	if err != nil {
		return err
	}
	defer disc.Close()
	ch := disc.Events()
	tr := discover.NewTracker()
	fmt.Fprintln(os.Stderr, "正在扫描局域网…")
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
	peers := tr.Snapshot()
	if len(peers) == 0 {
		fmt.Println("未发现设备（对方需已运行 drop receive）")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("在线  %-16s %s:%d  %s\n",
			p.Announce.Name, p.IP, p.Announce.Port, protocol.DisplayID(p.Announce.ID))
	}
	return nil
}

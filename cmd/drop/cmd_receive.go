package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"

	"drop/core/identity"
	"drop/core/transfer"
	"drop/platform"
)

func cmdReceive(args []string) error {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	port := fs.Int("port", 42707, "TCP 监听端口")
	dir := fs.String("dir", "", "接收目录（默认 ~/Downloads/Drop）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*dir = filepath.Join(home, "Downloads", "Drop")
	}
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}
	r := transfer.NewReceiver(id, trust, transfer.ReceiveOptions{Port: *port, Dir: *dir, FreeDisk: platform.FreeDiskBytes})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return r.Run(ctx)
}

package main

import (
	"flag"

	"drop/core/history"
)

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	n := fs.Int("n", 20, "显示条数")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return history.Print(*n)
}

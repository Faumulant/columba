package main

import (
	"fmt"

	"drop/core/identity"
	"drop/core/protocol"
)

func cmdID() error {
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}
	fmt.Printf("设备名称   %s\n设备 ID    %s\n数据目录   %s\n已信任设备 %d 台\n",
		id.Name, protocol.DisplayID(id.ID()), identity.AppDir(), trust.Count())
	return nil
}

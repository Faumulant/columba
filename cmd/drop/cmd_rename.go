package main

import (
	"errors"
	"fmt"
	"strings"

	"drop/core/identity"
)

// drop rename <名称> —— 修改本机设备名（对方看到的显示名）。
// 不改动证书，设备 ID 与已有信任关系不受影响。
func cmdRename(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：drop rename <名称>")
	}
	name := strings.Join(args, " ")
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	if err := id.SetName(name); err != nil {
		return err
	}
	fmt.Printf("✓ 本机设备名已改为：%s\n", name)
	return nil
}

package main

import (
	"errors"
	"fmt"
	"strings"

	"drop/core/identity"
)

// drop alias <设备> <备注> —— 给已配对设备设置本机备注名；
// 备注为空串时清除。显示与 -to 匹配均优先使用备注名。
func cmdAlias(args []string) error {
	if len(args) < 2 {
		return errors.New("用法：drop alias <设备名称或ID> <备注>（备注留空清除）")
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return err
	}
	device := args[0]
	alias := strings.Join(args[1:], " ")
	if !trust.SetAlias(device, alias) {
		return fmt.Errorf("未找到已配对设备 %q（先 drop devices 查看列表）", device)
	}
	if alias == "" {
		fmt.Printf("✓ 已清除 %s 的备注名\n", device)
	} else {
		fmt.Printf("✓ %s 的备注名已设为：%s\n", device, alias)
	}
	return nil
}

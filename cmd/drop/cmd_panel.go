package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"drop/core/identity"
	"drop/platform"
)

// drop panel —— 在独立应用窗口打开管理面板（需要后台正在运行）。
func cmdPanel() error {
	raw, err := os.ReadFile(filepath.Join(identity.AppDir(), "ipc.json"))
	if err != nil {
		return errors.New("后台未运行：先启动 drop run，或从托盘打开面板")
	}
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &info); err != nil || info.Port == 0 {
		return errors.New("后台信息无效：重启 drop run")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/panel?t=%s", info.Port, info.Token)
	return platform.OpenPanelWindow(url)
}

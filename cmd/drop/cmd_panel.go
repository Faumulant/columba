package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"drop/core/identity"
)

// drop panel —— 请求后台 daemon 打开管理面板。面板归 daemon 所有，
// 本命令发完请求即退；后台未运行时给出指引。
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
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/panel/open?t=%s", info.Port, info.Token), "application/json", nil)
	if err != nil {
		return errors.New("后台未运行：先启动 drop run，或从托盘打开面板")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("面板打开失败：后台返回 " + resp.Status)
	}
	return nil
}

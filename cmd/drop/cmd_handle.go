package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"drop/core/history"
	"drop/platform"
)

// drop handle —— drop: 协议回调入口（通知按钮点击时由系统调用）。
// 支持的地址：drop:open?path=... / drop:reveal?path=...
//
// 安全：该协议已注册到系统，任何网页都能构造 drop: 链接诱导点击，
// 因此只允许打开本机传输历史中出现过的路径（接收目录或其中的文件），
// 不接受任意路径（尤其 UNC 路径，可能被用于远程加载可执行文件）。
func cmdHandle(args []string) error {
	if len(args) == 0 {
		return errors.New("缺少 drop: 地址")
	}
	raw := strings.TrimPrefix(args[0], "drop:")
	action, query, _ := strings.Cut(raw, "?")
	vals, err := url.ParseQuery(query)
	if err != nil {
		return err
	}
	path := vals.Get("path")
	if path == "" {
		return errors.New("缺少 path 参数")
	}
	if !knownPath(path) {
		return fmt.Errorf("路径不在本机传输记录中，拒绝打开: %s", path)
	}
	switch action {
	case "open":
		return platform.OpenPath(path)
	case "reveal":
		return platform.RevealPath(path)
	default:
		fmt.Fprintf(os.Stderr, "未知动作 %q\n", action)
		return nil
	}
}

// knownPath 路径白名单：历史记录中的接收目录（Entry.Path）或目录+文件名。
func knownPath(p string) bool {
	p = filepath.Clean(p)
	for _, e := range history.Load() {
		for _, cand := range []string{e.Path, filepath.Join(e.Path, e.Name)} {
			if cand == "" || cand == "." {
				continue
			}
			if strings.EqualFold(filepath.Clean(cand), p) {
				return true
			}
		}
	}
	return false
}

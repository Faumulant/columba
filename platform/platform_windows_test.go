//go:build windows

package platform

import "testing"

func TestFreeDiskBytes(t *testing.T) {
	n, err := FreeDiskBytes(`C:\`)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1<<20 {
		t.Fatalf("可用空间异常小: %d", n)
	}
	if _, err := FreeDiskBytes(`Q:\不存在的盘`); err == nil {
		t.Fatal("无效路径应返回错误")
	}
}

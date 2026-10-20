package identity

// saveLocked 规格：同目录临时文件 + rename 原子替换。写裂的 trust.json 会让
// 新进程解析失败 → 信任库为空 → 全部设备被拒；JSON 是缩进多行的，半截文件
// 连「保留内存视图」都救不回来。测试用足够大的库把重写拉成多次写调用，
// 并发读者只允许看到旧版或新版（完整 JSON 数组以 "]" 收尾）。
//
// 已知未修：CLI 与 daemon 各持内存视图、后写覆盖先写的丢更新问题仍在，
// 根治要跨平台文件锁（本次只保证不写裂）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrustSaveAtomicNeverTears(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	dir := AppDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建数据目录: %v", err)
	}
	list := make([]Peer, 0, 800)
	for i := 0; i < 800; i++ {
		list = append(list, Peer{
			ID:      fmt.Sprintf("%064X", i),
			Name:    "设备",
			CertPEM: strings.Repeat("M", 700),
			AddedAt: time.Now(),
		})
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatalf("构造信任库: %v", err)
	}
	trustPath := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trustPath, b, 0o600); err != nil {
		t.Fatalf("写入信任库: %v", err)
	}

	tr, err := LoadTrust()
	if err != nil {
		t.Fatalf("加载信任库: %v", err)
	}
	if tr.Count() != 800 {
		t.Fatalf("前置条件：应加载 800 台设备, got %d", tr.Count())
	}

	var torn atomic.Bool
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if raw, err := os.ReadFile(trustPath); err == nil &&
				(len(raw) == 0 || !bytes.HasSuffix(raw, []byte("]"))) {
				torn.Store(true)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// 反复保存：单轮撞进重写窗口的概率有限，多轮把它压成稳定复现
	for i := 0; i < 6; i++ {
		if !tr.SetAlias(list[i].ID, "备注名") {
			t.Fatalf("SetAlias 应命中设备 %d", i)
		}
	}
	close(stop)
	<-done

	if torn.Load() {
		t.Fatal("读者撞上写裂的 trust.json：保存必须原子替换")
	}
	if got := tr.Name(list[0].ID); got != "备注名" {
		t.Fatalf("备注名未生效: got %q", got)
	}
	// 重新加载必须拿到完整的 800 台（文件写裂时这里会掉到 0）
	reloaded, err := LoadTrust()
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if reloaded.Count() != 800 || reloaded.Name(list[0].ID) != "备注名" {
		t.Fatalf("重载后的信任库不完整: count=%d", reloaded.Count())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取数据目录: %v", err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("保存留下了临时文件 %s", e.Name())
		}
	}
}

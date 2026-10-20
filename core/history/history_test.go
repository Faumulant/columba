package history

// DeleteIDs 是整文件重写，而 daemon 随时可能在 Append、面板随时可能在读：
// 读者绝不能撞上半截 JSONL。写法规格是「同目录临时文件 + rename 原子替换」
//（见 writeAtomic）；完整文件每行都以换行结尾，所以读到空文件或末尾无换行
// 就等于撞上了重写中途。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeleteIDsRewriteIsAtomic(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatalf("创建数据目录: %v", err)
	}
	for i := 0; i < 4000; i++ {
		Append(Entry{Dir: "recv", Peer: "测试手机", Name: strings.Repeat("文件名", 20),
			Size: int64(i), Status: "ok"})
	}
	if before := Load(); len(before) != 4000 {
		t.Fatalf("前置条件：应有 4000 条记录, got %d", len(before))
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
			if raw, err := os.ReadFile(path()); err == nil &&
				(len(raw) == 0 || !bytes.HasSuffix(raw, []byte("\n"))) {
				torn.Store(true)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()

	// 反复重写：单轮撞进重写窗口的概率有限，多轮把它压成稳定复现
	removed := 0
	for round := 0; round < 12; round++ {
		var ids []string
		for i, e := range Load() {
			if i%80 == 0 && len(ids) < 50 {
				ids = append(ids, e.ID)
			}
		}
		n, err := DeleteIDs(ids)
		if err != nil {
			t.Fatalf("第 %d 轮 DeleteIDs 失败: %v", round, err)
		}
		removed += n
	}
	close(stop)
	<-done

	if torn.Load() {
		t.Fatal("读者撞上了半截历史文件：整文件重写必须原子替换")
	}
	if left := len(Load()); left != 4000-removed {
		t.Fatalf("删除后应剩 %d 条, got %d", 4000-removed, left)
	}
	// 临时文件随 rename 走掉，目录里不留残渣
	ents, err := os.ReadDir(filepath.Dir(path()))
	if err != nil {
		t.Fatalf("读取历史目录: %v", err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("重写留下了临时文件 %s", e.Name())
		}
	}
}

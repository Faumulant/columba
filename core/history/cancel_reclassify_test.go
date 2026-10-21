package history

// ReclassifyCanceled 的语义边界：取消通知与「读中断」两条事件先后不确定，
// 历史里必须恰好一条记录，且状态为 canceled。

import (
	"os"
	"path/filepath"
	"testing"
)

func setupHistoryDir(t *testing.T) {
	t.Helper()
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatalf("创建数据目录: %v", err)
	}
}

// 读中断先到（已写 failed）→ 取消到达时就地改写，不追加第二条。
func TestReclassifyCanceledRewritesFailed(t *testing.T) {
	setupHistoryDir(t)
	Append(Entry{Dir: "recv", Peer: "手机", PeerID: "AA:BB", Name: "x.txt",
		Size: 16, Status: StatusFailed, Error: "传输中断，已丢弃不完整文件"})

	if !ReclassifyCanceled("recv", "AA:BB", "x.txt", "对方已取消传输") {
		t.Fatal("应改写既有 failed 记录")
	}
	got := Load()
	if len(got) != 1 {
		t.Fatalf("改写不应追加新记录, got %d 条", len(got))
	}
	if got[0].Status != StatusCanceled {
		t.Fatalf("状态应为 canceled, got %q", got[0].Status)
	}
	if got[0].Error != "对方已取消传输" {
		t.Fatalf("文案应换成中性中文, got %q", got[0].Error)
	}
	if got[0].Size != 16 || got[0].Name != "x.txt" {
		t.Fatalf("其余字段应保留, got %+v", got[0])
	}
}

// 只改写最近一条匹配项：更早的同名 failed 记录保持原样（那是另一次传输）。
func TestReclassifyCanceledOnlyLatest(t *testing.T) {
	setupHistoryDir(t)
	Append(Entry{Dir: "recv", PeerID: "AA:BB", Name: "x.txt", Status: StatusFailed, Error: "旧故障"})
	Append(Entry{Dir: "recv", PeerID: "AA:BB", Name: "x.txt", Status: StatusFailed, Error: "传输中断，已丢弃不完整文件"})

	if !ReclassifyCanceled("recv", "AA:BB", "x.txt", "对方已取消传输") {
		t.Fatal("应改写最近一条")
	}
	got := Load()
	if len(got) != 2 {
		t.Fatalf("应仍有 2 条, got %d", len(got))
	}
	if got[0].Status != StatusFailed || got[0].Error != "旧故障" {
		t.Fatalf("更早的记录不该被动, got %+v", got[0])
	}
	if got[1].Status != StatusCanceled {
		t.Fatalf("最近一条应为 canceled, got %q", got[1].Status)
	}
}

// 方向/文件名/对端不匹配时不改写（防止把别人的记录改掉）。
func TestReclassifyCanceledRespectsFields(t *testing.T) {
	setupHistoryDir(t)
	Append(Entry{Dir: "recv", PeerID: "AA:BB", Name: "x.txt", Status: StatusFailed, Error: "传输中断"})

	cases := []struct{ dir, peerID, name string }{
		{"send", "AA:BB", "x.txt"},     // 方向不符
		{"recv", "CC:DD", "x.txt"},     // 对端不符
		{"recv", "AA:BB", "other.txt"}, // 文件名不符
	}
	for _, c := range cases {
		if ReclassifyCanceled(c.dir, c.peerID, c.name, "对方已取消传输") {
			t.Fatalf("不匹配的记录不该被改写: %+v", c)
		}
	}
	if Load()[0].Status != StatusFailed {
		t.Fatal("原记录应保持 failed")
	}
}

// 已成功落盘的记录绝不被取消改写。
func TestReclassifyCanceledIgnoresOK(t *testing.T) {
	setupHistoryDir(t)
	Append(Entry{Dir: "recv", PeerID: "AA:BB", Name: "x.txt", Status: StatusOK})

	if ReclassifyCanceled("recv", "AA:BB", "x.txt", "对方已取消传输") {
		t.Fatal("ok 记录不该被改写")
	}
	if Load()[0].Status != StatusOK {
		t.Fatal("ok 记录应保持 ok")
	}
}

// 对端 ID 比较忽略分隔符与大小写（与信任库口径一致）。
func TestReclassifyCanceledNormalizesPeerID(t *testing.T) {
	setupHistoryDir(t)
	Append(Entry{Dir: "recv", PeerID: "aa:bb:cc", Name: "x.txt", Status: StatusFailed, Error: "传输中断"})

	if !ReclassifyCanceled("recv", "AABBCC", "x.txt", "对方已取消传输") {
		t.Fatal("分隔符/大小写不同的同一对端应能匹配")
	}
	if Load()[0].Status != StatusCanceled {
		t.Fatal("应已改写为 canceled")
	}
}

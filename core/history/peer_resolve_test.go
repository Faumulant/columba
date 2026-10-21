package history

// 历史条目的对端名字解析：Peer 是传输当时的名字快照，PeerID 用于跟随改名。
// 四条规则都要钉死：
//   ① 有 PeerID 且信任库能解析 → 用当前名字（对方改名后旧记录跟随）
//   ② 有 PeerID 但设备已移除 → 保留当时的快照名
//   ③ 老记录无 PeerID → 保留快照名
//   ④ nameOf 为 nil（调用方没提供解析器）→ 原样返回

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePeerNames(t *testing.T) {
	// 信任库现状：id-A 已改名为「新名字」，id-B 已移除（查不到）
	current := map[string]string{
		"ID-A": "新名字",
	}
	nameOf := func(id string) string { return current[id] }

	entries := []Entry{
		{Peer: "旧名字", PeerID: "ID-A", Name: "a.txt"},   // ① 跟随改名
		{Peer: "已移除设备", PeerID: "ID-B", Name: "b.txt"}, // ② 保留快照
		{Peer: "老记录", Name: "c.txt"},                   // ③ 无 PeerID
	}
	got := ResolvePeerNames(entries, nameOf)

	if got[0].Peer != "新名字" {
		t.Errorf("有 PeerID 且可解析时应跟随改名, got %q", got[0].Peer)
	}
	if got[1].Peer != "已移除设备" {
		t.Errorf("设备已移除时应保留快照名, got %q", got[1].Peer)
	}
	if got[2].Peer != "老记录" {
		t.Errorf("无 PeerID 的老记录应保留快照名, got %q", got[2].Peer)
	}
	// 其余字段不得被改动
	if got[0].Name != "a.txt" || got[2].Name != "c.txt" {
		t.Error("解析只应改 Peer，不应动其他字段")
	}

	// ④ 未提供解析器：原样返回
	same := ResolvePeerNames(entries, nil)
	if same[0].Peer != "旧名字" {
		t.Errorf("nameOf 为 nil 时不应改动, got %q", same[0].Peer)
	}
}

// 解析器返回空串（设备在库里但显示名为空）时保留快照，避免把名字清空。
func TestResolvePeerNamesEmptyResult(t *testing.T) {
	entries := []Entry{{Peer: "快照名", PeerID: "ID-A"}}
	got := ResolvePeerNames(entries, func(string) string { return "" })
	if got[0].Peer != "快照名" {
		t.Errorf("解析结果为空时应保留快照名, got %q", got[0].Peer)
	}
}

// 老格式（无 peer_id 字段）的 JSONL 必须能正常反序列化，PeerID 为空串。
func TestLoadLegacyEntryWithoutPeerID(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatalf("创建数据目录: %v", err)
	}
	line := `{"time":"2026-09-26T14:43:07Z","dir":"send","peer":"localhost","name":"x.txt","size":1,"status":"ok"}` + "\n"
	if err := os.WriteFile(path(), []byte(line), 0o600); err != nil {
		t.Fatalf("写入老格式记录: %v", err)
	}
	es := Load()
	if len(es) != 1 {
		t.Fatalf("应读到 1 条, got %d", len(es))
	}
	if es[0].PeerID != "" {
		t.Errorf("老记录 PeerID 应为空串, got %q", es[0].PeerID)
	}
	if es[0].Peer != "localhost" {
		t.Errorf("老记录 Peer 应保留, got %q", es[0].Peer)
	}
}

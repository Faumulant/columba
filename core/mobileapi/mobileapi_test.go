package mobileapi

// 环回 e2e：同一进程内用 SetDataDir 造两套独立身份（各自独立的
// identity/trust/ipcache），接收端走独立端口避开本机真实 Drop 后台（42707），
// 目标解析走地址缓存直连 127.0.0.1（不触发局域网发现/子网探测，
// 全程不触碰本机正在运行的桌面端）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"drop/core/history"
	"drop/core/protocol"
	"drop/core/transfer"
)

// ─── 测试用 EventListener：记录全部事件，支持四种配对裁决策略 ───

type transferEvt struct {
	Dir, Peer, Name, Path, ErrInfo string
	Size                           int64
	OK                             bool
}

type textEvt struct{ Dir, Peer, Text string }

type pairEvt struct{ Name, ID string }

const (
	pairAcceptNow        = 0 // OnPairRequest 直接返回 true（即时允许）
	pairDecideAllow      = 1 // 返回 false，稍后 PairDecide(true)
	pairDecideDeny       = 2 // 返回 false，稍后 PairDecide(false)
	pairNeverDecide      = 3 // 返回 false 且不裁决（等超时拒绝）
	pairDecisionInterval = 100 * time.Millisecond
)

type testListener struct {
	mu         sync.Mutex
	transfers  []transferEvt
	texts      []textEvt
	pairs      []pairEvt
	changed    int
	pairPolicy int
}

func (l *testListener) OnTransfer(dir, peer, name, path string, size int64, ok bool, errInfo string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.transfers = append(l.transfers, transferEvt{Dir: dir, Peer: peer, Name: name, Path: path, Size: size, OK: ok, ErrInfo: errInfo})
}

func (l *testListener) OnText(dir, peer, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.texts = append(l.texts, textEvt{Dir: dir, Peer: peer, Text: text})
}

func (l *testListener) OnPairRequest(peerName, peerID string) bool {
	l.mu.Lock()
	l.pairs = append(l.pairs, pairEvt{Name: peerName, ID: peerID})
	policy := l.pairPolicy
	l.mu.Unlock()
	switch policy {
	case pairDecideAllow:
		go func() {
			time.Sleep(pairDecisionInterval)
			PairDecide(true)
		}()
		return false
	case pairDecideDeny:
		go func() {
			time.Sleep(pairDecisionInterval)
			PairDecide(false)
		}()
		return false
	default:
		return policy == pairAcceptNow
	}
}

func (l *testListener) OnProgress(dir, peer, name string, done, total int64) {}

func (l *testListener) OnDeviceChanged() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.changed++
}

func (l *testListener) snapshotTransfers() []transferEvt {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]transferEvt(nil), l.transfers...)
}

func (l *testListener) hasText(dir, text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.texts {
		if e.Dir == dir && e.Text == text {
			return true
		}
	}
	return false
}

func (l *testListener) pairCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pairs)
}

func waitText(t *testing.T, l *testListener, dir, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l.hasText(dir, text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 OnText(%s, %q) 超时", dir, text)
}

// waitTransfer 等待一个匹配的 recv 传输完成事件并返回它。
func waitTransfer(t *testing.T, l *testListener, name, path string, size int64) transferEvt {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range l.snapshotTransfers() {
			if e.Dir == "recv" && e.Name == name && e.Path == path && e.Size == size && e.OK {
				return e
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待传输事件超时: name=%s path=%s size=%d\n已有事件: %+v", name, path, size, l.snapshotTransfers())
	return transferEvt{}
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// mustJSON 把 []string 序列化为 JSON 数组（模拟 Kotlin 侧的传参方式）。
func mustJSON(items []string) string {
	b, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type selfInfo struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	DisplayID string `json:"display_id"`
}

func readSelfInfo(t *testing.T) selfInfo {
	t.Helper()
	var info selfInfo
	if err := json.Unmarshal([]byte(SelfInfo()), &info); err != nil || info.ID == "" {
		t.Fatalf("SelfInfo 解析失败: %v %s", err, SelfInfo())
	}
	return info
}

type deviceRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

func readDevices(t *testing.T) []deviceRow {
	t.Helper()
	var devs []deviceRow
	if err := json.Unmarshal([]byte(ListDevices()), &devs); err != nil {
		t.Fatalf("ListDevices JSON 解析失败: %v\n%s", err, ListDevices())
	}
	return devs
}

func parseHistory(t *testing.T, raw string) []history.Entry {
	t.Helper()
	var es []history.Entry
	if err := json.Unmarshal([]byte(raw), &es); err != nil {
		t.Fatalf("历史 JSON 解析失败: %v\n%s", err, raw)
	}
	return es
}

// setupReceiver 在 dir 下造一套身份并启动接收端（独立端口），返回本机信息与配对码。
func setupReceiver(t *testing.T, dir, name string, port int, l *testListener) (selfInfo, string) {
	t.Helper()
	must(t, SetDataDir(dir), "SetDataDir")
	must(t, SetPort(port), "SetPort")
	must(t, StartReceiver(filepath.Join(dir, "recv"), true, l), "StartReceiver")
	must(t, SetDeviceName(name), "SetDeviceName")
	info := readSelfInfo(t)
	if info.Name != name {
		t.Fatalf("设备名不符: got %q want %q", info.Name, name)
	}
	code, err := PairStart()
	if err != nil {
		t.Fatalf("PairStart: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("配对码应为 6 位: %q", code)
	}
	return info, code
}

// setupJoiner 在 dir 下造一套身份并预置对方地址缓存（环回直连，跳过局域网发现）。
func setupJoiner(t *testing.T, dir, name, peerID, peerAddr string) selfInfo {
	t.Helper()
	must(t, SetDataDir(dir), "SetDataDir")
	must(t, SetDeviceName(name), "SetDeviceName")
	info := readSelfInfo(t)
	if info.Name != name {
		t.Fatalf("设备名不符: got %q want %q", info.Name, name)
	}
	transfer.UpdateIPCache(peerID, peerAddr)
	return info
}

// ─── 主 e2e：配对 → 文本/文件互传 → 冲突改名 → 目录切换 → 历史 ───

func TestMobileE2E(t *testing.T) {
	// 缩短配对裁决超时，避免意外路径卡测试
	oldTimeout := pairDecisionTimeout
	pairDecisionTimeout = 2 * time.Second
	defer func() { pairDecisionTimeout = oldTimeout }()

	base := t.TempDir()
	dirA := filepath.Join(base, "identity-a")
	dirB := filepath.Join(base, "identity-b")
	recvDir := filepath.Join(dirA, "recv")
	recvDir2 := filepath.Join(base, "recv2")
	srcDir := filepath.Join(base, "src")
	const port = 42717

	l := &testListener{} // A 与 B 共用一个监听器：收发事件靠 dir 字段区分

	// ── 身份 A：接收端 ──
	infoA, code := setupReceiver(t, dirA, "A-PC", port, l)
	defer StopReceiver()
	defer StopDiscovery()

	// ── 身份 B：发送端（独立身份，预置 A 的环回地址缓存）──
	infoB := setupJoiner(t, dirB, "B-Phone", infoA.ID, "127.0.0.1:42717")
	if protocol.NormalizeID(infoA.ID) == protocol.NormalizeID(infoB.ID) {
		t.Fatal("两套数据目录应产生不同设备身份")
	}

	// ── 配对：B 携码加入，A 侧 OnPairRequest 即时允许 ──
	peerName, err := PairJoin(code)
	if err != nil {
		t.Fatalf("PairJoin: %v", err)
	}
	if peerName != "A-PC" {
		t.Fatalf("PairJoin 返回的对方名不符: got %q", peerName)
	}
	if l.pairCount() != 1 {
		t.Fatalf("应恰好收到一次 OnPairRequest, got %d", l.pairCount())
	}
	l.mu.Lock()
	pv := l.pairs[0]
	l.mu.Unlock()
	if pv.Name != "B-Phone" || protocol.NormalizeID(pv.ID) != protocol.NormalizeID(infoB.ID) {
		t.Fatalf("配对请求内容不符: %+v want B-Phone/%s", pv, infoB.ID)
	}

	// ── 双向信任库（配对协议真实互写，而非手工构造 trust.json）──
	must(t, SetDataDir(dirA), "切回 A")
	devs := readDevices(t)
	if len(devs) != 1 || devs[0].ID != infoB.ID || devs[0].Name != "B-Phone" {
		t.Fatalf("A 的设备列表不符: %+v", devs)
	}
	must(t, SetDataDir(dirB), "切回 B")
	devs = readDevices(t)
	if len(devs) != 1 || devs[0].ID != infoA.ID || devs[0].Name != "A-PC" {
		t.Fatalf("B 的设备列表不符: %+v", devs)
	}

	// ── 文本 B→A ──
	must(t, SendText(infoA.ID, "hello from B"), "SendText")
	waitText(t, l, "recv", "hello from B")

	// ── 文件 B→A（两个文件一次发送）──
	must(t, os.MkdirAll(srcDir, 0o755), "MkdirAll src")
	f1 := filepath.Join(srcDir, "hello.txt")
	f2 := filepath.Join(srcDir, "notes.md")
	must(t, os.WriteFile(f1, []byte("HELLO DROP"), 0o644), "write f1")
	must(t, os.WriteFile(f2, []byte("# notes\nsecond file"), 0o644), "write f2")
	must(t, SendFiles(infoA.ID, mustJSON([]string{f1, f2})), "SendFiles x2")
	waitTransfer(t, l, "hello.txt", filepath.Join(recvDir, "hello.txt"), 10)
	waitTransfer(t, l, "notes.md", filepath.Join(recvDir, "notes.md"), int64(len("# notes\nsecond file")))
	if data, err := os.ReadFile(filepath.Join(recvDir, "hello.txt")); err != nil || string(data) != "HELLO DROP" {
		t.Fatalf("hello.txt 落盘内容不符: %q %v", data, err)
	}

	// ── 重复发送同名文件 → 冲突改名，绝不覆盖 ──
	must(t, SendFiles(infoA.ID, mustJSON([]string{f1})), "SendFiles 重名")
	evt := waitTransfer(t, l, "hello (1).txt", filepath.Join(recvDir, "hello (1).txt"), 10)
	if _, err := os.Stat(evt.Path); err != nil {
		t.Fatalf("改名文件未落盘: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recvDir, "hello.txt")); err != nil {
		t.Fatalf("原文件不应被覆盖: %v", err)
	}

	// ── 运行中更改接收目录（DirFn 即时生效）──
	must(t, SetSaveDir(recvDir2), "SetSaveDir")
	must(t, SendFiles(infoA.ID, mustJSON([]string{f2})), "SendFiles 到新目录")
	waitTransfer(t, l, "notes.md", filepath.Join(recvDir2, "notes.md"), int64(len("# notes\nsecond file")))

	// ── SetTarget 默认目标 + 空目标发送 ──
	must(t, SetTarget(infoA.ID), "SetTarget")
	must(t, SendText("", "via set-target"), "SendText(空目标)")
	waitText(t, l, "recv", "via set-target")

	// ── 失败路径：源文件不存在 ──
	if err := SendFiles(infoA.ID, mustJSON([]string{filepath.Join(srcDir, "missing.bin")})); err == nil {
		t.Fatal("发送不存在的文件应报错")
	}

	// ── 历史：单进程双身份下，接收侧的记录落在调用期间的当前数据目录
	// （B 的目录）——这是测试环境的工件，生产单身份无此现象。──
	must(t, SetDataDir(dirB), "切回 B")
	entries := parseHistory(t, HistoryJSON())
	recvCount, sendCount, sawConflict := 0, 0, false
	for _, e := range entries {
		switch e.Dir {
		case "recv":
			recvCount++
		case "send":
			sendCount++
		}
		if e.Dir == "recv" && e.Name == "hello (1).txt" && e.Status == "ok" {
			sawConflict = true
		}
	}
	if recvCount != 6 || sendCount != 5 {
		t.Fatalf("历史条数不符: recv=%d send=%d（期望 6/5）\n%s", recvCount, sendCount, HistoryJSON())
	}
	if !sawConflict {
		t.Fatal("历史缺少冲突改名记录 hello (1).txt")
	}

	// ── 批量删除历史 ──
	var delID string
	for _, e := range entries {
		if e.Dir == "recv" && e.Name == "hello (1).txt" {
			delID = e.ID
		}
	}
	if delID == "" {
		t.Fatal("未找到待删除记录的 id")
	}
	must(t, DeleteHistory(mustJSON([]string{delID})), "DeleteHistory")
	entries = parseHistory(t, HistoryJSON())
	if len(entries) != 10 {
		t.Fatalf("删除后应剩 10 条, got %d", len(entries))
	}
	for _, e := range entries {
		if e.ID == delID {
			t.Fatal("记录未被删除")
		}
	}
	must(t, DeleteHistory(mustJSON([]string{"nonexistent-id"})), "删除不存在的 id 不应报错")

	// ── 停止接收端后 PairStart 应失败 ──
	StopReceiver()
	if _, err := PairStart(); err == nil {
		t.Fatal("接收端停止后 PairStart 应失败")
	}
}

// ── 配对裁决：宿主返回 false 后调 PairDecide(false) → 拒绝 ──

func TestPairDecideDeny(t *testing.T) {
	oldTimeout := pairDecisionTimeout
	pairDecisionTimeout = 2 * time.Second
	defer func() { pairDecisionTimeout = oldTimeout }()

	base := t.TempDir()
	l := &testListener{pairPolicy: pairDecideDeny}
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A2-PC", 42719, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B2-Phone", infoA.ID, "127.0.0.1:42719")

	if _, err := PairJoin(code); err == nil {
		t.Fatal("对方 PairDecide(false) 后 PairJoin 应失败")
	}
	// 拒绝后双方信任库都不应写入
	if devs := ListDevices(); devs != "[]" {
		t.Fatalf("拒绝后 B 的设备列表应为空: %s", devs)
	}
	if l.pairCount() != 1 {
		t.Fatalf("应收到一次 OnPairRequest, got %d", l.pairCount())
	}
}

// ── 配对裁决：宿主始终不裁决 → 超时拒绝，且不阻塞其他流程 ──

func TestPairDecideTimeout(t *testing.T) {
	oldTimeout := pairDecisionTimeout
	pairDecisionTimeout = 500 * time.Millisecond
	defer func() { pairDecisionTimeout = oldTimeout }()

	base := t.TempDir()
	l := &testListener{pairPolicy: pairNeverDecide}
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A3-PC", 42721, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B3-Phone", infoA.ID, "127.0.0.1:42721")

	start := time.Now()
	if _, err := PairJoin(code); err == nil {
		t.Fatal("超时未裁决时 PairJoin 应失败")
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("应在超时后被拒（实际 %v）", elapsed)
	}
	// 裁决槽位应已释放：再次出码不受影响
	if _, err := PairStart(); err != nil {
		t.Fatalf("超时拒绝后应能再次出码: %v", err)
	}
}

// ── 参数校验 ──

func TestParamValidation(t *testing.T) {
	if err := SetPort(0); err == nil {
		t.Fatal("SetPort(0) 应报错")
	}
	if err := SetPort(70000); err == nil {
		t.Fatal("SetPort(70000) 应报错")
	}
	if err := SetDataDir(""); err == nil {
		t.Fatal("SetDataDir(\"\") 应报错")
	}
	if err := SetSaveDir(""); err == nil {
		t.Fatal("SetSaveDir(\"\") 应报错")
	}
	if err := DeleteHistory("[]"); err != nil {
		t.Fatalf("DeleteHistory(\"[]\") 不应报错: %v", err)
	}
	if err := DeleteHistory("not-json"); err == nil {
		t.Fatal("DeleteHistory 非 JSON 参数应报错")
	}
	if HistoryJSON() != "[]" {
		t.Fatalf("空历史应返回 []: %s", HistoryJSON())
	}
	if err := PairDecide(true); err == nil {
		t.Fatal("无待裁决请求时 PairDecide 应报错")
	}
}

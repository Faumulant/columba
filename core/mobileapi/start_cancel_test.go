package mobileapi

// 传输开始事件与取消（安卓侧）的语义边界：
//
//   - OnTransferStart 在小文件上也会触发（不能只靠 OnProgress：它受 800ms
//     节流，2 字节的文件一次进度都不会有，只挂 OnProgress 的宿主永远看不到
//     「传输中」条目）
//   - 多文件时只宣告一次开始，且是整批口径（「N 个文件」+ 总字节）
//   - CancelSend 在没有发送时返回错误（宿主据此区分「点了没反应」与「已取消」）
//   - 取消后 OnTransfer 带 ok=false 且历史记 canceled（不是 failed）

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"drop/core/history"
)

// 小文件也必须触发 OnTransferStart。
func TestTransferStartFiresForTinyFile(t *testing.T) {
	base := t.TempDir()
	l := &testListener{}
	// 端口必须与 mobileapi_test.go 里那些固定端口错开：同包内测试虽然串行，
	// 但端口一旦重复，将来任何并行的改动都会立刻撞上（42721 曾被两个用例共用）。
	const port = 42729
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A-Start", port, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B-Start", infoA.ID, "127.0.0.1:"+strconv.Itoa(port))
	if _, err := PairJoin(code); err != nil {
		t.Fatalf("PairJoin: %v", err)
	}

	src := filepath.Join(t.TempDir(), "tiny.txt")
	must(t, writeFile(src, "hi"), "写源文件")
	must(t, SendFiles(infoA.ID, mustJSON([]string{src})), "SendFiles")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if starts := l.snapshotStarts(); len(starts) > 0 {
			st := starts[0]
			if st.Dir != "send" {
				t.Fatalf("开始事件方向应为 send, got %q", st.Dir)
			}
			if st.Name != "tiny.txt" {
				t.Fatalf("单文件开始事件名应为文件名, got %q", st.Name)
			}
			if st.Total != 2 {
				t.Fatalf("开始事件 total 应为 2, got %d", st.Total)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("2 字节的文件也应触发 OnTransferStart（否则宿主端「传输中」条目永不出现）")
}

// 多文件只宣告一次开始，且为整批口径。
func TestTransferStartOncePerBatch(t *testing.T) {
	base := t.TempDir()
	l := &testListener{}
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A-Batch", 42723, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B-Batch", infoA.ID, "127.0.0.1:42723")
	if _, err := PairJoin(code); err != nil {
		t.Fatalf("PairJoin: %v", err)
	}

	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	must(t, writeFile(a, "aaaa"), "写 a")
	must(t, writeFile(b, "bbbbbb"), "写 b")
	must(t, SendFiles(infoA.ID, mustJSON([]string{a, b})), "SendFiles x2")

	// 等发送完成，再检查开始事件只有一条
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range l.snapshotTransfers() {
			if e.Dir == "send" && e.OK {
				goto done
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("发送完成事件超时")
done:
	// 只看发送侧：接收端逐文件各自宣告开始（每个文件一条，符合「每文件一条」口径）
	var starts []startEvt
	for _, st := range l.snapshotStarts() {
		if st.Dir == "send" {
			starts = append(starts, st)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("多文件应只宣告一次发送开始, got %d 次: %+v", len(starts), starts)
	}
	if starts[0].Name != "2 个文件" {
		t.Fatalf("多文件开始事件名应为「2 个文件」, got %q", starts[0].Name)
	}
	if starts[0].Total != 10 {
		t.Fatalf("开始事件 total 应为总字节 10, got %d", starts[0].Total)
	}
}

// 没有进行中的发送时 CancelSend 返回错误。
func TestCancelSendWithoutTransfer(t *testing.T) {
	if err := CancelSend(); err == nil {
		t.Fatal("无发送时 CancelSend 应返回错误（不能静默成功）")
	}
}

// 取消进行中的发送：历史记 canceled，且 Error 为中文文案。
func TestCancelSendMarksCanceled(t *testing.T) {
	base := t.TempDir()
	l := &testListener{}
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A-Cancel", 42725, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B-Cancel", infoA.ID, "127.0.0.1:42725")
	if _, err := PairJoin(code); err != nil {
		t.Fatalf("PairJoin: %v", err)
	}

	// 大文件：环回传输约几十毫秒，靠 sleep 抢窗口不稳定；改用「先取消再发送」
	// 不可行（互斥在 SendFiles 内部），所以这里用较大的文件 + 立即取消。
	big := filepath.Join(t.TempDir(), "big.bin")
	must(t, writeFile(big, strings.Repeat("x", 32<<20)), "写大文件")

	errCh := make(chan error, 1)
	go func() { errCh <- SendFiles(infoA.ID, mustJSON([]string{big})) }()

	// 等互斥句柄出现再取消
	deadline := time.Now().Add(5 * time.Second)
	canceled := false
	for time.Now().Before(deadline) {
		if err := CancelSend(); err == nil {
			canceled = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !canceled {
		t.Fatal("发送期间 CancelSend 应成功（互斥句柄未建立）")
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("取消后 SendFiles 应返回错误")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 SendFiles 未返回")
	}

	// 历史里最近一条 send 应为 canceled
	entries := parseHistory(t, HistoryJSON())
	var last *history.Entry
	for i := range entries {
		if entries[i].Dir == "send" {
			last = &entries[i]
		}
	}
	if last == nil {
		t.Fatal("取消也应留下一条 send 历史")
	}
	if last.Status != history.StatusCanceled {
		t.Fatalf("取消应记 status=canceled, got %q（用户不该看到「失败：context canceled」）", last.Status)
	}
	if strings.Contains(last.Error, "context canceled") {
		t.Fatalf("Error 不该是英文技术串, got %q", last.Error)
	}
}

// 取消后互斥应释放：可以再次发送。
func TestCancelSendReleasesMutex(t *testing.T) {
	base := t.TempDir()
	l := &testListener{}
	infoA, code := setupReceiver(t, filepath.Join(base, "a"), "A-Reuse", 42727, l)
	defer StopReceiver()
	defer StopDiscovery()
	setupJoiner(t, filepath.Join(base, "b"), "B-Reuse", infoA.ID, "127.0.0.1:42727")
	if _, err := PairJoin(code); err != nil {
		t.Fatalf("PairJoin: %v", err)
	}

	big := filepath.Join(t.TempDir(), "big.bin")
	must(t, writeFile(big, strings.Repeat("y", 32<<20)), "写大文件")
	errCh := make(chan error, 1)
	go func() { errCh <- SendFiles(infoA.ID, mustJSON([]string{big})) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := CancelSend(); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-errCh

	// 互斥已释放：CancelSend 应重新报「没有进行中的发送」
	if err := CancelSend(); err == nil {
		t.Fatal("取消后互斥应释放，再次 CancelSend 应报错")
	}

	// 且可以立刻再发一个小文件
	small := filepath.Join(t.TempDir(), "again.txt")
	must(t, writeFile(small, "ok"), "写小文件")
	must(t, SendFiles(infoA.ID, mustJSON([]string{small})), "取消后再发送")
}

// writeFile 写文件（测试辅助）。
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

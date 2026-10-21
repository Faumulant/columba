package transfer

// 取消传输与整批进度的语义边界：
//
//   - 传输开始即上报一次进度（done=0）：小文件在 800ms ticker 触发前就传完，
//     没有这次首报，宿主端的「传输中」条目与圆环永远不会出现
//   - 多文件按整批口径上报（name 为「N 个文件」、分母为总字节），否则宿主端
//     会看到进度逐文件回绕
//   - 取消记 canceled 而非 failed：用户主动停不是故障，展示文案为「已取消」
//   - 接收端收到 /v1/cancel 后删 .part、记 canceled，且与随后必然发生的
//     「读 body 中断」不重复记两条
//   - 未配对设备调 /v1/cancel 被拒（与其余数据端点同一授权门槛）

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"drop/core/history"
	"drop/core/protocol"
)

// progressSpy 收集 ProgressHook 的上报序列。
type progressSpy struct {
	mu   sync.Mutex
	recs []progressRec
}

type progressRec struct {
	dir, peer, name string
	done, total     int64
}

func (p *progressSpy) hook(dir, peer, name string, done, total int64) {
	p.mu.Lock()
	p.recs = append(p.recs, progressRec{dir, peer, name, done, total})
	p.mu.Unlock()
}

func (p *progressSpy) snapshot() []progressRec {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]progressRec, len(p.recs))
	copy(out, p.recs)
	return out
}

// installProgress 把 ProgressHook 换成 spy，测试结束恢复（全局变量，必须还原）。
func installProgress(t *testing.T) *progressSpy {
	t.Helper()
	spy := &progressSpy{}
	old := ProgressHook
	ProgressHook = spy.hook
	t.Cleanup(func() { ProgressHook = old })
	return spy
}

// 传输开始立即上报一次 done=0：这是「传输中条目」能不出现的根因修复。
// 用 0 字节文件构造「比 ticker 还快」的传输，验证仍有首报。
func TestProgressReportsStartImmediately(t *testing.T) {
	spy := installProgress(t)
	h := newRecvHarness(t, nil)
	h.pairSender()

	src := filepath.Join(t.TempDir(), "tiny.txt")
	must(t, os.WriteFile(src, []byte("hi"), 0o600), "写源文件")

	// Target.ID 用于 pin 对端证书指纹，必须是接收端的 ID（不是发送方自己的）
	target := Target{
		Addr: h.srv.Listener.Addr().String(),
		ID:   fingerprintOf(h),
		Name: testPeerName,
	}
	if _, err := Send(context.Background(), h.sender, target, []string{src}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 首报在 progressReader 的 goroutine 里发出，Send 返回不代表它已经跑完——
	// 传输极快时 stopLog() 关闭 done 通道甚至可能早于 report() 被调度。
	// 直接快照会偶发拿到空列表（CI 上真实踩到过）。这里等它出现，带超时。
	var recs []progressRec
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		recs = spy.snapshot()
		if len(recs) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(recs) == 0 {
		t.Fatal("传输应至少上报一次进度（首报 done=0），否则宿主端「传输中」条目永不出现")
	}
	first := recs[0]
	if first.dir != "send" {
		t.Fatalf("首报方向应为 send, got %q", first.dir)
	}
	if first.done != 0 {
		t.Fatalf("首报 done 应为 0, got %d", first.done)
	}
	if first.total != 2 {
		t.Fatalf("首报 total 应为文件大小 2, got %d", first.total)
	}
	if first.name != "tiny.txt" {
		t.Fatalf("单文件上报名应为文件名, got %q", first.name)
	}
}

// 多文件按整批口径上报：name 为「N 个文件」，total 为总字节。
func TestProgressAggregatesBatch(t *testing.T) {
	spy := installProgress(t)
	h := newRecvHarness(t, nil)
	h.pairSender()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	must(t, os.WriteFile(a, []byte("aaaa"), 0o600), "写 a")
	must(t, os.WriteFile(b, []byte("bbbbbb"), 0o600), "写 b")

	target := Target{Addr: h.srv.Listener.Addr().String(), ID: fingerprintOf(h), Name: testPeerName}
	if _, err := Send(context.Background(), h.sender, target, []string{a, b}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 同 TestProgressReportsStartImmediately：进度在 goroutine 里发，
	// Send 返回不代表全部上报都到齐了。这里等「最后一条 done>=4」出现
	// （即第二个文件的首报带上了已完成文件的字节），再断言。
	var sendRecs []progressRec
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sendRecs = sendRecs[:0]
		for _, r := range spy.snapshot() {
			// 只看发送侧：接收端也会经同一 ProgressHook 上报，dir="recv" 是单文件口径
			if r.dir == "send" {
				sendRecs = append(sendRecs, r)
			}
		}
		if n := len(sendRecs); n > 0 && sendRecs[n-1].done >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(sendRecs) == 0 {
		t.Fatal("应有发送侧进度上报")
	}
	for _, r := range sendRecs {
		if r.total != 10 {
			t.Fatalf("分母应为整批总字节 10（4+6），got %d（逐文件口径会让进度来回回绕）", r.total)
		}
		if r.name != "2 个文件" {
			t.Fatalf("多文件上报名应为「2 个文件」, got %q", r.name)
		}
		if r.done < 0 || r.done > r.total {
			t.Fatalf("done 越界: %d/%d", r.done, r.total)
		}
	}
	// 首个文件落盘后，第二个文件的首报应带上已完成的 4 字节
	last := sendRecs[len(sendRecs)-1]
	if last.done < 4 {
		t.Fatalf("后续上报应累计已完成文件的字节（至少 4），got %d", last.done)
	}
}

// 取消：Send 返回 ErrCanceled（不是原始 ctx 错误），历史记 canceled + 中文文案。
//
// 用「已取消的 ctx」而不是「传一半再 cancel」：本机 loopback 传 8MB 只要 0.14s，
// 靠 sleep 抢中途时机会变成 flaky（实测取消常常晚于传完）。已取消的 ctx 会让
// 请求在 offer 阶段就失败，走的是同一条 failureStatus 映射路径，语义一致且确定。
// 真正的「传一半被取消」由 TestReceiverCancelCleansPart 从接收端侧覆盖。
func TestCancelMarksHistoryCanceled(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	src := filepath.Join(t.TempDir(), "f.txt")
	must(t, os.WriteFile(src, []byte("payload"), 0o600), "写源文件")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就已取消
	target := Target{Addr: h.srv.Listener.Addr().String(), ID: fingerprintOf(h), Name: testPeerName}

	_, err := Send(ctx, h.sender, target, []string{src})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("取消后 Send 应返回 ErrCanceled, got %v", err)
	}

	entries := history.Load()
	if len(entries) == 0 {
		t.Fatal("取消也应留下一条历史")
	}
	e := entries[len(entries)-1]
	if e.Dir != "send" {
		t.Fatalf("方向应为 send, got %q", e.Dir)
	}
	if e.Status != history.StatusCanceled {
		t.Fatalf("取消应记 status=canceled, got %q", e.Status)
	}
	if e.Error != ErrCanceled.Error() {
		t.Fatalf("取消的 Error 应是中文文案「%s」而非原始 ctx 错误, got %q", ErrCanceled.Error(), e.Error)
	}
}

// 接收端收到 /v1/cancel：清 .part、记一条 canceled，且不与随后「读中断」重复。
func TestReceiverCancelCleansPart(t *testing.T) {
	notified := make(chan failEvt, 8)
	h := newRecvHarness(t, func(o *ReceiveOptions) {
		o.OnFailed = func(peer, name string, canceled bool, reason string) {
			notified <- failEvt{peer: peer, name: name, canceled: canceled, reason: reason}
		}
	})
	h.pairSender()

	data := []byte("0123456789abcdef")
	sid := mustOffer(t, h, protocol.FileMeta{Path: "x.txt", Size: int64(len(data))})

	// 开一个上传但只写一半就断开：模拟发送方中途取消
	u := h.url(protocol.PathUpload) + "?s=" + sid + "&f=f0"
	req, err := http.NewRequest(http.MethodPost, u, &halfReader{data: data, n: 8})
	if err != nil {
		t.Fatalf("构造上传请求: %v", err)
	}
	req.ContentLength = int64(len(data))
	_, _, _ = h.send(req) // 读中断，接收端会记一条 failed

	// 随后发送方补发取消通知
	status, body, err := h.postJSON(protocol.PathCancel, protocol.CancelMsg{
		Proto: protocol.Version, SessionID: sid, FileID: "f0",
	})
	if err != nil {
		t.Fatalf("cancel 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("已配对设备取消应成功, got %d: %s", status, body)
	}

	// .part 必须被清掉
	parts := filepath.Join(h.dir, ".drop-parts")
	_ = filepath.Walk(parts, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(p) == ".part" {
			t.Fatalf("取消后不应残留 .part: %s", p)
		}
		return nil
	})

	entries := history.Load()
	var canceled, failed int
	for _, e := range entries {
		switch e.Status {
		case history.StatusCanceled:
			canceled++
		case history.StatusFailed:
			failed++
		}
	}
	if canceled != 1 {
		t.Fatalf("应恰好有一条 canceled 记录, got %d", canceled)
	}
	// 取消通知可能早于或晚于读中断：无论顺序，都不该出现「取消 + 失败」两条
	if failed != 0 {
		t.Fatalf("取消后不应再记 failed（两端各一条失败记录会让用户困惑）, got %d", failed)
	}

	// 宿主必须收到结束事件：手机端的「传输中」条目靠它退场，
	// 缺了它条目会永久停在最后一帧（真机实测 6% 卡住）。
	// 顺序不确定：读中断可能先触发一条 failed 通知，取消随后再触发 canceled，
	// 宿主两次都收场是幂等的（条目本来就是清空），关键是有一次 canceled。
	var sawCanceled, sawAny bool
	deadline := time.After(2 * time.Second)
	for !sawCanceled {
		select {
		case ev := <-notified:
			sawAny = true
			if ev.canceled {
				sawCanceled = true
				if ev.name != "x.txt" {
					t.Fatalf("结束事件应带文件名, got %q", ev.name)
				}
			}
		case <-deadline:
			if !sawAny {
				t.Fatal("取消后必须回调 OnFailed，否则宿主「传输中」条目永不退场")
			}
			t.Fatal("取消后必须有一条 canceled=true 的结束事件")
		}
	}
}

// failEvt 接收失败回调的快照。
type failEvt struct {
	peer, name, reason string
	canceled           bool
}

// 端到端：发送方取消 → 接收端历史必须记 canceled，而不是「传输中断」。
//
// 这是真机验证暴露的缺陷：最初只实现了接收端的 /v1/cancel 处理，发送端
// 从未发出这个请求，接收端只能从「读中断」判断，于是把用户主动取消记成
// 「传输中断」+ 红叉。本测试钉死「发送端真的发了通知」这一点。
func TestSendCancelNotifiesReceiver(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	// 大文件 + 限速读取，保证取消发生在传输中途（loopback 太快，小文件会先传完）
	dir := t.TempDir()
	big := filepath.Join(dir, "slow.bin")
	must(t, os.WriteFile(big, make([]byte, 4<<20), 0o600), "写大文件")

	target := Target{Addr: h.srv.Listener.Addr().String(), ID: fingerprintOf(h), Name: testPeerName}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Send(ctx, h.sender, target, []string{big})
		done <- err
	}()

	// 等接收端真的收到 offer 并开始收数据，再取消
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.recv.mu.Lock()
		n := len(h.recv.sessions)
		h.recv.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("取消后应返回 ErrCanceled, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 Send 未返回")
	}

	// 给取消通知一点到达时间（它是异步发出的，不在 Send 的返回路径上阻塞太久）。
	//
	// 断言口径（两处都踩过坑，别再改回去）：
	//   ① 只统计 dir="recv"：本次测试里发送侧与接收侧共用同一个 LOCALAPPDATA
	//      （mobileapi 的 SetDataDir 就是改进程环境变量），两边的历史写进同一个
	//      history.jsonl，各记一条 canceled。不区分方向就会误判成「重复记录」。
	//   ② 不断言「不能有 failed」：读中断与取消通知是两条独立网络事件，先后不确定，
	//      读中断先到时必然先写一条 failed、随后 cancel 才改写。断言中途不能出现
	//      failed 会变成竞态测试（CI 上实测偶发失败，本地却通过）。
	// 真正要钉死的是接收侧最终态：恰好一条 canceled。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var canceled, failed int
		for _, e := range history.Load() {
			if e.Dir != "recv" {
				continue // 见 ①
			}
			switch e.Status {
			case history.StatusCanceled:
				canceled++
			case history.StatusFailed:
				failed++
			}
		}
		if canceled == 1 && failed == 0 {
			return
		}
		if canceled > 1 {
			t.Fatalf("接收侧同一次取消不该记两条 canceled（读到 %d 条）", canceled)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("接收侧应记恰好一条 canceled：发送方必须在取消时发 /v1/cancel，"+
		"否则对端只能从读中断判断、把用户取消记成「传输中断」。现有历史: %+v", history.Load())
}

// 未配对设备调 /v1/cancel 必须被拒：否则任意局域网设备能篡改本机历史。
func TestReceiverCancelRejectsUnpaired(t *testing.T) {
	h := newRecvHarness(t, nil) // 不 pairSender
	status, body, err := h.postJSON(protocol.PathCancel, protocol.CancelMsg{
		Proto: protocol.Version, SessionID: "whatever",
	})
	if err != nil {
		t.Fatalf("cancel 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备取消应 401, got %d: %s", status, body)
	}
}

// failureStatus 的状态映射：取消类错误归 canceled，其余归 failed。
func TestFailureStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"取消", context.Canceled, history.StatusCanceled},
		{"超时", context.DeadlineExceeded, history.StatusCanceled},
		{"普通错误", errors.New("connection reset by peer"), history.StatusFailed},
	}
	for _, c := range cases {
		got, msg := failureStatus(c.err)
		if got != c.want {
			t.Fatalf("%s: 状态应为 %q, got %q", c.name, c.want, got)
		}
		if msg == "" {
			t.Fatalf("%s: 文案不应为空", c.name)
		}
	}
	if s, _ := failureStatus(context.Canceled); s == history.StatusCanceled {
		if _, msg := failureStatus(context.Canceled); msg == context.Canceled.Error() {
			t.Fatal("取消文案应是中文，不该把 context canceled 直接给用户看")
		}
	}
}

// halfReader 只输出前 n 字节后报错，模拟传输中途断开。
type halfReader struct {
	data []byte
	n    int
	sent bool
}

func (r *halfReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("客户端中途断开")
	}
	r.sent = true
	n := copy(p, r.data[:r.n])
	return n, nil
}

// fingerprintOf 取 harness 接收端的证书指纹（Target.ID 用它 pin 对端）。
func fingerprintOf(h *recvHarness) string {
	return h.recv.id.ID()
}

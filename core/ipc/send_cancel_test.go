package ipc

// 发送互斥与取消端点（桌面侧）的语义边界：
//
//   - 同一时刻只允许一个面板发起的发送：第二次点击返回 409，不能静默串台
//     （/api/state 的 progress 只保留一条，并发发送会让圆环数字对不上谁是谁）
//   - 没有进行中的发送时取消返回 409，而不是静默成功
//   - 取消会真正触发该次发送的 ctx，且清空互斥标记供下一次发送使用
//
// 这里不跑真实传输：传输本身的行为已由 core/transfer 的 cancel_test.go 覆盖，
// 本文件只钉 IPC 层的状态机（谁持有 cancel、何时释放）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newTestServer 不读盘、不建身份：只测状态机，字段直接构造。
func newTestServer() *Server {
	return &Server{decision: make(chan bool, 1), probeSeen: map[string]time.Time{}}
}

// 有发送在跑时，再点发送必须被拒。
func TestSendMutexRejectsSecondSend(t *testing.T) {
	s := newTestServer()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.sendMu.Lock()
	s.sendCancel = cancel
	s.sendName = "a.txt"
	s.sendMu.Unlock()

	// 直接走互斥判定（handleSendFiles 的入队部分）的等价路径
	s.sendMu.Lock()
	busy := s.sendCancel != nil
	s.sendMu.Unlock()
	if !busy {
		t.Fatal("有 cancel 句柄时应判定为发送中")
	}

	// 通过真实 handler 验证返回 409：先构造 multipart 请求
	req := newMultipartRequest(t, "a.txt", []byte("x"))
	w := httptest.NewRecorder()
	s.handleSendFiles(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("发送中再次发送应 409, got %d: %s", w.Code, w.Body.String())
	}
	// 被拒的请求不能把已有发送的 cancel 顶掉
	s.sendMu.Lock()
	stillSame := s.sendCancel != nil
	s.sendMu.Unlock()
	if !stillSame {
		t.Fatal("被拒的第二次发送不得清掉进行中发送的句柄")
	}
}

// 取消端点：没有进行中的发送时返回 409。
func TestSendCancelWithoutSend(t *testing.T) {
	s := newTestServer()
	w := httptest.NewRecorder()
	s.handleSendCancel(w, httptest.NewRequest(http.MethodPost, "/api/send-cancel", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("无发送时取消应 409, got %d: %s", w.Code, w.Body.String())
	}
}

// 取消端点：有发送时真正触发 ctx，并回报取消的对象。
func TestSendCancelTriggersContext(t *testing.T) {
	s := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())
	s.sendMu.Lock()
	s.sendCancel = cancel
	s.sendName = "3 个文件"
	s.sendMu.Unlock()

	w := httptest.NewRecorder()
	s.handleSendCancel(w, httptest.NewRequest(http.MethodPost, "/api/send-cancel", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("取消应成功, got %d: %s", w.Code, w.Body.String())
	}
	select {
	case <-ctx.Done():
		// 期望
	case <-time.After(2 * time.Second):
		t.Fatal("取消未触发发送的 ctx")
	}
	if body := w.Body.String(); body == "" || !contains(body, "3 个文件") {
		t.Fatalf("应答应回报被取消的对象, got %q", body)
	}
}

// clearSend 释放互斥：之后可以再次发送。
func TestClearSendReleasesMutex(t *testing.T) {
	s := newTestServer()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.sendMu.Lock()
	s.sendCancel = cancel
	s.sendName = "a.txt"
	s.sendMu.Unlock()

	s.clearSend()

	s.sendMu.Lock()
	left := s.sendCancel
	name := s.sendName
	s.sendMu.Unlock()
	if left != nil || name != "" {
		t.Fatalf("clearSend 后应清空句柄与名字, got cancel=%v name=%q", left != nil, name)
	}
	// 清空后取消应回到「没有进行中的发送」
	w := httptest.NewRecorder()
	s.handleSendCancel(w, httptest.NewRequest(http.MethodPost, "/api/send-cancel", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("clearSend 后取消应 409, got %d", w.Code)
	}
}

// clearSend 幂等：取消路径上 ctx 已被 cancel 过，再调一次不得 panic。
func TestClearSendIdempotent(t *testing.T) {
	s := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())
	s.sendMu.Lock()
	s.sendCancel = cancel
	s.sendMu.Unlock()
	_ = ctx

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.clearSend() }()
	}
	wg.Wait()
	if ctx.Err() == nil {
		t.Fatal("clearSend 应确保 ctx 已取消")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

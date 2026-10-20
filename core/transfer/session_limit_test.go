package transfer

// 会话上限的判定口径：只数「仍在传 / 仍在等」的会话。
//
// 背景：整批完成的会话落盘后并不立刻从 r.sessions 删除——它要留下 saved
// 供发送方补投 complete 时做幂等判定（见 handleComplete），等 janitor 按
// 30 分钟 TTL 收走。若上限把这类「已完工但等 TTL」的会话也算进去，连续发
// 几批文件就会误报「接收端正忙」，而实际上既没占文件句柄也没占暂存目录。
//
// 这里不真发 maxSessions 批文件（64 × 传输太慢），直接把会话表填成目标
// 形状后打一次 offer，只验证计数口径。

import (
	"net/http"
	"testing"
	"time"

	"drop/core/protocol"
)

// seedSessions 直接往会话表里塞 n 个会话，每个会话一个文件，done 决定
// 该会话是否算「完工」。sid 前缀用于区分，避免与真实会话撞名。
func seedSessions(h *recvHarness, prefix string, n int, done bool) {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	for i := 0; i < n; i++ {
		sid := prefix + "-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		s := &session{
			files: map[string]*fileState{
				"f0": {meta: protocol.FileMeta{ID: "f0", Path: sid + ".bin"}, done: done},
			},
			last: time.Now(),
		}
		h.recv.sessions[sid] = s
	}
}

func TestSessionLimitCountsOnlyInFlight(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	// 1) 填满「仍在传」的会话 → 再 offer 必须 429
	seedSessions(h, "busy", maxSessions, false)
	status, body, err := h.postOffer(protocol.FileMeta{Path: "x.txt", Size: 1})
	if err != nil {
		t.Fatalf("offer 请求失败: %v", err)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("在途会话占满上限时应 429, got %d: %s", status, body)
	}

	// 2) 把这批标记为完工（落盘后等 TTL 的状态）→ 上限不再被占，offer 通过
	h.recv.mu.Lock()
	for _, s := range h.recv.sessions {
		for _, st := range s.files {
			st.done = true
			st.saved = "done.bin" // 完工标志：saved 非空即已落盘
		}
	}
	h.recv.mu.Unlock()
	status, body, err = h.postOffer(protocol.FileMeta{Path: "y.txt", Size: 1})
	if err != nil {
		t.Fatalf("offer 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("已完工的会话不应占上限（等 TTL 收走即可）, got %d: %s", status, body)
	}

	// 3) 完工会话必须仍留在表里：补投 complete 的幂等判定要靠它
	if n := h.sessionCount(); n <= maxSessions {
		t.Fatalf("完工会话不应被立即删除（补投 complete 需要）, got %d 个会话", n)
	}
}

package transfer

// 解锁状态同步：已配对设备之间互通「设备数量上限已解除」。
//
// 语义边界（都在这份测试里钉死）：
//   - 已配对设备推 true → 接收端写本机 unlocked.flag，并回调 OnUnlocked
//   - 未配对设备推 → 401，绝不写标记（否则任意局域网设备都能刷开上限）
//   - 推 false → 不撤销（解锁是一次性用户决定，不支持远程抹掉）
//   - 本机已解锁时收到重复推送 → 幂等，不重复回调

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"drop/core/identity"
	"drop/core/protocol"
)

func TestUnlockSyncWritesFlag(t *testing.T) {
	var unlockedFrom []string
	h := newRecvHarness(t, func(o *ReceiveOptions) {
		o.OnUnlocked = func(peer string) { unlockedFrom = append(unlockedFrom, peer) }
	})
	h.pairSender()

	if identity.IsUnlocked() {
		t.Fatal("前置条件：接收端不应已解锁")
	}

	status, body, err := h.postJSON(protocol.PathUnlock, protocol.UnlockMsg{
		Proto: protocol.Version, Unlocked: true,
	})
	if err != nil {
		t.Fatalf("unlock 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("已配对设备推解锁应成功, got %d: %s", status, body)
	}
	if !identity.IsUnlocked() {
		t.Fatal("收到解锁同步后本机应写入 unlocked.flag")
	}
	if len(unlockedFrom) != 1 || unlockedFrom[0] != testPeerName {
		t.Fatalf("应回调 OnUnlocked 且带对端名, got %v", unlockedFrom)
	}

	// 重复推送幂等：不重复回调（宿主不必反复刷 UI）
	status, _, err = h.postJSON(protocol.PathUnlock, protocol.UnlockMsg{
		Proto: protocol.Version, Unlocked: true,
	})
	if err != nil {
		t.Fatalf("第二次 unlock 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("重复推送应仍成功, got %d", status)
	}
	if len(unlockedFrom) != 1 {
		t.Fatalf("本机已解锁时不应重复回调, got %d 次", len(unlockedFrom))
	}
}

// 未配对设备推解锁必须被拒：这是「任意局域网设备都能刷开上限」的防线。
func TestUnlockSyncRejectsUnpaired(t *testing.T) {
	h := newRecvHarness(t, nil) // 不 pairSender
	status, body, err := h.postJSON(protocol.PathUnlock, protocol.UnlockMsg{
		Proto: protocol.Version, Unlocked: true,
	})
	if err != nil {
		t.Fatalf("unlock 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备推解锁应 401, got %d: %s", status, body)
	}
	if identity.IsUnlocked() {
		t.Fatal("被拒的推送不得写解锁标记")
	}
}

// 推 false 不撤销：解锁是一次性用户决定，不支持远程抹掉。
func TestUnlockSyncDoesNotRevoke(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()
	if err := identity.SetUnlocked(); err != nil {
		t.Fatalf("前置：本机解锁失败: %v", err)
	}

	status, body, err := h.postJSON(protocol.PathUnlock, protocol.UnlockMsg{
		Proto: protocol.Version, Unlocked: false,
	})
	if err != nil {
		t.Fatalf("unlock 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("推 false 应被接受（幂等无操作）, got %d: %s", status, body)
	}
	if !identity.IsUnlocked() {
		t.Fatal("推 false 不得撤销本机解锁状态")
	}
}

// 端到端：PushUnlock 打真实接收端，标记落到对端数据目录。
func TestPushUnlockEndToEnd(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	target := Target{
		Addr: h.srv.Listener.Addr().String(),
		ID:   identity.Fingerprint(h.recv.id.Cert()),
		Name: testPeerName,
	}
	if err := PushUnlock(context.Background(), h.sender, target); err != nil {
		t.Fatalf("PushUnlock: %v", err)
	}
	if !identity.IsUnlocked() {
		t.Fatal("PushUnlock 后接收端应已解锁")
	}
	// 标记文件真实落盘（不只内存态）
	if _, err := os.Stat(filepath.Join(identity.AppDir(), "unlocked.flag")); err != nil {
		t.Fatalf("unlocked.flag 应存在于 %s: %v", identity.AppDir(), err)
	}
}

package discover

// 发现层的两条硬规矩：①宣告循环全进程只有一条（宿主与接收器共用同一个
// Discovery，两条循环会互抢 pconn 的连接级组播接口，见 StartAnnouncing）；
// ②单播邻居名单必须随心跳过期收缩（DHCP 换网后旧 IP 会被别人占用）。
// 循环条数用 goroutine 增量观察：不为测试引入发送计数钩子。

import (
	"encoding/json"
	"net"
	"runtime"
	"testing"
	"time"

	"drop/core/protocol"
)

// newBound 起一个真实绑定的 Discovery（宣告会真发组播包，测试只在
// 一次性窗口内进行）。绑定发现端口失败说明环境不允许，跳过而非失败。
func newBound(t *testing.T) *Discovery {
	t.Helper()
	d, err := New("test-self")
	if err != nil {
		t.Skipf("发现端口不可用，跳过: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// waitGoroutines 轮询直到 NumGoroutine 达到 want 或超时（goroutine 起停
// 不是瞬时的，固定 sleep 在慢机器上会假失败）。
func waitGoroutines(want int) (int, bool) {
	for i := 0; i < 100; i++ {
		if n := runtime.NumGoroutine(); n == want {
			return n, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine(), false
}

func TestStartAnnouncingIsIdempotent(t *testing.T) {
	d := newBound(t)
	// New 只起收包协程；以此为基线，宣告循环必须只加一条
	base := runtime.NumGoroutine()

	ann := protocol.Announce{Proto: protocol.Version, ID: "peer-a", Name: "旧名字", Port: 42707}
	d.StartAnnouncing(ann)
	if _, ok := waitGoroutines(base + 1); !ok {
		t.Fatalf("StartAnnouncing 后应恰好新增 1 条宣告循环（基线 %d）: 当前 %d", base, runtime.NumGoroutine())
	}

	// 宿主与接收器各调一次：第二次只换载荷，绝不能再起循环
	d.StartAnnouncing(ann)
	d.StartAnnouncing(ann)
	if n := runtime.NumGoroutine(); n != base+1 {
		t.Fatalf("重复 StartAnnouncing 起了第二条循环：基线 %d，当前 %d", base, n)
	}

	// 载荷必须是「最近一次」的：改名后宣告要实时喊出新名字
	renamed := ann
	renamed.Name = "新名字"
	d.StartAnnouncing(renamed)
	d.amu.Lock()
	got := protocol.Announce{}
	json.Unmarshal(d.ann, &got)
	d.amu.Unlock()
	if got.Name != "新名字" {
		t.Fatalf("d.ann 应被更新为最新载荷, got %q", got.Name)
	}

	d.Close()
	if _, ok := waitGoroutines(base); !ok {
		t.Fatalf("Close 后宣告循环应退出（基线 %d）: 当前 %d", base, runtime.NumGoroutine())
	}
}

func TestUnicastPeersExpire(t *testing.T) {
	d := &Discovery{unicast: map[string]time.Time{}}
	fresh := net.ParseIP("192.168.1.31")
	stale := net.ParseIP("192.168.1.30")
	d.rememberUnicast(stale)
	// 把 stale 的收到时刻拨到过期窗口之外（等价于对端离网/换 IP 后无人应答）
	d.uniMu.Lock()
	d.unicast[stale.String()] = time.Now().Add(-ExpireAfter)
	d.uniMu.Unlock()
	d.rememberUnicast(fresh)

	if got := d.unicastPeers(time.Now()); len(got) != 1 || got[0] != fresh.String() {
		t.Fatalf("过期的单播邻居应被剪除, got %v", got)
	}
	d.uniMu.Lock()
	n := len(d.unicast)
	d.uniMu.Unlock()
	if n != 1 {
		t.Fatalf("剪除应落在名单上（只剩 1 条）, got %d", n)
	}
	// 对端仍在：窗口内重新收到宣告即重新学回
	d.rememberUnicast(stale)
	if got := d.unicastPeers(time.Now()); len(got) != 2 {
		t.Fatalf("心跳未超时的邻居应保留, got %v", got)
	}
}

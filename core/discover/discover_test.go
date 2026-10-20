package discover

// 发现层的三条硬规矩：①宣告循环全进程只有一条（宿主与接收器共用同一个
// Discovery，两条循环会互抢 pconn 的连接级组播接口，见 StartAnnouncing）；
// ②单播邻居名单必须随心跳过期收缩（DHCP 换网后旧 IP 会被别人占用）；
// ③对端日志按「设备 + 来源地址」去重，不逐条落盘（宣告每 2 秒一条）。
// 循环起停用 Discovery.loopDone 观测：进程级 goroutine 计数在并行跑包时不可靠。

import (
	"encoding/json"
	"net"
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

// 注意：此处不再用进程级 goroutine 计数判定循环起停。go test ./... 并行跑
// 多个包时，同进程里其他包的 goroutine 随时增减，全局计数在 CI 上会假失败
//（实测「基线 3 / 当前 2」）。改用 Discovery.loopDone 精确观测。

func TestStartAnnouncingIsIdempotent(t *testing.T) {
	d := newBound(t)

	ann := protocol.Announce{Proto: protocol.Version, ID: "peer-a", Name: "旧名字", Port: 42707}
	d.StartAnnouncing(ann)
	// 用 loopDone 判定循环是否在跑，不用进程级 goroutine 计数：
	// go test ./... 并行跑多个包时，其他包的 goroutine 随时增减，
	// 全局计数在 CI 上会假失败（实测「基线 3 / 当前 2」）。
	d.amu.Lock()
	done1 := d.loopDone
	d.amu.Unlock()
	if done1 == nil {
		t.Fatal("StartAnnouncing 后应有宣告循环")
	}

	// 宿主与接收器各调一次：第二次只换载荷，绝不能再起循环
	d.StartAnnouncing(ann)
	d.StartAnnouncing(ann)
	d.amu.Lock()
	done2 := d.loopDone
	d.amu.Unlock()
	if done1 != done2 {
		t.Fatal("重复 StartAnnouncing 起了第二条循环（loopDone 换了新通道）")
	}
	select {
	case <-done1:
		t.Fatal("重复 StartAnnouncing 不应让循环退出")
	default:
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
	// 精确观测循环退出：loopDone 关闭即代表宣告协程已返回。
	// 仍保留超时，避免 Close 语义坏掉时测试挂死。
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 后宣告循环应在 2 秒内退出")
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

func TestLogSeenDedup(t *testing.T) {
	var lines []string
	old := Debugf
	Debugf = func(format string, args ...any) {
		lines = append(lines, format)
	}
	defer func() { Debugf = old }()

	d := &Discovery{}
	ann := protocol.Announce{Proto: protocol.Version, ID: "AABBCC", Name: "手机", Port: 42707}
	ip1 := net.ParseIP("192.168.1.20")
	ip2 := net.ParseIP("192.168.1.21")

	for i := 0; i < 10; i++ { // 同设备同地址连发 10 条宣告（20 秒的量）
		d.logSeen(ann, ip1)
	}
	if len(lines) != 1 {
		t.Fatalf("同一设备同一地址只应记一次, got %d", len(lines))
	}

	d.logSeen(ann, ip2) // DHCP 换地址：必须再记一条（排障要看地址变化）
	if len(lines) != 2 {
		t.Fatalf("来源地址变化应再记一条, got %d", len(lines))
	}

	ann2 := protocol.Announce{Proto: protocol.Version, ID: "DDEEFF", Name: "平板", Port: 42707}
	d.logSeen(ann2, ip1) // 另一台设备：独立去重
	if len(lines) != 3 {
		t.Fatalf("不同设备应独立记录, got %d", len(lines))
	}
}

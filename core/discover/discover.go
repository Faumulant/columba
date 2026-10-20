// Package discover 通过 UDP 组播发现同一局域网内的 Drop 设备。
// 宣告本身不携带认证，设备身份的真实性由 TLS 层的证书指纹校验保证。
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"drop/core/protocol"
)

const (
	// MulticastAddr 发现组播地址与端口（TTL=1，不出本网段）。
	MulticastAddr = "224.0.0.240:42708"
	MulticastPort = 42708
	AnnounceEvery = 2 * time.Second
	// ExpireAfter 在线判定窗口：组播在 Wi-Fi 上有丢包（部分路由器不转发
	// 无线组播），窗口过窄会导致在线状态反复横跳（面板反复重绘）。20 秒
	// ≈ 连续丢失 9 次宣告才判离线。
	ExpireAfter = 20 * time.Second
)

// Debugf 发现层诊断日志钩子：宿主（transfer）注入，落 dbg 文件。
// nil 时静默。
var Debugf func(format string, args ...any)

// Event 收到的一条设备宣告。
type Event struct {
	Announce protocol.Announce
	IP       string // 宣告来源 IP
}

// Discovery 组播发现 socket：在所有可用网卡上加入组播组，收发共用一个端口。
// VPN/虚拟机/多网卡环境下系统默认路由常把组播引向错误网卡，
// 因此逐网卡加入并逐网卡发送，而不是依赖系统默认选择。
type Discovery struct {
	pconn   *ipv4.PacketConn
	group   *net.UDPAddr
	ifaces  []*net.Interface
	events  chan Event
	stop    chan struct{}
	once    sync.Once
	now     chan struct{} // 触发立即宣告（点信标「立即重发现」）；缓冲 1，连点合并
	amu     sync.Mutex
	ann     []byte // 最近一次宣告载荷；宣告循环每轮取它，本机改名实时生效
	running bool   // 宣告循环已在跑（StartAnnouncing 幂等）
	// loopDone 宣告循环退出时关闭（由 Close 经 stop 触发）。仅测试用：
	// 进程级 goroutine 计数在 go test ./... 并行跑包时会被其他包干扰，
	// 无法可靠判定「循环已退出」（CI 上实测假失败）。nil 表示尚未启动。
	loopDone chan struct{}

	uniMu   sync.Mutex
	unicast map[string]time.Time // 最近宣告过我们的对端 IP → 收到时刻（宣告即心跳，超时剪除见 sendAll）

	// seen 对端日志去重表：设备 ID → 最近记录过的来源 IP。只由 readLoop
	// 单 goroutine 读写，故不加锁（见 logSeen）。
	seen map[string]string
}

// rememberUnicast 记录一个宣告来源地址。组播被 AP/路由器过滤的网络里，
// 对端收不到我们的组播——此后宣告循环对它逐个单播补充，这是对端得知
// 我们仍在的唯一途径。宣告即心跳。
func (d *Discovery) rememberUnicast(ip net.IP) {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return
	}
	d.uniMu.Lock()
	if d.unicast == nil {
		d.unicast = map[string]time.Time{}
	}
	d.unicast[ip.String()] = time.Now()
	d.uniMu.Unlock()
}

// unicastPeers 取出心跳窗口内的单播邻居，并顺手剪除过期的。DHCP 换网后
// 旧 IP 可能已被别人占用，名单只增不减既无界增长又每轮对死地址徒劳发包；
// 对端若仍在，下一个 ExpireAfter 窗口内会重新学回。now 由调用方注入
// （与 transfer 的 sweepSessions 同套路，免测试等 20 秒）。
func (d *Discovery) unicastPeers(now time.Time) []string {
	d.uniMu.Lock()
	defer d.uniMu.Unlock()
	out := make([]string, 0, len(d.unicast))
	for a, seen := range d.unicast {
		if now.Sub(seen) >= ExpireAfter {
			delete(d.unicast, a)
			continue
		}
		out = append(out, a)
	}
	return out
}

// New 绑定发现端口、加入组播组并开始收包。
func New(selfID string) (*Discovery, error) {
	group, err := net.ResolveUDPAddr("udp4", MulticastAddr)
	if err != nil {
		return nil, err
	}
	cfg := net.ListenConfig{Control: setReuseAddr}
	conn, err := cfg.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", MulticastPort))
	if err != nil {
		return nil, fmt.Errorf("无法绑定发现端口: %w", err)
	}
	d := &Discovery{
		pconn:  ipv4.NewPacketConn(conn),
		group:  group,
		events: make(chan Event, 16),
		stop:   make(chan struct{}),
		now:    make(chan struct{}, 1),
	}
	all, err := net.Interfaces()
	if err != nil || len(all) == 0 {
		// netlink 受限（部分安卓 SELinux 策略，netlinkrib: permission denied）：
		// 退回系统默认组播接口 Join（nil 即内核默认），收包仍可用
		if jerr := d.pconn.JoinGroup(nil, group); jerr == nil {
			if debug() {
				fmt.Fprintf(os.Stderr, "[discover] netlink 受限，已加入默认组播接口\n")
			}
		}
	}
	for i := range all {
		ifi := all[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 ||
			ifi.Flags&net.FlagLoopback != 0 || !hasIPv4(&ifi) {
			continue
		}
		if err := d.pconn.JoinGroup(&ifi, group); err == nil {
			d.ifaces = append(d.ifaces, &ifi)
		} else if debug() {
			fmt.Fprintf(os.Stderr, "[discover] 加入组播失败 %s: %v\n", ifi.Name, err)
		}
	}
	d.pconn.SetMulticastLoopback(true)
	if debug() {
		fmt.Fprintf(os.Stderr, "[discover] 已加入组播的网卡: %d 张\n", len(d.ifaces))
		for _, ifi := range d.ifaces {
			fmt.Fprintf(os.Stderr, "[discover]   %s\n", ifi.Name)
		}
	}
	go d.readLoop(selfID)
	return d, nil
}

func debug() bool { return os.Getenv("DROP_DEBUG") != "" }

// Events 返回设备宣告通道，Close 后关闭。
func (d *Discovery) Events() <-chan Event { return d.events }

// StartAnnouncing 设置宣告载荷并确保有且只有一条宣告循环在跑。
// 宿主（daemon）与接收器会对同一个共享实例各调一次，载荷内容本就相同；
// 若起两条循环，SetMulticastInterface 的连接级状态会被两条循环互抢
// （A 拨到 eth0 还没写、B 已拨到 wlan0，那张卡的组播这一轮全丢），
// 单播名单也会每轮发两遍。
func (d *Discovery) StartAnnouncing(a protocol.Announce) {
	b, _ := json.Marshal(a)
	d.amu.Lock()
	d.ann = b
	start := !d.running
	d.running = true
	if start {
		d.loopDone = make(chan struct{})
	}
	done := d.loopDone
	d.amu.Unlock()
	if !start {
		return
	}
	go func() {
		defer close(done) // 让测试能精确观测「循环已退出」
		tick := time.NewTicker(AnnounceEvery)
		defer tick.Stop()
		for {
			d.sendAll() // 每轮取 d.ann：本机改名后喊出去的就是新名字
			select {
			case <-d.stop:
				return
			case <-tick.C:
			case <-d.now:
			}
		}
	}()
}

// AnnounceNow 触发一轮立即宣告，不打断周期循环，连点合并为一次。
// 未在宣告中为空操作。所有发送仍在发现协程内执行：SetMulticastInterface
// 是连接级状态，跨 goroutine 并发设置会互相干扰。
func (d *Discovery) AnnounceNow() {
	select {
	case d.now <- struct{}{}:
	default:
	}
}

func (d *Discovery) sendAll() {
	d.amu.Lock()
	b := d.ann
	d.amu.Unlock()
	if len(d.ifaces) == 0 {
		// 无已枚举网卡（netlink 受限）：交给内核默认组播接口发送
		d.pconn.WriteTo(b, nil, d.group)
	} else {
		for _, ifi := range d.ifaces {
			d.pconn.SetMulticastInterface(ifi)
			if _, err := d.pconn.WriteTo(b, nil, d.group); err != nil && debug() {
				fmt.Fprintf(os.Stderr, "[discover] 宣告发送失败 %s: %v\n", ifi.Name, err)
			}
		}
	}
	// 单播补充：组播被 AP/路由器过滤的网络（热点常见）里，对已认识
	// 的对端逐个寄送——宣告即心跳，常开冗余，成本约等于零
	for _, pa := range d.unicastPeers(time.Now()) {
		ra, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(pa, strconv.Itoa(MulticastPort)))
		if err != nil {
			continue
		}
		if _, err := d.pconn.WriteTo(b, nil, ra); err != nil && debug() {
			fmt.Fprintf(os.Stderr, "[discover] 单播宣告失败 %s: %v\n", pa, err)
		}
	}
}

// Close 停止宣告与收包。
func (d *Discovery) Close() {
	d.once.Do(func() {
		close(d.stop)
		d.pconn.Close()
	})
}

func (d *Discovery) readLoop(selfID string) {
	defer close(d.events)
	buf := make([]byte, 1500)
	self := protocol.NormalizeID(selfID)
	for {
		n, _, from, err := d.pconn.ReadFrom(buf)
		if err != nil {
			return
		}
		var a protocol.Announce
		if json.Unmarshal(buf[:n], &a) != nil || a.Proto != protocol.Version {
			continue
		}
		if protocol.NormalizeID(a.ID) == self {
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		// 只在「新对端出现 / 来源 IP 变了」时留一条：宣告每 2 秒一条，
		// 逐条落盘会把 daemon.log 塞满（实测一天 8MB，2MB 轮换下真正的
		// 传输记录全被冲掉），而排障要看的恰恰是「谁来了、从哪个地址」。
		d.logSeen(a, ua.IP)
		d.rememberUnicast(ua.IP)
		select {
		case d.events <- Event{Announce: a, IP: ua.IP.String()}:
		default:
		}
	}
}

// logSeen 记录对端日志去重表：同一设备同一地址只记一次。readLoop 是唯一
// 调用方（单 goroutine），故 seen 无需加锁。
func (d *Discovery) logSeen(a protocol.Announce, ip net.IP) {
	if Debugf == nil {
		return
	}
	id := protocol.NormalizeID(a.ID)
	addr := ip.String()
	if d.seen == nil {
		d.seen = map[string]string{}
	}
	if d.seen[id] == addr {
		return
	}
	d.seen[id] = addr
	Debugf("收到宣告 src=%s id=%s name=%s", addr, protocol.DisplayID(a.ID), a.Name)
}

func hasIPv4(ifi *net.Interface) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return true
		}
	}
	return false
}

// PeerInfo 最近宣告过的设备。
type PeerInfo struct {
	Announce protocol.Announce
	IP       string
	Seen     time.Time
}

// Tracker 收集宣告，供 discover 与 send 使用。
type Tracker struct {
	mu    sync.Mutex
	peers map[string]PeerInfo
}

func NewTracker() *Tracker {
	return &Tracker{peers: map[string]PeerInfo{}}
}

func (t *Tracker) Feed(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.peers[protocol.NormalizeID(e.Announce.ID)] = PeerInfo{
		Announce: e.Announce,
		IP:       e.IP,
		Seen:     time.Now(),
	}
}

// Snapshot 返回未过期的设备列表，按名称排序。
func (t *Tracker) Snapshot() []PeerInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []PeerInfo
	for _, p := range t.peers {
		if time.Since(p.Seen) < ExpireAfter {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Announce.Name < out[j].Announce.Name })
	return out
}

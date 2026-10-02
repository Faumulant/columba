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

// Event 收到的一条设备宣告。
type Event struct {
	Announce protocol.Announce
	IP       string // 宣告来源 IP
}

// Discovery 组播发现 socket：在所有可用网卡上加入组播组，收发共用一个端口。
// VPN/虚拟机/多网卡环境下系统默认路由常把组播引向错误网卡，
// 因此逐网卡加入并逐网卡发送，而不是依赖系统默认选择。
type Discovery struct {
	pconn  *ipv4.PacketConn
	group  *net.UDPAddr
	ifaces []*net.Interface
	events chan Event
	stop   chan struct{}
	once   sync.Once
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

// StartAnnouncing 周期性地在每张已加入的网卡上宣告本机身份。
func (d *Discovery) StartAnnouncing(a protocol.Announce) {
	b, _ := json.Marshal(a)
	go func() {
		tick := time.NewTicker(AnnounceEvery)
		defer tick.Stop()
		for {
			d.sendAll(b)
			select {
			case <-d.stop:
				return
			case <-tick.C:
			}
		}
	}()
}

func (d *Discovery) sendAll(b []byte) {
	if len(d.ifaces) == 0 {
		// 无已枚举网卡（netlink 受限）：交给内核默认组播接口发送
		d.pconn.WriteTo(b, nil, d.group)
		return
	}
	for _, ifi := range d.ifaces {
		d.pconn.SetMulticastInterface(ifi)
		if _, err := d.pconn.WriteTo(b, nil, d.group); err != nil && debug() {
			fmt.Fprintf(os.Stderr, "[discover] 宣告发送失败 %s: %v\n", ifi.Name, err)
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
		select {
		case d.events <- Event{Announce: a, IP: ua.IP.String()}:
		default:
		}
	}
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

package transfer

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"drop/core/discover"
	"drop/core/identity"
	"drop/core/protocol"
)

// Probe 子网探测兜底发现（供 devices 在线状态等组播之外的路径使用）。
// 探测到的对端会收到一封单播宣告作「引子信」：组播被过滤的网络里，
// 对端收不到我们的组播宣告，由此得知我们的地址并回以宣告，双向点亮。
func Probe(id *identity.Identity) ([]Target, error) {
	found, err := probeSubnet(id, defaultPort)
	if err != nil {
		return nil, err
	}
	announceUnicast(id, found)
	return found, nil
}

// announceUnicast 把本机宣告逐个单播寄到对端的发现端口。发完即忘：
// 对端的发现循环本就监听该端口，单播与组播一视同仁。
func announceUnicast(id *identity.Identity, targets []Target) {
	b, err := json.Marshal(protocol.Announce{
		Proto: protocol.Version,
		ID:    id.ID(),
		Name:  id.Name,
		Port:  defaultPort,
	})
	if err != nil {
		return
	}
	for _, t := range targets {
		host, _, err := net.SplitHostPort(t.Addr)
		if err != nil {
			continue
		}
		ra, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(discover.MulticastPort)))
		if err != nil {
			continue
		}
		c, err := net.DialUDP("udp", nil, ra)
		if err != nil {
			continue
		}
		c.Write(b)
		c.Close()
	}
}

// probeSubnet 组播不可靠时的兜底发现：扫描本机所在 IPv4 网段的服务端口，
// 与开放者完成 TLS 握手并读取其设备证书识别身份。出站 TCP 不受防火墙
// 入站规则影响，因此在不转发组播的路由器/AP 环境下也能可靠发现设备。
// 返回按 IP 排序的候选目标（不含本机）。
func probeSubnet(id *identity.Identity, port int) ([]Target, error) {
	self := id.ID()
	nets, err := localNetworks()
	if err != nil {
		return nil, err
	}
	if DebugLog != nil {
		DebugLog("probe: 网段=%v", nets)
	}
	// 1) 并行探测各网段的服务端口
	var mu sync.Mutex
	var open []string
	var wg sync.WaitGroup
	sem := make(chan struct{}, 256)
	for _, base := range nets {
		for i := 1; i <= 254; i++ {
			addr := net.JoinHostPort(net.IPv4(base[0], base[1], base[2], byte(i)).To4().String(), strconv.Itoa(port))
			wg.Add(1)
			sem <- struct{}{}
			go func(a string) {
				defer wg.Done()
				defer func() { <-sem }()
				c, err := net.DialTimeout("tcp", a, 600*time.Millisecond)
				if err != nil {
					return
				}
				c.Close()
				mu.Lock()
				open = append(open, a)
				mu.Unlock()
			}(addr)
		}
	}
	wg.Wait()
	sort.Strings(open)
	if DebugLog != nil {
		DebugLog("probe: 开放端口=%d", len(open))
	}

	// 2) 对开放端口做 TLS 握手识别身份（排除本机）
	var out []Target
	for _, addr := range open {
		t, err := identify(id, addr, 2*time.Second)
		if err != nil || t.ID == self {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// identify 与目标完成一次 TLS 握手，读取对端设备证书。
func identify(id *identity.Identity, addr string, timeout time.Duration) (Target, error) {
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, id.ClientConfig(""))
	if err != nil {
		return Target{}, err
	}
	defer conn.Close()
	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return Target{}, fmt.Errorf("对端未提供证书")
	}
	c := cs.PeerCertificates[0]
	return Target{Addr: addr, ID: identity.Fingerprint(c), Name: c.Subject.CommonName}, nil
}

// localNetworks 收集本机所有非回环 IPv4 网段的 /24 基地址（去重）。
// netlink 被禁时（部分安卓 SELinux 策略，netlinkrib: permission denied）
// 回退到 UDP connect 选路套路。
func localNetworks() ([]net.IP, error) {
	if out, err := interfaces24(); err == nil && len(out) > 0 {
		return out, nil
	}
	return udpRoute24s()
}

// interfaces24 经 net.Interfaces（rtnetlink）枚举网段。PC 与部分安卓可用。
func interfaces24() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []net.IP
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			base := net.IPv4(ip[0], ip[1], ip[2], 0).To4()
			if !seen[base.String()] {
				seen[base.String()] = true
				out = append(out, base)
			}
		}
	}
	return out, nil
}

// udpRoute24s 无 netlink 时的网段回退：UDP connect 不发包，只让内核选路，
// 读出本机源地址后取其 /24。对多个公网目标做一遍，尽量覆盖策略路由。
func udpRoute24s() ([]net.IP, error) {
	seen := map[string]bool{}
	var out []net.IP
	for _, t := range []string{"8.8.8.8:53", "1.1.1.1:53", "223.5.5.5:53", "114.114.114.114:53"} {
		c, err := net.DialTimeout("udp4", t, time.Second)
		if err != nil {
			continue
		}
		if la, ok := c.LocalAddr().(*net.UDPAddr); ok && la.IP != nil {
			if ip := la.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				base := net.IPv4(ip[0], ip[1], ip[2], 0).To4()
				if !seen[base.String()] {
					seen[base.String()] = true
					out = append(out, base)
				}
			}
		}
		c.Close()
	}
	if len(out) == 0 {
		return nil, errors.New("无法确定本机网段（netlink 受限且无可用路由）")
	}
	return out, nil
}

// 注入 discover 的诊断钩子：落 dbg 文件（与 DebugLog 同通道）。
func init() {
	discover.Debugf = func(format string, args ...any) {
		if DebugLog != nil {
			DebugLog("[discover] "+format, args...)
		}
	}
}

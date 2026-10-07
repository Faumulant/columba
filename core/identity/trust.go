package identity

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"drop/core/protocol"
)

// Peer 一台已信任设备。Alias 是本机给它的备注名，优先于 Name 显示。
type Peer struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Alias   string    `json:"alias,omitempty"`
	CertPEM string    `json:"cert_pem"`
	AddedAt time.Time `json:"added_at"`
	// LastIP 该设备最近一次经过认证的 TLS 连接来源 IP（数据端点均要求
	// 已配对）。用于校验未认证宣告的改名请求是否可信。
	LastIP string `json:"last_ip,omitempty"`
}

// DisplayName 备注名优先。
func (p Peer) DisplayName() string {
	if p.Alias != "" {
		return p.Alias
	}
	return p.Name
}

// TrustStore 本机可信设备库。文件被外部（如另一进程的 CLI 配对）修改时
// 自动重载，保证常驻 daemon 与 CLI 的视图一致。
type TrustStore struct {
	mu      sync.Mutex
	path    string
	peers   map[string]Peer
	lastMod time.Time
	loaded  bool
}

func LoadTrust() (*TrustStore, error) {
	t := &TrustStore{
		path:  filepath.Join(AppDir(), "trust.json"),
		peers: map[string]Peer{},
	}
	t.reloadLocked()
	return t, nil
}

// reloadLocked 重新加载磁盘上的信任库（调用方需已持锁）。
func (t *TrustStore) reloadLocked() {
	st, err := os.Stat(t.path)
	if err != nil {
		return
	}
	if t.loaded && st.ModTime().Equal(t.lastMod) {
		return
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var list []Peer
	if err := json.Unmarshal(raw, &list); err != nil {
		return // 文件损坏时保留内存视图
	}
	peers := map[string]Peer{}
	for _, p := range list {
		peers[protocol.NormalizeID(p.ID)] = p
	}
	t.peers = peers
	t.lastMod = st.ModTime()
	t.loaded = true
}

// Add 加入信任库并持久化；已存在时不覆盖。name 为对方自报的设备名，
// 空则退回证书 CN。返回入库后的显示名。
func (t *TrustStore) Add(id string, cert *x509.Certificate, name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	pid := protocol.NormalizeID(id)
	if _, ok := t.peers[pid]; !ok {
		if name == "" {
			name = cert.Subject.CommonName
		}
		t.peers[pid] = Peer{
			ID:      pid,
			Name:    name,
			CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})),
			AddedAt: time.Now(),
		}
		t.saveLocked()
	}
	return t.peers[pid].DisplayName()
}

func (t *TrustStore) Has(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	_, ok := t.peers[protocol.NormalizeID(id)]
	return ok
}

// Name 返回该设备的显示名（备注优先），未知返回空串。
func (t *TrustStore) Name(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	return t.peers[protocol.NormalizeID(id)].DisplayName()
}

// SetAlias 给已信任设备设置备注名；按 ID（可带分隔符）或当前显示名匹配。
// alias 为空串表示清除备注。返回是否找到该设备。
func (t *TrustStore) SetAlias(idOrName, alias string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	key := protocol.NormalizeID(idOrName)
	alias = strings.TrimSpace(alias)
	for id, p := range t.peers {
		if id == key || p.DisplayName() == idOrName {
			p.Alias = alias
			t.peers[id] = p
			t.saveLocked()
			return true
		}
	}
	return false
}

// RefreshName 宣告得知对端自报名变化时刷新缓存名，让改名自动跨设备同步
// （无需重新配对）。备注名优先显示、永不被覆盖。
// 安全约束：组播宣告未认证、ID 可伪造，因此仅接受来源 IP 与该设备
// 最近一次认证连接一致的宣告（LastIP），否则静默忽略。返回是否变更。
func (t *TrustStore) RefreshName(id, name, ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	pid := protocol.NormalizeID(id)
	p, ok := t.peers[pid]
	if !ok || p.Alias != "" || name == "" || p.Name == name {
		return false
	}
	if ip == "" || p.LastIP == "" || p.LastIP != ip {
		return false
	}
	p.Name = name
	t.peers[pid] = p
	t.saveLocked()
	return true
}

// NoteAddr 记录某设备最近一次经过认证的 TLS 连接来源 IP（数据端点均要求
// 已配对，认证在 TLS 握手完成）。供 RefreshName 的来源校验使用。
func (t *TrustStore) NoteAddr(id, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	pid := protocol.NormalizeID(id)
	p, ok := t.peers[pid]
	if !ok || p.LastIP == ip || ip == "" {
		return
	}
	p.LastIP = ip
	t.peers[pid] = p
	t.saveLocked()
}

// Remove 移除一台已信任设备（面板设备管理用）。
func (t *TrustStore) Remove(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	key := protocol.NormalizeID(id)
	if _, ok := t.peers[key]; !ok {
		return false
	}
	delete(t.peers, key)
	t.saveLocked()
	return true
}

// List 返回全部已信任设备，按显示名排序。
func (t *TrustStore) List() []Peer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	out := make([]Peer, 0, len(t.peers))
	for _, p := range t.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName() < out[j].DisplayName() })
	return out
}

func (t *TrustStore) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloadLocked()
	return len(t.peers)
}

func (t *TrustStore) saveLocked() {
	list := make([]Peer, 0, len(t.peers))
	for _, p := range t.peers {
		list = append(list, p)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(t.path, b, 0o600); err == nil {
		if st, err := os.Stat(t.path); err == nil {
			t.lastMod = st.ModTime()
			t.loaded = true
		}
	}
}

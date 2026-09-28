package transfer

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"drop/core/identity"
	"drop/core/protocol"
)

// 设备地址缓存：daemon 把最近确认过的 设备ID→地址 写入本机文件，
// 发送端优先直连缓存地址（省去 5-10 秒的发现/探测），失败再走完整发现。

type ipCacheEntry struct {
	Addr string `json:"addr"`
	Seen string `json:"seen"`
}

var ipCacheMu sync.Mutex

func ipCachePath() string { return filepath.Join(identity.AppDir(), "ipcache.json") }

// UpdateIPCache 记录一台设备最近可用的地址。
func UpdateIPCache(devID, addr string) {
	devID = protocol.NormalizeID(devID)
	if devID == "" || addr == "" {
		return
	}
	ipCacheMu.Lock()
	defer ipCacheMu.Unlock()
	m := map[string]ipCacheEntry{}
	if raw, err := os.ReadFile(ipCachePath()); err == nil {
		json.Unmarshal(raw, &m)
	}
	m[devID] = ipCacheEntry{Addr: addr, Seen: time.Now().Format(time.RFC3339)}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		os.WriteFile(ipCachePath(), b, 0o600)
	}
}

// CachedTarget 返回缓存中该设备的地址，并用一次快速 TLS 握手确认
// 地址仍然有效（对端身份必须与目标 ID 一致）。不可用返回 false。
func CachedTarget(id *identity.Identity, targetID string) (Target, bool) {
	targetID = protocol.NormalizeID(targetID)
	if targetID == "" {
		return Target{}, false
	}
	ipCacheMu.Lock()
	m := map[string]ipCacheEntry{}
	if raw, err := os.ReadFile(ipCachePath()); err == nil {
		json.Unmarshal(raw, &m)
	}
	ipCacheMu.Unlock()
	e, ok := m[targetID]
	if !ok || e.Addr == "" {
		return Target{}, false
	}
	t, err := identify(id, e.Addr, 1500*time.Millisecond)
	if err != nil || protocol.NormalizeID(t.ID) != targetID {
		return Target{}, false
	}
	return t, true
}

// IsRetryable 判断发送错误是否为网络类（值得重试），逻辑性拒绝不重试。
// 优先用错误类型判定（net.Error / EOF 等，随 %w 包装链透传），
// 英文系统错误文案仅作兜底——绝不匹配中文业务文案，避免改文案即失效。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"eof", "timeout", "reset", "refused"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

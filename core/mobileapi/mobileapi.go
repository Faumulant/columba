// Package mobileapi 是 Drop 的 Go→Android 桥接层（gomobile bind 的源包）。
//
// 设计约束：gomobile 不能绑定带函数字段的结构体（现有 transfer.ReceiveOptions
// 无法直接暴露），也不能跨绑定传递任意 Go 类型——实测仅支持
// string/int64/bool/[]byte 与接口，[]string 参数会被静默丢弃，因此批量参数
// 统一用 JSON 字符串。本包以「包级函数 + EventListener 回调接口 + JSON 字符串」
// 的显式小面暴露核心能力，内部组合 core/ 协议核心（discover/identity/history/
// transfer）完成装配。
//
// 本包只依赖 core/ 协议核心，禁止依赖 core/ipc 与 platform
// （安卓端与电脑端目录级分离）。
//
// 线程约定：所有事件回调（EventListener 的方法）都在独立 Go goroutine 中异步
// 分发，不会阻塞核心传输；Kotlin 实现回调时如需更新 UI，须自行切换到主线程。
// 回调内抛出的异常会被捕获并忽略，不会击穿核心。
//
// 数据目录：core/identity 以 LOCALAPPDATA 环境变量定位身份/信任库/历史/IP 缓存。
// Android 端应在首次使用前调用 SetDataDir 固定到应用专属目录（getExternalFilesDir）；
// 不设置时回退到 identity.AppDir 的默认行为。同一进程内可通过 SetDataDir 切换
// 多套独立身份（测试/多账号场景），切换后所有后续调用都作用于新目录。
package mobileapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"drop/core/discover"
	"drop/core/history"
	"drop/core/identity"
	"drop/core/protocol"
	"drop/core/transfer"
)

const (
	// defaultPort 与协议/桌面端一致：子网探测兜底只扫描该端口，
	// 手机接收端监听它才能被电脑端在组播不通时探测发现。
	defaultPort = 42707
	// maxPairedDevices 配对设备上限（与桌面端默认一致）。
	maxPairedDevices = 3
	// discoverWait 完整发现（组播等待 + 子网探测兜底）的等待时长。
	discoverWait = 7 * time.Second
)

// pairDecisionTimeout 配对请求等待宿主裁决的时限，超时视为拒绝。
// 用 var 而非 const：测试需要缩短它。
var pairDecisionTimeout = 3 * time.Minute

func init() {
	transfer.DebugLog = dbg
	// 传输进度：转发给宿主事件（Bus 更新通知进度条）
	transfer.ProgressHook = func(dir, peer, name string, done, total int64) {
		st.dispatchProgress(dir, peer, name, done, total)
	}
}

// dbg 调试输出：数据目录根的 columba-dbg.log（adb shell 可读）。
// gomobile bind 不转发 stderr 到 logcat，log.Printf 在安卓上是黑洞。
// 超过 2MB 轮换一次，避免长期运行无限增长。
func dbg(format string, args ...any) {
	msg := fmt.Sprintf("[columba-dbg] "+format, args...)
	log.Printf("%s", msg)
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		// 0666 + Chmod：FUSE/sdcardfs 下 shell（ext_data_rw 组）要能 cat，
		// 0600 会被拒；数据目录根与 files 根各写一份（ROM 权限行为不一）
		for _, p := range []string{
			filepath.Join(dir, "columba-dbg.log"),
			filepath.Join(filepath.Dir(dir), "columba-dbg.log"),
		} {
			if st, err := os.Stat(p); err == nil && st.Size() > 2<<20 {
				os.Remove(p + ".old")
				os.Rename(p, p+".old")
			}
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666); err == nil {
				f.Chmod(0o666)
				fmt.Fprintln(f, time.Now().Format("15:04:05.000"), msg)
				f.Close()
			}
		}
	}
}

// EventListener 事件回调（gomobile 接口，由 Kotlin 实现并注册）。
// 全部在 Go goroutine 中异步调用，实现方不得假设调用线程。
type EventListener interface {
	// OnTransfer 文件收/发完成。dir 为 "recv"（收到）或 "send"（发出）；
	// ok=false 时 errInfo 携带失败原因（目前仅发送侧会失败事件，
	// 接收侧失败只入历史，不触发本事件）。
	OnTransfer(dir string, peer string, name string, path string, size int64, ok bool, errInfo string)
	// OnText 文本收/发。dir 同上；发送失败经 SendText 返回值表达，不触发本事件。
	OnText(dir string, peer string, text string)
	// OnPairRequest 收到配对请求。返回 true 表示立即允许；
	// 返回 false 表示稍后由宿主调用 PairDecide 裁决（超时视为拒绝）。
	OnPairRequest(peerName string, peerID string) bool
	// OnDeviceChanged 设备列表/在线状态发生变化（配对成功、移除、上下线）。
	OnDeviceChanged()
	// OnProgress 传输进行中（约 800ms 节流一次；done==total 由 OnTransfer 收尾）。
	// done/total 为当前文件的字节进度。
	OnProgress(dir string, peer string, name string, done int64, total int64)
}

type deviceCtx struct {
	id    *identity.Identity
	trust *identity.TrustStore
}

// pairPending 一次待裁决的配对请求。
type pairPending struct {
	result chan bool
}

type state struct {
	mu sync.Mutex

	dataDir string // SetDataDir 固定的数据目录；"" 时用 identity.AppDir 默认
	devices map[string]*deviceCtx

	listener EventListener

	recv       *transfer.Receiver
	recvCancel context.CancelFunc
	saveDir    string
	autoSave   bool
	port       int
	target     transfer.Target // SetTarget 选中的发送目标

	disc       *discover.Discovery
	discCancel context.CancelFunc
	tracker    *discover.Tracker
	discAuto   bool // 发现是否由 StartReceiver 自动拉起（StopReceiver 连带停止）

	pending *pairPending
}

func newState() *state { return &state{devices: map[string]*deviceCtx{}} }

var st = newState()

// ─── 初始化与设置 ────────────────────────────────────────────────

// SetDataDir 设置数据目录（身份/信任库/历史/IP 缓存的根）。
// Android 端应在任何使用身份的调用之前传入应用专属目录；
// 实现为重定向 identity.AppDir 所依赖的环境变量，不改动共享核心。
// 之后所有调用都作用于该目录；可再次调用切换到另一套独立身份。
func SetDataDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("数据目录不能为空")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	if err := os.Setenv("LOCALAPPDATA", abs); err != nil {
		return err
	}
	st.mu.Lock()
	st.dataDir = abs
	st.mu.Unlock()
	return nil
}

// SetPort 设置接收端 TCP 监听端口（默认 42707，须在 StartReceiver 之前调用）。
// 正常部署不需要；多实例/本机同跑桌面端等场景用于避开端口冲突。
func SetPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口超出范围: %d", port)
	}
	st.mu.Lock()
	st.port = port
	st.mu.Unlock()
	return nil
}

// SetDeviceName 修改本机设备名（宣告与配对显示用）。设备 ID 不变。
func SetDeviceName(name string) error {
	d, err := st.device()
	if err != nil {
		return err
	}
	return d.id.SetName(name)
}

// SelfInfo 本机身份信息（JSON）：{"name","id","display_id"}。
func SelfInfo() string {
	d, err := st.device()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mobileapi: SelfInfo 失败:", err)
		return "{}"
	}
	b, _ := json.Marshal(struct {
		Name      string `json:"name"`
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}{Name: d.id.Name, ID: d.id.ID(), DisplayID: protocol.DisplayID(d.id.ID())})
	return string(b)
}

// SetSaveDir 运行中更改接收目录（设置页切换接收文件夹即时生效）。
func SetSaveDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("接收目录不能为空")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	st.mu.Lock()
	st.saveDir = abs
	st.mu.Unlock()
	return nil
}

// ─── 生命周期 ────────────────────────────────────────────────────

// StartReceiver 启动接收端：TLS 监听 + 组播宣告 + 发现监听（在线状态）。
// autoSave 记录接收模式（D3）：Go 侧两种模式都落到 saveDir，
// 手动模式的「保存到…」由 Kotlin 用 SAF 把文件从应用目录搬走。
func StartReceiver(saveDir string, autoSave bool, listener EventListener) error {
	saveDir = strings.TrimSpace(saveDir)
	if saveDir == "" {
		return errors.New("接收目录不能为空")
	}
	absDir, err := filepath.Abs(saveDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return err
	}
	d, err := st.device()
	if err != nil {
		return err
	}

	st.mu.Lock()
	if st.recv != nil {
		st.mu.Unlock()
		return errors.New("接收端已在运行")
	}
	port := st.port
	if port == 0 {
		port = defaultPort
	}
	st.listener = listener
	st.saveDir = absDir
	st.autoSave = autoSave
	st.mu.Unlock()

	// 端口预检：让「端口被占」在启动时立即报错，而不是静默失败在后台协程里
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("无法监听端口 %d: %w（如与本机桌面版 Drop 冲突，可先 SetPort 换端口）", port, err)
	}
	ln.Close()

	recv := transfer.NewReceiver(d.id, d.trust, transfer.ReceiveOptions{
		Port: port,
		Dir:  absDir,
		DirFn: func() string {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.saveDir
		},
		ConfirmPeer: st.confirmPeer, // 配对裁决走 OnPairRequest/PairDecide
		MaxDevices:  func() int { return maxPairedDevices },
		OnReceived: func(peer, name, path string) {
			st.dispatchTransfer("recv", peer, name, path, fileSize(path), true, "")
		},
		OnText: func(peer, text string) {
			st.dispatchText("recv", peer, text)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	st.mu.Lock()
	st.recv, st.recvCancel = recv, cancel
	st.mu.Unlock()
	go func() {
		if err := recv.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "mobileapi: 接收端异常退出:", err)
			st.mu.Lock()
			if st.recv == recv {
				st.recv, st.recvCancel = nil, nil
			}
			st.mu.Unlock()
		}
	}()

	// 发现监听（宣告由 Receiver.Run 内部完成）：供设备在线状态使用
	if err := st.ensureDiscovery(true); err != nil {
		fmt.Fprintln(os.Stderr, "※ 设备发现不可用（不影响接收与直连发送）:", err)
	}
	return nil
}

// StopReceiver 停止接收端；由 StartReceiver 自动拉起的发现监听一并停止
// （显式 StartDiscovery 启动的不受影响）。未裁决的配对请求按拒绝处理。
func StopReceiver() {
	st.mu.Lock()
	recv, cancel := st.recv, st.recvCancel
	st.recv, st.recvCancel = nil, nil
	pend := st.pending
	st.pending = nil
	st.mu.Unlock()
	if pend != nil {
		select {
		case pend.result <- false:
		default:
		}
	}
	if cancel != nil {
		cancel()
	}
	_ = recv
	st.mu.Lock()
	auto := st.discAuto
	st.mu.Unlock()
	if auto {
		StopDiscovery()
	}
}

// StartDiscovery 启动组播发现监听（只听不宣告；宣告随接收端启动），
// 用于目标设备发现与在线状态。listener 为 nil 时沿用已注册的监听器。
func StartDiscovery(listener EventListener) error {
	st.mu.Lock()
	if listener != nil {
		st.listener = listener
	}
	st.mu.Unlock()
	return st.ensureDiscovery(false)
}

// TriggerDiscover 立即广播一轮宣告（点信标「立即重发现」用），不打断
// 周期循环；发现未启动时为空操作。实际发送在发现协程内完成。
func TriggerDiscover() {
	st.mu.Lock()
	d := st.disc
	st.mu.Unlock()
	if d != nil {
		d.AnnounceNow()
	}
}

// StopDiscovery 停止发现监听。在线状态随之全部离线。
func StopDiscovery() {
	st.mu.Lock()
	disc, cancel := st.disc, st.discCancel
	st.disc, st.discCancel, st.tracker = nil, nil, nil
	st.discAuto = false
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if disc != nil {
		disc.Close()
	}
}

// ensureDiscovery 幂等启动发现监听。auto 标记是否由接收端连带拉起。
func (st *state) ensureDiscovery(auto bool) error {
	d, err := st.device()
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.disc != nil {
		if auto {
			st.discAuto = true
		}
		return nil
	}
	disc, err := discover.New(d.id.ID())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr := discover.NewTracker()
	st.disc, st.discCancel, st.tracker = disc, cancel, tr
	if auto {
		st.discAuto = true
	}
	go func() {
		for e := range disc.Events() {
			tr.Feed(e)
			// 对端改名自动同步进信任库（备注名优先，不受影响）
			if d.trust.RefreshName(e.Announce.ID, e.Announce.Name, e.IP) {
				st.dispatchChanged()
			}
		}
	}()
	go st.watchOnline(ctx)
	return nil
}

// watchOnline 周期比对在线集合，变化时通知宿主（设备上下线 → 刷新列表）。
func (st *state) watchOnline(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st.mu.Lock()
		tr := st.tracker
		st.mu.Unlock()
		if tr == nil {
			continue
		}
		cur := map[string]bool{}
		for _, p := range tr.Snapshot() {
			cur[protocol.NormalizeID(p.Announce.ID)] = true
		}
		if !sameKeys(cur, last) {
			last = cur
			st.dispatchChanged()
		}
	}
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// ─── 设备与配对 ──────────────────────────────────────────────────

// ListDevices 已配对设备列表（JSON）：[{"id","name","online"}]，
// name 为显示名（备注优先），online 为最近 20 秒内有无宣告。
func ListDevices() string {
	d, err := st.device()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mobileapi: ListDevices 失败:", err)
		return "[]"
	}
	st.mu.Lock()
	tr := st.tracker
	st.mu.Unlock()
	type devJSON struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	}
	out := make([]devJSON, 0, 8)
	for _, p := range d.trust.List() {
		online := false
		if tr != nil {
			for _, lp := range tr.Snapshot() {
				if protocol.NormalizeID(lp.Announce.ID) == p.ID {
					online = true
					break
				}
			}
		}
		out = append(out, devJSON{ID: p.ID, Name: p.DisplayName(), Online: online})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// SetAlias 给已配对设备设置本机备注名（空串清除）；idOrName 支持 ID 或当前显示名。
// 备注名仅本机生效，优先于对方自报名显示。
func SetAlias(idOrName, alias string) error {
	d, err := st.device()
	if err != nil {
		return err
	}
	if !d.trust.SetAlias(idOrName, alias) {
		return fmt.Errorf("未找到设备 %q", idOrName)
	}
	st.dispatchChanged()
	return nil
}

// RemoveDevice 移除一台已配对设备（双向信任需对方自行移除）。
func RemoveDevice(id string) error {
	d, err := st.device()
	if err != nil {
		return err
	}
	if !d.trust.Remove(id) {
		return fmt.Errorf("设备 %s 不在信任列表中", protocol.DisplayID(id))
	}
	st.dispatchChanged()
	return nil
}

// PairStart 本机出码（备用路径：电脑输入手机的配对码）。接收端须在运行中。
// 码 5 分钟有效、一次性；对方加入后经 OnPairRequest 弹窗裁决。
func PairStart() (string, error) {
	st.mu.Lock()
	recv := st.recv
	st.mu.Unlock()
	if recv == nil {
		return "", errors.New("接收端未运行：请先 StartReceiver")
	}
	return recv.StartPairing()
}

// PairJoin 输入对方配对码完成配对，返回对方设备名。
// 目标解析顺序：地址缓存（恰有一条时直连）→ 发现快照（恰有一台在线时直连）
// → 完整发现（组播等待 + 子网探测兜底，候选不唯一时报错）。
// 一旦有过直连尝试而未成，就不再做全网段兜底（一次性配对码
// 不应盲目试遍局域网），把失败原因交还用户。
func PairJoin(code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("配对码不能为空")
	}
	d, err := st.device()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	lastErr := errors.New("未发现可配对的设备：确认对方已进入配对等待且与本机同网段")
	attempted := false
	// 1) 地址缓存恰有一条：多半是此前发现/配过的设备，先直连验证（毫秒级）
	if t, ok := soleCachedTarget(d); ok {
		attempted = true
		dbg("PairJoin: 缓存直连尝试 %s", t.Addr)
		if name, err := transfer.Pair(ctx, d.id, d.trust, t, code); err == nil {
			transfer.UpdateIPCache(t.ID, t.Addr)
			st.dispatchChanged()
			return name, nil
		} else {
			dbg("PairJoin: 缓存直连失败 err=%v", err)
			lastErr = err
		}
	}
	// 2) 发现快照恰有一台在线设备
	if t, ok := st.soleLivePeer(); ok {
		attempted = true
		dbg("PairJoin: 快照在线设备 %s", t.Addr)
		if name, err := transfer.Pair(ctx, d.id, d.trust, t, code); err == nil {
			transfer.UpdateIPCache(t.ID, t.Addr)
			st.dispatchChanged()
			return name, nil
		} else {
			dbg("PairJoin: 快照直连失败 err=%v", err)
			lastErr = err
		}
	}
	// 已尝试过直连候选而未成：不再向全网段兜底广播配对码
	//（一次性码不应盲目试遍局域网），把失败原因交还用户重试。
	if attempted {
		return "", lastErr
	}
	// 3) 完整发现（候选多于一份时不盲目尝试，交用户指定）
	t, err := transfer.FindPeer(d.id, "", discoverWait, nil)
	dbg("PairJoin: 完整发现 err=%v", err)
	if err != nil {
		return "", err
	}
	name, err := transfer.Pair(ctx, d.id, d.trust, t, code)
	if err != nil {
		return "", err
	}
	transfer.UpdateIPCache(t.ID, t.Addr)
	st.dispatchChanged()
	return name, nil
}

// PairDecide 对挂起的配对请求作出裁决（配对码为一次性，码在请求到达时已失效）。
func PairDecide(allow bool) error {
	st.mu.Lock()
	pend := st.pending
	st.mu.Unlock()
	if pend == nil {
		return errors.New("当前没有待裁决的配对请求")
	}
	select {
	case pend.result <- allow:
	default:
	}
	return nil
}

// confirmPeer 作为 transfer.ConfirmPeer 的实现：缓存待裁决请求并异步通知
// 宿主；OnPairRequest 返回 true 视为即时允许，否则等 PairDecide 或超时。
func (st *state) confirmPeer(name, id string) bool {
	st.mu.Lock()
	if st.listener == nil || st.pending != nil {
		st.mu.Unlock()
		return false
	}
	pend := &pairPending{result: make(chan bool, 1)}
	st.pending = pend
	l := st.listener
	st.mu.Unlock()

	go func() {
		defer recoverPanic("OnPairRequest")
		if l.OnPairRequest(name, id) {
			select {
			case pend.result <- true:
			default:
			}
		}
	}()

	timer := time.NewTimer(pairDecisionTimeout)
	defer timer.Stop()
	var allowed bool
	select {
	case allowed = <-pend.result:
	case <-timer.C:
	}
	st.mu.Lock()
	if st.pending == pend {
		st.pending = nil
	}
	st.mu.Unlock()
	return allowed
}

// soleCachedTarget 地址缓存恰有一条时返回验证过的直连目标。
func soleCachedTarget(d *deviceCtx) (transfer.Target, bool) {
	// ipcache.json 的格式与 core/transfer 约定耦合，这里是唯一读取点
	raw, err := os.ReadFile(filepath.Join(identity.AppDir(), "ipcache.json"))
	if err != nil {
		return transfer.Target{}, false
	}
	m := map[string]struct {
		Addr string `json:"addr"`
		Seen string `json:"seen"`
	}{}
	if json.Unmarshal(raw, &m) != nil || len(m) != 1 {
		return transfer.Target{}, false
	}
	for id := range m {
		if t, ok := transfer.CachedTarget(d.id, id); ok {
			return t, true
		}
	}
	return transfer.Target{}, false
}

// soleLivePeer 发现快照恰有一台在线设备时返回它。
func (st *state) soleLivePeer() (transfer.Target, bool) {
	st.mu.Lock()
	tr := st.tracker
	st.mu.Unlock()
	if tr == nil {
		return transfer.Target{}, false
	}
	snap := tr.Snapshot()
	if len(snap) != 1 {
		return transfer.Target{}, false
	}
	p := snap[0]
	return transfer.Target{
		Addr: net.JoinHostPort(p.IP, strconv.Itoa(p.Announce.Port)),
		ID:   p.Announce.ID,
		Name: p.Announce.Name,
	}, true
}

// ─── 发送 ────────────────────────────────────────────────────────

// SetTarget 设置默认发送目标（左上角设备选择）。传空串清除选择。
func SetTarget(targetID string) error {
	if strings.TrimSpace(targetID) == "" {
		st.mu.Lock()
		st.target = transfer.Target{}
		st.mu.Unlock()
		return nil
	}
	d, err := st.device()
	if err != nil {
		return err
	}
	t, err := st.resolveTarget(d, targetID)
	if err != nil {
		return err
	}
	st.mu.Lock()
	st.target = t
	st.mu.Unlock()
	return nil
}

// SendText 发送文本到目标设备（targetID 传空串时用 SetTarget 的选择）。
func SendText(targetID, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("文本为空")
	}
	d, err := st.device()
	if err != nil {
		return err
	}
	t, err := st.resolveTarget(d, targetID)
	if err != nil {
		return err
	}
	peer := st.displayName(d, t)
	if err := transfer.SendText(context.Background(), d.id, t, text); err != nil {
		dbg("SendText 失败 target=%s err=%v", t.Addr, err)
		return err
	}
	st.dispatchText("send", peer, text)
	return nil
}

// SendFiles 发送文件/文件夹到目标设备（targetID 传空串时用 SetTarget 的选择）。
// pathsJSON 为 JSON 字符串数组，如 ["<cache>/a.jpg","<cache>/b.mp4"]——
// gomobile 不支持 []string 参数（会被静默丢弃），
// Android 端先把 Uri 落到缓存路径再以 JSON 传入。完成后触发 OnTransfer(send)。
func SendFiles(targetID string, pathsJSON string) error {
	paths, err := decodeStrings(pathsJSON)
	if err != nil {
		return err
	}
	d, err := st.device()
	if err != nil {
		return err
	}
	return st.sendFiles(d, targetID, paths)
}

func (st *state) sendFiles(d *deviceCtx, targetID string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("没有可发送的文件")
	}
	t, err := st.resolveTarget(d, targetID)
	if err != nil {
		return err
	}
	peer := st.displayName(d, t)
	total := pathsSize(paths)
	name, err := transfer.Send(context.Background(), d.id, t, paths,
		transfer.SendOptions{SkipSourcePath: true}) // 应用缓存路径对历史无意义
	st.dispatchTransfer("send", peer, name, paths[0], total, err == nil, errText(err))
	return err
}

// resolveTarget 目标解析：地址缓存直连 → 发现快照匹配 → 完整发现。
func (st *state) resolveTarget(d *deviceCtx, targetID string) (transfer.Target, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		st.mu.Lock()
		t := st.target
		st.mu.Unlock()
		if t.ID == "" {
			return transfer.Target{}, errors.New("未指定发送目标：请先 SetTarget 或传入设备 ID")
		}
		return t, nil
	}
	// 1) 地址缓存直连（快速路径，TLS 握手验证身份）
	if t, ok := transfer.CachedTarget(d.id, targetID); ok {
		dbg("resolve: 缓存直连 %s", t.Addr)
		return t, nil
	}
	// 2) 本进程发现快照按 ID/名称匹配
	if t, ok := st.trackerMatch(d, targetID); ok {
		dbg("resolve: 快照匹配 %s", t.Addr)
		return t, nil
	}
	// 3) 完整发现（组播等待 + 子网探测兜底；只从已配对设备中选）
	dbg("resolve: 完整发现 targetID=%q", targetID)
	t, err := transfer.FindPeer(d.id, targetID, discoverWait, d.trust)
	dbg("resolve: FindPeer err=%v", err)
	return t, err
}

// trackerMatch 在发现快照里按 ID（忽略分隔符/大小写）、设备名或本机备注名匹配。
func (st *state) trackerMatch(d *deviceCtx, match string) (transfer.Target, bool) {
	st.mu.Lock()
	tr := st.tracker
	st.mu.Unlock()
	if tr == nil {
		return transfer.Target{}, false
	}
	norm := protocol.NormalizeID(match)
	for _, p := range tr.Snapshot() {
		if protocol.NormalizeID(p.Announce.ID) == norm ||
			strings.EqualFold(p.Announce.Name, match) ||
			(match != "" && d.trust.Name(p.Announce.ID) == match) {
			t := transfer.Target{
				Addr: net.JoinHostPort(p.IP, strconv.Itoa(p.Announce.Port)),
				ID:   p.Announce.ID,
				Name: p.Announce.Name,
			}
			transfer.UpdateIPCache(t.ID, t.Addr)
			return t, true
		}
	}
	return transfer.Target{}, false
}

func (st *state) displayName(d *deviceCtx, t transfer.Target) string {
	if n := d.trust.Name(t.ID); n != "" {
		return n
	}
	if t.Name != "" {
		return t.Name
	}
	return t.Addr
}

// ─── 历史 ────────────────────────────────────────────────────────

// HistoryJSON 全部传输历史（JSON 数组，含 id 供批量删除）。
func HistoryJSON() string {
	entries := history.Load()
	if entries == nil {
		entries = []history.Entry{}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// DeleteHistory 批量删除指定 id 的历史记录。idsJSON 为 JSON 字符串数组
// （gomobile 不支持 []string 参数）。
// 历史文件尚不存在时视为空，直接成功。
func DeleteHistory(idsJSON string) error {
	ids, err := decodeStrings(idsJSON)
	if err != nil {
		return err
	}
	_, err = history.DeleteIDs(ids)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ─── 内部工具 ────────────────────────────────────────────────────

// device 取当前数据目录的身份与信任库（按目录缓存，多套身份互不串扰）。
func (st *state) device() (*deviceCtx, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := st.dataDir
	if key == "" {
		key = identity.AppDir()
	}
	if d, ok := st.devices[key]; ok {
		return d, nil
	}
	id, err := identity.LoadOrCreate()
	if err != nil {
		return nil, fmt.Errorf("加载设备身份失败: %w", err)
	}
	trust, err := identity.LoadTrust()
	if err != nil {
		return nil, fmt.Errorf("加载信任库失败: %w", err)
	}
	d := &deviceCtx{id: id, trust: trust}
	st.devices[key] = d
	return d, nil
}

func (st *state) dispatchTransfer(dir, peer, name, path string, size int64, ok bool, errInfo string) {
	st.mu.Lock()
	l := st.listener
	st.mu.Unlock()
	if l == nil {
		return
	}
	go func() {
		defer recoverPanic("OnTransfer")
		l.OnTransfer(dir, peer, name, path, size, ok, errInfo)
	}()
}

func (st *state) dispatchProgress(dir, peer, name string, done, total int64) {
	st.mu.Lock()
	l := st.listener
	st.mu.Unlock()
	if l == nil {
		return
	}
	go func() {
		defer recoverPanic("OnProgress")
		l.OnProgress(dir, peer, name, done, total)
	}()
}

func (st *state) dispatchText(dir, peer, text string) {
	st.mu.Lock()
	l := st.listener
	st.mu.Unlock()
	if l == nil {
		return
	}
	go func() {
		defer recoverPanic("OnText")
		l.OnText(dir, peer, text)
	}()
}

func (st *state) dispatchChanged() {
	st.mu.Lock()
	l := st.listener
	st.mu.Unlock()
	if l == nil {
		return
	}
	go func() {
		defer recoverPanic("OnDeviceChanged")
		l.OnDeviceChanged()
	}()
}

func recoverPanic(where string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "mobileapi: 事件回调 %s 异常（已忽略）: %v\n", where, r)
	}
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// pathsSize 统计发送内容的总大小（文件夹递归；统计失败按 0 计，仅用于事件展示）。
func pathsSize(paths []string) int64 {
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			total += info.Size()
			continue
		}
		filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				if fi, err := d.Info(); err == nil {
					total += fi.Size()
				}
			}
			return nil
		})
	}
	return total
}

func errText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

// decodeStrings 解析 JSON 字符串数组参数。
// gomobile 只支持 []byte 一种切片，[]string 参数会被静默丢弃
// （实测），因此批量参数统一走 JSON 字符串。
func decodeStrings(s string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("参数应为 JSON 字符串数组: %w", err)
	}
	return out, nil
}

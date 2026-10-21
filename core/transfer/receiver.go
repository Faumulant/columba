// Package transfer 实现文件传输：清单协商、流式上传、完整性与落盘。
package transfer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"drop/core/discover"
	"drop/core/history"
	"drop/core/identity"
	"drop/core/protocol"
)

const (
	maxManifestFiles = 1000
	maxSessions      = 64 // 并发会话上限，防止对端（即使是已配对设备）耗尽本机资源
	sessionTTL       = 30 * time.Minute
	pairCodeTTL      = 5 * time.Minute
)

// ReceiveOptions 接收端配置。ConfirmPeer 非空时启用配对模式（drop pair），
// 收到配对请求时由它决定是否接受；nil 时拒绝一切配对请求。
// OnReceived 在每个文件完整落盘后被调用（常驻模式用于弹系统通知）。
// ReceiveOptions 接收端配置。ConfirmPeer 非空时启用配对模式，收到配对请求
// 时由它决定是否接受；nil 时拒绝一切配对请求。DirFn 非空时动态决定接收
// 目录（面板可运行中更改）。OnReceived/OnText 用于常驻模式弹通知。
// MaxDevices 返回设备配对上限（解锁后可返回极大值）。
// OnUnlocked 非空时，收到已配对设备同步来的解锁状态后回调（宿主据此刷新 UI）。
// ExitOnPair 仅用于 drop pair 命令：配对完成后退出；常驻模式必须为 false。
type ReceiveOptions struct {
	Port        int
	Dir         string
	DirFn       func() string
	ConfirmPeer func(name, id string) bool
	OnReceived  func(peer, name, path string)
	// OnFailed 一个文件的接收以失败/取消收场时调用（成功走 OnReceived）。
	// 必须成对：宿主靠它把「传输中」条目收场，缺了它条目会永久卡在最后一帧
	// （手机端实测：取消后临时条目停在 6% 不消失）。canceled 为 true 表示
	// 对端主动取消（/v1/cancel），false 表示真失败（断线、校验失败等）。
	OnFailed   func(peer, name string, canceled bool, reason string)
	OnText     func(peer, text string)
	MaxDevices func() int
	OnUnlocked func(peer string)
	FreeDisk   func(path string) (uint64, error)
	ExitOnPair bool
}

type fileState struct {
	meta  protocol.FileMeta
	part  string
	file  *os.File
	hash  hash.Hash
	bytes int64
	got   bool // 上传完成，等待 complete 校验
	done  bool // complete 已受理（防并发重复落盘）
	// recorded 本文件的结局已写入历史。/v1/cancel 与「上传读中断」是两条独立
	// 的网络事件、先后不确定：先到者负责写记录，后到者只改写（取消时把已写的
	// failed 改成 canceled），绝不追加第二条。
	recorded bool
	// saved 为落盘后的最终文件名，非空即本次传输已成功。发送方补投
	// complete（响应丢包）时靠它返回同一结果而不是 409。
	saved string
}

type session struct {
	senderID string
	files    map[string]*fileState
	last     time.Time
}

type pairState struct {
	code    string
	expires time.Time
	used    bool
}

// Receiver 接收端。
type Receiver struct {
	id    *identity.Identity
	trust *identity.TrustStore
	opt   ReceiveOptions

	mu       sync.Mutex
	sessions map[string]*session

	pairMu   sync.Mutex
	pairing  *pairState
	paired   chan struct{}
	pairOnce sync.Once

	announcer *discover.Discovery // 外部注入的发现实例：与宿主共用单一 socket（双实例下单播宣告只投递给其一，见 Run）

	pairFailsMu sync.Mutex
	pairFails   map[string]*pairFail
}

// pairFail 配对失败限速记录（按来源 IP）。
type pairFail struct {
	fails    int
	winStart time.Time
}

// pairAllowed 配对尝试限速判定：10 分钟窗口内失败超过 5 次拒绝继续尝试。
const pairFailMax = 5

func (r *Receiver) pairAllowed(ip string) bool {
	r.pairFailsMu.Lock()
	defer r.pairFailsMu.Unlock()
	if r.pairFails == nil {
		r.pairFails = map[string]*pairFail{}
	}
	// 顺手清扫过期记录，限速表只增不减会无界增长
	for k, v := range r.pairFails {
		if time.Since(v.winStart) > 10*time.Minute {
			delete(r.pairFails, k)
		}
	}
	f, ok := r.pairFails[ip]
	if !ok {
		return true
	}
	return f.fails < pairFailMax
}

func (r *Receiver) pairFail(ip string) {
	r.pairFailsMu.Lock()
	defer r.pairFailsMu.Unlock()
	if r.pairFails == nil {
		r.pairFails = map[string]*pairFail{}
	}
	f, ok := r.pairFails[ip]
	if !ok || time.Since(f.winStart) > 10*time.Minute {
		r.pairFails[ip] = &pairFail{fails: 1, winStart: time.Now()}
		return
	}
	f.fails++
}

func NewReceiver(id *identity.Identity, trust *identity.TrustStore, opt ReceiveOptions) *Receiver {
	return &Receiver{
		id:       id,
		trust:    trust,
		opt:      opt,
		sessions: map[string]*session{},
		paired:   make(chan struct{}),
	}
}

// SetAnnouncer 注入宿主的发现实例：接收器不再自建，宣告与宿主 UI 跟踪
// 共用同一 socket（双实例时单播宣告只投递给其一，热点实测踩坑）。
func (r *Receiver) SetAnnouncer(d *discover.Discovery) { r.announcer = d }

// StartPairing 生成本次一次性配对码并进入等待状态。
func (r *Receiver) StartPairing() (string, error) {
	if r.opt.ConfirmPeer == nil {
		return "", errors.New("未启用配对模式")
	}
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	r.pairMu.Lock()
	r.pairing = &pairState{code: code, expires: time.Now().Add(pairCodeTTL)}
	r.pairMu.Unlock()
	return code, nil
}

// dir 当前接收目录（面板可在运行中更改）。
func (r *Receiver) dir() string {
	if r.opt.DirFn != nil {
		if d := r.opt.DirFn(); d != "" {
			return d
		}
	}
	return r.opt.Dir
}

// Run 启动 TCP+TLS 监听与组播宣告，阻塞直到 ctx 取消。
func (r *Receiver) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.dir(), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", r.opt.Port))
	if err != nil {
		return err
	}
	tlsCfg := r.id.ServerConfig()
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(tls.NewListener(ln, tlsCfg)) }()

	ann := protocol.Announce{Proto: protocol.Version, ID: r.id.ID(), Name: r.id.Name, Port: r.opt.Port}
	disc := r.announcer
	if disc == nil {
		var err error
		disc, err = discover.New(r.id.ID())
		if err != nil {
			fmt.Fprintln(os.Stderr, "※ 设备发现不可用（发送方仍可用 -addr 直连）:", err)
		} else {
			defer disc.Close()
		}
	}
	if disc != nil {
		// 宣告即心跳：组播 + 对已认识对端的单播（见 discover.sendAll）。
		// 共用宿主实例的原因：双实例时单播宣告只投递给「最后绑定」的
		// socket——接收器实例收得到，宿主 UI 跟踪实例饿死（热点实测）。
		// 宿主（daemon）已经起过宣告循环，这里只会换载荷。
		disc.StartAnnouncing(ann)
	}
	go r.janitor(ctx)

	mode := "接收就绪"
	if r.opt.ExitOnPair {
		mode = "配对等待中"
	}
	fmt.Fprintf(os.Stderr, "%s：%s [%s]\n监听端口 %d → 接收目录 %s\nCtrl+C 退出\n",
		mode, r.id.Name, protocol.DisplayID(r.id.ID()), r.opt.Port, r.dir())

	// 仅 drop pair 模式在配对完成后退出；常驻模式忽略 paired 信号，
	// 配对完成后继续接收（nil channel 永不触发，HTTP 服务始终受本函数管理）。
	var pairedCh <-chan struct{}
	if r.opt.ExitOnPair {
		pairedCh = r.paired
	}
	select {
	case <-ctx.Done():
		srv.Close()
		return nil
	case <-pairedCh:
		srv.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case protocol.PathPair:
		r.handlePair(w, req)
		return
	case protocol.PathOffer, protocol.PathUpload, protocol.PathComplete, protocol.PathText, protocol.PathUnlock, protocol.PathCancel:
	default:
		http.NotFound(w, req)
		return
	}
	// 授权门槛：数据端点只对已配对设备开放（认证已在 TLS 握手完成）。
	if !r.trust.Has(peerID(req)) {
		http.Error(w, "设备未配对：请先在两端执行 drop pair 完成配对", http.StatusUnauthorized)
		return
	}
	// 记录认证连接的来源 IP（宣告改名校验的信任锚点）
	if c := certOf(req); c != nil {
		if h, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			r.trust.NoteAddr(peerID(req), h)
		}
	}
	switch req.URL.Path {
	case protocol.PathOffer:
		r.handleOffer(w, req)
	case protocol.PathUpload:
		r.handleUpload(w, req)
	case protocol.PathComplete:
		r.handleComplete(w, req)
	case protocol.PathText:
		r.handleText(w, req)
	case protocol.PathUnlock:
		r.handleUnlock(w, req)
	case protocol.PathCancel:
		r.handleCancel(w, req)
	}
}

// handleUnlock 接收对端的解锁状态同步。
//
// 授权门槛与其余数据端点一致：只有已配对设备能发。收到 unlocked=true 就在
// 本机也写解锁标记——用户在任意一端点过「解除上限」，其余已配对设备自动跟随。
// 反向（unlocked=false）不做任何事：解锁是一次性决定，不支持远程撤销，
// 否则一台设备误发就能把用户花钱解锁的状态抹掉。
func (r *Receiver) handleUnlock(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var um protocol.UnlockMsg
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&um); err != nil || um.Proto != protocol.Version {
		http.Error(w, "请求无效", http.StatusBadRequest)
		return
	}
	if um.Unlocked && !identity.IsUnlocked() {
		if err := identity.SetUnlocked(); err != nil {
			http.Error(w, "写入解锁标记失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if r.opt.OnUnlocked != nil {
			r.opt.OnUnlocked(r.peerName(req))
		}
	}
	writeJSON(w, protocol.UnlockAck{OK: true})
}

// handleCancel 接收发送方的主动取消通知：清掉未完成的 .part，把这条记成
// canceled（而不是 failed）。必须在 handleUpload 的读循环之外完成，因此这里
// 只动状态、不碰 st.file 的读写缓冲。
func (r *Receiver) handleCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cm protocol.CancelMsg
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&cm); err != nil || cm.Proto != protocol.Version {
		http.Error(w, "请求无效", http.StatusBadRequest)
		return
	}
	peer, pid := r.peerName(req), peerID(req)
	r.mu.Lock()
	s := r.sessions[cm.SessionID]
	var targets []*fileState
	if s != nil {
		if cm.FileID != "" {
			if st := s.files[cm.FileID]; st != nil {
				targets = append(targets, st)
			}
		} else {
			for _, st := range s.files {
				targets = append(targets, st)
			}
		}
	}
	type rec struct {
		name string
		size int64
		part string
		// first 本路径是第一个给这个文件定结局的：只有 first 负责写历史，
		// 否则两条路径各写一条（读中断 + 取消通知），历史里会出现重复条目。
		first bool
	}
	var recs []rec
	for _, st := range targets {
		if st.done {
			continue // 已落盘：取消通知迟到，忽略
		}
		if st.file != nil {
			st.file.Close()
			st.file = nil
		}
		st.hash = nil
		first := !st.recorded
		st.recorded = true
		recs = append(recs, rec{name: st.meta.Path, size: st.meta.Size, part: st.part, first: first})
	}
	r.mu.Unlock()

	for _, rc := range recs {
		os.Remove(rc.part)
		if rc.first {
			// 本路径先到：直接记 canceled
			r.recordStatus(peer, pid, rc.name, "", rc.size, history.StatusCanceled, "对方已取消传输")
		} else {
			// 读中断先到、已写了一条 failed：就地改写成 canceled，不追加第二条。
			// 改不到（极少数：failed 还没落盘完）就什么都不做——读中断那条最终
			// 会被用户看到成「传输中断」，比冒重复条目的风险好。
			history.ReclassifyCanceled("recv", pid, rc.name, "对方已取消传输")
		}
		// 无论历史是改写还是新增，宿主都要收到一次「结束」——否则手机端
		// 的传输中条目会永久停在最后一帧
		if r.opt.OnFailed != nil {
			r.opt.OnFailed(peer, filepath.Base(rc.name), true, "对方已取消传输")
		}
	}
	writeJSON(w, protocol.UnlockAck{OK: true})
}

// handleText 接收文本内容：不落盘，入历史 + 通知回调。
func (r *Receiver) handleText(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var tm protocol.TextMsg
	if err := json.NewDecoder(io.LimitReader(req.Body, 128<<10)).Decode(&tm); err != nil || tm.Proto != protocol.Version {
		http.Error(w, "请求无效", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(tm.Text)
	if text == "" {
		http.Error(w, "文本为空", http.StatusBadRequest)
		return
	}
	peer := r.peerName(req)
	history.Append(history.Entry{Dir: "recv", Peer: peer, PeerID: peerID(req), Kind: "text", Status: "ok", Text: text})
	if r.opt.OnText != nil {
		r.opt.OnText(peer, text)
	}
	writeJSON(w, protocol.CompleteAck{OK: true})
}

func (r *Receiver) handlePair(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.opt.ConfirmPeer == nil {
		writeJSON(w, protocol.PairAck{Message: "本机未处于配对模式"})
		return
	}
	var pr protocol.PairReq
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&pr); err != nil || pr.Proto != protocol.Version {
		writeJSON(w, protocol.PairAck{Message: "请求无效"})
		return
	}
	// 失败限速：同一来源 IP 在 10 分钟窗口内配对失败超 5 次，暂时拒绝
	//（防 6 位码爆破与配对码焚烧）
	ip := hostOnly(req.RemoteAddr)
	if !r.pairAllowed(ip) {
		writeJSON(w, protocol.PairAck{Message: "尝试过于频繁，请 10 分钟后再试"})
		return
	}
	c := certOf(req)
	if c == nil {
		r.pairFail(ip)
		writeJSON(w, protocol.PairAck{Message: "缺少设备证书"})
		return
	}
	pid := identity.Fingerprint(c)

	r.pairMu.Lock()
	st := r.pairing
	valid := st != nil && !st.used && time.Now().Before(st.expires) &&
		subtle.ConstantTimeCompare([]byte(st.code), []byte(strings.TrimSpace(pr.Code))) == 1
	if valid {
		st.used = true // 一次性：无论最终是否确认，配对码立即失效
	}
	r.pairMu.Unlock()
	if valid {
		// 配对码命中即清空该来源的限速记录。注意：pairFails 由
		// pairFailsMu 保护，绝不能在持有 pairMu 的临界区里二次
		// 加锁 pairMu（Go 互斥锁不可重入，曾在此自死锁）。
		r.pairFailsMu.Lock()
		delete(r.pairFails, hostOnly(req.RemoteAddr))
		r.pairFailsMu.Unlock()
	}
	if !valid {
		r.pairFail(ip)
		if os.Getenv("DROP_DEBUG") != "" {
			r.pairMu.Lock()
			dbg := "nil"
			if r.pairing != nil {
				dbg = fmt.Sprintf("code=%q used=%v expires_in=%v", r.pairing.code, r.pairing.used, time.Until(r.pairing.expires))
			}
			r.pairMu.Unlock()
			fmt.Fprintf(os.Stderr, "[pair] 收到码 %q 当前状态 %s\n", strings.TrimSpace(pr.Code), dbg)
		}
		writeJSON(w, protocol.PairAck{Message: "配对码无效或已过期"})
		return
	}

	if r.opt.MaxDevices != nil && r.trust.Count() >= r.opt.MaxDevices() {
		// 文案不能指向具体界面：两端都有入口（电脑面板设置页 / 手机设置页设备卡），
		// 此前写「面板设置」让手机用户找不到地方。
		writeJSON(w, protocol.PairAck{Message: "已达设备配对上限（可在设置里解除）"})
		return
	}

	name := strings.TrimSpace(pr.Name)
	if name == "" {
		name = c.Subject.CommonName
	}
	if name == "" {
		name = "未命名设备"
	}

	fmt.Fprintf(os.Stderr, "※ 配对请求来自 %s [%s]\n", name, protocol.DisplayID(pid))
	if !r.opt.ConfirmPeer(name, pid) {
		writeJSON(w, protocol.PairAck{Message: "对方拒绝了配对"})
		return
	}
	r.trust.Add(pid, c, name)
	fmt.Fprintf(os.Stderr, "✓ 配对完成：%s [%s] 已加入可信设备\n", name, protocol.DisplayID(pid))
	writeJSON(w, protocol.PairAck{OK: true, Name: r.id.Name})
	r.pairOnce.Do(func() { close(r.paired) })
}

func (r *Receiver) handleOffer(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var off protocol.Offer
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&off); err != nil {
		http.Error(w, "清单解析失败", http.StatusBadRequest)
		return
	}
	if off.Proto != protocol.Version {
		http.Error(w, "协议版本不匹配", http.StatusBadRequest)
		return
	}
	if len(off.Files) == 0 || len(off.Files) > maxManifestFiles {
		http.Error(w, "清单为空或文件数超限", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	// 上限只数仍在传/仍在等的会话：整批完成的会话只是留下 saved 供补投判定，
	// 既不占文件句柄也不占暂存目录，等 janitor 按 TTL 收走即可。
	busy := 0
	for _, s := range r.sessions {
		for _, st := range s.files {
			if !st.done {
				busy++
				break
			}
		}
	}
	if busy >= maxSessions {
		r.mu.Unlock()
		http.Error(w, "接收端正忙：并发会话过多，请稍后重试", http.StatusTooManyRequests)
		return
	}
	r.mu.Unlock()

	// 磁盘空间预检（按清单实际总大小，而非对端自报的 TotalSize）；
	// 同一清单内的重复路径会写同一个 .part 互相损坏，直接拒绝。
	var total int64
	seenPaths := map[string]bool{}
	for _, f := range off.Files {
		if seenPaths[f.Path] {
			http.Error(w, "清单包含重复路径: "+f.Path, http.StatusBadRequest)
			return
		}
		seenPaths[f.Path] = true
		total += f.Size
	}
	if r.opt.FreeDisk != nil {
		if free, err := r.opt.FreeDisk(r.dir()); err == nil && total > int64(free) {
			http.Error(w, "接收端磁盘空间不足", http.StatusInsufficientStorage)
			return
		}
	}

	sid := randHex(16)
	s := &session{senderID: peerID(req), files: make(map[string]*fileState, len(off.Files)), last: time.Now()}
	for i, f := range off.Files {
		if err := safeRelPath(f.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if f.Size < 0 {
			http.Error(w, "非法的文件大小", http.StatusBadRequest)
			return
		}
		f.ID = fmt.Sprintf("f%d", i)
		s.files[f.ID] = &fileState{
			meta: f,
			part: filepath.Join(partDir(r.dir(), sid), filepath.FromSlash(f.Path)+".part"),
		}
	}
	r.mu.Lock()
	r.sessions[sid] = s
	r.mu.Unlock()

	fmt.Fprintf(os.Stderr, "收到 %s 的发送请求：%d 个文件，共 %s，自动接收\n",
		r.peerName(req), len(off.Files), protocol.HumanBytes(off.TotalSize))
	writeJSON(w, protocol.OfferAck{SessionID: sid})
}

func (r *Receiver) handleUpload(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.mu.Lock()
	s := r.sessions[req.URL.Query().Get("s")]
	var st *fileState
	if s != nil {
		st = s.files[req.URL.Query().Get("f")]
	}
	if st != nil {
		switch {
		case st.got || st.file != nil:
			r.mu.Unlock()
			http.Error(w, "该文件已在传输中", http.StatusConflict)
			return
		case req.ContentLength >= 0 && req.ContentLength != st.meta.Size:
			r.mu.Unlock()
			http.Error(w, "文件大小与清单不符", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(st.part), 0o755); err != nil {
			r.mu.Unlock()
			http.Error(w, "无法创建目录", http.StatusInternalServerError)
			return
		}
		f, err := os.Create(st.part)
		if err != nil {
			r.mu.Unlock()
			http.Error(w, "无法写入 .part 文件", http.StatusInternalServerError)
			return
		}
		st.file, st.hash = f, sha256.New()
	}
	r.mu.Unlock()
	if st == nil {
		http.Error(w, "未知会话或文件", http.StatusNotFound)
		return
	}

	discard := func(code int, msg string) {
		// 加锁收尾：/v1/cancel 可能同时进来，两边都要关句柄、都要决定谁写历史
		r.mu.Lock()
		if st.file != nil {
			st.file.Close()
			st.file = nil
		}
		st.hash = nil
		first := !st.recorded
		st.recorded = true
		r.mu.Unlock()
		os.Remove(st.part)
		if first {
			r.record(r.peerName(req), peerID(req), st.meta.Path, "", st.meta.Size, false, msg)
			r.notifyFailed(req, st.meta.Path, false, msg)
		}
		http.Error(w, msg, code)
	}
	src := &progressReader{r: io.LimitReader(req.Body, st.meta.Size+1), total: st.meta.Size, dir: "recv", peer: r.peerName(req), name: filepath.Base(st.meta.Path)}
	stopLog := src.startLog() // 终端进度 + ProgressHook（手机端接收进度通知）
	// 传输心跳：每分钟刷新会话活跃时间，防 janitor 误杀慢速大文件传输
	stopHeartbeat := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-tick.C:
				r.touch(req.URL.Query().Get("s"))
			}
		}
	}()
	n, err := io.Copy(io.MultiWriter(st.file, st.hash), src)
	close(stopHeartbeat)
	stopLog()
	if err != nil {
		// 读 body 中断的原因有两种：对端主动取消，或网络真的断了。两者在
		// 这一层无法区分，所以这里不猜——统一记 failed。主动取消会先走
		// /v1/cancel 把记录改成 canceled（见 handleCancel），那时这里的
		// 追加记录会被取消标记覆盖成中性文案。
		discard(http.StatusBadGateway, "传输中断，已丢弃不完整文件")
		return
	}
	if n != st.meta.Size {
		discard(http.StatusBadRequest, "文件大小与清单不符，已丢弃")
		return
	}
	st.file.Close()
	r.mu.Lock()
	st.file, st.got = nil, true
	s.last = time.Now()
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// partDir 会话的 .part 暂存目录（按会话隔离，避免不同会话的同名清单路径互相截断）。
func partDir(base, sid string) string {
	return filepath.Join(base, ".drop-parts", sid)
}

func (r *Receiver) handleComplete(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cr protocol.CompleteReq
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&cr); err != nil {
		http.Error(w, "请求解析失败", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	s := r.sessions[cr.SessionID]
	var st *fileState
	if s != nil {
		st = s.files[cr.FileID]
	}
	if st == nil {
		r.mu.Unlock()
		http.Error(w, "未知会话或文件", http.StatusNotFound)
		return
	}
	if st.saved != "" {
		// 补投的 complete：上一次已落盘，只是响应丢在半路。回 409 会让发送方
		// 把成功的传输报成失败，用户重发就在对端堆出双份记录。
		saved := st.saved
		r.mu.Unlock()
		writeJSON(w, protocol.CompleteAck{OK: true, SavedAs: filepath.ToSlash(saved)})
		return
	}
	if !st.got || st.file != nil || st.done {
		r.mu.Unlock()
		http.Error(w, "文件尚未传完或已受理", http.StatusConflict)
		return
	}
	st.done = true // 同一文件只允许一个 complete 进入落盘流程
	r.mu.Unlock()
	computed := hex.EncodeToString(st.hash.Sum(nil))
	if !strings.EqualFold(computed, cr.SHA256) {
		os.Remove(st.part)
		r.record(r.peerName(req), peerID(req), st.meta.Path, "", st.meta.Size, false, "SHA-256 校验失败")
		writeJSON(w, protocol.CompleteAck{Message: "SHA-256 校验失败，文件已丢弃"})
		return
	}
	dir := r.dir()
	// 取名、建目录、改名三步整体持锁：两台设备同时发同名文件（各自独立会话）
	// 时两边都判定「这个名字还空着」并选中同一个 final，后一次 rename 会静默
	// 覆盖前一份已校验的成品——同名绝不静默覆盖是这张卡的立身承诺。
	r.mu.Lock()
	final := ResolveConflict(dir, filepath.FromSlash(st.meta.Path))
	// meta.Path 可含子目录（文件夹传输）：落盘前确保目标父目录存在
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, final)), 0o755); err != nil {
		r.mu.Unlock()
		os.Remove(st.part)
		r.record(r.peerName(req), peerID(req), filepath.Base(final), "", st.meta.Size, false, "创建目标目录失败: "+err.Error())
		http.Error(w, "保存文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(st.part, filepath.Join(dir, final)); err != nil {
		r.mu.Unlock()
		os.Remove(st.part)
		r.record(r.peerName(req), peerID(req), filepath.Base(final), "", st.meta.Size, false, "保存文件失败: "+err.Error())
		http.Error(w, "保存文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	st.saved = final
	idle := true
	for _, f := range s.files {
		if !f.done {
			idle = false
			break
		}
	}
	r.mu.Unlock()
	if idle {
		// 暂存目录空了就立刻回收；会话记录留到 janitor 超时——补投 complete
		// 的幂等判定要靠它，此处删除就只能回 404，发送方无从得知文件已落盘。
		os.RemoveAll(partDir(dir, cr.SessionID))
	}
	savedPath := filepath.Join(dir, final)
	r.record(r.peerName(req), peerID(req), filepath.Base(final), final, st.meta.Size, true, "")
	fmt.Fprintf(os.Stderr, "✓ 已接收 %s（保存为 %s）\n", filepath.Base(final), savedPath)
	if r.opt.OnReceived != nil {
		r.opt.OnReceived(r.peerName(req), filepath.Base(final), savedPath)
	}
	writeJSON(w, protocol.CompleteAck{OK: true, SavedAs: filepath.ToSlash(final)})
}

// notifyFailed 接收侧失败/取消的统一出口（宿主据此收场「传输中」条目）。
func (r *Receiver) notifyFailed(req *http.Request, path string, canceled bool, reason string) {
	if r.opt.OnFailed == nil {
		return
	}
	r.opt.OnFailed(r.peerName(req), filepath.Base(path), canceled, reason)
}

// hostOnly 从 ip:port 提取主机部分；无端口时原样返回。
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// touch 会话活跃心跳：长传输期间由上传处理器周期调用，防 janitor 误杀。
func (r *Receiver) touch(sid string) {
	r.touchAt(sid, time.Now())
}

// touchAt touch 的时钟注入版本（测试 sweep 与心跳的交互用），行为一致。
func (r *Receiver) touchAt(sid string, now time.Time) {
	r.mu.Lock()
	if s, ok := r.sessions[sid]; ok {
		s.last = now
	}
	r.mu.Unlock()
}

func (r *Receiver) record(peer, peerID, name, path string, size int64, ok bool, errMsg string) {
	status := history.StatusFailed
	if ok {
		status = history.StatusOK
	}
	r.recordStatus(peer, peerID, name, path, size, status, errMsg)
}

// recordStatus 按显式状态记一条接收历史（ok / failed / canceled）。
func (r *Receiver) recordStatus(peer, peerID, name, path string, size int64, status, errMsg string) {
	e := history.Entry{Dir: "recv", Peer: peer, PeerID: peerID, Name: name, Size: size, Status: status, Path: path}
	if status != history.StatusOK {
		e.Error = errMsg
	}
	history.Append(e)
}

func (r *Receiver) janitor(ctx context.Context) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.sweepSessions(time.Now())
		}
	}
}

// sweepSessions 清理超过 TTL 未活跃的会话：关闭仍挂起的文件句柄、移除会话
// 并异步删除其 .part 暂存目录。now 由调用方注入（janitor 传真实时钟，测试
// 可传任意时刻直接验证，免等 5 分钟 ticker），逻辑与原 janitor 内联清扫一致。
func (r *Receiver) sweepSessions(now time.Time) {
	r.mu.Lock()
	var staleDirs []string
	for sid, s := range r.sessions {
		if now.Sub(s.last) > sessionTTL {
			for _, st := range s.files {
				if st.file != nil {
					st.file.Close()
				}
			}
			delete(r.sessions, sid)
			staleDirs = append(staleDirs, partDir(r.dir(), sid)) // 目录删除放锁外
		}
	}
	r.mu.Unlock()
	for _, d := range staleDirs {
		go os.RemoveAll(d)
	}
}

func certOf(req *http.Request) *x509.Certificate {
	if req.TLS != nil && len(req.TLS.PeerCertificates) > 0 {
		return req.TLS.PeerCertificates[0]
	}
	return nil
}

func peerID(req *http.Request) string {
	if c := certOf(req); c != nil {
		return identity.Fingerprint(c)
	}
	return ""
}

// peerName 对端显示名：信任库备注/自报名优先（跟随对端改名自动同步），
// 证书 CN 兜底——CN 在身份创建时定格，对端改名后不再变化，故仅作最后回退。
func (r *Receiver) peerName(req *http.Request) string {
	if req.TLS != nil && len(req.TLS.PeerCertificates) > 0 {
		cert := req.TLS.PeerCertificates[0]
		if n := r.trust.Name(identity.Fingerprint(cert)); n != "" {
			return n
		}
		if n := cert.Subject.CommonName; n != "" {
			return n
		}
	}
	return "未知设备"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

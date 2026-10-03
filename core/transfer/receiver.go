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
// ExitOnPair 仅用于 drop pair 命令：配对完成后退出；常驻模式必须为 false。
type ReceiveOptions struct {
	Port        int
	Dir         string
	DirFn       func() string
	ConfirmPeer func(name, id string) bool
	OnReceived  func(peer, name, path string)
	OnText      func(peer, text string)
	MaxDevices  func() int
	FreeDisk    func(path string) (uint64, error)
	ExitOnPair  bool
}

type fileState struct {
	meta  protocol.FileMeta
	part  string
	file  *os.File
	hash  hash.Hash
	bytes int64
	got   bool // 上传完成，等待 complete 校验
	done  bool // complete 已受理（防并发重复落盘）
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
	disc, err := discover.New(r.id.ID())
	if err != nil {
		fmt.Fprintln(os.Stderr, "※ 设备发现不可用（发送方仍可用 -addr 直连）:", err)
	} else {
		defer disc.Close()
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
	case protocol.PathOffer, protocol.PathUpload, protocol.PathComplete, protocol.PathText:
	default:
		http.NotFound(w, req)
		return
	}
	// 授权门槛：数据端点只对已配对设备开放（认证已在 TLS 握手完成）。
	if !r.trust.Has(peerID(req)) {
		http.Error(w, "设备未配对：请先在两端执行 drop pair 完成配对", http.StatusUnauthorized)
		return
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
	}
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
	history.Append(history.Entry{Dir: "recv", Peer: peer, Kind: "text", Status: "ok", Text: text})
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
	c := certOf(req)
	if c == nil {
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
	if !valid {
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
		writeJSON(w, protocol.PairAck{Message: "已达设备配对上限（可在面板设置中解锁）"})
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
	if len(r.sessions) >= maxSessions {
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
		st.file.Close()
		os.Remove(st.part)
		r.mu.Lock()
		st.file, st.hash = nil, nil
		r.mu.Unlock()
		r.record(r.peerName(req), st.meta.Path, "", st.meta.Size, false, msg)
		http.Error(w, msg, code)
	}
	n, err := io.Copy(io.MultiWriter(st.file, st.hash), io.LimitReader(req.Body, st.meta.Size+1))
	if err != nil {
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
		r.record(r.peerName(req), st.meta.Path, "", st.meta.Size, false, "SHA-256 校验失败")
		writeJSON(w, protocol.CompleteAck{Message: "SHA-256 校验失败，文件已丢弃"})
		return
	}
	final := ResolveConflict(r.dir(), filepath.FromSlash(st.meta.Path))
	if err := os.Rename(st.part, filepath.Join(r.dir(), final)); err != nil {
		os.Remove(st.part)
		http.Error(w, "保存文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	r.mu.Lock()
	delete(s.files, cr.FileID)
	if len(s.files) == 0 {
		delete(r.sessions, cr.SessionID)
	}
	r.mu.Unlock()
	if len(s.files) == 0 {
		os.RemoveAll(partDir(r.dir(), cr.SessionID)) // 会话结束，清理暂存目录
	}
	savedPath := filepath.Join(r.dir(), final)
		r.record(r.peerName(req), filepath.Base(final), final, st.meta.Size, true, "")
	fmt.Fprintf(os.Stderr, "✓ 已接收 %s（保存为 %s）\n", filepath.Base(final), savedPath)
	if r.opt.OnReceived != nil {
		r.opt.OnReceived(r.peerName(req), filepath.Base(final), savedPath)
	}
	writeJSON(w, protocol.CompleteAck{OK: true, SavedAs: filepath.ToSlash(final)})
}

func (r *Receiver) record(peer, name, path string, size int64, ok bool, errMsg string) {
	e := history.Entry{Dir: "recv", Peer: peer, Name: name, Size: size, Status: "failed", Path: path}
	if ok {
		e.Status = "ok"
	} else {
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
			r.mu.Lock()
			for sid, s := range r.sessions {
				if time.Since(s.last) > sessionTTL {
					for _, st := range s.files {
						if st.file != nil {
							st.file.Close()
						}
					}
					delete(r.sessions, sid)
					go os.RemoveAll(partDir(r.dir(), sid)) // 目录删除放锁外
				}
			}
			r.mu.Unlock()
		}
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

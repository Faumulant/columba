package transfer

// 接收端 HTTP 层测试（httptest + 真实 TLS 直连）：
//
//   - 服务端 TLS 与生产一致（identity.ServerConfig：要求客户端出示设备证书，
//     证书即接收端身份）；客户端 TLS 与生产一致（identity.ClientConfig：按
//     指纹 pin 接收端）。请求按协议顺序直打 /v1/offer → /v1/upload → /v1/complete。
//   - 每个测试独立搭建 Receiver（独立身份、信任库、接收目录、历史文件）：
//     identity.AppDir 优先读 LOCALAPPDATA，测试把它重定向进临时目录，
//     身份/信任库/历史全部隔离，互不污染、不触碰真实用户数据。
//   - janitor 的 5 分钟 ticker 在测试里不可等待：清扫逻辑已抽为
//     sweepSessions(now)（时间可注入），测试直接调用，行为与 ticker 路径一致；
//     心跳同样经 touchAt 注入时刻（钉死 F5：心跳过的会话不被 30 分钟 TTL 误杀）。
//   - 接收历史经 history.Load 读取（真实生产读取路径），用于校验 Path 修复
//     （2f2369a：recv 成功条目的 Path 必须是最终文件路径，而非接收目录本身）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drop/core/history"
	"drop/core/identity"
	"drop/core/protocol"
)

const testPeerName = "测试手机" // 配对时写入接收端信任库的发送方显示名

// must 与 core/mobileapi/mobileapi_test.go 的辅助模式保持一致。
func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ─── 测试装置：一套隔离的接收端 + 一台已持证书的发送方客户端 ───

type recvHarness struct {
	recv      *Receiver
	srv       *httptest.Server
	client    *http.Client // 携带发送方证书（是否已配对取决于 pairSender）
	recvTrust *identity.TrustStore
	sender    *identity.Identity
	base      string // 接收目录的父目录（用于检测一级目录逃逸）
	dir       string // 接收目录
}

// newRecvHarness 搭一套完全隔离的接收端。tune 可改写 ReceiveOptions
// （如挂 OnReceived 回调）。默认不配对：需要时调用 pairSender。
func newRecvHarness(t *testing.T, tune func(*ReceiveOptions)) *recvHarness {
	t.Helper()
	dataDir := t.TempDir()
	base := t.TempDir()
	dir := filepath.Join(base, "recv")
	must(t, os.MkdirAll(dir, 0o755), "创建接收目录")

	// 接收端身份 + 信任库（AppDir = dataDir/Drop）
	t.Setenv("LOCALAPPDATA", dataDir)
	recvID, err := identity.LoadOrCreate()
	must(t, err, "创建接收端身份")
	recvTrust, err := identity.LoadTrust()
	must(t, err, "加载接收端信任库")

	// 发送方独立身份（独立数据目录），随后切回接收端目录，
	// 使接收路径上的 history.Append 落进接收端的数据目录。
	t.Setenv("LOCALAPPDATA", t.TempDir())
	senderID, err := identity.LoadOrCreate()
	must(t, err, "创建发送方身份")
	t.Setenv("LOCALAPPDATA", dataDir)

	opt := ReceiveOptions{Dir: dir}
	if tune != nil {
		tune(&opt)
	}
	recv := NewReceiver(recvID, recvTrust, opt)

	// 服务端 TLS 与生产 Run() 完全一致：要求客户端出示设备证书。
	srv := httptest.NewUnstartedServer(recv)
	srv.Listener = tls.NewListener(srv.Listener, recvID.ServerConfig())
	srv.Start()
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   senderID.ClientConfig(recvID.ID()), // 指纹 pin 接收端
		ForceAttemptHTTP2: false,
	}}
	return &recvHarness{
		recv: recv, srv: srv, client: client, recvTrust: recvTrust,
		sender: senderID, base: base, dir: dir,
	}
}

// pairSender 模拟配对完成：把发送方指纹写进接收端信任库
// （与 handlePair 收尾的 trust.Add 同一路径）。
func (h *recvHarness) pairSender() {
	h.recvTrust.Add(h.sender.ID(), h.sender.Cert(), testPeerName)
}

func (h *recvHarness) url(path string) string {
	return "https://" + h.srv.Listener.Addr().String() + path
}

// ─── 请求原语：返回错误而非 Fatal，可在任意 goroutine 使用 ───

func (h *recvHarness) send(req *http.Request) (int, []byte, error) {
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

func (h *recvHarness) postJSON(path string, in any) (int, []byte, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, h.url(path), bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return h.send(req)
}

func (h *recvHarness) postOffer(files ...protocol.FileMeta) (int, []byte, error) {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return h.postJSON(protocol.PathOffer, protocol.Offer{
		Proto:      protocol.Version,
		SenderID:   h.sender.ID(),
		SenderName: h.sender.Name,
		Files:      files,
		TotalSize:  total,
	})
}

func (h *recvHarness) postUpload(sid, fid string, data []byte) (int, []byte, error) {
	u := h.url(protocol.PathUpload) + "?" + url.Values{"s": {sid}, "f": {fid}}.Encode()
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	return h.send(req)
}

func (h *recvHarness) postComplete(sid, fid, sha string) (int, []byte, error) {
	return h.postJSON(protocol.PathComplete, protocol.CompleteReq{
		SessionID: sid, FileID: fid, SHA256: sha,
	})
}

// ─── 断言型便捷封装（仅在测试主 goroutine 使用） ───

// mustOffer 断言 offer 被接受并返回会话 ID。注意接收端按清单下标
// 重排文件 ID（f0、f1、…），清单自带的 ID 会被覆盖。
func mustOffer(t *testing.T, h *recvHarness, files ...protocol.FileMeta) string {
	t.Helper()
	status, body, err := h.postOffer(files...)
	if err != nil {
		t.Fatalf("offer 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("offer 应被接受: %d: %s", status, body)
	}
	var ack protocol.OfferAck
	if err := json.Unmarshal(body, &ack); err != nil {
		t.Fatalf("OfferAck 解析失败: %v (%s)", err, body)
	}
	if ack.SessionID == "" {
		t.Fatalf("OfferAck 缺少会话 ID: %s", body)
	}
	return ack.SessionID
}

func mustUpload(t *testing.T, h *recvHarness, sid, fid string, data []byte) {
	t.Helper()
	status, body, err := h.postUpload(sid, fid, data)
	if err != nil {
		t.Fatalf("upload 请求失败: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("upload 应成功: %d: %s", status, body)
	}
}

// complete 返回 complete 的原始结果；SHA 校验失败走 HTTP 200 + OK=false
// 的应答契约（发送方据 ack.OK 判定失败），故这里不对状态码下断言。
func (h *recvHarness) complete(t *testing.T, sid, fid, sha string) (protocol.CompleteAck, int) {
	t.Helper()
	status, body, err := h.postComplete(sid, fid, sha)
	if err != nil {
		t.Fatalf("complete 请求失败: %v", err)
	}
	var ack protocol.CompleteAck
	if status == http.StatusOK {
		if err := json.Unmarshal(body, &ack); err != nil {
			t.Fatalf("CompleteAck 解析失败: %v (%s)", err, body)
		}
	}
	return ack, status
}

// ─── 会话/磁盘/历史观察（包内直查，与生产同进程） ───

func (h *recvHarness) sessionExists(sid string) bool {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	_, ok := h.recv.sessions[sid]
	return ok
}

func (h *recvHarness) sessionCount() int {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	return len(h.recv.sessions)
}

func (h *recvHarness) fileStateOf(sid, fid string) *fileState {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	s := h.recv.sessions[sid]
	if s == nil {
		return nil
	}
	return s.files[fid]
}

// ageSession 把会话活跃时间拨到指定时刻（模拟时间流逝、未心跳）。
func (h *recvHarness) ageSession(sid string, last time.Time) {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	if s := h.recv.sessions[sid]; s != nil {
		s.last = last
	}
}

func (h *recvHarness) setPartFile(sid, fid string, f *os.File) {
	h.recv.mu.Lock()
	defer h.recv.mu.Unlock()
	if st := h.recv.sessions[sid].files[fid]; st != nil {
		st.file = f
	}
}

func (h *recvHarness) recvHistory(t *testing.T) []history.Entry {
	t.Helper()
	return history.Load()
}

// lastRecvEntry 返回最近一条 recv 历史记录。
func lastRecvEntry(t *testing.T, h *recvHarness) history.Entry {
	t.Helper()
	es := h.recvHistory(t)
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Dir == "recv" {
			return es[i]
		}
	}
	t.Fatalf("历史中没有任何 recv 记录: %+v", es)
	return history.Entry{}
}

// assertNoStrayFiles 确认接收目录的父目录里除了接收目录本身没有多出任何
// 条目（一级目录逃逸检测；穿越变体在 offer 阶段已被拒，更深层的逃逸
// 无从落盘）。
func (h *recvHarness) assertNoStrayFiles(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(h.base)
	must(t, err, "读取接收目录的父目录")
	for _, e := range ents {
		if e.Name() == filepath.Base(h.dir) && e.IsDir() {
			continue
		}
		t.Fatalf("接收目录之外出现了意外条目: %s", e.Name())
	}
}

// ─── 1. 正常接收链路：offer → upload → complete → 落盘 + 历史 ───

func TestReceiverHappyPath(t *testing.T) {
	var onRecv struct {
		sync.Mutex
		called     bool
		peer, name string
		path       string
	}
	h := newRecvHarness(t, func(o *ReceiveOptions) {
		o.OnReceived = func(peer, name, path string) {
			onRecv.Lock()
			defer onRecv.Unlock()
			onRecv.called, onRecv.peer, onRecv.name, onRecv.path = true, peer, name, path
		}
	})
	h.pairSender()

	content := []byte("Columba 飞鸠 —— 局域网文件投递\n")
	sid := mustOffer(t, h, protocol.FileMeta{Path: "report.txt", Size: int64(len(content))})
	mustUpload(t, h, sid, "f0", content)
	ack, status := h.complete(t, sid, "f0", sha256Hex(content))

	if status != http.StatusOK || !ack.OK {
		t.Fatalf("complete 应成功: status=%d ack=%+v", status, ack)
	}
	if ack.SavedAs != "report.txt" {
		t.Fatalf("SavedAs 不符: got %q", ack.SavedAs)
	}

	// 文件落在接收目录内、内容逐字节一致
	saved := filepath.Join(h.dir, filepath.FromSlash(ack.SavedAs))
	got, err := os.ReadFile(saved)
	must(t, err, "读取落盘文件")
	if !bytes.Equal(got, content) {
		t.Fatalf("落盘内容不符: got %q", got)
	}

	// 会话记录不再随最后一个文件删除（补投 complete 要靠它判定幂等，回收
	// 交给 janitor 超时），但暂存目录必须立刻清干净
	if _, err := os.Stat(partDir(h.dir, sid)); !os.IsNotExist(err) {
		t.Fatalf("会话结束后 .part 暂存目录应被清理: %v", err)
	}

	// 历史：恰好一条 recv 记录，状态 ok，Path == 最终文件完整路径
	// （钉死 2f2369a：不得再写成接收目录本身，也不得丢成空串）
	es := h.recvHistory(t)
	if len(es) != 1 {
		t.Fatalf("历史应恰好 1 条记录, got %d:\n%+v", len(es), es)
	}
	e := es[0]
	if e.Status != "ok" {
		t.Fatalf("历史状态应为 ok: %+v", e)
	}
	wantPath := filepath.FromSlash(ack.SavedAs)
	if e.Path != wantPath {
		t.Fatalf("历史 Path 应为最终文件路径 %q（不得是接收目录本身 %q 或空串）, got %q",
			wantPath, h.dir, e.Path)
	}
	if e.Name != "report.txt" {
		t.Fatalf("历史 Name 应为最终文件名, got %q", e.Name)
	}
	if e.Size != int64(len(content)) || e.Peer != testPeerName || e.Error != "" {
		t.Fatalf("历史 Size/Peer/Error 不符: %+v", e)
	}

	// OnReceived 通知回调拿到的是落盘的绝对路径
	onRecv.Lock()
	defer onRecv.Unlock()
	if !onRecv.called {
		t.Fatal("OnReceived 应被回调")
	}
	if onRecv.path != saved || onRecv.name != "report.txt" || onRecv.peer != testPeerName {
		t.Fatalf("OnReceived 参数不符: got (%q, %q, %q), want (%q, report.txt, %q)",
			onRecv.peer, onRecv.name, onRecv.path, testPeerName, saved)
	}
}

// TestReceiverNestedDestinationDirectory 覆盖清单路径带子目录的场景
// （文件夹发送：sender 的 manifest 会产出 "根目录/子目录/文件" 形态的 Path）。
// 钉死的是落盘前必须为最终路径建父目录：handleUpload 建的是 .part 暂存目录
// 内的父目录，不是接收目录内的，缺这一步 Windows 上 rename 直接失败。
func TestReceiverNestedDestinationDirectory(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	content := []byte("带子目录的文件内容")
	sid := mustOffer(t, h, protocol.FileMeta{Path: "docs/report.txt", Size: int64(len(content))})
	mustUpload(t, h, sid, "f0", content)
	status, body, err := h.postComplete(sid, "f0", sha256Hex(content))
	must(t, err, "complete 请求")

	if status != http.StatusOK {
		t.Fatalf("带子目录的接收在 complete 阶段被拒: status=%d body=%s", status, body)
	}

	var ack protocol.CompleteAck
	must(t, json.Unmarshal(body, &ack), "解析 CompleteAck")
	if !ack.OK || ack.SavedAs != "docs/report.txt" {
		t.Fatalf("带子目录的接收应成功: ack=%+v", ack)
	}
	saved := filepath.Join(h.dir, "docs", "report.txt")
	got, err := os.ReadFile(saved)
	must(t, err, "读取落盘文件")
	if !bytes.Equal(got, content) {
		t.Fatalf("落盘内容不符: got %q", got)
	}
	e := lastRecvEntry(t, h)
	if e.Status != "ok" || e.Path != filepath.FromSlash("docs/report.txt") || e.Name != "report.txt" {
		t.Fatalf("历史记录不符（Path 应含子目录的最终文件路径）: %+v", e)
	}
	if _, err := os.Stat(partDir(h.dir, sid)); !os.IsNotExist(err) {
		t.Fatalf("最后一个文件落盘后 .part 暂存目录应被清理: %v", err)
	}
}

// ─── 2. 恶意路径穿越：清单路径逃不出接收目录（安全测试） ───

func TestReceiverRejectsPathTraversal(t *testing.T) {
	// 反斜杠变体必须覆盖：Windows 上 filepath 同时认正/反斜杠，
	// `..\..\evil.txt` 与正斜杠版本是同等的穿越攻击。
	variants := []string{
		"../../evil.txt",          // 正斜杠穿越
		`..\..\evil.txt`,          // 反斜杠穿越（Windows 语义）
		`..\..\..\..\evil.txt`,    // 深层反斜杠穿越
		`C:\Windows\evil.txt`,     // Windows 盘符绝对路径
		`C:/Windows/evil.txt`,     // 盘符 + 正斜杠
		`\\server\share\evil.txt`, // Windows UNC 路径
		"/etc/passwd",             // POSIX 绝对路径
		"a/../../evil.txt",        // 前缀合法、中段穿越
	}
	for _, p := range variants {
		t.Run(p, func(t *testing.T) {
			h := newRecvHarness(t, nil)
			h.pairSender()
			content := []byte("evil")

			status, body, err := h.postOffer(protocol.FileMeta{Path: p, Size: int64(len(content))})
			if err != nil {
				t.Fatalf("offer 请求失败: %v", err)
			}

			if status == http.StatusBadRequest {
				// offer 阶段拒绝：不得创建会话，不得落任何盘，不得写历史
				if h.sessionCount() != 0 {
					t.Fatalf("被拒的 offer 不应创建会话（%d 个在册）", h.sessionCount())
				}
				ents, err := os.ReadDir(h.dir)
				must(t, err, "读取接收目录")
				if len(ents) != 0 {
					t.Fatalf("被拒的 offer 不应产生任何落盘: %v", ents)
				}
				if es := h.recvHistory(t); len(es) != 0 {
					t.Fatalf("被拒的 offer 不应写历史: %+v", es)
				}
				if !strings.Contains(string(body), "非法的文件路径") {
					t.Fatalf("拒绝原因应说明路径非法: %s", body)
				}
				return
			}

			// 未被拒绝的变体只可能出现在非 Windows 平台（反斜杠是合法文件名
			// 字符）：必须完整走完流程且落盘仍在接收目录内——同样是安全契约。
			if runtime.GOOS == "windows" {
				t.Fatalf("Windows 语义下穿越路径 %q 必须在 offer 阶段拒绝（400）, got %d: %s",
					p, status, body)
			}
			if status != http.StatusOK {
				t.Fatalf("非 Windows 平台反斜杠文件名应被接受: %d: %s", status, body)
			}
			var ack protocol.OfferAck
			must(t, json.Unmarshal(body, &ack), "解析 OfferAck")
			mustUpload(t, h, ack.SessionID, "f0", content)
			cack, cstatus := h.complete(t, ack.SessionID, "f0", sha256Hex(content))
			if cstatus != http.StatusOK || !cack.OK {
				t.Fatalf("complete 应成功: status=%d ack=%+v", cstatus, cack)
			}
			h.assertNoStrayFiles(t) // 一级逃逸检测（..\-named 文件必须还在接收目录里）
			var files int
			must(t, filepath.WalkDir(h.dir, func(_ string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					files++
				}
				return nil
			}), "遍历接收目录")
			if files != 1 {
				t.Fatalf("接收目录内应恰好落盘 1 个文件, got %d", files)
			}
		})
	}
}

// ─── 3. 0 字节文件：行为钉死 ───
//
// 以当前实现为准：0 字节文件被完整接受——空 .part、SHA-256(e3b0c4…)
// 校验通过、正常落盘入历史。若未来改为显式拒绝，应同步更新本测试与协议文档。

func TestReceiverZeroByteFile(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	var content []byte // 0 字节
	sid := mustOffer(t, h, protocol.FileMeta{Path: "empty.txt", Size: 0})
	mustUpload(t, h, sid, "f0", content) // 空 body，ContentLength=0 与清单一致
	ack, status := h.complete(t, sid, "f0", sha256Hex(content))

	if status != http.StatusOK || !ack.OK {
		t.Fatalf("0 字节文件应被完整接受: status=%d ack=%+v", status, ack)
	}
	got, err := os.ReadFile(filepath.Join(h.dir, "empty.txt"))
	must(t, err, "读取落盘文件")
	if len(got) != 0 {
		t.Fatalf("落盘文件应为 0 字节, got %d 字节", len(got))
	}
	if _, err := os.Stat(partDir(h.dir, sid)); !os.IsNotExist(err) {
		t.Fatalf("0 字节文件落盘后 .part 暂存目录应被清理: %v", err)
	}
	es := h.recvHistory(t)
	if len(es) != 1 {
		t.Fatalf("历史应恰好 1 条记录, got %d", len(es))
	}
	e := es[0]
	if e.Status != "ok" || e.Size != 0 || e.Path != "empty.txt" || e.Name != "empty.txt" {
		t.Fatalf("0 字节文件的历史记录不符: %+v", e)
	}
}

// ─── 4. SHA-256 不匹配：拒绝、清 .part、不落盘、历史记失败 ───

func TestReceiverSHA256Mismatch(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	content := []byte("0123456789")
	sid := mustOffer(t, h, protocol.FileMeta{Path: "data.bin", Size: int64(len(content))})
	mustUpload(t, h, sid, "f0", content)

	badSHA := sha256Hex([]byte("完全不同的内容"))
	ack, status := h.complete(t, sid, "f0", badSHA)

	// 应答契约：HTTP 200 + OK=false + 原因（发送方据 ack.OK 判定失败并报错）
	if status != http.StatusOK {
		t.Fatalf("SHA 不匹配应以 HTTP 200 + OK=false 表达, got %d", status)
	}
	if ack.OK {
		t.Fatal("SHA 不匹配时 ack.OK 应为 false")
	}
	if !strings.Contains(ack.Message, "SHA-256") {
		t.Fatalf("ack.Message 应说明 SHA-256 校验失败: %q", ack.Message)
	}

	// .part 被清理、接收目录不落盘
	st := h.fileStateOf(sid, "f0")
	if st == nil {
		t.Fatal("校验失败后会话应保留（等待发送方决定重试或由 janitor 超时回收）")
	}
	if _, err := os.Stat(st.part); !os.IsNotExist(err) {
		t.Fatalf(".part 文件应被清理: %v", err)
	}
	ents, err := os.ReadDir(h.dir)
	must(t, err, "读取接收目录")
	for _, e := range ents {
		if e.Name() != ".drop-parts" {
			t.Fatalf("校验失败不应在接收目录落盘: 发现 %q", e.Name())
		}
	}

	// 历史记失败：Status=failed、Error 说明原因、Path 为空
	// （修复后失败记录不再携带任何路径，更不得写成接收目录本身）
	e := lastRecvEntry(t, h)
	if e.Status != "failed" {
		t.Fatalf("历史状态应为 failed: %+v", e)
	}
	if !strings.Contains(e.Error, "SHA-256") {
		t.Fatalf("历史 Error 应说明 SHA-256 校验失败: %q", e.Error)
	}
	if e.Path != "" {
		t.Fatalf("失败记录的 Path 应为空, got %q", e.Path)
	}

	// done 防重放：同一文件不能再次 complete（也不允许补传正确哈希翻案）
	_, status = h.complete(t, sid, "f0", sha256Hex(content))
	if status != http.StatusConflict {
		t.Fatalf("重放 complete 应 409, got %d", status)
	}
}

// ─── 5. 并发同名文件：不同会话同时 complete 同名文件，冲突改名、双双完整落盘 ───
//
// 取名（ResolveConflict）与改名（os.Rename）之间不持锁时，两台设备同时发
// 同名文件会各自判定「这个名字还空着」并选中同一个 final，后落盘的一份静默
// 覆盖前一份已校验的成品。旧用例串行执行 complete，恰好绕开了这个窗口——
// bug 因此潜伏至今，所以这里必须真并发地下发两个 complete。
func TestReceiverConcurrentSameName(t *testing.T) {
	const rounds = 8 // 单轮撞进窗口的概率有限，多轮把「修复前必失败」压成稳定复现
	h := newRecvHarness(t, nil)
	h.pairSender()

	for round := 0; round < rounds; round++ {
		base := fmt.Sprintf("photo%d", round)
		name := base + ".jpg"
		// 同长度不同内容：两份 SHA 都合法，覆盖后字节数不变，只有内容能暴露
		contentA := bytes.Repeat([]byte{byte('A' + round), 1, 2, 3}, 32<<10)
		contentB := bytes.Repeat([]byte{byte('B' + round), 1, 2, 3}, 32<<10)
		sidA := mustOffer(t, h, protocol.FileMeta{Path: name, Size: int64(len(contentA))})
		sidB := mustOffer(t, h, protocol.FileMeta{Path: name, Size: int64(len(contentB))})
		mustUpload(t, h, sidA, "f0", contentA)
		mustUpload(t, h, sidB, "f0", contentB) // .part 目录按会话隔离，互不干扰

		var wg sync.WaitGroup
		acks := make([]protocol.CompleteAck, 2)
		codes := make([]int, 2)
		start := make(chan struct{})
		for i, spec := range []struct {
			sid  string
			data []byte
		}{{sidA, contentA}, {sidB, contentB}} {
			wg.Add(1)
			go func(i int, sid string, data []byte) {
				defer wg.Done()
				<-start
				status, body, err := h.postComplete(sid, "f0", sha256Hex(data))
				codes[i] = status
				if err != nil {
					t.Errorf("第 %d 轮并发 complete 请求失败: %v", round, err)
					return
				}
				json.Unmarshal(body, &acks[i])
			}(i, spec.sid, spec.data)
		}
		close(start)
		wg.Wait()

		if codes[0] != http.StatusOK || !acks[0].OK || codes[1] != http.StatusOK || !acks[1].OK {
			t.Fatalf("第 %d 轮：并发 complete 都应成功 A(%d %+v) B(%d %+v)",
				round, codes[0], acks[0], codes[1], acks[1])
		}
		if acks[0].SavedAs == acks[1].SavedAs {
			t.Fatalf("第 %d 轮：并发同名 complete 选中了同一个落盘名 %q（互相覆盖）", round, acks[0].SavedAs)
		}
		// 落盘按完成先后错开命名：先到者保留原名，后到者改名 (1)
		gotNames := map[string]bool{acks[0].SavedAs: true, acks[1].SavedAs: true}
		if !gotNames[name] || !gotNames[base+" (1).jpg"] {
			t.Fatalf("第 %d 轮：应一份原名一份 (1) 后缀, got %q / %q", round, acks[0].SavedAs, acks[1].SavedAs)
		}

		// 两个文件都完整保存、互不覆盖
		for _, want := range [][]byte{contentA, contentB} {
			kept := false
			for _, ack := range acks {
				got, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(ack.SavedAs)))
				must(t, err, "读取 "+ack.SavedAs)
				if bytes.Equal(got, want) {
					kept = true
					break
				}
			}
			if !kept {
				t.Fatalf("第 %d 轮：有一份内容被同名覆盖丢失", round)
			}
		}
	}

	// 历史：每轮两条 ok 记录，Path 各自指向最终落盘文件
	es := h.recvHistory(t)
	if len(es) != 2*rounds {
		t.Fatalf("历史应 %d 条记录, got %d", 2*rounds, len(es))
	}
	for _, e := range es {
		if e.Dir != "recv" || e.Status != "ok" {
			t.Fatalf("历史应为 recv/ok: %+v", e)
		}
		if _, err := os.Stat(filepath.Join(h.dir, e.Path)); err != nil {
			t.Fatalf("历史 Path 应指向真实存在的落盘文件 %q: %v", e.Path, err)
		}
	}
}

// ─── 6a. janitor 过期清理：超时未心跳的会话连同 .part 一起被清扫 ───

func TestJanitorSweepExpiresIdleSessions(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	// 两个文件的清单只传完第一个：会话滞留，.part 已在磁盘上
	sid := mustOffer(t, h,
		protocol.FileMeta{Path: "slow1.bin", Size: 4},
		protocol.FileMeta{Path: "slow2.bin", Size: 4},
	)
	mustUpload(t, h, sid, "f0", []byte("1234"))
	if _, err := os.Stat(partDir(h.dir, sid)); err != nil {
		t.Fatalf("前置条件：.part 暂存目录应存在: %v", err)
	}

	// 把活跃时间拨回 2×TTL 之前（模拟闲置超时、无心跳）。
	// 真实 janitor 靠 5 分钟 ticker + 30 分钟 TTL，测试用注入时刻的
	// sweepSessions 直接验证同一份清扫逻辑。
	now := time.Now()
	h.ageSession(sid, now.Add(-2*sessionTTL))
	h.recv.sweepSessions(now)

	if h.sessionExists(sid) {
		t.Fatal("超时未心跳的会话应被 sweep 清理")
	}
	// 暂存目录删除在锁外异步执行：轮询等待
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(partDir(h.dir, sid)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("超时会话的 .part 暂存目录未被清理")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// sweep 还负责关闭传输中挂起的文件句柄（资源卫生，防句柄泄漏）
	sid2 := mustOffer(t, h, protocol.FileMeta{Path: "hanging.bin", Size: 8})
	st := h.fileStateOf(sid2, "f0")
	if st == nil {
		t.Fatal("前置条件：文件状态应存在")
	}
	must(t, os.MkdirAll(filepath.Dir(st.part), 0o755), "创建 .part 目录")
	f, err := os.Create(st.part)
	must(t, err, "打开模拟传输中的文件句柄")
	h.setPartFile(sid2, "f0", f)
	h.ageSession(sid2, now.Add(-2*sessionTTL))
	h.recv.sweepSessions(now.Add(time.Second))
	if h.sessionExists(sid2) {
		t.Fatal("超时会话（含挂起句柄）应被清理")
	}
	// 关闭探测用二次 Close（跨平台稳定：已关闭的文件再 Close 必报
	// os.ErrClosed；若 sweep 漏关，这里 Close 成功返回 nil 即失败）
	if err := f.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("sweep 应关闭挂起的文件句柄（二次 Close 应报已关闭）: %v", err)
	}
}

// ─── 6b. 心跳过的会话存活：钉死 F5（janitor 不得误杀慢速大文件） ───

func TestJanitorHeartbeatKeepsSessionAlive(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	// 两个文件的清单只传完第一个：会话滞留
	sid := mustOffer(t, h,
		protocol.FileMeta{Path: "big1.bin", Size: 4},
		protocol.FileMeta{Path: "big2.bin", Size: 4},
	)
	mustUpload(t, h, sid, "f0", []byte("abcd"))

	// 模拟慢速大文件：offer/upload 发生在 40 分钟前（远超 30 分钟 TTL），
	// 但上传期间心跳每分钟刷新活跃时间，最近一次心跳在“当前时刻”1 分钟前。
	// 修复前的行为：janitor 只看 offer 时刻的 last → 会话被误杀、.part 被删。
	now := time.Now()
	h.ageSession(sid, now.Add(-40*time.Minute)) // 拨回 offer/上传时刻（无心跳基线）
	h.recv.touchAt(sid, now.Add(-time.Minute))  // F5 心跳刷新（touch 的注入时刻版）
	h.recv.sweepSessions(now)

	if !h.sessionExists(sid) {
		t.Fatal("心跳过的会话必须存活：janitor 不得误杀慢速大文件传输（F5 回归）")
	}
	st := h.fileStateOf(sid, "f0")
	if st == nil {
		t.Fatal("存活会话的文件状态应保留")
	}
	if _, err := os.Stat(st.part); err != nil {
		t.Fatalf("存活会话的 .part 不应被清理: %v", err)
	}

	// 对照组：同一接收端上另一个从未心跳且超时的会话仍被正常清扫
	sid2 := mustOffer(t, h, protocol.FileMeta{Path: "idle.bin", Size: 1})
	h.ageSession(sid2, now.Add(-2*sessionTTL))
	h.recv.sweepSessions(now.Add(time.Second))
	if h.sessionExists(sid2) {
		t.Fatal("超时未心跳的会话仍应被清扫")
	}
}

// ─── 7. 未配对设备：数据端点一律 401，且不留任何痕迹 ───

func TestReceiverRejectsUnpairedDevice(t *testing.T) {
	h := newRecvHarness(t, nil) // 不调用 pairSender：发送方指纹不在信任库
	content := []byte("x")

	// offer → 401
	status, body, err := h.postOffer(protocol.FileMeta{Path: "unpaired.txt", Size: int64(len(content))})
	if err != nil {
		t.Fatalf("offer 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备的 offer 应 401, got %d: %s", status, body)
	}

	// upload / complete / text 同样 401
	status, body, err = h.postUpload("sid-nope", "f0", content)
	if err != nil {
		t.Fatalf("upload 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备的 upload 应 401, got %d: %s", status, body)
	}
	status, body, err = h.postComplete("sid-nope", "f0", sha256Hex(content))
	if err != nil {
		t.Fatalf("complete 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备的 complete 应 401, got %d: %s", status, body)
	}
	status, body, err = h.postJSON(protocol.PathText, protocol.TextMsg{Proto: protocol.Version, Text: "hi"})
	if err != nil {
		t.Fatalf("text 请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("未配对设备的 text 应 401, got %d: %s", status, body)
	}

	// 不产生任何会话、文件与历史
	if h.sessionCount() != 0 {
		t.Fatalf("未配对请求不应创建会话（%d 个在册）", h.sessionCount())
	}
	ents, err := os.ReadDir(h.dir)
	must(t, err, "读取接收目录")
	if len(ents) != 0 {
		t.Fatalf("未配对请求不应产生任何落盘: %v", ents)
	}
	if es := h.recvHistory(t); len(es) != 0 {
		t.Fatalf("未配对请求不应写历史: %+v", es)
	}

	// 完全无证书的客户端：在生产 TLS 配置（RequireAnyClientCert）下
	// 握手层直接被拒，连 HTTP 请求都进不来
	bare := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13},
	}}
	req, err := http.NewRequest(http.MethodPost, h.url(protocol.PathOffer), strings.NewReader("{}"))
	must(t, err, "构造无证书请求")
	if _, err := bare.Do(req); err == nil {
		t.Fatal("无证书客户端应在 TLS 握手层被拒绝")
	}
}

// ─── 8. 第 5 项修复的回归钉：complete 响应丢包时只补 complete，绝不重投 upload
//
// 一次传输两步（upload 送内容 → complete 落盘）。旧行为：complete 遇瞬时网络错
// 就整传重试，重投 upload 命中对端 409「已在传输中」（不可重试），于是已经落盘
// 的文件被报成失败并写失败历史，用户手动重发＝对端双份记录。
func TestSenderRetriesOnlyCompleteWhenResponseLost(t *testing.T) {
	h := newRecvHarness(t, nil)
	h.pairSender()

	content := []byte("complete 的响应丢在半路，不该把已落盘的文件说成失败\n")
	src := filepath.Join(t.TempDir(), "resume.txt")
	must(t, os.WriteFile(src, content, 0o644), "准备待发送文件")

	var uploads, completes atomic.Int64
	dropped := false
	// 服务端 TLS 与生产（及本文件其余用例）一致：同一张接收端证书。第一次
	// complete 让服务端照常落盘，再把响应掐掉——真实丢包的等价物。
	proxy := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case protocol.PathUpload:
			uploads.Add(1)
		case protocol.PathComplete:
			completes.Add(1)
			if dropped {
				break
			}
			dropped = true
			h.recv.ServeHTTP(httptest.NewRecorder(), req)
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
			return
		}
		h.recv.ServeHTTP(w, req)
	})
	srv := httptest.NewUnstartedServer(proxy)
	srv.Listener = tls.NewListener(srv.Listener, h.recv.id.ServerConfig())
	srv.Start()
	t.Cleanup(srv.Close)

	_, err := Send(context.Background(), h.sender, Target{
		Addr: srv.Listener.Addr().String(),
		ID:   identity.Fingerprint(h.recv.id.Cert()),
		Name: "测试接收端",
	}, []string{src})
	if err != nil {
		t.Fatalf("内容已送达、只是 complete 丢响应时 Send 应最终成功: %v", err)
	}
	if n := uploads.Load(); n != 1 {
		t.Fatalf("内容已送达后不得重投 upload（对端只会回 409）: got %d 次", n)
	}
	if n := completes.Load(); n != 2 {
		t.Fatalf("应恰好补投一次 complete: got %d 次", n)
	}

	got, err := os.ReadFile(filepath.Join(h.dir, "resume.txt"))
	must(t, err, "读取落盘文件")
	if !bytes.Equal(got, content) {
		t.Fatalf("落盘内容不符: %q", got)
	}
	// 补投的 complete 走幂等回执：既不重复落盘也不重复写历史
	var recv, send, failed int
	for _, e := range history.Load() {
		if e.Status != "ok" {
			failed++
			continue
		}
		switch e.Dir {
		case "recv":
			recv++
		case "send":
			send++
		}
	}
	if failed != 0 || recv != 1 || send != 1 {
		t.Fatalf("一次成功传输应只有 recv/send 各一条 ok 记录: recv=%d send=%d failed=%d", recv, send, failed)
	}
}

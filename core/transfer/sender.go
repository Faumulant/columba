package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"drop/core/discover"
	"drop/core/history"
	"drop/core/identity"
	"drop/core/protocol"
)

const defaultPort = 42707

// Target 发送目标。ID 用于 pin 住对端证书指纹，可空（调试直连）。
type Target struct {
	Addr string
	ID   string
	Name string
}

// item 清单项：线上元数据 + 本地源路径。
type item struct {
	meta  protocol.FileMeta
	local string
}

// FindCandidates 发现当前局域网内所有 Drop 设备（组播优先，
// 收不到时自动回退子网探测；不含本机）。
func FindCandidates(id *identity.Identity, wait time.Duration) ([]discover.PeerInfo, error) {
	disc, err := discover.New(id.ID())
	if err != nil {
		return nil, fmt.Errorf("设备发现不可用: %w（可用 -addr 直连）", err)
	}
	defer disc.Close()
	tr := discover.NewTracker()
	deadline := time.After(wait)
loop:
	for {
		select {
		case e, ok := <-disc.Events():
			if !ok {
				break loop
			}
			tr.Feed(e)
		case <-deadline:
			break loop
		}
	}
	all := tr.Snapshot()
	if DebugLog != nil {
		DebugLog("FindCandidates: 组播快照=%d", len(all))
	}
	if len(all) == 0 {
		// 组播一无所获（路由器/AP 不转发无线组播是常态）→ 子网探测兜底
		if found, perr := probeSubnet(id, defaultPort); perr == nil && len(found) > 0 {
			if DebugLog != nil {
				DebugLog("FindCandidates: 探测到 %d 台", len(found))
			}
			all = peersFromTargets(found)
		} else if DebugLog != nil {
			DebugLog("FindCandidates: 探测为空 err=%v", perr)
		}
	}
	return all, nil
}

// FindPeer 发现目标设备。trust 非空时只在已配对设备中选择
// （配对发起方传 nil，因为对方尚未入信任库）。match 为空且恰好
// 只有一台候选设备时直接选中。
func FindPeer(id *identity.Identity, match string, wait time.Duration, trust *identity.TrustStore) (Target, error) {
	all, err := FindCandidates(id, wait)
	if err != nil {
		return Target{}, err
	}
	peers := all[:0:0]
	for _, p := range all {
		if trust == nil || trust.Has(p.Announce.ID) {
			peers = append(peers, p)
		}
	}
	if DebugLog != nil {
		DebugLog("FindPeer: 发现=%d 信任内=%d trust=%v", len(all), len(peers), trust != nil)
	}
	if match != "" {
		if t := matchPeer(peers, match, trust); t == nil {
			if DebugLog != nil {
				ids := make([]string, len(peers))
				for i, p := range peers {
					ids[i] = p.Announce.Name + "/" + protocol.DisplayID(p.Announce.ID)
				}
				DebugLog("FindPeer: 匹配失败 match=%q 候选=[%s]", match, strings.Join(ids, ", "))
			}
			return Target{}, fmt.Errorf("未找到设备 %q", match)
		} else {
			UpdateIPCache(t.Announce.ID, net.JoinHostPort(t.IP, strconv.Itoa(t.Announce.Port)))
			return targetOf(*t), nil
		}
	}
	switch len(peers) {
	case 0:
		if len(all) > 0 {
			var sb strings.Builder
			sb.WriteString("发现的设备尚未配对，先执行 drop pair 完成配对：\n")
			for _, p := range all {
				fmt.Fprintf(&sb, "  %-16s %s\n", p.Announce.Name, protocol.DisplayID(p.Announce.ID))
			}
			return Target{}, errors.New(strings.TrimRight(sb.String(), "\n"))
		}
		return Target{}, errors.New("未发现设备：确认对方已运行 drop receive，或用 -addr 直连")
	case 1:
		UpdateIPCache(peers[0].Announce.ID, net.JoinHostPort(peers[0].IP, strconv.Itoa(peers[0].Announce.Port)))
		return targetOf(peers[0]), nil
	default:
		var sb strings.Builder
		sb.WriteString("发现多台设备，请用 -to 指定：\n")
		for _, p := range peers {
			fmt.Fprintf(&sb, "  %-16s %s\n", p.Announce.Name, protocol.DisplayID(p.Announce.ID))
		}
		return Target{}, errors.New(strings.TrimRight(sb.String(), "\n"))
	}
}

func peersFromTargets(ts []Target) []discover.PeerInfo {
	out := make([]discover.PeerInfo, 0, len(ts))
	for _, t := range ts {
		i := strings.LastIndex(t.Addr, ":")
		port, _ := strconv.Atoi(t.Addr[i+1:])
		out = append(out, discover.PeerInfo{
			Announce: protocol.Announce{Proto: protocol.Version, ID: t.ID, Name: t.Name, Port: port},
			IP:       t.Addr[:i],
			Seen:     time.Now(),
		})
	}
	return out
}

func matchPeer(peers []discover.PeerInfo, match string, trust *identity.TrustStore) *discover.PeerInfo {
	for i := range peers {
		p := peers[i]
		if strings.EqualFold(p.Announce.Name, match) || protocol.NormalizeID(p.Announce.ID) == protocol.NormalizeID(match) {
			return &p
		}
	}
	// 再按本机备注名匹配
	if trust != nil {
		for i := range peers {
			p := peers[i]
			if trust.Name(p.Announce.ID) == match {
				return &p
			}
		}
	}
	return nil
}

func targetOf(p discover.PeerInfo) Target {
	return Target{
		Addr: net.JoinHostPort(p.IP, strconv.Itoa(p.Announce.Port)),
		ID:   p.Announce.ID,
		Name: p.Announce.Name,
	}
}

// Pair 发起配对：携带对方生成的一次性配对码连接其配对端点，
// 对方人工确认后双方互写信任库。返回对方设备名。
func Pair(ctx context.Context, id *identity.Identity, trust *identity.TrustStore, t Target, code string) (string, error) {
	// 对方需要人工确认，故不设整体超时；连接超时由 Dialer 控制。
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   id.ClientConfig(t.ID),
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: false,
		},
	}
	body, err := json.Marshal(protocol.PairReq{Proto: protocol.Version, Code: code, Name: id.Name})
	if err != nil {
		return "", err
	}
	u := (&url.URL{Scheme: "https", Host: t.Addr, Path: protocol.PathPair}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接对方: %w", err)
	}
	defer resp.Body.Close()
	var ack protocol.PairAck
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ack); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK || !ack.OK {
		msg := ack.Message
		if msg == "" {
			msg = resp.Status
		}
		return "", errors.New(msg)
	}
	if len(resp.TLS.PeerCertificates) > 0 {
		c := resp.TLS.PeerCertificates[0]
		trust.Add(identity.Fingerprint(c), c, ack.Name)
	}
	return ack.Name, nil
}

// SendText 把一段文本发给目标设备（协议的文本内容类型，接收端不落盘）。
func SendText(ctx context.Context, id *identity.Identity, t Target, text string) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   id.ClientConfig(t.ID),
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: false,
		},
	}
	body, err := json.Marshal(protocol.TextMsg{Proto: protocol.Version, Text: text})
	if err != nil {
		return err
	}
	u := (&url.URL{Scheme: "https", Host: t.Addr, Path: protocol.PathText}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接对方: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(errBody(resp))
	}
	history.Append(history.Entry{Dir: "send", Peer: t.Name, Kind: "text", Text: text, Status: "ok"})
	return nil
}

// SendOptions 发送选项。SkipSourcePath 为 true 时历史不记录来源路径
// （面板拖拽发送的文件来自临时目录，记录下来也打不开）。
type SendOptions struct{ SkipSourcePath bool }

// Send 把 paths 中的文件/文件夹发给目标设备，返回内容的显示名
// （单文件为文件名，多文件为"N 个文件"）。
func Send(ctx context.Context, id *identity.Identity, t Target, paths []string, opts ...SendOptions) (string, error) {
	skipPath := len(opts) > 0 && opts[0].SkipSourcePath
	items, total, err := manifest(paths)
	if err != nil {
		return "", err
	}
	srcPath := ""
	if !skipPath {
		srcPath = items[0].local
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   id.ClientConfig(t.ID),
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: false,
		},
	}
	off := protocol.Offer{
		Proto:      protocol.Version,
		SenderID:   id.ID(),
		SenderName: id.Name,
		Files:      metas(items),
		TotalSize:  total,
	}
	var ack protocol.OfferAck
	if err := postJSON(ctx, client, t.Addr, protocol.PathOffer, off, &ack); err != nil {
		history.Append(history.Entry{Dir: "send", Peer: t.Name, Name: displayName(items), Size: total, Status: "failed", Path: srcPath, Error: err.Error()})
		return displayName(items), fmt.Errorf("无法发起发送: %w", err)
	}
	if ack.Message != "" || ack.SessionID == "" {
		err := fmt.Errorf("对方拒绝了发送: %s", ack.Message)
		history.Append(history.Entry{Dir: "send", Peer: t.Name, Name: displayName(items), Size: total, Status: "failed", Path: srcPath, Error: err.Error()})
		return displayName(items), err
	}
	fmt.Fprintf(os.Stderr, "开始传输 %d 个文件，共 %s\n", len(items), protocol.HumanBytes(total))
	var firstErr error
	for _, it := range items {
		uploadedSHA, err := uploadOne(ctx, client, t.Addr, ack.SessionID, it, t.Name)
		if err != nil && IsRetryable(err) && ctx.Err() == nil {
			time.Sleep(600 * time.Millisecond) // 网络抖动：同一文件重试一次
			if uploadedSHA != "" {
				// 内容已收、只是 complete 这一步没走通：补 complete，绝不重投
				// upload——重投命中对端 409「已在传输中」（不可重试），会把已经
				// 落盘的文件报成失败，用户手动重发＝对端双份记录。
				err = completeOne(ctx, client, t.Addr, ack.SessionID, it, uploadedSHA)
			} else {
				_, err = uploadOne(ctx, client, t.Addr, ack.SessionID, it, t.Name)
			}
		}
		if err != nil {
			firstErr = err
			break // 连接已不可靠，后续文件不再尝试
		}
	}
	name := displayName(items)
	if firstErr != nil {
		history.Append(history.Entry{Dir: "send", Peer: t.Name, Name: name, Size: total, Status: "failed", Path: srcPath, Error: firstErr.Error()})
		return name, firstErr
	}
	history.Append(history.Entry{Dir: "send", Peer: t.Name, Name: name, Size: total, Status: "ok", Path: srcPath})
	fmt.Fprintf(os.Stderr, "完成：%s → %s\n", name, t.Name)
	return name, nil
}

// uploadOne 送出一个文件：先传内容，再发 complete 落盘。uploadedSHA 非空表示
// 内容已送达对端（此时 err 只可能出在 complete 这一步），Send 的重试据此只补
// complete、不重投内容。
func uploadOne(ctx context.Context, client *http.Client, addr, sid string, it item, peer string) (uploadedSHA string, err error) {
	f, err := os.Open(it.local)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	pr := &progressReader{r: io.TeeReader(f, h), total: it.meta.Size, dir: "send", peer: peer, name: filepath.Base(it.meta.Path)}
	stop := pr.startLog()
	defer stop()

	u := (&url.URL{
		Scheme:   "https",
		Host:     addr,
		Path:     protocol.PathUpload,
		RawQuery: url.Values{"s": {sid}, "f": {it.meta.ID}}.Encode(),
	}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pr)
	if err != nil {
		return "", err
	}
	req.ContentLength = it.meta.Size
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("传输失败: %w", err)
	}
	// 先读 body（错误详情在其中）再关闭，同时让连接可复用
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		} else {
			msg = resp.Status + ": " + msg
		}
		return "", fmt.Errorf("接收端拒绝: %s", msg)
	}
	// 内容已进对端的 .part：此后重投这段会被判「已在传输中」
	sha := hex.EncodeToString(h.Sum(nil))
	return sha, completeOne(ctx, client, addr, sid, it, sha)
}

// completeOne 通知对端校验并落盘。sha 由调用方给出：重试时内容已在对端，
// 不能重读本地文件算哈希（源可能已被改动，那会让对端判校验失败）。
func completeOne(ctx context.Context, client *http.Client, addr, sid string, it item, sha string) error {
	cr := protocol.CompleteReq{
		SessionID: sid,
		FileID:    it.meta.ID,
		SHA256:    sha,
		Bytes:     it.meta.Size,
	}
	var cack protocol.CompleteAck
	if err := postJSON(ctx, client, addr, protocol.PathComplete, cr, &cack); err != nil {
		return err
	}
	if !cack.OK {
		return fmt.Errorf("%s 校验失败: %s", it.meta.Path, cack.Message)
	}
	fmt.Fprintf(os.Stderr, "✓ %s 已发送（对方保存为 %s）\n", it.meta.Path, cack.SavedAs)
	return nil
}

// manifest 把命令行路径展开为清单。文件夹保留目录结构。
func manifest(paths []string) ([]item, int64, error) {
	var items []item
	var total int64
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, 0, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, 0, err
		}
		if !info.IsDir() {
			items = append(items, item{
				meta:  protocol.FileMeta{ID: fmt.Sprintf("f%d", len(items)), Path: filepath.Base(abs), Size: info.Size()},
				local: abs,
			})
			total += info.Size()
			continue
		}
		root := filepath.Base(abs)
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				return err
			}
			items = append(items, item{
				meta: protocol.FileMeta{
					ID:   fmt.Sprintf("f%d", len(items)),
					Path: root + "/" + filepath.ToSlash(rel),
					Size: fi.Size(),
				},
				local: path,
			})
			total += fi.Size()
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	if len(items) == 0 {
		return nil, 0, errors.New("没有可发送的文件")
	}
	if len(items) > maxManifestFiles {
		return nil, 0, fmt.Errorf("一次最多发送 %d 个文件", maxManifestFiles)
	}
	return items, total, nil
}

func metas(items []item) []protocol.FileMeta {
	out := make([]protocol.FileMeta, len(items))
	for i, it := range items {
		out[i] = it.meta
	}
	return out
}

func displayName(items []item) string {
	if len(items) == 1 {
		return items[0].meta.Path
	}
	return fmt.Sprintf("%d 个文件", len(items))
}

func postJSON(ctx context.Context, client *http.Client, addr, endpoint string, in any, out any) error {
	u := (&url.URL{Scheme: "https", Host: addr, Path: endpoint}).String()
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(errBody(resp))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func errBody(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		return resp.Status
	}
	return fmt.Sprintf("%s: %s", resp.Status, msg)
}

// ProgressHook 传输进度回调：dir 为 "send"/"recv"，done/total 为当前文件的
// 字节进度。由宿主按需设置（手机端联动通知进度条，PC 端暂未使用）；
// 约 800ms 节流一次，传输结束由宿主的完成事件收尾。
var ProgressHook func(dir, peer, name string, done, total int64)

type progressReader struct {
	r               io.Reader
	total           int64
	done            atomic.Int64
	dir, peer, name string
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done.Add(int64(n))
	}
	return n, err
}

func (p *progressReader) startLog() func() {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(800 * time.Millisecond)
		defer tick.Stop()
		last := int64(-1) // 上次上报的百分比（节流）
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				fmt.Fprintf(os.Stderr, "\r  %s / %s", protocol.HumanBytes(p.done.Load()), protocol.HumanBytes(p.total))
				if ProgressHook != nil {
					done := p.done.Load()
					pct := int64(0)
					if p.total > 0 {
						pct = done * 100 / p.total
					}
					if pct != last {
						last = pct
						ProgressHook(p.dir, p.peer, p.name, done, p.total)
					}
				}
			}
		}
	}()
	return func() {
		close(done)
		fmt.Fprintf(os.Stderr, "\r  %s / %s\n", protocol.HumanBytes(p.total), protocol.HumanBytes(p.total))
	}
}

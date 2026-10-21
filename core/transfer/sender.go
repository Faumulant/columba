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
	history.Append(history.Entry{Dir: "send", Peer: t.Name, PeerID: t.ID, Kind: "text", Text: text, Status: "ok"})
	return nil
}

// PushUnlock 把本机「已解除设备数量上限」的状态推给一台已配对设备。
//
// 语义：解锁是一次性、不可撤销的用户决定（桌面端点一次即永久生效），所以这里
// 只推 true，不推 false。对端收到后写自己的 unlocked.flag，用户不必在每台设备
// 上重复点击。
//
// 失败不影响本机解锁结果：对端离线/未启动时静默失败，下次任一端触发同步会再推。
func PushUnlock(ctx context.Context, id *identity.Identity, t Target) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   id.ClientConfig(t.ID),
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: false,
		},
		Timeout: 10 * time.Second,
	}
	body, err := json.Marshal(protocol.UnlockMsg{Proto: protocol.Version, Unlocked: true})
	if err != nil {
		return err
	}
	u := (&url.URL{Scheme: "https", Host: t.Addr, Path: protocol.PathUnlock}).String()
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
		status, msg := failureStatus(err)
		history.Append(history.Entry{Dir: "send", Peer: t.Name, PeerID: t.ID, Name: displayName(items), Size: total, Status: status, Path: srcPath, Error: msg})
		if status == history.StatusCanceled {
			return displayName(items), ErrCanceled
		}
		return displayName(items), fmt.Errorf("无法发起发送: %w", err)
	}
	if ack.Message != "" || ack.SessionID == "" {
		err := fmt.Errorf("对方拒绝了发送: %s", ack.Message)
		history.Append(history.Entry{Dir: "send", Peer: t.Name, PeerID: t.ID, Name: displayName(items), Size: total, Status: history.StatusFailed, Path: srcPath, Error: err.Error()})
		return displayName(items), err
	}
	fmt.Fprintf(os.Stderr, "开始传输 %d 个文件，共 %s\n", len(items), protocol.HumanBytes(total))
	// 整批进度口径：宿主端据此显示「N 个文件」的汇总百分比，而不是逐文件回绕
	agg := &progressAgg{total: total, label: batchLabel(items)}
	var firstErr error
	for _, it := range items {
		uploadedSHA, err := uploadOne(ctx, client, t.Addr, ack.SessionID, it, t.Name, agg)
		if err != nil && IsRetryable(err) && ctx.Err() == nil {
			time.Sleep(600 * time.Millisecond) // 网络抖动：同一文件重试一次
			if uploadedSHA != "" {
				// 内容已收、只是 complete 这一步没走通：补 complete，绝不重投
				// upload——重投命中对端 409「已在传输中」（不可重试），会把已经
				// 落盘的文件报成失败，用户手动重发＝对端双份记录。
				err = completeOne(ctx, client, t.Addr, ack.SessionID, it, uploadedSHA)
			} else {
				_, err = uploadOne(ctx, client, t.Addr, ack.SessionID, it, t.Name, agg)
			}
		}
		if err != nil {
			firstErr = err
			break // 连接已不可靠，后续文件不再尝试
		}
		agg.done.Add(it.meta.Size) // 本文件已落盘，计入整批进度
	}
	name := displayName(items)
	if firstErr != nil {
		status, errText := failureStatus(firstErr)
		history.Append(history.Entry{Dir: "send", Peer: t.Name, PeerID: t.ID, Name: name, Size: total, Status: status, Path: srcPath, Error: errText})
		if status == history.StatusCanceled {
			// 告诉对端「是用户主动取消的」：只靠读中断，接收端无法区分取消与断网，
			// 会把这次记成「传输中断」（真机实测）。必须在 ctx 之外用新 ctx 发，
			// 原 ctx 已经取消、带着它发请求只会立刻失败。
			notifyCancel(client, t.Addr, ack.SessionID, items)
			return name, ErrCanceled // 对外只报取消，不把 ctx canceled 当异常抛给界面
		}
		return name, firstErr
	}
	history.Append(history.Entry{Dir: "send", Peer: t.Name, PeerID: t.ID, Name: name, Size: total, Status: history.StatusOK, Path: srcPath})
	fmt.Fprintf(os.Stderr, "完成：%s → %s\n", name, t.Name)
	return name, nil
}

// notifyCancel 发送取消通知（尽力而为：失败不影响本地取消结果）。
//
// 用独立的短超时 ctx：调用点上的 ctx 已经因用户取消而失效，拿它发请求必然
// 立刻失败。对端是旧版本时该端点不存在（404），同样忽略——旧版本本来也只能
// 从读中断判断，行为不比现在差。
func notifyCancel(client *http.Client, addr, sid string, items []item) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 逐文件发：接收端按 FileID 精确清 .part 与记历史；空 FileID 表示整批
	for _, it := range items {
		_ = postJSON(ctx, client, addr, protocol.PathCancel, protocol.CancelMsg{
			Proto: protocol.Version, SessionID: sid, FileID: it.meta.ID,
		}, nil)
	}
}

// uploadOne 送出一个文件：先传内容，再发 complete 落盘。uploadedSHA 非空表示
// 内容已送达对端（此时 err 只可能出在 complete 这一步），Send 的重试据此只补
// complete、不重投内容。agg 非空时进度按整批口径上报。
func uploadOne(ctx context.Context, client *http.Client, addr, sid string, it item, peer string, agg *progressAgg) (uploadedSHA string, err error) {
	f, err := os.Open(it.local)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	pr := &progressReader{r: io.TeeReader(f, h), total: it.meta.Size, dir: "send", peer: peer, name: filepath.Base(it.meta.Path), agg: agg}
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

// ErrCanceled 用户主动取消传输（宿主经 ctx 取消）。与网络故障区分开：
// 取消不是失败，历史记 canceled、两端文案都是「已取消」。
var ErrCanceled = errors.New("传输已取消")

// failureStatus 把发送错误映射成历史状态与展示文案。ctx 取消（用户点了取消）
// 记 canceled 并给中文文案，其余记 failed 并保留原始错误便于排查。
func failureStatus(err error) (status, msg string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// DeadlineExceeded 也归为取消：6 小时超时对用户而言同样是「没传完就停了」，
		// 展示「context deadline exceeded」只会让人以为是故障
		return history.StatusCanceled, ErrCanceled.Error()
	}
	return history.StatusFailed, err.Error()
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
	if out == nil {
		return nil // 调用方不关心应答体（如取消通知）
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

// ProgressHook 传输进度回调：dir 为 "send"/"recv"，done/total 为字节进度。
// 由宿主按需设置（手机端联动通知与「传输中」条目，PC 端顶栏圆环）。
// 传输开始时立即回调一次（done=0），此后约 800ms 节流一次且只在百分比变化时
// 回调；传输结束由宿主的完成事件收尾。
//
// 多文件传输时 done/total 与 name 都是「整批」口径（见 progressAgg）：name 为
// 「N 个文件」，done 为已完成文件字节 + 当前文件已传字节。否则宿主端会看到
// 每个文件各自从 0% 跳到 100%，汇总进度条来回横跳。
var ProgressHook func(dir, peer, name string, done, total int64)

// StartHook 一次传输开始时回调（在首个 ProgressHook 之前，done 恒为 0）。
// 与 ProgressHook 分开的原因：进度回调受 800ms 节流、且只在百分比变化时触发，
// 宿主若靠「收到 done==0 的进度」判断开始，会把「开始」与「首个 tick」混为一谈；
// 显式开始事件让宿主的「传输中」条目出现时机确定。
// name/total 口径与 ProgressHook 一致（多文件为整批）。
var StartHook func(dir, peer, name string, total int64)

// progressAgg 一次多文件传输的整体进度累加器。total 为全部字节，done 为已完成
// 文件的字节累计（原子）；单文件传输也走它，只是 done 恒为 0、total 即文件大小，
// 这样两条路径的上报口径一致，宿主不必分辨。
type progressAgg struct {
	total   int64
	done    atomic.Int64
	label   string      // 整批显示名：单文件为文件名，多文件为「N 个文件」
	started atomic.Bool // 开始事件已发过（多文件只宣告一次）
}

// batchLabel 整批显示名。单文件用 base name（进度不该带目录层级）。
func batchLabel(items []item) string {
	if len(items) == 1 {
		return filepath.Base(items[0].meta.Path)
	}
	return fmt.Sprintf("%d 个文件", len(items))
}

type progressReader struct {
	r               io.Reader
	total           int64
	done            atomic.Int64
	dir, peer, name string
	agg             *progressAgg // 非空时上报整批进度而非本文件进度
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done.Add(int64(n))
	}
	return n, err
}

// snapshot 上报口径：有 agg 时把已完成文件的字节并进来、分母换成整批总字节。
func (p *progressReader) snapshot() (done, total int64, name string) {
	done, total, name = p.done.Load(), p.total, p.name
	if p.agg != nil {
		done += p.agg.done.Load()
		total = p.agg.total
		if p.agg.label != "" {
			name = p.agg.label
		}
	}
	return
}

func (p *progressReader) startLog() func() {
	// 显式开始事件：只对整批的第一个文件触发一次（多文件时每个文件的
	// progressReader 都会走到这里，但「一次传输」只该宣告一次开始）。
	if p.agg != nil && p.agg.started.CompareAndSwap(false, true) && StartHook != nil {
		StartHook(p.dir, p.peer, p.agg.label, p.agg.total)
	} else if p.agg == nil && StartHook != nil {
		StartHook(p.dir, p.peer, p.name, p.total)
	}

	done := make(chan struct{})
	go func() {
		// 首次上报在 goroutine 内完成，last 只被这一个 goroutine 读写，无需加锁
		last := int64(-1)
		report := func() {
			if ProgressHook == nil {
				return
			}
			d, t, name := p.snapshot()
			pct := int64(0)
			if t > 0 {
				pct = d * 100 / t
			}
			if pct != last {
				last = pct
				ProgressHook(p.dir, p.peer, name, d, t)
			}
		}
		// 开始即上报：ticker 800ms 才首触发、且只在百分比变化时回调，
		// 小文件（<800ms 传完）会一次都不上报，宿主端「传输中」条目永不出现。
		report()

		tick := time.NewTicker(800 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				fmt.Fprintf(os.Stderr, "\r  %s / %s", protocol.HumanBytes(p.done.Load()), protocol.HumanBytes(p.total))
				report()
			}
		}
	}()
	return func() {
		close(done)
		fmt.Fprintf(os.Stderr, "\r  %s / %s\n", protocol.HumanBytes(p.total), protocol.HumanBytes(p.total))
	}
}

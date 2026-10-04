// Package ipc 提供面板与后台 daemon 之间的本地服务：
// 仅监听 127.0.0.1，凭启动时生成的 token 鉴权。
package ipc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
	"drop/platform"
)

const (
	deviceLimit      = 3
	// Version 应用版本：/api/state 下发，面板关于卡显示；发版时同步更新
	Version = "0.1"
	ipcPreferredPort = 42709
)

// userscriptTemplate 生成给篡改猴的脚本；token/端口在生成时烧入。
const userscriptTemplate = `// ==UserScript==
// @name         Columba 发送
// @namespace    drop.local
// @version      1.0.0
// @description  划选文字 / 右键媒体 / 当前页面 → 一键发送到 Columba 设备
// @match        *://*/*
// @connect      127.0.0.1
// @connect      localhost
// @grant        GM_xmlhttpRequest
// @grant        GM_registerMenuCommand
// @run-at       document-end
// @updateURL    http://127.0.0.1:__PORT__/userscript.user.js?t=__TOKEN__
// @downloadURL  http://127.0.0.1:__PORT__/userscript.user.js?t=__TOKEN__
// ==/UserScript==
(function () {
  const API = "http://127.0.0.1:__PORT__";
  const TOKEN = "__TOKEN__";
  function sendText(text) {
    GM_xmlhttpRequest({
      method: "POST",
      url: API + "/api/send-text?t=" + TOKEN,
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ text }),
      onload: r => {
        let ok = false, msg = r.statusText;
        try { const d = JSON.parse(r.responseText); ok = !!d.ok; msg = d.message || ""; } catch (e) {}
        flash(ok ? "✓ 已发送" : "✗ " + (msg || "发送失败"), !ok);
      },
      onerror: () => flash("✗ 无法连接 Drop 后台", true)
    });
  }
  // 划选文字时浮现"发送到 Drop"按钮
  let pill = null, lastSel = "";
  function hide() { if (pill) { pill.remove(); pill = null; } }
  document.addEventListener("mouseup", e => {
    if (pill && pill.contains(e.target)) return;
    setTimeout(() => {
      const sel = (window.getSelection().toString() || "").trim();
      if (!sel || sel === lastSel) { if (!sel) lastSel = ""; hide(); return; }
      lastSel = sel; hide();
      pill = document.createElement("div");
      pill.textContent = "发送到 Columba";
      pill.style.cssText = "position:fixed;z-index:2147483647;background:#2563eb;color:#fff;font-size:12px;padding:6px 12px;border-radius:99px;cursor:pointer;box-shadow:0 4px 14px rgba(37,99,235,.4);font-family:sans-serif;left:" + (e.clientX + 12) + "px;top:" + (e.clientY + 10) + "px";
      pill.onclick = ev => { ev.stopPropagation(); sendText(sel); lastSel = ""; hide(); };
      document.body.appendChild(pill);
    }, 10);
  });
  function flash(text, err) {
    const d = document.createElement("div");
    d.textContent = text;
    d.style.cssText = "position:fixed;z-index:2147483647;left:50%;top:18%;transform:translateX(-50%);background:" + (err ? "#7f1d1d" : "#111827") + ";color:#f9fafb;font-size:13px;padding:10px 16px;border-radius:12px;font-family:sans-serif;box-shadow:0 8px 26px rgba(0,0,0,.3)";
    document.body.appendChild(d);
    setTimeout(() => d.remove(), 2600);
  }
  // 右键媒体记忆：在图片/视频上右键后，可从菜单发送其地址
  let lastMedia = null;
  document.addEventListener("contextmenu", e => {
    const t = e.target && e.target.closest && e.target.closest("img,video");
    lastMedia = t ? (t.currentSrc || t.src || "") : null;
  }, true);
  if (typeof GM_registerMenuCommand === "function") {
    GM_registerMenuCommand("Drop：发送当前页面", () => sendText(location.href));
    GM_registerMenuCommand("Drop：发送选中文本/剪贴板", () => {
      const sel = (window.getSelection().toString() || "").trim();
      if (sel) return sendText(sel);
      if (navigator.clipboard && navigator.clipboard.readText) {
        navigator.clipboard.readText().then(t => { if (t.trim()) sendText(t.trim()); else flash("剪贴板为空", true); }).catch(() => flash("无法读取剪贴板（需浏览器授权）", true));
      } else flash("剪贴板为空", true);
    });
    GM_registerMenuCommand("Drop：发送右键的图片/视频", () => {
      if (lastMedia) sendText(lastMedia); else flash("请先在图片或视频上右键", true);
    });
  }
})();`

// TouchPanel 记录面板活跃（/api/state 每次命中时调用）。
func (s *Server) TouchPanel() {
	s.probeMu.Lock()
	s.panelAt = time.Now()
	s.probeMu.Unlock()
}

// StartProber 面板打开期间每 30 秒后台探测一次缺失设备的地址，
// 结果写入 probeSeen 供 onlineSet 读取；面板关闭时探测自动停止。
// 注意：探测是对全网段的端口扫描，频率不宜再高，避免被安全软件误判。
// StartUpdateCheck 定时查询 GitHub 最新发布版本；仓库未公开、无网络、
// 限流时一律静默跳过（latestVer 保持空，面板不提示）。仅记录比当前
// 版本新的 tag，下载与安装由用户在浏览器手动完成。
func (s *Server) StartUpdateCheck(ctx context.Context) {
	go func() {
		time.Sleep(15 * time.Second) // 启动后稍等，网络就绪
		for {
			s.checkLatestOnce()
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	}()
}

func (s *Server) checkLatestOnce() {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/Faumulant/columba/releases/latest", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var v struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return
	}
	tag := strings.TrimPrefix(strings.TrimSpace(v.TagName), "v")
	if tag == "" || !versionNewer(Version, tag) {
		return
	}
	s.latestMu.Lock()
	s.latestTag = tag
	s.latestAt = time.Now()
	s.latestMu.Unlock()
}

func (s *Server) latestVer() string {
	s.latestMu.Lock()
	defer s.latestMu.Unlock()
	return s.latestTag
}

// versionNewer 语义化版本比较（按数字段逐段比较）：latest 是否比 cur 新。
func versionNewer(cur, latest string) bool {
	nums := func(v string) (out []int) {
		for _, p := range strings.Split(v, ".") {
			n, _ := strconv.Atoi(strings.TrimSpace(p))
			out = append(out, n)
		}
		return
	}
	a, b := nums(cur), nums(latest)
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if y != x {
			return y > x
		}
	}
	return false
}

func (s *Server) StartProber(ctx context.Context) {
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			s.probeMu.Lock()
			active := time.Since(s.panelAt) < 15*time.Second
			s.probeMu.Unlock()
			if !active {
				continue
			}
			need := false
			for _, p := range s.trust.List() {
				if !s.announceOnline(p.ID) {
					need = true
					break
				}
			}
			if !need {
				continue
			}
		if found, err := transfer.Probe(s.id); err == nil {
			s.probeMu.Lock()
			// 整体替换为最新一轮探测结果：本轮找到 = 在线（保持到下轮）。
			// 本轮没扫到的旧条目保留一轮宽限（Wi-Fi 探测偶发超时），
			// 连续两轮缺失才判离线——消除扫描期间的假离线闪烁。
			seen := map[string]time.Time{}
			for _, t := range found {
				seen[protocol.NormalizeID(t.ID)] = time.Now()
			}
			for id, ts := range s.probeSeen {
				if _, ok := seen[id]; !ok && time.Since(ts) < 65*time.Second {
					seen[id] = ts
				}
			}
			s.probeSeen = seen
			s.probeMu.Unlock()
		}
		}
	}()
}

func (s *Server) announceOnline(devID string) bool {
	key := protocol.NormalizeID(devID)
	for _, info := range s.peers.Snapshot() {
		if protocol.NormalizeID(info.Announce.ID) == key {
			return true
		}
	}
	return false
}

// faviconIco 内嵌的应用图标（浏览器窗口/任务栏图标用）。
//
//go:embed favicon.ico
var faviconIco []byte

// PanelHash 内嵌面板内容的 SHA-256，供启动器判断 daemon 是否为旧版本。
func PanelHash() string {
	sum := sha256.Sum256(panelHTML)
	return hex.EncodeToString(sum[:])
}

func (s *Server) userscript() string {
	return strings.NewReplacer(
		"__PORT__", fmt.Sprint(s.port),
		"__TOKEN__", s.token,
	).Replace(userscriptTemplate)
}

// Config 面板可修改的持久设置。
type Config struct {
	Target  string `json:"target"`
	RecvDir string `json:"recv_dir"`
}

// Server 本地 IPC 服务。
type Server struct {
	id    *identity.Identity
	trust *identity.TrustStore
	recv  *transfer.Receiver
	peers *discover.Tracker

	cfgMu   sync.Mutex
	cfg     Config
	cfgPath string
	token   string
	port    int

	// 收/发完成通知钩子（daemon 接线到系统通知）
	NotifyFile      func(dir, peer, name string) // dir: send/recv
	NotifyText      func(dir, peer, text string)
	NotifyErr       func(title, msg string)
	OnDeviceRemoved func(display string)

	pairMu   sync.Mutex
	pendName string
	pendID   string
	decision chan bool

	probeMu   sync.Mutex
	probeSeen map[string]time.Time

	latestMu     sync.Mutex
	latestTag    string    // GitHub 上比当前版本新的 tag（无/未知 = 空串）
	latestAt     time.Time // 上次检查时间，避免频繁请求
	panelAt   time.Time // 最近一次面板访问

	OnShutdown func() // daemon 收到关闭指令时回调（启动器接线到退出流程）
}

// New 创建服务（读取配置、生成 token），随后 SetReceiver 并 Start。
func New(id *identity.Identity, trust *identity.TrustStore, peers *discover.Tracker) (*Server, error) {
	s := &Server{
		id:        id,
		trust:     trust,
		peers:     peers,
		cfgPath:   filepath.Join(identity.AppDir(), "config.json"),
		decision:  make(chan bool, 1),
		probeSeen: map[string]time.Time{},
	}
	if raw, err := os.ReadFile(s.cfgPath); err == nil {
		json.Unmarshal(raw, &s.cfg)
	}
	// token 持久化：重启后复用，面板/篡改猴脚本的地址永久有效
	if raw, err := os.ReadFile(filepath.Join(identity.AppDir(), "ipc.json")); err == nil {
		var prev struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(raw, &prev) == nil && prev.Token != "" {
			s.token = prev.Token
		}
	}
	if s.token == "" {
		tok := make([]byte, 32)
		if _, err := rand.Read(tok); err != nil {
			return nil, err
		}
		s.token = hex.EncodeToString(tok)
	}
	return s, nil
}

// SetReceiver 挂载接收端（配对确认经面板裁决）。
func (s *Server) SetReceiver(r *transfer.Receiver) { s.recv = r }

// RecvDir 当前接收目录（面板设置优先）。
func (s *Server) RecvDir() string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.RecvDir
}

// Target 当前选中的发送目标。
func (s *Server) Target() string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.Target
}

// Start 启动本地服务，返回面板访问 URL。
// 优先绑定固定端口（篡改猴脚本的自动更新依赖稳定地址），被占时退随机端口。
func (s *Server) Start(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ipcPreferredPort)))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	if err := s.saveIPC(); err != nil {
		return "", err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != http.ErrServerClosed {
			log.Println("IPC 服务异常:", err)
		}
	}()
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	return fmt.Sprintf("http://127.0.0.1:%d/panel?t=%s", s.port, s.token), nil
}

func (s *Server) saveIPC() error {
	b, _ := json.MarshalIndent(map[string]any{"port": s.port, "token": s.token}, "", "  ")
	return os.WriteFile(filepath.Join(identity.AppDir(), "ipc.json"), b, 0o600)
}

func (s *Server) loadConfig() {
	if raw, err := os.ReadFile(s.cfgPath); err == nil {
		json.Unmarshal(raw, &s.cfg)
	}
}

func (s *Server) saveConfig() {
	b, _ := json.MarshalIndent(s.cfg, "", "  ")
	os.WriteFile(s.cfgPath, b, 0o600)
}

// FeedAnnounce 由 daemon 喂入收到的设备宣告。
func (s *Server) FeedAnnounce(e discover.Event) { s.peers.Feed(e) }

// confirmPeer 配对确认回调：登记待确认请求，等待面板裁决。
func (s *Server) ConfirmPeer(name, id string) bool {
	s.pairMu.Lock()
	s.pendName, s.pendID = name, id
	s.pairMu.Unlock()
	ch := make(chan bool, 1)
	s.pairMu.Lock()
	s.decision = ch
	s.pairMu.Unlock()
	select {
	case allow := <-ch:
		return allow
	case <-time.After(90 * time.Second):
		s.pairMu.Lock()
		s.pendName, s.pendID = "", ""
		s.pairMu.Unlock()
		return false
	}
}

// --- HTTP ---

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("t") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case req.URL.Path == "/favicon.ico":
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(faviconIco)
	case req.URL.Path == "/userscript.user.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write([]byte(s.userscript()))
	case req.URL.Path == "/panel":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(panelHTML)
	case strings.HasPrefix(req.URL.Path, "/api/"):
		s.api(w, req)
	default:
		http.NotFound(w, req)
	}
}

func (s *Server) api(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			http.Error(w, fmt.Sprintf("internal: %v", r), http.StatusInternalServerError)
		}
	}()
	switch {
	case req.URL.Path == "/api/state" && req.Method == http.MethodGet:
		s.handleState(w, req)
	case req.URL.Path == "/api/select" && req.Method == http.MethodPost:
		var in struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || in.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.cfgMu.Lock()
		s.cfg.Target = protocol.NormalizeID(in.ID)
		s.saveConfig()
		s.cfgMu.Unlock()
		writeOK(w)
	case req.URL.Path == "/api/send-files" && req.Method == http.MethodPost:
		s.handleSendFiles(w, req)
	case req.URL.Path == "/api/send-text" && req.Method == http.MethodPost:
		s.handleSendText(w, req)
	case req.URL.Path == "/api/pair/start" && req.Method == http.MethodPost:
		code, err := s.recv.StartPairing()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"code": code})
	case req.URL.Path == "/api/pair/pending" && req.Method == http.MethodGet:
		s.pairMu.Lock()
		defer s.pairMu.Unlock()
		if s.pendID == "" {
			writeJSON(w, map[string]any{"waiting": false})
			return
		}
		writeJSON(w, map[string]any{"waiting": true, "name": s.pendName, "id": s.pendID})
	case req.URL.Path == "/api/pair/decision" && req.Method == http.MethodPost:
		var in struct {
			Allow bool `json:"allow"`
		}
		json.NewDecoder(req.Body).Decode(&in)
		s.pairMu.Lock()
		ch := s.decision
		s.pendName, s.pendID = "", ""
		s.pairMu.Unlock()
		if ch != nil {
			ch <- in.Allow
		}
		writeOK(w)
	case req.URL.Path == "/api/pair/join" && req.Method == http.MethodPost:
		var in struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Code) == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 90*time.Second)
		defer cancel()
		// 配对码本身就是验证，但绝不能广播给所有候选设备——同网攻击者
		// 伪造宣告即可截获配对码并被静默信任（配对劫持）。候选不唯一
		// 一律中止，交由用户确认唯一在线后再试（与手机端设计一致）。
		cands, err := transfer.FindCandidates(s.id, 7*time.Second)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": errMsg(err)})
			return
		}
		var targets []discover.PeerInfo
		for _, t := range cands {
			if protocol.NormalizeID(t.Announce.ID) != protocol.NormalizeID(s.id.ID()) {
				targets = append(targets, t)
			}
		}
		if len(targets) == 0 {
			writeJSON(w, map[string]any{"ok": false, "message": "未发现可配对的设备：确认对方已进入配对等待"})
			return
		}
		if len(targets) > 1 {
			writeJSON(w, map[string]any{"ok": false, "message": "发现多台候选设备，为防配对码误投已中止：请只让一台设备处于配对等待后再试"})
			return
		}
		name, err := transfer.Pair(ctx, s.id, s.trust, targetOfInfo(targets[0]), in.Code)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": errMsg(err)})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "name": name})
	case req.URL.Path == "/api/device/remove" && req.Method == http.MethodPost:
		var in struct {
			ID string `json:"id"`
		}
		json.NewDecoder(req.Body).Decode(&in)
		p := s.trust.List()
		var display string
		for _, x := range p {
			if x.ID == protocol.NormalizeID(in.ID) {
				display = x.DisplayName()
			}
		}
		if !s.trust.Remove(in.ID) {
			http.Error(w, "设备不存在", http.StatusNotFound)
			return
		}
		if display != "" && s.OnDeviceRemoved != nil {
			s.OnDeviceRemoved(display)
		}
		writeOK(w)
	case req.URL.Path == "/api/alias" && req.Method == http.MethodPost:
		var in struct {
			ID    string `json:"id"`
			Alias string `json:"alias"`
		}
		json.NewDecoder(req.Body).Decode(&in)
		if !s.trust.SetAlias(in.ID, in.Alias) {
			http.Error(w, "设备不存在", http.StatusNotFound)
			return
		}
		writeOK(w)
	case req.URL.Path == "/api/recvdir" && req.Method == http.MethodPost:
		var in struct {
			Dir string `json:"dir"`
		}
		json.NewDecoder(req.Body).Decode(&in)
		dir := strings.TrimSpace(in.Dir)
		if !filepath.IsAbs(dir) {
			http.Error(w, "需要绝对路径", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, "目录不可创建: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.cfgMu.Lock()
		s.cfg.RecvDir = dir
		s.saveConfig()
		s.cfgMu.Unlock()
		writeOK(w)
	case (req.URL.Path == "/api/open" || req.URL.Path == "/api/reveal") && req.Method == http.MethodGet:
		path := req.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "缺少 path", http.StatusBadRequest)
			return
		}
		var err error
		if req.URL.Path == "/api/open" {
			err = platform.OpenPath(path)
		} else {
			err = platform.RevealPath(path)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w)
	case req.URL.Path == "/api/firefox-ext" && req.Method == http.MethodPost:
		dir := FirefoxExtDir()
		if dir == "" {
			http.Error(w, "未找到扩展目录（exe 同级需存在 extension\firefox）", http.StatusNotFound)
			return
		}
		warning, err := platform.RegisterFirefoxExtension(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "dir": dir, "warning": warning})
	case req.URL.Path == "/api/history/delete" && req.Method == http.MethodPost:
		var in struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&in); err != nil || len(in.IDs) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n, err := history.DeleteIDs(in.IDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "removed": n})
	case req.URL.Path == "/api/pick-dir" && req.Method == http.MethodPost:
		dir, err := platform.PickFolder("选择 Drop 接收文件夹")
		if err != nil || dir == "" {
			writeJSON(w, map[string]any{"ok": false, "message": "未选择文件夹"})
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": "目录不可用: " + err.Error()})
			return
		}
		s.cfgMu.Lock()
		s.cfg.RecvDir = dir
		s.saveConfig()
		s.cfgMu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "dir": dir})
	case req.URL.Path == "/api/shutdown" && req.Method == http.MethodPost:
		if s.OnShutdown != nil {
			s.OnShutdown()
		}
		writeOK(w)
	case req.URL.Path == "/api/autostart" && req.Method == http.MethodPost:
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var err error
		if in.On {
			err = platform.SetAutostart()
		} else {
			err = platform.ClearAutostart()
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w)
	case req.URL.Path == "/api/unlock" && req.Method == http.MethodPost:
		if err := identity.SetUnlocked(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w)
	default:
		http.NotFound(w, req)
	}
}

func (s *Server) handleState(w http.ResponseWriter, req *http.Request) {
	type dev struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	}
	online := s.onlineSet()
	var devices []dev
	for _, p := range s.trust.List() {
		devices = append(devices, dev{ID: p.ID, Name: p.DisplayName(), Online: online[p.ID]})
	}
	s.cfgMu.Lock()
	cfg := s.cfg
	s.cfgMu.Unlock()
	writeJSON(w, map[string]any{
		"devices":            devices,
		"target":             cfg.Target,
		"recv_dir":           cfg.RecvDir,
		"unlocked":           identity.IsUnlocked(),
		"version":            Version,
		"latest":             s.latestVer(),
		"limit":              deviceLimit,
		"ext_dir":            FirefoxExtDir(),
		"ext_dir_chromium":   chromiumExtDir(),
		"firefox_registered": platform.FirefoxExtensionRegistered(),
		"autostart":          platform.AutostartEnabled(),
		"userscript_url":     "/userscript.user.js?t=" + s.token,
		"history":            history.Load(),
	})
}

// onlineSet 已配对设备中确认存活的集合：宣告窗口（20 秒）内听到宣告，
// 或最近一次后台探测确认存活（结果保持到下次探测整体替换——避免探测
// 扫描期间出现「假离线」导致面板状态闪烁）。
func (s *Server) onlineSet() map[string]bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	online := map[string]bool{}
	for _, info := range s.peers.Snapshot() {
		online[protocol.NormalizeID(info.Announce.ID)] = true
	}
	for id := range s.probeSeen {
		online[id] = true
	}
	return online
}

// targetAddr 找到目标设备的当前地址：缓存直连 → 宣告 → 探测。
func (s *Server) targetAddr(targetID string) (transfer.Target, error) {
	key := protocol.NormalizeID(targetID)
	if t, ok := transfer.CachedTarget(s.id, key); ok {
		return t, nil
	}
	for _, info := range s.peers.Snapshot() {
		if protocol.NormalizeID(info.Announce.ID) == key {
			t := transfer.Target{
				Addr: net.JoinHostPort(info.IP, fmt.Sprint(info.Announce.Port)),
				ID:   info.Announce.ID, Name: info.Announce.Name,
			}
			transfer.UpdateIPCache(key, t.Addr)
			return t, nil
		}
	}
	found, err := transfer.Probe(s.id)
	if err != nil {
		return transfer.Target{}, err
	}
	for _, t := range found {
		if protocol.NormalizeID(t.ID) == key {
			transfer.UpdateIPCache(key, t.Addr)
			return t, nil
		}
	}
	return transfer.Target{}, errors.New("目标设备不在线")
}

// handleSendFiles 面板拖拽/选择的文件：先落到临时目录（保留原名），
// 再走既有文件发送管线，后台完成后弹系统通知。
func (s *Server) handleSendFiles(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "上传解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	files := req.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "没有文件", http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	target := s.cfg.Target
	s.cfgMu.Unlock()
	if target == "" {
		http.Error(w, "尚未选择目标设备", http.StatusBadRequest)
		return
	}
	if !s.trust.Has(target) {
		http.Error(w, "目标设备未配对", http.StatusBadRequest)
		return
	}

	spool, err := os.MkdirTemp("", "drop-upload-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var paths []string
	for _, fh := range files {
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || strings.ContainsRune(name, 0) {
			continue
		}
		dst := filepath.Join(spool, name)
		if _, err := os.Stat(dst); err == nil { // 同名去重
			dst = filepath.Join(spool, fmt.Sprintf("(%d)%s", time.Now().UnixNano()%1000, name))
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		dstF, err := os.Create(dst)
		if err != nil {
			src.Close()
			continue
		}
		if _, err := io.Copy(dstF, src); err != nil {
			dstF.Close()
			continue
		}
		dstF.Close()
		src.Close()
		paths = append(paths, dst)
	}
	if len(paths) == 0 {
		os.RemoveAll(spool)
		http.Error(w, "没有有效文件", http.StatusBadRequest)
		return
	}

	// 先应答，传输在后台执行，结果经系统通知与历史呈现
	go func() {
		defer os.RemoveAll(spool)
		t, err := s.targetAddr(target)
		if err != nil {
			if s.NotifyErr != nil {
				s.NotifyErr("Drop 发送失败", err.Error())
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		sent, err := transfer.Send(ctx, s.id, t, paths, transfer.SendOptions{SkipSourcePath: true})
		if err != nil {
			if s.NotifyErr != nil {
				s.NotifyErr("Drop 发送失败", fmt.Sprintf("%s：%v", sent, err))
			}
			return
		}
		if s.NotifyFile != nil {
			s.NotifyFile("send", t.Name, sent)
		}
	}()
	writeJSON(w, map[string]any{"ok": true, "started": len(paths)})
}

func (s *Server) handleSendText(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 128<<10)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		http.Error(w, "文本为空", http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	target := s.cfg.Target
	s.cfgMu.Unlock()
	if target == "" {
		http.Error(w, "尚未选择目标设备", http.StatusBadRequest)
		return
	}
	if !s.trust.Has(target) {
		http.Error(w, "目标设备未配对", http.StatusBadRequest)
		return
	}
	t, err := s.targetAddr(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	if err := transfer.SendText(ctx, s.id, t, text); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if s.NotifyText != nil {
		s.NotifyText("send", t.Name, text)
	}
	writeOK(w)
}

func writeOK(w http.ResponseWriter) { writeJSON(w, map[string]any{"ok": true}) }
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// chromiumExtDir 探测 Chromium 系浏览器的扩展目录。
func chromiumExtDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(exe), "extension", "chromium")
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		return ""
	}
	return dir
}

// FirefoxExtDir 探测浏览器扩展目录：exe 同级的 extension/firefox。
// 供面板与 drop install 共用（唯一实现）。
func FirefoxExtDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(exe), "extension", "firefox")
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		return ""
	}
	return dir
}

// targetOfInfo 由宣告信息构造发送目标。
func targetOfInfo(p discover.PeerInfo) transfer.Target {
	return transfer.Target{
		Addr: net.JoinHostPort(p.IP, fmt.Sprint(p.Announce.Port)),
		ID:   p.Announce.ID, Name: p.Announce.Name,
	}
}

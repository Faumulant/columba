package ipc

// 浏览器扩展「发送此图片/视频」的后端：daemon 下载 URL 内容再走文件管线。
//
// 为什么下载在 daemon 侧：IPC token 由 native-host 持有、从不进入扩展，
// 这是有意的安全设计。扩展只交 URL 过来。
//
// 本文件覆盖三类边界：
//   - 只接受 http/https（blob:/data:/file: 跨进程取不到，必须明确报错）
//   - 文件名从 URL 或 Content-Type 推导（接收端靠扩展名决定能否「打开」）
//   - 文件名消毒（对端自报的 URL 不可信，不能造成路径穿越）

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"drop/core/identity"
)

func TestFileNameForURL(t *testing.T) {
	cases := []struct {
		name        string
		rawURL      string
		contentType string
		kind        string
		want        string
	}{
		{"URL 自带扩展名", "https://a.com/pics/cat.jpg", "image/jpeg", "image", "cat.jpg"},
		{"无扩展名按 Content-Type 补", "https://a.com/image", "image/png", "image", "image.png"},
		{"Content-Type 带 charset", "https://a.com/x", "image/gif; charset=binary", "image", "x.gif"},
		{"路径是根时用 kind", "https://a.com/", "video/mp4", "video", "video.mp4"},
		{"都不认识时兜底 .bin", "https://a.com/blob", "application/octet-stream", "image", "blob.bin"},
		{"URL 编码的文件名要解码", "https://a.com/%E7%8C%AB.jpg", "", "image", "猫.jpg"},
		{"路径穿越要消毒", "https://a.com/../../etc/passwd", "", "image", "passwd.bin"},
		{"Windows 非法字符要消毒", "https://a.com/a:b*c.png", "", "image", "a_b_c.png"},
		// `?` 在 URL 里是查询串分隔符，path.Base 拿不到它后面的内容：
		// a.com/x?a=1.png 的路径是 "/x"，扩展名要从 Content-Type 补。
		{"查询串不算路径", "https://a.com/x?a=1.png", "image/png", "image", "x.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse(c.rawURL)
			if err != nil {
				t.Fatalf("解析 URL: %v", err)
			}
			got := fileNameForURL(u, c.contentType, c.kind)
			if got != c.want {
				t.Fatalf("文件名应为 %q, got %q", c.want, got)
			}
			// 任何情况下都不许带路径分隔符
			if strings.ContainsAny(got, `/\`) {
				t.Fatalf("文件名不得含路径分隔符: %q", got)
			}
		})
	}
}

// 非 http/https 一律拒绝：blob:/data: 是页面内引用，file: 越权。
func TestHandleSendURLRejectsNonHTTP(t *testing.T) {
	s := newTestServer()
	for _, bad := range []string{
		"blob:https://a.com/1234",
		"data:image/png;base64,AAAA",
		"file:///C:/Windows/win.ini",
		"ftp://a.com/x.jpg",
		"",
	} {
		body := strings.NewReader(`{"url":` + quoteJSON(bad) + `,"kind":"image"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/send-url", body)
		w := httptest.NewRecorder()
		s.handleSendURL(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q 应被拒绝为 400, got %d: %s", bad, w.Code, w.Body.String())
		}
	}
}

// 未选目标设备时明确报错，而不是静默什么都不做。
func TestHandleSendURLNeedsTarget(t *testing.T) {
	s := newTestServer()
	body := strings.NewReader(`{"url":"https://a.com/x.jpg","kind":"image"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/send-url", body)
	w := httptest.NewRecorder()
	s.handleSendURL(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未选目标应 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "目标设备") {
		t.Fatalf("错误文案应说明缺目标设备, got %q", w.Body.String())
	}
}

// 目标未配对时拒绝（与 send-files 同一授权门槛）。
func TestHandleSendURLNeedsTrustedTarget(t *testing.T) {
	// 需要一个真实的 TrustStore：newTestServer 的 trust 是 nil，直接调 Has 会 panic。
	// LoadTrust 读 AppDir()（= LOCALAPPDATA/Drop），用 t.Setenv 隔离到临时目录。
	t.Setenv("LOCALAPPDATA", t.TempDir())
	trust, err := identity.LoadTrust()
	if err != nil {
		t.Fatalf("加载信任库: %v", err)
	}
	s := newTestServer()
	s.trust = trust
	s.cfg.Target = "DEADBEEF" // 未配对

	body := strings.NewReader(`{"url":"https://a.com/x.jpg","kind":"image"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/send-url", body)
	w := httptest.NewRecorder()
	s.handleSendURL(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未配对目标应 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "未配对") {
		t.Fatalf("错误文案应说明未配对, got %q", w.Body.String())
	}
}

func quoteJSON(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

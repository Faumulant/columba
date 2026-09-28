// Package history 记录本地传输历史（追加式 JSONL）。
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"drop/core/identity"
	"drop/core/protocol"
)

// Entry 一条传输记录。Dir 为 "send"（发出）或 "recv"（收到）；
// Kind 为 "file"（默认，空串视为 file）或 "text"（文本内容本身，不落盘）。
type Entry struct {
	ID     string    `json:"id,omitempty"` // 加载时按行位置+内容哈希生成，供批量删除定位
	Time   time.Time `json:"time"`
	Dir    string    `json:"dir"`
	Peer   string    `json:"peer"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Status string    `json:"status"` // ok / failed
	Path   string    `json:"path,omitempty"`
	Error  string    `json:"error,omitempty"`
	Kind   string    `json:"kind,omitempty"`
	Text   string    `json:"text,omitempty"`
}

var mu sync.Mutex

func path() string { return filepath.Join(identity.AppDir(), "history.jsonl") }

// Append 追加一条记录；失败仅静默（历史绝不影响传输本身）。
func Append(e Entry) {
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
}

// lineID 行位置 + 内容哈希：即使中间有新增/删除导致行号偏移，
// 内容未变的行 ID 依然稳定，删除时不会误删。
func lineID(idx int, line string) string {
	sum := sha256.Sum256([]byte(line))
	return fmt.Sprintf("h%d-%s", idx, hex.EncodeToString(sum[:])[:8])
}

// Load 读取全部历史（面板用；JSONL 体积在本项目场景下可忽略）。
func Load() []Entry {
	mu.Lock()
	defer mu.Unlock()
	raw, err := os.ReadFile(path())
	if err != nil {
		return nil
	}
	var out []Entry
	for i, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if ln == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(ln), &e) == nil {
			e.ID = lineID(i, ln)
			out = append(out, e)
		}
	}
	return out
}

// DeleteIDs 删除指定 ID 的记录，返回实际删除条数。
func DeleteIDs(ids []string) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	raw, err := os.ReadFile(path())
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var kept []string
	removed := 0
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		if want[lineID(i, ln)] {
			removed++
			continue
		}
		kept = append(kept, ln)
	}
	if removed == 0 {
		return 0, nil
	}
	out := strings.Join(kept, "\n") + "\n"
	if err := os.WriteFile(path(), []byte(out), 0o600); err != nil {
		return 0, err
	}
	return removed, nil
}

// Print 打印最近 n 条记录。
func Print(n int) error {
	raw, err := os.ReadFile(path())
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("暂无传输记录")
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		fmt.Println("暂无传输记录")
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, ln := range lines {
		var e Entry
		if json.Unmarshal([]byte(ln), &e) != nil {
			continue
		}
		mark := "✓"
		if e.Status != "ok" {
			mark = "✗"
		}
		dir := "→"
		if e.Dir == "recv" {
			dir = "←"
		}
		name := e.Name
		if e.Kind == "text" {
			name = "文本：" + firstRunes(e.Text, 18)
		}
		fmt.Printf("%s %s %s %-12s %-32s %s\n",
			e.Time.Format("01-02 15:04"), mark, dir, e.Peer, name, protocol.HumanBytes(e.Size))
	}
	return nil
}

func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

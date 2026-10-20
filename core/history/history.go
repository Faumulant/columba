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
	// 原子替换：daemon 可能正在 Append、面板可能正在读，整文件重写会让读者
	// 撞上半截历史（半截 JSONL 即损坏记录）。跨进程丢更新仍未修，根治要文件锁。
	if err := writeAtomic(path(), out); err != nil {
		return 0, err
	}
	return removed, nil
}

// writeAtomic 同目录临时文件 + rename 替换（CreateTemp 建出来就是 0600）。
//
// Windows 的 os.Open 不带 FILE_SHARE_DELETE：读者正持有 history.jsonl 时
// rename 报 Access is denied。这不是偶发噪音——面板开着时每 2 秒轮询一次
// /api/state（每次都 Load 整份历史），delete 与轮询撞上是常态，而「删除历史」
// 失败会让用户重复点击却看不到任何变化。故重试窗口按「跨过一个完整轮询周期」
// 设计：200ms 起，指数退避到 1.2s，累计约 4 秒，远超面板 2 秒的轮询间隔。
func writeAtomic(p, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 成功后这里是 ENOENT，幂等
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	wait := 200 * time.Millisecond
	for i := 0; ; i++ {
		if err := os.Rename(tmp.Name(), p); err == nil {
			return nil
		} else if i >= 6 {
			return err
		}
		time.Sleep(wait)
		if wait < 1200*time.Millisecond {
			wait *= 2
		}
	}
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

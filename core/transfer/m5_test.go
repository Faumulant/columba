package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConflict(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	if got := ResolveConflict(dir, "a.txt"); got != "a (1).txt" {
		t.Fatalf("冲突改名错误：got %q", got)
	}
	if got := ResolveConflict(dir, "b.txt"); got != "b.txt" {
		t.Fatalf("不冲突不应改名：got %q", got)
	}
}

func TestSafeRelPath(t *testing.T) {
	for _, bad := range []string{"../x", "/abs", "C:/x", "", "..", "a/../../b"} {
		if safeRelPath(bad) == nil {
			t.Fatalf("应拒绝 %q", bad)
		}
	}
	for _, good := range []string{"docs/a.txt", "图片.png", "a/b/c.zip"} {
		if safeRelPath(good) != nil {
			t.Fatalf("应接受 %q", good)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	cases := map[string]bool{
		"传输失败: connection reset":   true,
		"无法连接对方: timeout":          true,
		"对方拒绝了发送: 磁盘空间不足":          false,
		"SHA-256 校验失败，文件已丢弃":       false,
		"接收端拒绝: 409 Conflict: ...": false,
	}
	for msg, want := range cases {
		if got := IsRetryable(errString(msg)); got != want {
			t.Fatalf("IsRetryable(%q) = %v, want %v", msg, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

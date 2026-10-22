//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// GetShortPathNameW 取 8.3 短名（测试辅助）。
func getShortPath(p string) (string, error) {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(ptr, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// normalizeLnkData 必须把 8.3 短名与长名视作同一路径。
//
// 这是 CI 上「内容不正确」的真正原因：PowerShell 回显的 TargetPath 保留了
// 写入时的短名（CI runner 的用户名超过 8 字符，产生 ~1 形式的短名），
// 而 os.Executable() 返回长名。两者指向同一文件，字符串却不相等。
func TestNormalizeLnkDataHandlesShortName(t *testing.T) {
	tmp := t.TempDir()
	long := filepath.Join(tmp, "VeryLongDirectoryName")
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Skipf("无法创建目录: %v", err)
	}
	exe := filepath.Join(long, "program.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Skipf("无法创建文件: %v", err)
	}

	short, err := getShortPath(exe)
	if err != nil || short == "" || strings.EqualFold(short, exe) {
		t.Skip("本机未生成 8.3 短名，跳过（CI 上可覆盖）")
	}
	t.Logf("短名=%q 长名=%q", short, exe)

	a := normalizeLnkData(short + "|sendto --to ABC")
	b := normalizeLnkData(exe + "|sendto --to ABC")
	if !strings.EqualFold(a, b) {
		t.Fatalf("短名未归一：\n short=%q -> %q\n long =%q -> %q", short, a, exe, b)
	}
}

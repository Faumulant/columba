package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// randomCode 生成 6 位数字一次性配对码。
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// safeRelPath 校验清单中的相对路径：拒绝绝对路径与目录穿越。
func safeRelPath(p string) error {
	s := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "/") || strings.ContainsRune(s, 0) {
		return fmt.Errorf("非法的文件路径 %q", p)
	}
	if len(s) >= 2 && s[1] == ':' {
		return fmt.Errorf("非法的文件路径 %q", p)
	}
	return nil
}

// ResolveConflict 同名冲突：photo.jpg → photo (1).jpg，绝不静默覆盖。
func ResolveConflict(dir, name string) string {
	if name == "" {
		return name
	}
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
}

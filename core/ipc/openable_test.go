package ipc

// open/reveal 白名单的边界：面板只对四类目标发起 open（见 checkOpenable）。
// 这里把放行与拒绝两侧都钉死，重点是被拒绝的形态——token 长期复用（ipc.json
// 持久），泄露即等于任意路径执行器：ShellExecute 会按扩展名拉起关联程序，
// 含 .exe 与 UNC 远程路径。

import (
	"os"
	"path/filepath"
	"testing"

	"drop/core/history"
)

func TestCheckOpenable(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dataDir)

	recvDir := filepath.Join(t.TempDir(), "Drop")
	mustMkdir(t, recvDir)
	// 前缀相近的邻居目录：前缀字符串比较会误放行，必须拒绝
	sibling := recvDir + "Evil"
	mustMkdir(t, sibling)

	// 一条历史记录：落盘文件 + 其所在目录
	received := filepath.Join(recvDir, "photo.jpg")
	mustWrite(t, received)
	history.Append(history.Entry{Dir: "recv", Name: "photo.jpg", Path: received, Status: "ok"})

	s := &Server{}
	s.cfg.RecvDir = recvDir

	allow := []string{
		received,                        // 历史落盘文件
		recvDir,                         // 接收目录本身
		filepath.Join(recvDir, "a.txt"), // 接收目录之下（即便文件不存在，路径合法）
		projectURL,                      // 关于卡的项目主页
	}
	for _, p := range allow {
		if err := s.checkOpenable(p); err != nil {
			t.Errorf("应放行 %q: %v", p, err)
		}
	}

	deny := []string{
		filepath.Join(sibling, "x.exe"), // 前缀相近的邻居目录
		sibling,                         // 邻居目录本身
		filepath.Join(recvDir, "..", "outside.exe"), // 穿越出接收目录
		`C:\Windows\System32\calc.exe`,              // 任意绝对路径
		`\\evil-host\share\payload.exe`,             // UNC 远程路径
		`//evil-host/share/payload.exe`,             // UNC 的另一种写法
		"relative/path.txt",                         // 相对路径
		"https://evil.example.com/x",                // 非项目主页 URL
	}
	for _, p := range deny {
		if err := s.checkOpenable(p); err == nil {
			t.Errorf("应拒绝 %q", p)
		}
	}
}

// 扩展目录是「打开目录」按钮的目标，必须放行（不受接收目录白名单影响）。
func TestCheckOpenableAllowsExtensionDir(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	dir := FirefoxExtDir()
	if dir == "" {
		t.Skip("未找到扩展目录（exe 同级无 extension/firefox），跳过")
	}
	s := &Server{}
	if err := s.checkOpenable(dir); err != nil {
		t.Fatalf("扩展目录应放行 %q: %v", dir, err)
	}
}

// underDir 是白名单的核心判定，单独钉死：
// 「等于或在其下」放行，前缀相近的兄弟目录必须拒绝。
// 上面 TestCheckOpenableAllowsExtensionDir 在测试二进制路径下会跳过，
// 这里保证判定逻辑本身始终有覆盖。
func TestUnderDir(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "Downloads", "Drop")
	cases := []struct {
		path string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "a.txt"), true},
		{filepath.Join(root, "sub", "b.txt"), true},
		{root + "Evil", false},
		{filepath.Join(root+"Evil", "x.exe"), false},
		{filepath.Join("C:", "Users", "me", "Downloads"), false},
		{"", false},
	}
	for _, c := range cases {
		if got := underDir(c.path, root); got != c.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", c.path, root, got, c.want)
		}
	}
	if underDir("anything", "") {
		t.Error("空目录基准不应放行任何路径")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件 %s: %v", p, err)
	}
}

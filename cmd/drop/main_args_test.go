package main

// 浏览器拉起 Native Messaging 宿主时的参数形状。
//
// 这是一个**真实踩过的生产故障**：Chrome/Edge/Firefox 拉起宿主时第一个参数是
// 调用方 origin（`chrome-extension://<id>/`），而原实现只在「无参数」时才进宿主
// 模式 → 真实调用落到命令分发、打印「未知命令」后 exit 2，浏览器侧报
// `Error when communicating with the native messaging host`。
//
// 自测脚本恰好是无参数启动的，所以「自测通过、浏览器全挂」。这组用例钉死
// 「认 origin 参数」这一点，同时保证真正的命令行不会被误判。

import "testing"

func TestIsBrowserOrigin(t *testing.T) {
	cases := []struct {
		arg  string
		want bool
		note string
	}{
		{"chrome-extension://pgihdeeaofkecmpdldpagfhnehmibgkf/", true, "Chrome/Edge"},
		{"moz-extension://6f3a1e2b-1234-5678-9abc-def012345678/", true, "Firefox MV3"},
		{"drop@example.com", true, "Firefox 旧式 ID"},
		{"", false, "空参数"},
		{"run", false, "真实命令"},
		{"native-host", false, "真实命令"},
		{"install", false, "真实命令"},
		{"--help", false, "选项"},
		{"C:/some/path/dropw.exe", false, "路径"},
		{"chrome-extension:", false, "只有前缀没有 //"},
	}
	for _, c := range cases {
		if got := isBrowserOrigin(c.arg); got != c.want {
			t.Fatalf("%s（%q）: 期望 %v, got %v", c.note, c.arg, c.want, got)
		}
	}
}

// 真正的命令不能被当成宿主调用——否则 `dropw run` 会去读 stdin 挂住。
func TestCommandsNotMistakenForHost(t *testing.T) {
	for _, cmd := range []string{
		"run", "install", "uninstall", "pair", "devices", "send", "receive",
		"history", "sendto", "handle", "native-host", "discover", "id", "rename", "alias",
	} {
		if isBrowserOrigin(cmd) {
			t.Fatalf("命令 %q 被误判成浏览器 origin", cmd)
		}
	}
}

// Package platform 抹平系统能力差异：通知、托盘、资源管理器集成、
// 自启与防火墙。Windows 为真实现，其他平台为最小桩（保持核心可交叉编译）。
package platform

// Action 通知上的一个按钮，点击后经 drop: 协议回调本机。
type Action struct {
	Label string // 按钮文字
	URL   string // drop:open?path=... / drop:reveal?path=...
}

// Notif 一条系统通知。
type Notif struct {
	Title string
	Body  string
	// Actions 通知上的按钮。为空时点击通知本体走 OpenURL（若有）。
	Actions []Action
	// OpenURL 点击通知本体（非按钮）时的目标，通常传 drop:panel 打开管理面板。
	// 空表示点击无动作（纯告知类通知）。
	OpenURL string
}

// TrayItem 托盘菜单项。
type TrayItem struct {
	Label   string
	OnClick func()
}

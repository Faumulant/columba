package transfer

// DebugLog 可选调试输出钩子：手机桥接层（core/mobileapi）在 init 时挂接，
// 把发现/探测路径的关键日志导到 logcat 与调试文件；电脑端不设置，保持零输出。
var DebugLog func(format string, args ...any)

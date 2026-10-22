<div align="left">
    <picture>
        <source media="(prefers-color-scheme: dark)" srcset="android/app/src/main/res/drawable-nodpi/dove_white.png">
        <img alt="Columba 商标：衔着信封的斑鸠" src="android/app/src/main/res/drawable-nodpi/dove_black.png" width="96">
    </picture>

<h1>Columba（飞鸠）—— 配对一次，此后只发「发送到某设备」</h1>

[![Latest release](https://img.shields.io/github/v/release/Faumulant/columba?style=flat-square)](https://github.com/Faumulant/columba/releases/latest)
[![MIT Licensed](https://img.shields.io/badge/license-MIT-brightgreen.svg?style=flat-square)](LICENSE)
![CI](https://github.com/Faumulant/columba/workflows/CI/badge.svg)

</div>

仅局域网内的跨设备文件投递工具。没有账号、没有云、没有服务器：两端在同一局域网内直接建立 TLS 1.3 双向认证连接，文件不经任何第三方。桌面端是 Windows 上的两个 exe（`drop.exe` 命令行 + `dropw.exe` 后台），手机端是一个 APK。

## Requirements

| 端 | 要求 |
| --- | --- |
| Windows | 未声明并验证最低版本：`cmd/drop/manifest.xml` 的 `supportedOS` 列了 Windows 7 / 8 / 8.1 / 10 的 GUID，但 CI 只在 `windows-latest` 上跑过。运行不需要 Go |
| 安卓 | Android 8.0（`minSdk 26`）及以上，只提供 arm64-v8a |
| 网络 | 两端同网段，UDP 组播可达。不做中继，跨 NAT 不通 |
| 管理面板 | WebView2 Runtime 用于内嵌面板窗口。缺失时不报错，退回系统默认浏览器打开同一页面，功能不受影响 |
| 构建 | Go 1.27（`go.mod`）；安卓需 JDK 21 与 Android SDK |

## Installation

**Windows**：从 [Releases](https://github.com/Faumulant/columba/releases/latest) 下载 `Columba-0.1-setup.exe` 双击安装。安装器按用户安装到 `%LOCALAPPDATA%\Programs\Columba`，不需要管理员；唯一需要 UAC 的是末尾可选的防火墙规则，拒绝也不影响其余功能。装完托盘出现鸠的图标即表示后台已在运行，`drop:` 协议回调、Native Messaging 宿主、开机自启同时注册完成。

**安卓**：从同一页面下载 `Columba-0.1-arm64-v8a.apk` 传到手机点击安装。

> **两端均未做代码签名**：Windows 弹 SmartScreen（点「更多信息」→「仍要运行」），安卓提示「未知来源」（允许该来源安装）。这是所有未签名新软件的通用提示，不是病毒警告。

**从源码构建**（Windows）：

```bash
build.bat
```

产物 `drop.exe`（CLI）+ `dropw.exe`（后台 daemon，GUI 子系统，双击不出黑窗）。`build.bat` 会先清空 `GOWORK`/`GOFLAGS`：`go.work` 里的 `replace` 指向 `third_party/go-webview2`（面板内嵌必需的 fork），而 gomobile bind 反过来要求 `GOWORK=off`，两边都不能继承对方的环境。

## Configuration

安装器**不把安装目录加入 PATH**。下面的命令在 `%LOCALAPPDATA%\Programs\Columba` 下执行，或从源码根目录执行构建产物。

后台 daemon 的启动参数：

```bash
dropw.exe run                  # 常驻：托盘 + 自动接收 + 收件通知
dropw.exe run -port 42707      # TCP 监听端口，默认 42707
dropw.exe run -dir D:\收件      # 初始接收目录，默认 %USERPROFILE%\Downloads\Drop；面板里改过之后以面板为准
```

环境变量：

| 变量 | 作用 |
| --- | --- |
| `DROP_DEBUG` | 非空时在 stderr 输出发现与配对的状态日志 |
| `DROP_AUTO_CONFIRM` | 非空时配对请求自动确认，不读 stdin，用于无人值守部署 |

本机数据（设备身份、信任库、传输历史）在 `%LOCALAPPDATA%\Drop`。卸载默认保留，重装不需要重新配对。

## Quick start

配对只需一次：

```bash
drop pair              # 一方生成 6 位配对码，5 分钟内有效，一次性
drop pair 359226       # 另一方携码加入；发起方终端显示对方名称与指纹，按 y 确认
```

之后：

```bash
drop devices           # 已配对设备与在线/离线状态
drop rename 我的手机     # 改本机名（对方看到的显示名）
drop alias localhost 手机  # 给对方起备注名，显示与 -to 均优先用它
drop send 文件.zip       # 只会发给已配对设备；多台在线时用 -to 指定
```

日常投递不必敲命令：资源管理器里选中文件 → 右键 → 发送到 → 你的设备，后台完成传输并弹通知。SendTo 菜单按已配对设备自动维护。

## What it does

| 能力 | 入口 |
| --- | --- |
| 文件与文件夹投递 | `drop send`、右键「发送到」、面板拖拽 |
| 文本投递（不落盘成文件） | 面板「文本」页的输入框、浏览器扩展右键 |
| 网页内容投递 | 浏览器扩展：页面、选中文本、链接、图片、视频 |
| 管理面板 | 托盘 →「打开面板」，或 `drop panel`；左侧按文件 / 音频 / 图片 / 视频 / 文本五类浏览，可筛全部 / 收到 / 发出 |
| 设备发现与排障 | `drop discover`、`drop history -n 20`、`drop receive`（前台接收） |

完整命令列表见 `drop`（无参数运行即打印 usage），含安装用的 `drop install` / `drop firewall` / `drop uninstall`。

## Documentation

- [docs/protocol.md](docs/protocol.md) — 协议契约 v1（身份、发现、加密、配对、传输）。任何平台按此实现即可互通。
- [docs/release-process.md](docs/release-process.md) — 构建、打包、打 tag、发 draft release 的完整步骤。
- [docs/release-notes-v0.1.md](docs/release-notes-v0.1.md) — v0.1 发布说明。
- [docs/ui-preview/](docs/ui-preview/) — 面板与弹窗的样式预览。

## Testing

```bash
go test -count=1 -timeout 300s ./...
```

覆盖 `cmd/drop`、`core/discover`、`core/history`、`core/identity`、`core/ipc`、`core/mobileapi`、`core/transfer`、`platform` 八个包。CI 另跑 `gofmt -l cmd core platform tools` 与 `go vet ./...`（`platform_windows.go` 的 4 条历史 `unsafe.Pointer` 提示已单独豁免，理由见 [`.github/workflows/ci.yml`](.github/workflows/ci.yml) 注释）。

`platform` 包里有三条与 Windows 路径编码相关的回归用例：中文 / emoji / 韩文设备名、SendTo 目录本身含非 ANSI 字符、以及 8.3 短名与长名的等价性。它们守的是两个容易复发的坑——`WScript.Shell` 的 `Save()` 按系统 ANSI 代码页转换**整条路径**（失败时静默，不报错也不建文件），以及 PowerShell 回显的 `TargetPath` 可能保留 8.3 短名而 `os.Executable()` 返回长名。改动快捷方式相关代码后务必让它们保持绿色。

浏览器扩展与 Native Messaging 宿主需要先构建 `dropw.exe`：

```bash
node tools/test_extension.js            # 用假 chrome API 跑一遍 background.js
python tools/test_native_host.py        # 带 origin 参数启动宿主，模拟浏览器真实调用
```

## 设计决策

| 决策点 | 结论 |
| --- | --- |
| 技术栈 | Go 单二进制；依赖 `fyne.io/systray`、`jchv/go-webview2`、`golang.org/x/net`、`golang.org/x/sys` |
| 发现 | UDP 组播（`224.0.0.240:42708`），逐网卡加入与宣告，不用 mDNS 库 |
| 加密 | TLS 1.3 双向证书指纹互认，不用 CA 链；配对码仅作一次性授权凭证，不参与密钥派生 |
| 设备身份 | 首次运行生成 Ed25519 密钥对与 10 年期自签证书；设备 ID = 证书 DER 的 SHA-256 |
| 接收行为 | 已信任设备自动接收，仅弹通知 |
| 落盘 | `.part` + SHA-256 双端校验 + 原子改名；同名自动改写为 `photo (1).jpg`，绝不覆盖 |
| 路径安全 | 清单携带相对路径，落盘前拒绝绝对路径与目录穿越 |

## 目录结构

```text
cmd/drop/           唯一二进制入口：CLI 命令、安装/卸载、Native Messaging 宿主
core/protocol/      线上消息与契约（平台无关）
core/identity/      设备身份、信任库、TLS 指纹互认
core/discover/      UDP 组播发现（逐网卡加入与宣告、单播心跳）
core/transfer/      清单、流式上传、.part + SHA-256 + 原子改名、同名冲突
core/history/       本地传输历史（JSONL）
core/ipc/           daemon 内置 HTTP API + 内嵌管理面板（go:embed）
core/mobileapi/     Go → Android 桥接层（gomobile bind）
platform/           Windows 托盘 / 通知 / 注册表 / 面板窗口
extension/          Firefox 与 Chromium 扩展
android/            Kotlin + Jetpack Compose 工程
installer/          Inno Setup 6 安装器脚本
tools/              扩展与 Native Messaging 的离线测试脚本
docs/               协议契约与发布流程
```

## 浏览器扩展

扩展只传**网页里看到的东西**：页面、选中文本、链接、图片、视频（右键菜单），明确不做文件拖拽。链路是扩展 → Native Messaging（`dropw.exe`，凭据不出浏览器）→ daemon IPC → 目标设备。

- **Chrome / Edge**：`chrome://extensions` → 开发者模式 → 加载 `extension/chromium/`。manifest 内置固定 key，扩展 ID 恒为 `pgihdeeaofkecmpdldpagfhnehmibgkf`
- **Firefox**：需要 Developer Edition（正式版强制签名无法关闭）。面板 ⚙ 设置 → 浏览器扩展 → 一键安装，重启 Firefox 即出现。签名豁免由 `drop install` 以管理员权限写入 Firefox 安装目录，或手动 `about:config` → `xpinstall.signatures.required` → `false`

## 安卓端

底栏三个入口：接收页 / 发送（FAB）/ 设置。接收页按五类浏览记录，文本以文本本体呈现。设置里可切「自动保存（选接收文件夹）」与「手动保存」，可开图片同时入相册，可开后台常驻接收。

构建需要先重建 AAR（必须在仓库根，`GOWORK=off`）。`android/app/libs/` 不入库，需先创建：

```bash
GOWORK=off gomobile bind -target=android -androidapi 26 -ldflags "-s -w" \
  -o android/app/libs/mobileapi.aar ./core/mobileapi
cd android && ./gradlew :app:assembleRelease
```

## 已知边界

- **仅局域网**：不做中继，跨网段 / 跨 NAT 不通。
- **未做代码签名**：Windows 会弹 SmartScreen，安卓需允许「未知来源」。
- **断点续传未做**：中断即整传重试（网络抖动时同一文件自动重试一次）。
- **macOS / iOS 未实现**：`platform/platform_other.go` 只保留最小实现让核心可编译，系统集成留待各平台原生客户端。
- **APK 只发 arm64-v8a**：`splits.abi` 已配好完整包与每架构分包，其余架构需自行构建。

## 许可

MIT，见 [LICENSE](LICENSE)。

---

[![](https://img.shields.io/badge/powered_by-dsh-4D6BFE?style=flat-square&logo=deepseek&logoColor=white)](https://github.com/deepseek-ai/deepseek-harness)

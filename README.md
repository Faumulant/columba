# Columba（飞鸠）

极轻量、仅局域网的跨设备文件投递工具。**配对一次，此后只需要"发送到某设备"。**

没有账号、没有云、没有服务器：两端在同一局域网内直接建立 TLS 1.3 双向认证连接，
文件不经任何第三方。桌面端是单个 exe（约 9.5 MB，无运行时依赖），手机端是一个 APK。

## 现在的状态

| 端 | 形态 | 状态 |
|---|---|---|
| Windows 桌面 | `drop.exe`（CLI）+ `dropw.exe`（后台 daemon + 托盘 + 管理面板） | 已用真实局域网验证 |
| Android | `app-release.apk`（Compose 原生 UI，复用 Go 核心） | 已用真实设备验证 |
| 浏览器扩展 | Firefox / Chromium（MV3），右键发送网页内容 | 已实现，待真机右键验收 |

功能覆盖：核心链路 / 配对与信任 / 系统集成 / 管理面板 / 浏览器扩展 /稳定性打磨 / 安卓端。后续按实际使用反馈迭代。

## 构建

桌面端（Windows，需 Go 1.27+）：

```bash
build.bat
```

产物：`drop.exe`（CLI）+ `dropw.exe`（后台 daemon，GUI 子系统，双击不出黑窗）。

> `build.bat` 会先清空 `GOWORK`/`GOFLAGS`：`go.work` 里的 `replace` 指向
> `third_party/go-webview2`（面板嵌入必需的 fork），而 gomobile bind 反过来要求
> `GOWORK=off`。两边都不能继承对方的环境。

手机端：

```bash
# 1. 重建 AAR（必须在仓库根，GOWORK=off）
GOWORK=off gomobile bind -target=android -androidapi 26 -ldflags "-s -w" \
  -o android/app/libs/mobileapi.aar ./core/mobileapi

# 2. 构建 APK
cd android && ./gradlew :app:assembleRelease
```

`JAVA_HOME` 需 JDK 21，`ANDROID_HOME` 指向 Android SDK。AAR 是本地构建产物，不入库。

## 用法

### 桌面：一次性安装

```bash
drop install      # 防火墙规则（需管理员）+ drop: 协议 + 开机自启 + 扩展宿主
drop run          # 常驻后台：托盘图标 + 自动接收 + 收件通知
```

之后资源管理器里 **选中文件 → 右键 → 发送到 → 我的手机**，后台完成传输并弹通知
（已接收的通知带「打开 / 打开所在位置」）。SendTo 菜单按已配对设备自动维护。

### 配对（一次，终身互信）

```bash
./drop pair                   # 一方生成 6 位配对码，5 分钟有效
./drop pair 359226            # 另一方携码加入；对方终端显示名称与指纹，按 y 确认
```

配对完成后：

```bash
./drop rename 我的手机         # 改本机名（对方看到的显示名）
./drop devices                # 已配对设备 + 在线/离线
./drop alias localhost 手机    # 给对方起备注名（显示与 -to 均优先用它）
./drop send 文件.zip           # 只会发给已配对设备
```

### 桌面：管理面板

托盘右键 →「打开面板」（或 `drop panel`），以 Edge App 模式弹出独立窗口：

- 顶部下拉选目标设备（在线状态圆点）
- 按文件 / 音频 / 图片 / 视频 / 文本浏览传输记录，可按「全部/收到/发出」筛选
- 文本以文本本体传输（不落盘成文件），页顶输入框可直接发文字
- 文件拖进窗口任意位置 = 后台发送给选中设备
- ⚙ 设置：接收文件夹、设备管理、备注名、开机自启、主题、扩展安装

### 手机端

底栏三控件：接收页 / 发送（FAB）/ 设置。接收页按五类浏览记录，文本以文本本体
呈现；设置里可切「自动保存（选接收文件夹）」与「手动保存」，可开图片同时入相册、
可开后台常驻接收。

### 浏览器扩展

扩展只传**网页里看到的东西**：页面、选中文本、链接、图片、视频（右键菜单），
明确不做文件拖拽。链路：扩展 → Native Messaging（`dropw.exe`，凭据不出浏览器）
→ daemon IPC → 目标设备。

- **Chrome / Edge**：`chrome://extensions` → 开发者模式 → 加载 `extension/chromium/`
  （manifest 内置固定 key，扩展 ID 恒为 `pgihdeeaofkecmpdldpagfhnehmibgkf`）
- **Firefox**：需要 Developer Edition（正式版强制签名无法关闭），面板 ⚙ 设置 →
  浏览器扩展 → 一键安装，重启 Firefox 即出现

## 设计决策

| 决策点 | 结论 |
|---|---|
| 技术栈 | Go 单二进制；除 `golang.org/x/net` 等少量依赖外无第三方框架 |
| 发现 | UDP 组播（224.0.0.240:42708），逐网卡加入与宣告，不用 mDNS 库 |
| 加密 | TLS 1.3 双向证书指纹互认；配对码仅作一次性授权凭证，不参与密钥派生 |
| 接收行为 | 已信任设备自动接收，仅弹通知 |
| 落盘 | `.part` + SHA-256 双端校验 + 原子改名；同名自动改写为 `photo (1).jpg`，绝不覆盖 |
| 协议契约 | [docs/protocol.md](docs/protocol.md)，任何平台按此实现即可互通 |

## 目录结构

```text
cmd/drop/           桌面 CLI 入口（run/pair/send/install/native-host…）
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
docs/               协议契约与发布流程
```

## 已知边界

- **仅局域网**：不做中继，跨网段/跨 NAT 不通。
- 正式版 Firefox 无法安装未签名扩展 → 用 Developer Edition 或篡改猴脚本版。
- 断点续传未做：中断即整传重试（小文件成本可接受）。
- macOS / iOS 未实现。

## 许可

MIT，见 [LICENSE](LICENSE)。

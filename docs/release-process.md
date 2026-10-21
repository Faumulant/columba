# 发布流程（Releases）

> 2026-10-22 首次跑通（v0.1）。照这份做，别凭记忆。

## 前置

- 工作区干净：`git status --short` 无输出
- `core/ipc/server.go` 的 `Version` 常量与要打的 tag 对齐（`Version = "0.1"` ↔ tag `v0.1`）
- 版本信息已嵌入 exe：`cmd/drop/versioninfo.json` 里的 FileVersion / ProductVersion
  要同步改（否则「属性 → 详细信息」显示旧版本，也会影响杀软启发式评分）

## 步骤

```bash
cd <仓库根目录>

# 1. 构建产物（必须用 build.bat：它清 GOWORK，保证用 fork 的 go-webview2）
cmd //c build.bat

# 2. 构建安装包
cd installer
"/c/Users/<你>/AppData/Local/Programs/Inno Setup 6/ISCC.exe" columba.iss
cd ..

# 3. 汇总到 dist/ 并生成校验和
mkdir -p dist
cp drop.exe dropw.exe installer/dist/Columba-<版本>-setup.exe dist/
cd dist && sha256sum drop.exe dropw.exe Columba-<版本>-setup.exe > SHA256SUMS.txt && cd ..

# 4. 打 tag 并推送
git tag -a v<版本> -m "Columba v<版本>"
git push origin v<版本>

# 5. 创建 draft release（草稿：只有协作者可见，不对外）
gh release create v<版本> --draft \
  --title "Columba（飞鸠）v<版本>" \
  --notes-file docs/release-notes-v<版本>.md \
  dist/drop.exe dist/dropw.exe dist/Columba-<版本>-setup.exe dist/SHA256SUMS.txt

# 6. 核对
gh release view v<版本> --json isDraft,name,tagName,assets
```

## 注意

**`dist/` 里的旧文件会混进 release。** 首次跑时发现一个 9-26 的
`drop-extension.xpi` 躺在里面（早已过时的扩展包）。上传前先 `ls dist/` 看一眼，
只留本次要发的文件。

**SHA256 会由 GitHub 独立计算一次。** `gh release view --json assets` 里的
`digest` 字段是 GitHub 自己算的，与本地 `SHA256SUMS.txt` 对比可确认上传无损。

**draft 的 URL 形如 `.../releases/tag/untagged-xxxx`** —— 这是正常的：
draft 还没有正式 tag 指向，发布后才会变成 `.../releases/tag/v0.1`。

## 私有仓库下的更新检查（已实测）

`GET https://api.github.com/repos/Faumulant/columba/releases/latest`
在**私有仓库**下返回 **404**。代码里 `if resp.StatusCode != http.StatusOK { return }`
已静默跳过，实测：daemon 正常运行、`/api/state` 的 `latest` 为空、stderr 无报错。

**公开后已复测**（2026-10-22）：同一 URL 返回 **200**，`tag_name = v0.1`，
附件 `HTTP 200` 可下载。更新检查端到端验证：把 `Version` 临时改成 `0.0`
编译一份，运行 15 秒后 `/api/state` 的 `latest` 变成 `0.1` —— 功能确实生效。

## 发布（公开）后的检查清单

- [x] 仓库可见性改为 public（2026-10-22 已完成）
- [x] draft release 改为 Publish
- [x] `/releases/latest` 返回 200 且 `tag_name` 正确
- [x] 旧版本（0.0）能检测到新版本（`latest: 0.1`）
- [ ] README 里的下载链接指向 releases 页

## 尚未做（发布前置）

- **代码签名**：安装包与 exe 均未签名，SmartScreen 会拦、部分杀软会误报
  （实测 Windows Defender 曾判 `Trojan:Win32/Bearfoos.A!ml` 并删除文件）。
  release notes 里已写明「点更多信息 → 仍要运行」，但这是权宜之计。
- **安卓端分发**：APK 未进 release。需要单独渠道（或同一 release 里加附件）。

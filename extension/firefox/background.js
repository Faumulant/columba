// Drop 浏览器扩展后台：右键菜单 → Native Messaging → 本机 daemon。
//
// 菜单结构（标题会显示当前目标设备，用户一眼能看出会发给谁）：
//   Drop → 我的手机
//     ├ 发送此页面          （发页面地址）
//     ├ 发送选中文字        （发选中文本）
//     ├ 发送此链接          （发链接地址）
//     ├ 发送此图片          （**下载图片本体**后作为文件发送）
//     ├ 发送此视频          （**下载视频本体**后作为文件发送；过大则提示改用链接）
//     ├ 发送此图片的链接    （只发地址，轻量备选）
//     └ 发送到…  ▸  [设备A / 设备B（离线）]   ← 仅多设备时出现
//
// 命名原则：菜单文字说「发什么」，不说「发到哪个分类」。早期版本叫
// 「发送图片 / 发送视频」，让用户误以为发过去会变成图片文件、或被归类到
// 「图片」分类里——实际只发了 URL 字符串。现在动作与结果一致。

const HOST = "drop.drop";

let port = null;
// 串行队列：宿主逐条处理并回信、且回信不带请求 id，扩展同一时刻只允许
// 一个在途请求，否则连点右键时旧响应会被新请求取走（B 拿到 A 的结果）。
let queue = Promise.resolve();
let waiter = null; // 在途请求的收尾句柄：断开或发送失败时结算，避免队列永久挂起

function send(req) {
  const run = queue.then(() => dispatch(req));
  queue = run.then(() => undefined, () => undefined); // 单个失败不断链，后续请求继续
  return run;
}

function dispatch(req) {
  return new Promise(resolve => {
    if (!port) {
      port = chrome.runtime.connectNative(HOST);
      port.onDisconnect.addListener(() => {
        port = null;
        if (waiter) { const w = waiter; waiter = null; w(null); }
        if (chrome.runtime.lastError) console.warn("宿主断开:", chrome.runtime.lastError.message);
      });
    }
    const p = port; // 断开后 port 置空，结算时仍需用旧句柄摘除监听
    const settle = msg => {
      p.onMessage.removeListener(settle);
      if (waiter === settle) waiter = null;
      resolve(msg);
    };
    p.onMessage.addListener(settle);
    waiter = settle;
    try {
      p.postMessage(req); // Native Messaging 单消息上限 1MB，超大图片/选中文本会同步抛错
    } catch (e) {
      settle({ ok: false, message: String(e) }); // 消息根本没发出，立即结算成失败并提示
    }
  });
}

// sendFile 让 daemon 去下载并发送媒体本体。
//
// 为什么不在扩展里下载再上传：IPC token 由 native-host 持有、从不进入扩展
// （这是有意的安全设计，不能为了这个功能破坏它）。扩展只把 URL 交过去，
// Go 侧自己下载——顺带还能流式处理、不受扩展内存与 Native Messaging
// 单条 1MB 上限的约束。
async function sendFile(url, kind) {
  const st = await send({ path: "/api/send-url", method: "POST", body: { url, kind } });
  if (st && st.ok) return { ok: true };
  return { ok: false, message: (st && st.message) || "发送失败" };
}

// ─── 菜单构建 ────────────────────────────────────────────────
//
// 每次设备列表/目标变化都整体重建：contextMenus 的 create/remove 是异步的，
// 增量更新容易与 onInstalled 的首次创建撞车（同 id 重复创建会报错）。
// 重建前先 removeAll，保证幂等。

const MENU_ROOT = "root";
const CTX = ["page", "selection", "link", "image", "video", "audio"];

async function rebuildMenus() {
  const st = await send({ path: "/api/state", method: "GET" });
  const data = st && st.ok && st.data ? st.data : null;
  const devices = (data && data.devices) || [];
  const targetId = (data && data.target) || "";
  // daemon 没起来时 devices 为空：标题要说明「后台未运行」，而不是假装
  // 「未选择设备」——后者会让用户去面板里找设备，而真正的问题是后台没开。
  const daemonUp = !!(st && st.ok);
  const targetName = daemonUp
    ? ((devices.find(d => d.id === targetId) || {}).name || "（未选择设备）")
    : "（后台未运行）";

  await chrome.contextMenus.removeAll();

  // 根菜单标题带上目标设备名，右键时不用回想「现在发给谁」
  chrome.contextMenus.create({ id: MENU_ROOT, title: `Drop → ${targetName}`, contexts: CTX });

  // 文字类：发的是地址或选中文字（轻量、必成功）
  chrome.contextMenus.create({ id: "page", title: "发送此页面", parentId: MENU_ROOT, contexts: ["page"] });
  chrome.contextMenus.create({ id: "sel", title: "发送选中文字", parentId: MENU_ROOT, contexts: ["selection"] });
  chrome.contextMenus.create({ id: "link", title: "发送此链接", parentId: MENU_ROOT, contexts: ["link"] });

  // 媒体类：默认发本体（用户要的就是文件），另给一条「只发链接」的轻量选项
  chrome.contextMenus.create({ id: "img", title: "发送此图片", parentId: MENU_ROOT, contexts: ["image"] });
  chrome.contextMenus.create({ id: "video", title: "发送此视频", parentId: MENU_ROOT, contexts: ["video", "audio"] });
  chrome.contextMenus.create({ id: "imgurl", title: "只发送图片链接", parentId: MENU_ROOT, contexts: ["image"] });
  chrome.contextMenus.create({ id: "videourl", title: "只发送视频链接", parentId: MENU_ROOT, contexts: ["video", "audio"] });

  // 「发送到…」**始终显示**，即使只有一台设备。
  //
  // 早期版本只在多设备时显示，理由是「单设备多一层点选是负担」——这是错的：
  //   · 用户无法确认当前发往谁、也无法切换，而这是最基本的心智模型；
  //   · 配了第二台设备后菜单会突然多出一项，显得很莫名；
  //   · 「发送到…」本身就是「发现设备」的地方，藏起来等于让人找不到。
  // 单设备时它只多一层、内容一项，成本远小于「看不见自己在发给谁」。
  chrome.contextMenus.create({ id: "pick", title: "发送到…", parentId: MENU_ROOT, contexts: CTX });
  if (devices.length === 0) {
    chrome.contextMenus.create({
      id: "noop",
      title: daemonUp ? "（还没有配对设备，先在面板里添加）" : "（Columba 后台没在运行）",
      parentId: "pick",
      enabled: false,
      contexts: CTX,
    });
  }
  for (const d of devices) {
    const isCur = d.id === targetId;
    chrome.contextMenus.create({
      id: "dev:" + d.id,
      // 当前目标打勾（radio 让选中态由浏览器原生渲染，比手写 ✓ 更清楚）
      type: "radio",
      checked: isCur,
      title: d.name + (d.online ? "" : "（离线）"),
      parentId: "pick",
      contexts: CTX,
    });
  }
}

chrome.runtime.onInstalled.addListener(() => { rebuildMenus(); });
chrome.runtime.onStartup.addListener(() => { rebuildMenus(); });

// MV3 的 service worker 会休眠，而菜单是「一次建好就常驻」的静态结构。
// 期间设备可能增减、目标可能被面板改掉、daemon 可能重启，菜单就过期了。
//
// 刷新策略：定时 + 每次点击后。定时器在 worker 休眠时会被挂起，所以不能只靠它；
// 每次点击动作完成后再刷一次，保证「下一次右键」看到的是最新状态。
// 不用 chrome.alarms：它需要 alarms 权限，而 30 秒的精度对菜单足够，
// 且多一个权限对用户是多一份疑虑。
setInterval(() => { rebuildMenus(); }, 30000);

// ─── 点击处理 ────────────────────────────────────────────────

chrome.contextMenus.onClicked.addListener(async (info) => {
  const id = String(info.menuItemId);

  // 设备子菜单：切换 daemon 的当前目标，并立刻重建菜单让标题跟着变
  if (id.startsWith("dev:")) {
    const r = await send({ path: "/api/select", method: "POST", body: { id: id.slice(4) } });
    await rebuildMenus();
    badge(r && r.ok ? "已切换" : "失败");
    return;
  }

  switch (id) {
    // 文字类
    case "page":
      return finish(doText(info.pageUrl));
    case "sel":
      return finish(doText((info.selectionText || "").trim()));
    case "link":
      return finish(doText(info.linkUrl));
    case "imgurl":
      return finish(doText(info.srcUrl));
    case "videourl":
      return finish(doText(info.srcUrl || info.frameUrl || ""));

    // 媒体本体
    case "img":
      return finish(doFile(info.srcUrl, "image"));
    case "video":
      return finish(doFile(info.srcUrl || info.frameUrl || "", "video"));

    default:
      return; // root / pick / noop 等非动作项
  }
});

// finish 动作结束后刷一次菜单：用户可能在面板里换了目标、或刚配对完新设备，
// 下一次右键应当看到最新状态。失败不影响已完成的发送。
async function finish(promise) {
  await promise;
  try { await rebuildMenus(); } catch (_) {}
}

async function doText(text) {
  if (!text) { badge("无内容"); return; }
  const resp = await send({ path: "/api/send-text", method: "POST", body: { text } });
  if (resp && resp.ok) badge("已发送");
  else badge("失败");
}

async function doFile(url, kind) {
  if (!url) { badge("无内容"); return; }
  if (!/^https?:/i.test(url)) {
    // blob:/data: 是页面内临时引用，daemon 取不到；给出可操作的提示
    badge("不支持");
    console.warn("Drop: 该媒体地址是 " + String(url).split(":")[0] + ":，无法跨进程下载，请改用「只发送链接」");
    return;
  }
  badge("下载中");
  const r = await sendFile(url, kind);
  if (r.ok) { badge("已发送"); return; }
  badge("失败");
  // 扩展没有通知能力，badge 只能显示 4 个字，所以详情记到 console
  // 供排查（扩展页「检查视图」可见）
  console.warn("Drop 发送文件失败:", r.message, url);
}

function badge(txt) {
  chrome.action.setBadgeText({ text: txt.slice(0, 4) });
  chrome.action.setBadgeBackgroundColor({ color: txt === "失败" ? "#A85A4C" : "#1B1C1A" });
  if (txt !== "下载中") setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
}

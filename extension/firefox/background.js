// Drop 浏览器扩展后台：右键菜单 → Native Messaging → 本机 daemon。
//
// ─── 菜单设计（2026-10-21 重做）──────────────────────
//
// 早期版本把 7 个动作项（页面/选中文字/链接/图片/视频/只发链接×2）全部挂在
// 同一组 contexts 上，结果右键一张图片会同时冒出「发送此页面」「发送此图片」
// 「只发送图片链接」等一堆项——用户分不清「动作」与「选择器」的区别。
//
// 现在改为**单一动作项 + 由右键位置决定发什么**：
//
//   Drop → 我的手机
//     └ 发送此图片            ← 文字随右键对象变化：此页面/此选中内容/此链接/此图片/此视频
//     └ 发送到…  ▸  [设备A ● / 设备B（离线）]   ← 仅多设备时出现
//
// 原则：右键位置已经表达了意图，不该再让用户选一次。
//   · 有选中文字            → 发文字
//   · 右键在图片/视频上      → 下载本体发文件；**抓不到就自动退回发链接**
//   · 右键在链接上           → 发链接地址
//   · 空白处                → 发页面地址
// 用户永远只需要点一次，且不需要判断「我该选哪项」。

const HOST = "drop.drop";

let port = null;
// 串行队列：宿主逐条处理并回信、且回信不带 request id，扩展同一时刻只允许
// 一个在途请求，否则连点右键时旧响应会被新请求取走（B 拿到 A 的结果）。
let queue = Promise.resolve();
let waiter = null;

function send(req) {
  const run = queue.then(() => dispatch(req));
  queue = run.then(() => undefined, () => undefined);
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
    const p = port;
    const settle = msg => {
      p.onMessage.removeListener(settle);
      if (waiter === settle) waiter = null;
      resolve(msg);
    };
    p.onMessage.addListener(settle);
    waiter = settle;
    try {
      p.postMessage(req);
    } catch (e) {
      settle({ ok: false, message: String(e) });
    }
  });
}

// ─── 菜单构建 ────────────────────────────────────────────────

const MENU_ROOT = "root";
const MENU_ACT = "act";
const MENU_PICK = "pick";
const CTX = ["page", "selection", "link", "image", "video", "audio"];

// actionTitle 动作项的文字：随右键对象变化，让用户一眼看出「点下去会发什么」。
// 这是整套设计的核心——不靠用户选，靠菜单自己说清楚。
function actionTitle(info) {
  if (!info) return "发送此页面";
  if (info.mediaType === "image") return "发送此图片";
  if (info.mediaType === "video" || info.mediaType === "audio") return "发送此视频";
  if (info.linkUrl) return "发送此链接";
  if (info.selectionText && info.selectionText.trim()) return "发送此选中文字";
  return "发送此页面";
}

async function rebuildMenus() {
  const st = await send({ path: "/api/state", method: "GET" });
  const data = st && st.ok && st.data ? st.data : null;
  const devices = (data && data.devices) || [];
  const targetId = (data && data.target) || "";
  const daemonUp = !!(st && st.ok);
  const targetName = daemonUp
    ? ((devices.find(d => d.id === targetId) || {}).name || "（未选择设备）")
    : "（后台未运行）";

  await chrome.contextMenus.removeAll();

  // 根菜单标题带上目标设备名，右键时不用回想「现在发给谁」
  chrome.contextMenus.create({ id: MENU_ROOT, title: `Drop → ${targetName}`, contexts: CTX });

  // 唯一动作项。Chrome 的 contexts 决定它出现在哪些右键场景，
  // 文字由 onClicked 的 info 动态判断——但菜单文字是建菜单时定的，
  // 所以这里用「发送此内容」这个中性说法，避免说错。
  // 用户点下去后按实际对象自动决定发什么，不需要他先做判断。
  chrome.contextMenus.create({
    id: MENU_ACT,
    title: "发送此内容",
    parentId: MENU_ROOT,
    contexts: CTX,
  });

  // 「发送到…」**始终显示**（单设备也要能看见发给谁、也能切换）。
  // 单设备时它只多一层、内容一项，成本远小于「看不见自己在发给谁」。
  chrome.contextMenus.create({ id: MENU_PICK, title: "发送到…", parentId: MENU_ROOT, contexts: CTX });

  if (devices.length === 0) {
    chrome.contextMenus.create({
      id: "noop",
      title: daemonUp ? "（还没有配对设备，先在面板里添加）" : "（Columba 后台没在运行）",
      parentId: MENU_PICK,
      enabled: false,
      contexts: CTX,
    });
  }
  for (const d of devices) {
    chrome.contextMenus.create({
      id: "dev:" + d.id,
      type: "radio",
      checked: d.id === targetId,
      title: d.name + (d.online ? "" : "（离线）"),
      parentId: MENU_PICK,
      contexts: CTX,
    });
  }
}

chrome.runtime.onInstalled.addListener(() => { rebuildMenus(); });
chrome.runtime.onStartup.addListener(() => { rebuildMenus(); });

// MV3 的 service worker 会休眠，菜单是静态结构会过期（设备增减、目标被面板
// 改掉、daemon 重启）。30 秒定时 + 每次点击后刷新兜住；不用 chrome.alarms，
// 避免多要一个权限。
setInterval(() => { rebuildMenus(); }, 30000);

// ─── 点击处理 ────────────────────────────────────────────────

chrome.contextMenus.onClicked.addListener(async (info) => {
  const id = String(info.menuItemId);

  if (id.startsWith("dev:")) {
    const r = await send({ path: "/api/select", method: "POST", body: { id: id.slice(4) } });
    await rebuildMenus();
    badge(r && r.ok ? "已切换" : "失败");
    return;
  }
  if (id !== MENU_ACT) return; // root / pick / noop

  await handleSend(info);
  try { await rebuildMenus(); } catch (_) {}
});

// handleSend 由右键位置决定发什么——这是本设计的核心，用户不需要选。
//
// 优先级：媒体 > 选中文字 > 链接 > 页面。
// 媒体放最前是因为「在图片上右键」是最明确的手势；而 Chrome 在图片上右键时
// 也可能带 selectionText（图注被选中），那种情况用户更可能想要图片。
async function handleSend(info) {
  // 1) 媒体本体（抓不到就自动退回发链接，不给用户报错）
  const mediaUrl = info.srcUrl;
  if (mediaUrl && (info.mediaType === "image" || info.mediaType === "video" || info.mediaType === "audio")) {
    if (/^https?:/i.test(mediaUrl)) {
      badge("下载中");
      const r = await sendFile(mediaUrl, info.mediaType);
      if (r.ok) { badge("已发送"); return; }
      // 抓取失败：自动退回发链接。用户要的是「把这个东西发过去」，
      // 退一步发地址仍然满足大部分场景，比直接失败好。
      console.warn("Drop: 媒体本体抓取失败，改为发送链接：", r.message);
      const fb = await sendText(mediaUrl);
      badge(fb ? "已发链接" : "失败");
      return;
    }
    // blob:/data: 是页面内引用，daemon 取不到 → 直接发链接（虽然是临时地址，
    // 但至少让用户知道发生了什么，而不是静默失败）
    const fb = await sendText(mediaUrl);
    badge(fb ? "已发链接" : "失败");
    return;
  }

  // 2) 选中文字
  const sel = (info.selectionText || "").trim();
  if (sel) {
    badge((await sendText(sel)) ? "已发送" : "失败");
    return;
  }

  // 3) 链接
  if (info.linkUrl) {
    badge((await sendText(info.linkUrl)) ? "已发送" : "失败");
    return;
  }

  // 4) 页面地址
  badge((await sendText(info.pageUrl || "")) ? "已发送" : "失败");
}

async function sendText(text) {
  if (!text) return false;
  const r = await send({ path: "/api/send-text", method: "POST", body: { text } });
  return !!(r && r.ok);
}

// sendFile 让 daemon 去下载并发送媒体本体。
//
// 为什么不在扩展里下载再上传：IPC token 由 native-host 持有、从不进入扩展
// （有意的安全设计）。扩展只把 URL 交过去，Go 侧自己下载——顺带能流式处理，
// 不受扩展内存与 Native Messaging 单条 1MB 上限约束。
async function sendFile(url, kind) {
  const st = await send({ path: "/api/send-url", method: "POST", body: { url, kind } });
  if (st && st.ok) return { ok: true };
  return { ok: false, message: (st && st.message) || "发送失败" };
}

function badge(txt) {
  chrome.action.setBadgeText({ text: txt.slice(0, 4) });
  chrome.action.setBadgeBackgroundColor({ color: txt === "失败" ? "#A85A4C" : "#1B1C1A" });
  if (txt !== "下载中") setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
}

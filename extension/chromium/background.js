// Drop 浏览器扩展后台：右键菜单 → Native Messaging → 本机 daemon。
//
// ─── 菜单就是「发送到设备」，没有别的 ─────────────────────────
//
//   Drop → 我的手机            ← 只有一台设备时，这一项直接就是动作
//   或
//   Drop                       ← 多台设备时才分层
//     ├ 发送到 我的手机
//     └ 发送到 平板（离线）
//
// 用户只做一件事：选中要发的东西 → 右键 → 发送到某台设备。
// 发过去的是图片、视频、文字还是链接，**由右键指向的对象决定，用户不参与**：
//   · 在图片/视频上右键 → 发文件本体；抓不到就发它的地址
//   · 选中了文字        → 发文字
//   · 在链接上          → 发链接地址
//   · 空白处            → 发页面地址
//
// 菜单里刻意不出现「图片」「视频」「文字」「文件」这些词——那是实现细节。
// 早期版本按内容类型拆成 7 个菜单项，问题在于：
//   用户分不清「动作」与「选择器」，也分不清发的是文本还是文件。

const HOST = "drop.drop";

let port = null;
// 串行队列：宿主逐条处理并回信、且回信不带 request id，扩展同一时刻只允许
// 一个在途请求，否则连点右键时旧响应会被新请求取走。
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

const CTX = ["page", "selection", "link", "image", "video", "audio"];
const ID_ROOT = "root";
const ID_SEND = "send:"; // send:<设备ID>

async function rebuildMenus() {
  const st = await send({ path: "/api/state", method: "GET" });
  const data = st && st.ok && st.data ? st.data : null;
  const devices = (data && data.devices) || [];
  const daemonUp = !!(st && st.ok);

  await chrome.contextMenus.removeAll();

  // 不可用时给一条明确原因，而不是让菜单空着或消失
  if (!daemonUp || devices.length === 0) {
    chrome.contextMenus.create({
      id: "hint",
      title: daemonUp ? "Drop（还没有配对设备）" : "Drop（后台没在运行）",
      enabled: false,
      contexts: CTX,
    });
    return;
  }

  const label = d => d.name + (d.online ? "" : "（离线）");

  // 只有一台设备：顶层直接是动作，不再多一层
  if (devices.length === 1) {
    chrome.contextMenus.create({
      id: ID_SEND + devices[0].id,
      title: "发送到 " + label(devices[0]),
      contexts: CTX,
    });
    return;
  }

  // 多台设备：一层父菜单，每个设备一项
  chrome.contextMenus.create({ id: ID_ROOT, title: "Drop", contexts: CTX });
  for (const d of devices) {
    chrome.contextMenus.create({
      id: ID_SEND + d.id,
      title: "发送到 " + label(d),
      parentId: ID_ROOT,
      contexts: CTX,
    });
  }
}

chrome.runtime.onInstalled.addListener(() => { rebuildMenus(); });
chrome.runtime.onStartup.addListener(() => { rebuildMenus(); });

// MV3 的 service worker 会休眠，菜单是静态结构会过期（设备增减、daemon 重启）。
// 30 秒定时 + 每次点击后刷新兜住；不用 chrome.alarms，避免多要一个权限。
setInterval(() => { rebuildMenus(); }, 30000);

// ─── 点击：切到该设备，然后把右键指向的东西发过去 ──────────────

chrome.contextMenus.onClicked.addListener(async (info) => {
  const id = String(info.menuItemId);
  if (!id.startsWith(ID_SEND)) return; // hint / root 等非动作项

  const devId = id.slice(ID_SEND.length);
  const sel = await send({ path: "/api/select", method: "POST", body: { id: devId } });
  if (!sel || !sel.ok) { badge("失败"); return; }

  await deliver(info);
  try { await rebuildMenus(); } catch (_) {}
});

// deliver 把「右键指向的那个东西」发过去。用户不参与判断。
//
// 只有一处技术分支是必须的：图片/视频要传文件本体（用户要的就是那个文件），
// 其余都是传一段文字。但这属于实现细节，界面上不出现任何相关字样。
async function deliver(info) {
  const media = info.srcUrl;
  const isMedia = media && (info.mediaType === "image" || info.mediaType === "video" || info.mediaType === "audio");

  if (isMedia) {
    if (/^https?:/i.test(media)) {
      badge("发送中");
      const r = await send({ path: "/api/send-url", method: "POST", body: { url: media, kind: info.mediaType } });
      if (r && r.ok) { badge("已发送"); return; }
      // 抓不到本体就发地址：用户要的是「把这个东西发过去」，退一步仍然有用
      console.warn("Drop: 本体抓取失败，改发地址：", r && r.message);
    }
    // blob:/data: 是页面内引用，daemon 取不到，同样改发地址
    badge((await sendText(media)) ? "已发地址" : "失败");
    return;
  }

  const text = (info.selectionText || "").trim() || info.linkUrl || info.pageUrl || "";
  badge((await sendText(text)) ? "已发送" : "失败");
}

async function sendText(text) {
  if (!text) return false;
  const r = await send({ path: "/api/send-text", method: "POST", body: { text } });
  return !!(r && r.ok);
}

function badge(txt) {
  chrome.action.setBadgeText({ text: txt.slice(0, 4) });
  chrome.action.setBadgeBackgroundColor({ color: txt === "失败" ? "#A85A4C" : "#1B1C1A" });
  if (txt !== "发送中") setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
}

// Drop 浏览器扩展后台：右键菜单 → Native Messaging → 本机 daemon。
//
// 菜单结构（菜单标题会显示当前目标设备，用户一眼能看出会发给谁）：
//   Drop → 我的手机
//     ├ 发送页面
//     ├ 发送选中文本
//     ├ 发送链接
//     ├ 发送图片
//     ├ 发送视频
//     └ 发送到…  ▸  [设备A / 设备B / …]   ← 仅多设备时出现
//
// 结果经 badge 反馈（3 秒后清空），同时 daemon 会弹系统通知。

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

// ─── 菜单构建 ────────────────────────────────────────────────
//
// 每次设备列表/目标变化都整体重建：contextMenus 的 create/remove 是异步的，
// 增量更新容易与 onInstalled 的首次创建撞车（同 id 重复创建会报错）。
// 重建前先 removeAll，保证幂等。

const MENU_ROOT = "root";

async function rebuildMenus() {
  // 取 state：devices + target
  const st = await send({ path: "/api/state", method: "GET" });
  const data = st && st.ok && st.data ? st.data : null;
  const devices = (data && data.devices) || [];
  const targetId = (data && data.target) || "";
  const targetName = (devices.find(d => d.id === targetId) || {}).name || "（未选择设备）";

  await chrome.contextMenus.removeAll();

  // 根菜单标题带上目标设备名，右键时不用回想「现在发给谁」
  const root = { id: MENU_ROOT, title: `Drop → ${targetName}`, contexts: ["page", "selection", "link", "image", "video"] };
  chrome.contextMenus.create(root);

  const items = [
    { id: "page", title: "发送页面" },
    { id: "sel", title: "发送选中文本" },
    { id: "link", title: "发送链接" },
    { id: "img", title: "发送图片" },
    { id: "video", title: "发送视频" },
  ];
  for (const it of items) {
    chrome.contextMenus.create({ ...it, parentId: MENU_ROOT, contexts: ["page", "selection", "link", "image", "video"] });
  }

  // 多设备才给「发送到…」子菜单：单设备时多一层点选纯属负担
  if (devices.length > 1) {
    chrome.contextMenus.create({ id: "pick", title: "发送到…", parentId: MENU_ROOT, contexts: ["page", "selection", "link", "image", "video"] });
    for (const d of devices) {
      chrome.contextMenus.create({
        id: "dev:" + d.id,
        title: d.name + (d.online ? "" : "（离线）"),
        parentId: "pick",
        contexts: ["page", "selection", "link", "image", "video"],
      });
    }
  }
}

chrome.runtime.onInstalled.addListener(() => { rebuildMenus(); });
chrome.runtime.onStartup.addListener(() => { rebuildMenus(); });

// ─── 点击处理 ────────────────────────────────────────────────

// pendingTarget 由「发送到…」子菜单临时指定；用完即清，避免污染后续点击。
let pendingTarget = "";

chrome.contextMenus.onClicked.addListener(async (info) => {
  const id = String(info.menuItemId);

  // 设备子菜单：记下目标，等用户再点一次具体动作（菜单会关闭，所以这里
  // 直接切换 daemon 的当前目标，并立刻重建菜单让标题跟着变）
  if (id.startsWith("dev:")) {
    const devId = id.slice(4);
    await send({ path: "/api/select", method: "POST", body: { id: devId } });
    await rebuildMenus();
    badge("已切换");
    return;
  }

  let text = "";
  switch (id) {
    case "page":  text = info.pageUrl; break;
    case "sel":   text = (info.selectionText || "").trim(); break;
    case "link":  text = info.linkUrl; break;
    case "img":   text = info.srcUrl; break;
    case "video": text = info.srcUrl || info.frameUrl || ""; break;
    default: return; // root / pick 等非动作项
  }
  if (!text) { badge("无内容"); return; }

  const resp = await send({ path: "/api/send-text", method: "POST", body: { text } });
  if (resp && resp.ok) badge("已发送");
  else badge("失败");
});

function badge(txt) {
  chrome.action.setBadgeText({ text: txt.slice(0, 4) });
  chrome.action.setBadgeBackgroundColor({ color: txt === "失败" ? "#A85A4C" : "#1B1C1A" });
  setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
}

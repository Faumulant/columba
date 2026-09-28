// Drop 浏览器扩展后台：右键菜单 → Native Messaging → 本机 daemon。
// 发送目标使用面板中选中的设备；结果经系统通知反馈（daemon 弹出）。

const HOST = "drop.drop";
let port = null;

function send(req) {
  return new Promise(resolve => {
    if (!port) {
      port = chrome.runtime.connectNative(HOST);
      port.onDisconnect.addListener(p => {
        port = null;
        if (chrome.runtime.lastError) console.warn("宿主断开:", chrome.runtime.lastError.message);
      });
    }
    const handler = msg => { port.onMessage.removeListener(handler); resolve(msg); };
    port.onMessage.addListener(handler);
    port.postMessage(req);
  });
}

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({ id: "page", title: "把页面发送到设备", contexts: ["page"] });
  chrome.contextMenus.create({ id: "sel", title: "发送选中文本", contexts: ["selection"] });
  chrome.contextMenus.create({ id: "link", title: "发送链接", contexts: ["link"] });
  chrome.contextMenus.create({ id: "img", title: "发送图片", contexts: ["image"] });
  chrome.contextMenus.create({ id: "video", title: "发送视频", contexts: ["video"] });
});

chrome.contextMenus.onClicked.addListener(async (info) => {
  let text = "";
  let what = "";
  switch (info.menuItemId) {
    case "page":  text = info.pageUrl;  what = "页面"; break;
    case "sel":   text = (info.selectionText || "").trim(); what = "选中文本"; break;
    case "link":  text = info.linkUrl;  what = "链接"; break;
    case "img":   text = info.srcUrl;   what = "图片"; break;
    case "video": text = info.srcUrl || info.frameUrl || ""; what = "视频"; break;
  }
  if (!text) { badge("无内容"); return; }
  const resp = await send({ path: "/api/send-text", method: "POST", body: { text } });
  if (resp && resp.ok) badge("已发送");
  else badge("失败");
});

function badge(txt) {
  chrome.action.setBadgeText({ text: txt.slice(0, 4) });
  setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
}

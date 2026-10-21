// 扩展逻辑离线验证：用假的 chrome API 跑一遍 background.js。
//
// 核心设计原则（改动前先读这段，别再改回去）：
//   1. 菜单就是「发送到设备」，不出现图片/视频/文字/文件这些词——
//      发什么由右键指向的对象决定，不额外让人判断。
//   2. 单设备时顶层直接是动作，不再多一层。
//   3. 图片/视频发本体；抓不到就发地址（不报错）。
//   4. 其余发一段文字（选中文字 > 链接 > 页面地址）。

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const BG = path.join(__dirname, "..", "extension", "chromium", "background.js");

function makeEnv(state, opts) {
  opts = opts || {};
  const listeners = {};
  const created = [];
  const selected = [];
  const sentTexts = [];
  const sentURLs = [];

  const chrome = {
    runtime: {
      connectNative() {
        let onMsg = null;
        return {
          onDisconnect: { addListener() {} },
          onMessage: { addListener(fn) { onMsg = fn; }, removeListener() {} },
          postMessage(req) {
            const resp = handle(req);
            setTimeout(() => onMsg && onMsg(resp), 0);
          },
        };
      },
      onInstalled: { addListener(fn) { listeners.installed = fn; } },
      onStartup: { addListener(fn) { listeners.startup = fn; } },
      lastError: null,
    },
    contextMenus: {
      create(o) { created.push(o); },
      removeAll() { created.length = 0; return Promise.resolve(); },
      onClicked: { addListener(fn) { listeners.clicked = fn; } },
    },
    action: {
      setBadgeText(o) { listeners.lastBadge = o.text; },
      setBadgeBackgroundColor() {},
    },
  };

  function handle(req) {
    if (req.path === "/api/state") {
      if (opts.daemonDown) return { ok: false, message: "无法连接 Columba 后台" };
      return { ok: true, data: { devices: state.devices, target: state.target } };
    }
    if (req.path === "/api/select") {
      selected.push(req.body.id);
      state.target = req.body.id;
      return { ok: true };
    }
    if (req.path === "/api/send-text") { sentTexts.push(req.body.text); return { ok: true }; }
    if (req.path === "/api/send-url") {
      sentURLs.push(req.body);
      if (opts.mediaFails) return { ok: false, message: "下载失败: HTTP 403" };
      return { ok: true };
    }
    return { ok: false, message: "unknown" };
  }

  const sandbox = { chrome, console, setTimeout, setInterval: () => 0, Promise, String };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(BG, "utf8"), sandbox, { filename: BG });
  return { created, listeners, selected, sentTexts, sentURLs };
}

const tick = () => new Promise(r => setTimeout(r, 20));

let failed = 0;
function check(name, cond, extra) {
  if (cond) console.log("  ✓", name);
  else { failed++; console.log("  ✗", name, extra === undefined ? "" : `（${extra}）`); }
}

async function run() {
  // 场景 1：单设备 —— 顶层直接是动作
  {
    console.log("单设备：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "我的手机", online: true }], target: "AAA" });
    env.listeners.installed();
    await tick();

    check("顶层只有一项", env.created.length === 1, `实际 ${env.created.length} 项`);
    const item = env.created[0];
    check("该项就是发送动作", item.id === "send:AAA");
    check("标题为「发送到 我的手机」", item.title === "发送到 我的手机", item.title);
    check("没有多层父菜单", !env.created.some(m => m.parentId));
  }

  // 场景 2：菜单里绝不出现内容类型字样（用户反复强调的那条）
  {
    console.log("菜单文案：");
    const env = makeEnv({
      devices: [{ id: "AAA", name: "手机", online: true }, { id: "BBB", name: "平板", online: false }],
      target: "AAA",
    });
    env.listeners.installed();
    await tick();

    const titles = env.created.map(m => m.title || "");
    const banned = ["图片", "视频", "文字", "文件", "链接", "页面", "内容", "选中"];
    const hit = banned.filter(w => titles.some(t => t.includes(w)));
    check("菜单不含任何内容类型字样", hit.length === 0, "出现了：" + hit.join("/"));
    check("多设备时父菜单叫 Drop", titles.includes("Drop"));
    check("每个设备一项", env.created.filter(m => String(m.id).startsWith("send:")).length === 2);
    check("离线设备有标注", titles.some(t => t.includes("平板") && t.includes("离线")));
  }

  // 场景 3：点设备项 → 先切目标，再发
  {
    console.log("点击分派：");
    const env = makeEnv({
      devices: [{ id: "AAA", name: "手机", online: true }, { id: "BBB", name: "平板", online: true }],
      target: "AAA",
    });
    env.listeners.installed();
    await tick();

    // 空白处 → 页面地址
    await env.listeners.clicked({ menuItemId: "send:BBB", pageUrl: "https://a.com/p" });
    await tick();
    check("先切到被点的设备", env.selected.at(-1) === "BBB");
    check("再发页面地址", env.sentTexts.at(-1) === "https://a.com/p");

    // 选中文字优先
    await env.listeners.clicked({ menuItemId: "send:BBB", pageUrl: "https://a.com/p", selectionText: "  字  " });
    await tick();
    check("选中文字优先且 trim", env.sentTexts.at(-1) === "字");

    // 链接优先于页面
    await env.listeners.clicked({ menuItemId: "send:BBB", pageUrl: "https://a.com/p", linkUrl: "https://a.com/l" });
    await tick();
    check("链接优先于页面", env.sentTexts.at(-1) === "https://a.com/l");

    // 图片 → 发本体
    const beforeURLs = env.sentURLs.length;
    const beforeTexts = env.sentTexts.length;
    await env.listeners.clicked({ menuItemId: "send:BBB", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "https://a.com/cat.png" });
    await tick();
    check("图片发本体", env.sentURLs.length === beforeURLs + 1);
    check("send-url 带 kind", env.sentURLs.at(-1).kind === "image");
    check("成功时不再额外发文字", env.sentTexts.length === beforeTexts);

    // 视频 → 发本体
    await env.listeners.clicked({ menuItemId: "send:BBB", pageUrl: "https://a.com/p", mediaType: "video", srcUrl: "https://a.com/v.mp4" });
    await tick();
    check("视频发本体", env.sentURLs.at(-1).url === "https://a.com/v.mp4");

    // 媒体 + 选中文字 → 媒体优先
    const b2 = env.sentURLs.length;
    await env.listeners.clicked({
      menuItemId: "send:BBB", pageUrl: "https://a.com/p",
      mediaType: "image", srcUrl: "https://a.com/x.png", selectionText: "图注",
    });
    await tick();
    check("媒体优先于选中文字", env.sentURLs.length === b2 + 1);

    // 点父菜单/提示项不应发东西
    const t0 = env.sentTexts.length, u0 = env.sentURLs.length, s0 = env.selected.length;
    await env.listeners.clicked({ menuItemId: "root" });
    await env.listeners.clicked({ menuItemId: "hint" });
    await tick();
    check("非动作项不发任何请求",
      env.sentTexts.length === t0 && env.sentURLs.length === u0 && env.selected.length === s0);
  }

  // 场景 4：本体抓取失败 → 自动发地址
  {
    console.log("抓取失败回退：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "手机", online: true }], target: "AAA" }, { mediaFails: true });
    env.listeners.installed();
    await tick();
    await env.listeners.clicked({ menuItemId: "send:AAA", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "https://a.com/locked.png" });
    await tick();
    check("抓取失败后发了地址", env.sentTexts.at(-1) === "https://a.com/locked.png");
    check("徽标提示已发地址", env.listeners.lastBadge === "已发地址", env.listeners.lastBadge);
  }

  // 场景 5：blob 地址直接发地址
  {
    console.log("blob 地址：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "手机", online: true }], target: "AAA" });
    env.listeners.installed();
    await tick();
    const before = env.sentURLs.length;
    await env.listeners.clicked({ menuItemId: "send:AAA", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "blob:https://a.com/123" });
    await tick();
    check("blob 不走 send-url", env.sentURLs.length === before);
    check("blob 发地址", env.sentTexts.at(-1) === "blob:https://a.com/123");
  }

  // 场景 6：异常状态给明确原因
  {
    console.log("异常状态：");
    const e1 = makeEnv({ devices: [], target: "" });
    e1.listeners.installed(); await tick();
    check("无设备时只有一条禁用提示",
      e1.created.length === 1 && e1.created[0].enabled === false && e1.created[0].title.includes("还没有配对设备"),
      JSON.stringify(e1.created.map(m => m.title)));

    const e2 = makeEnv({ devices: [], target: "" }, { daemonDown: true });
    e2.listeners.installed(); await tick();
    check("后台未运行说明原因",
      e2.created.length === 1 && e2.created[0].title.includes("后台没在运行"),
      JSON.stringify(e2.created.map(m => m.title)));
  }

  console.log(failed === 0 ? "\n全部通过" : `\n${failed} 项失败`);
  process.exit(failed === 0 ? 0 : 1);
}

run().catch(e => { console.error(e); process.exit(1); });

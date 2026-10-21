// 扩展逻辑离线验证：用假的 chrome API 跑一遍 background.js。
//
// 右键菜单本身只能人工点，但「菜单树结构 + 按右键位置决定发什么」是纯逻辑，
// 可以在 Node 里跑，CI 也能守住。核心断言是设计原则：
//   · 只有一个动作项（不给用户选「发什么」的机会）
//   · 动作由右键位置决定：媒体 > 选中文字 > 链接 > 页面
//   · 媒体抓取失败要自动退回发链接，而不是报错

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const BG = path.join(__dirname, "..", "extension", "chromium", "background.js");

// ─── 假 chrome / daemon ────────────────────────────────────
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
    if (req.path === "/api/send-text") {
      sentTexts.push(req.body.text);
      return { ok: true };
    }
    if (req.path === "/api/send-url") {
      sentURLs.push(req.body);
      // opts.mediaFails：模拟媒体抓取失败（用于验证「自动退回发链接」）
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
  // 场景 1：菜单结构 —— 只应有一个动作项
  {
    console.log("菜单结构：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "我的手机", online: true }], target: "AAA" });
    env.listeners.installed();
    await tick();

    const ids = env.created.map(m => m.id);
    const acts = env.created.filter(m => m.id === "act");
    check("只有一个动作项", acts.length === 1, `实际 ${acts.length} 个`);
    check("没有旧的多动作项（page/sel/img/video）",
      !ids.includes("page") && !ids.includes("sel") && !ids.includes("img") && !ids.includes("video"));
    check("根菜单标题带设备名", env.created.find(m => m.id === "root").title === "Drop → 我的手机");
    check("「发送到…」始终显示", ids.includes("pick"));
    const dev = env.created.find(m => m.id === "dev:AAA");
    check("当前目标 radio 勾选", dev && dev.type === "radio" && dev.checked === true);
  }

  // 场景 2：按右键位置决定发什么（核心设计）
  {
    console.log("按位置分派：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "手机", online: true }], target: "AAA" });
    env.listeners.installed();
    await tick();

    // 2a) 空白处 → 发页面地址
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/page" });
    await tick();
    check("空白处发页面地址", env.sentTexts.at(-1) === "https://a.com/page");

    // 2b) 选中文字 → 发文字（优先于页面）
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/page", selectionText: "  选中的字  " });
    await tick();
    check("选中文字发文字（且 trim）", env.sentTexts.at(-1) === "选中的字");

    // 2c) 链接 → 发链接
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/p", linkUrl: "https://a.com/l" });
    await tick();
    check("链接发链接地址", env.sentTexts.at(-1) === "https://a.com/l");

    // 2d) 图片 → 下载本体（走 send-url，不发链接）
    const beforeURLs = env.sentURLs.length;
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "https://a.com/cat.png" });
    await tick();
    check("图片走 send-url 发本体", env.sentURLs.length === beforeURLs + 1);
    check("send-url 带 kind=image", env.sentURLs.at(-1).kind === "image");
    check("send-url 带原始地址", env.sentURLs.at(-1).url === "https://a.com/cat.png");

    // 2e) 视频 → 同样走本体
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/p", mediaType: "video", srcUrl: "https://a.com/v.mp4" });
    await tick();
    check("视频走 send-url 发本体", env.sentURLs.at(-1).url === "https://a.com/v.mp4");

    // 2f) 媒体上右键但同时有选中文字 → 媒体优先（在图上右键是最明确的手势）
    const before2 = env.sentURLs.length;
    await env.listeners.clicked({
      menuItemId: "act", pageUrl: "https://a.com/p",
      mediaType: "image", srcUrl: "https://a.com/x.png", selectionText: "图注",
    });
    await tick();
    check("媒体优先于选中文字", env.sentURLs.length === before2 + 1);
  }

  // 场景 3：媒体抓取失败 → 自动退回发链接（不报错）
  {
    console.log("抓取失败回退：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "手机", online: true }], target: "AAA" }, { mediaFails: true });
    env.listeners.installed();
    await tick();
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "https://a.com/locked.png" });
    await tick();
    check("抓取失败后发了链接", env.sentTexts.at(-1) === "https://a.com/locked.png");
    check("徽标提示已发链接", env.listeners.lastBadge === "已发链接", env.listeners.lastBadge);
  }

  // 场景 4：blob: 地址直接发链接（daemon 取不到页面内引用）
  {
    console.log("blob 地址：");
    const env = makeEnv({ devices: [{ id: "AAA", name: "手机", online: true }], target: "AAA" });
    env.listeners.installed();
    await tick();
    const before = env.sentURLs.length;
    await env.listeners.clicked({ menuItemId: "act", pageUrl: "https://a.com/p", mediaType: "image", srcUrl: "blob:https://a.com/123" });
    await tick();
    check("blob 不走 send-url", env.sentURLs.length === before);
    check("blob 退回发链接", env.sentTexts.at(-1) === "blob:https://a.com/123");
  }

  // 场景 5：无设备 / 后台未运行
  {
    console.log("异常状态：");
    const e1 = makeEnv({ devices: [], target: "" });
    e1.listeners.installed(); await tick();
    const noop1 = e1.created.find(m => m.id === "noop");
    check("无设备给禁用提示", noop1 && noop1.enabled === false && noop1.title.includes("面板"));

    const e2 = makeEnv({ devices: [], target: "" }, { daemonDown: true });
    e2.listeners.installed(); await tick();
    const root2 = e2.created.find(m => m.id === "root");
    check("后台未运行时标题说明", root2.title.includes("后台未运行"), root2.title);
    const noop2 = e2.created.find(m => m.id === "noop");
    check("提示指向启动后台", noop2 && noop2.title.includes("后台"));
  }

  // 场景 6：切换设备
  {
    console.log("切换设备：");
    const env = makeEnv({
      devices: [{ id: "AAA", name: "手机", online: true }, { id: "BBB", name: "平板", online: false }],
      target: "AAA",
    });
    env.listeners.installed();
    await tick();
    await env.listeners.clicked({ menuItemId: "dev:BBB" });
    await tick();
    check("切换调用了 /api/select", env.selected.at(-1) === "BBB");
    check("切换后标题跟着变", env.created.find(m => m.id === "root").title === "Drop → 平板");
    const b = env.created.find(m => m.id === "dev:BBB");
    check("离线设备有标注", b.title.includes("离线"));
  }

  console.log(failed === 0 ? "\n全部通过" : `\n${failed} 项失败`);
  process.exit(failed === 0 ? 0 : 1);
}

run().catch(e => { console.error(e); process.exit(1); });

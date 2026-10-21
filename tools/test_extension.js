// 扩展逻辑离线验证：用假的 chrome API 跑一遍 background.js，
// 检查菜单构建、设备子菜单、点击处理是否符合预期。
//
// 为什么这么测：右键菜单只能人工点，但「菜单树结构 + 点击分支」是纯逻辑，
// 可以在 Node 里跑。这样 CI 也能守住（扩展代码目前没有任何自动化测试）。

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const BG = path.join(__dirname, "..", "extension", "chromium", "background.js");

// ─── 假 chrome / daemon ────────────────────────────────────
function makeEnv(state, opts) {
  opts = opts || {};
  const menus = [];
  const listeners = {};
  const created = [];

  const chrome = {
    runtime: {
      connectNative() {
        let onMsg = null;
        return {
          onDisconnect: { addListener() {} },
          onMessage: {
            addListener(fn) { onMsg = fn; },
            removeListener() {},
          },
          postMessage(req) {
            // 模拟宿主应答
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
      create(opts) { created.push(opts); },
      removeAll() { created.length = 0; return Promise.resolve(); },
      onClicked: { addListener(fn) { listeners.clicked = fn; } },
    },
    action: {
      setBadgeText(o) { menus.push(["badge", o.text]); },
      setBadgeBackgroundColor() {},
    },
  };

  // 假 daemon：/api/state 返回预设；/api/select 记录切换；/api/send-url 记录媒体
  const selected = [];
  const sentURLs = [];
  function handle(req) {
    if (req.path === "/api/state") {
      // daemonDown：模拟后台没在跑（native host 连不上 → ok=false 无 data）
      if (opts.daemonDown) return { ok: false, message: "无法连接 Columba 后台" };
      return { ok: true, data: { devices: state.devices, target: state.target } };
    }
    if (req.path === "/api/select") {
      selected.push(req.body.id);
      state.target = req.body.id;
      return { ok: true };
    }
    if (req.path === "/api/send-text") {
      return { ok: !!state.target, message: state.target ? "" : "未选择目标设备" };
    }
    if (req.path === "/api/send-url") {
      sentURLs.push(req.body);
      return { ok: true };
    }
    return { ok: false, message: "unknown" };
  }

  // 定时刷新菜单用 setInterval；测试里给个空实现，避免进程挂住不退出
  const sandbox = { chrome, console, setTimeout, setInterval: () => 0, Promise, String };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(BG, "utf8"), sandbox, { filename: BG });

  return { created, listeners, selected, sentURLs, sandbox };
}

const tick = () => new Promise(r => setTimeout(r, 10));

// ─── 断言辅助 ──────────────────────────────────────────────
let failed = 0;
function check(name, cond, extra) {
  if (cond) {
    console.log("  ✓", name);
  } else {
    failed++;
    console.log("  ✗", name, extra === undefined ? "" : `（${extra}）`);
  }
}

async function run() {
  // 场景 1：单设备 —— 不应出现「发送到…」子菜单
  {
    console.log("单设备：");
    const state = {
      devices: [{ id: "AAA", name: "我的手机", online: true }],
      target: "AAA",
    };
    const env = makeEnv(state);
    env.listeners.installed();
    await tick();

    const ids = env.created.map(m => m.id);
    check("创建了根菜单", ids.includes("root"));
    check("根菜单标题带设备名", env.created.find(m => m.id === "root").title === "Drop → 我的手机");
    check("文字类三项都在", ["page", "sel", "link"].every(i => ids.includes(i)));
    check("媒体本体两项都在", ["img", "video"].every(i => ids.includes(i)));
    check("「只发链接」备选也在", ["imgurl", "videourl"].every(i => ids.includes(i)));
    // 单设备也必须能看见「发送到…」并确认当前目标——早期版本单设备时藏起来，
    // 扩展里找不到选择设备的东西，那正是这个错误。
    check("单设备也显示「发送到…」", ids.includes("pick"));
    check("单设备也列出设备项", ids.includes("dev:AAA"));
    const devItem = env.created.find(m => m.id === "dev:AAA");
    check("当前目标用 radio 勾选", devItem.type === "radio" && devItem.checked === true,
      JSON.stringify({ type: devItem.type, checked: devItem.checked }));

    // 菜单文案不能说「发到某分类」，要说「发什么」（此处曾出现误解）
    const titles = env.created.map(m => m.title || "");
    check("文案用「此页面/此图片」而非「发送页面/发送图片」",
      titles.includes("发送此页面") && titles.includes("发送此图片") && titles.includes("发送此视频"));
    check("不再出现歧义文案「发送图片」", !titles.includes("发送图片"));

    // 点「发送此图片」→ 应走 send-url（发本体，不是发链接）
    await env.listeners.clicked({ menuItemId: "img", srcUrl: "https://example.com/a.png" });
    await tick();
    check("点图片走 send-url 而非 send-text", env.sentURLs.length === 1);
    check("send-url 带上 kind=image", env.sentURLs[0] && env.sentURLs[0].kind === "image");
    check("send-url 带上原始地址", env.sentURLs[0] && env.sentURLs[0].url === "https://example.com/a.png");

    // 点「只发送图片链接」→ 应走 send-text（不发本体）
    await env.listeners.clicked({ menuItemId: "imgurl", srcUrl: "https://example.com/b.png" });
    await tick();
    check("「只发链接」不触发 send-url", env.sentURLs.length === 1);
  }

  // 场景 2：多设备 —— 出现「发送到…」与设备子项，且离线有标注
  {
    console.log("多设备：");
    const state = {
      devices: [
        { id: "AAA", name: "我的手机", online: true },
        { id: "BBB", name: "平板", online: false },
      ],
      target: "AAA",
    };
    const env = makeEnv(state);
    env.listeners.installed();
    await tick();

    const ids = env.created.map(m => m.id);
    check("出现「发送到…」", ids.includes("pick"));
    check("列出两台设备", ids.includes("dev:AAA") && ids.includes("dev:BBB"));
    const b = env.created.find(m => m.id === "dev:BBB");
    check("离线设备有标注", b.title.includes("离线"), b.title);

    // 点设备子项 → 切换目标
    await env.listeners.clicked({ menuItemId: "dev:BBB" });
    await tick();
    check("切换目标调用了 /api/select", env.selected.length === 1 && env.selected[0] === "BBB");
    // 切换后菜单重建，标题应更新（BBB 离线，标题仍是 BBB）
    const rootAfter = env.created.find(m => m.id === "root");
    check("切换后标题跟着变", rootAfter && rootAfter.title === "Drop → 平板", rootAfter && rootAfter.title);
  }

  // 场景 3：未选择设备 —— 标题要明确提示
  {
    console.log("未选择设备：");
    const state = { devices: [{ id: "AAA", name: "我的手机", online: true }], target: "" };
    const env = makeEnv(state);
    env.listeners.installed();
    await tick();
    const root = env.created.find(m => m.id === "root");
    check("标题提示未选择", root.title.includes("未选择"), root.title);
  }

  // 场景 3.5：没有配对设备 —— 菜单要给出可执行的说明，不能是空子菜单
  {
    console.log("无设备：");
    const state = { devices: [], target: "" };
    const env = makeEnv(state);
    env.listeners.installed();
    await tick();
    const ids = env.created.map(m => m.id);
    check("无设备仍有「发送到…」", ids.includes("pick"));
    const noop = env.created.find(m => m.id === "noop");
    check("给出禁用提示项", !!noop && noop.enabled === false);
    check("提示指向面板添加设备", !!noop && noop.title.includes("面板"), noop && noop.title);
  }

  // 场景 3.6：daemon 未运行 —— 标题要说明「后台未运行」，
  // 而不是假装「未选择设备」（后者会让用户去面板里白找一圈）
  {
    console.log("后台未运行：");
    const env = makeEnv({ devices: [], target: "" }, { daemonDown: true });
    env.listeners.installed();
    await tick();
    const root = env.created.find(m => m.id === "root");
    check("标题说明后台未运行", root.title.includes("后台未运行"), root.title);
    const noop = env.created.find(m => m.id === "noop");
    check("提示指向启动后台", !!noop && noop.title.includes("后台"), noop && noop.title);
  }

  // 场景 4：点根菜单/发送到…（非动作项）不应发消息
  {
    console.log("非动作项：");
    const state = { devices: [{ id: "AAA", name: "x", online: true }], target: "AAA" };
    const env = makeEnv(state);
    env.listeners.installed();
    await tick();
    let sent = 0;
    // 拦截 send-text：点击 root 后不应有 send 调用（用 selected 之外的方式观察）
    const before = env.selected.length;
    await env.listeners.clicked({ menuItemId: "root" });
    await env.listeners.clicked({ menuItemId: "pick" });
    await tick();
    check("点非动作项不切换设备", env.selected.length === before);
  }

  console.log(failed === 0 ? "\n全部通过" : `\n${failed} 项失败`);
  process.exit(failed === 0 ? 0 : 1);
}

run().catch(e => { console.error(e); process.exit(1); });

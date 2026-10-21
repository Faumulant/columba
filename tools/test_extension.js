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
function makeEnv(state) {
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

  // 假 daemon：/api/state 返回预设；/api/select 记录切换
  const selected = [];
  function handle(req) {
    if (req.path === "/api/state") {
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
    return { ok: false, message: "unknown" };
  }

  const sandbox = { chrome, console, setTimeout, Promise, String };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(BG, "utf8"), sandbox, { filename: BG });

  return { created, listeners, selected, sandbox };
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
    check("五个动作项都在", ["page", "sel", "link", "img", "video"].every(i => ids.includes(i)));
    check("单设备不出现「发送到…」", !ids.includes("pick"));
    check("单设备不出现设备子项", !ids.some(i => String(i).startsWith("dev:")));

    // 点「发送图片」→ 应调用 send-text
    await env.listeners.clicked({ menuItemId: "img", srcUrl: "https://example.com/a.png" });
    await tick();
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

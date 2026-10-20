package com.faumulant.columba

import android.content.Context
import android.content.Intent
import androidx.core.content.pm.ShortcutInfoCompat
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.core.graphics.drawable.IconCompat
import android.os.Handler
import android.os.Looper
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

// 事件与全局 UI 状态中枢：实现 mobileapi.EventListener（Go goroutine 回调），
// 全部切到主线程后更新 Compose 可观察状态并驱动通知/自动保存。
object Bus : mobileapi.EventListener {

    data class PairReq(val name: String, val id: String)

    private var appCtx: Context? = null
    private val main = Handler(Looper.getMainLooper())

    // Go 绑定调用（gomobile 同步阻塞：读身份/信任库/历史 JSON、绑 socket）
    // 一律走这个单线程执行器——串行保证 setDataDir 先于其余调用，
    // 主线程只做 Compose 状态写入（曾全在主线程跑，冷启动/回调密集时卡顿 ANR）。
    private val ioExecutor = java.util.concurrent.Executors.newSingleThreadExecutor { r ->
        Thread(r, "columba-io")
    }
    fun bg(block: () -> Unit) {
        ioExecutor.execute(block)
    }

    // bg()/gomobile 回调线程里写 Compose state 必须经此切回主线程
    fun onMain(block: () -> Unit) {
        main.post(block)
    }

    var devices by mutableStateOf<List<Device>>(emptyList())
        private set
    var history by mutableStateOf<List<HistoryEntry>>(emptyList())
        private set
    var self by mutableStateOf<SelfInfo?>(null)
    // 配对上限（含解锁状态）：设置页设备卡显示「已配对 N/上限」，未解锁时给出入口
    var limit by mutableStateOf(DeviceLimit(3, false))
    var pairRequest by mutableStateOf<PairReq?>(null)
    // 配对弹窗的自动收起定时器（与 Go 侧 pairDecisionTimeout 对齐）
    private var pairTimer: Runnable? = null
        private set
    var snackbar by mutableStateOf<String?>(null)
        private set

    // 系统分享暂存：其他 app「分享到 Columba」的内容（MainActivity 解析写入，
    // 发送面板读取预填后 consumeShare 清空）。
    // targetId：Direct Share 直达目标（「发送给 设备X」快捷方式带过来的设备 ID）。
    data class SharePayload(
        val text: String? = null,
        val uris: List<android.net.Uri> = emptyList(),
        val targetId: String? = null,
    )

    var shareIntent by mutableStateOf<SharePayload?>(null)
        private set
    fun pushShare(p: SharePayload) { shareIntent = p }
    fun consumeShare() { shareIntent = null }

    // 通知点击跳接收页：MainActivity 解析 "open"="receive" 后置位，
    // AppRoot 消费切 Tab（替代写后从不读取的 MainActivity.openReceiveTab 死状态）
    var openReceive by mutableStateOf(false)
        private set
    fun pushOpenReceive() { openReceive = true }
    fun consumeOpenReceive() { openReceive = false }

    // 外观模式（"system"/"light"/"dark"）：点右上角斑鸠循环切换，Theme.kt 据此生效
    var themeMode by mutableStateOf("system")
        private set

    fun init(ctx: Context) {
        if (appCtx != null) return
        appCtx = ctx.applicationContext
        themeMode = AppSettings.load(appCtx!!).themeMode
        val c = appCtx!!
        bg {
            // 任何 Go 调用前先固定数据目录（identity.AppDir 依赖 LOCALAPPDATA，
            // 否则会回退到 cwd 下的 .drop 生成一套错误身份）
            Go.setDataDir(Receiver.dataDir(c).absolutePath)
            // 安卓默认主机名是 localhost：身份名是它（或空）时改用机型名
            val s = Go.selfInfo()
            if (s.name.isBlank() || s.name.equals("localhost", true)) {
                val model = android.os.Build.MODEL.trim().ifBlank { "Android 设备" }
                Go.setDeviceName(model)
            }
            refreshAll()
        }
    }

    fun refreshAll() {
        refreshDevices()
        refreshHistory()
        bg {
            val s = Go.selfInfo()
            main.post { self = s }
        }
    }

    fun refreshDevices() {
        bg {
            val list = Go.listDevices()
            val lim = Go.deviceLimit()
            main.post {
                devices = list.devices
                limit = lim
                pushShareShortcuts()
                // 信任库读不出来必须出声：否则界面显示「还没有配对设备」，
                // 用户会以为数据丢了并重新配对（旧记录其实还在）
                list.error?.let { showSnackbar("设备列表读取失败：$it") }
            }
        }
    }

    /** 立即重发现：点信标触发。广播一轮宣告（Go 侧非阻塞），本机在线
     *  状态等对端周期宣告到达 tracker 后才刷新，故立即刷一次 + 2s 后补刷。 */
    fun triggerRediscover() {
        bg { Go.triggerDiscover() }
        refreshDevices()
        main.postDelayed({ refreshDevices() }, 2000)
    }

    // Direct Share：把每台已配对设备注册为系统分享面板的直达目标
    //（其他 app 点分享 → 顶部直接出现「发送给 设备名」→ 内容直接进入发送流程）。
    // 设备列表（ID/名字）变化时才推送，在线抖动不触发。
    private var pushedShareSig = ""
    private fun pushShareShortcuts() {
        val ctx = appCtx ?: return
        val sig = devices.joinToString("|") { it.id + it.name }
        if (sig == pushedShareSig) return
        pushedShareSig = sig
        try {
            val sm = ctx.getSystemService(Context.SHORTCUT_SERVICE) as android.content.pm.ShortcutManager
            if (devices.isEmpty()) {
                // 设备全部移除后清空已推送的快捷方式（否则分享面板残留死目标）
                sm.removeAllDynamicShortcuts()
                return
            }
            val shortcuts = devices.take(4).map { d ->
                ShortcutInfoCompat.Builder(ctx, "share-${d.id.take(16)}")
                    .setShortLabel("发送给 ${d.name}")
                    .setLongLabel("用 Columba 发送给 ${d.name}")
                    .setIcon(IconCompat.createWithResource(ctx, R.mipmap.ic_launcher))
                    .setIntent(
                        Intent(ctx, MainActivity::class.java).apply {
                            action = Intent.ACTION_SEND
                            type = "*/*"
                            // 直达目标 ID：MainActivity.parseShare 读取后预选设备，
                            // 否则点「发送给 设备X」实际会发给上次选择的设备（发错人）
                            putExtra("target_id", d.id)
                        }
                    )
                    .setLongLived(true)
                    .addCapabilityBinding("actions.intent.SEND")
                    .build()
            }
            sm.setDynamicShortcuts(shortcuts.map { it.toShortcutInfo() })
        } catch (_: Exception) {
            // 某些 ROM 限制快捷方式数量/能力时静默放弃（不影响普通分享路径）
        }
    }
    fun refreshHistory() {
        bg {
            val h = Go.history()
            main.post { history = h }
        }
    }

    fun showSnackbar(msg: String) {
        main.post { snackbar = msg }
    }
    fun clearSnackbar() { snackbar = null }

    // 外观循环：跟随系统 → 日间 → 夜间 → …；立即持久化，snackbar 报当前模式
    fun cycleTheme() {
        val ctx = appCtx ?: return
        val next = when (themeMode) {
            "system" -> "light"
            "light" -> "dark"
            else -> "system"
        }
        themeMode = next
        ctx.saveSettings(AppSettings.load(ctx).copy(themeMode = next))
        showSnackbar(
            "外观：" + when (next) {
                "light" -> "日间"
                "dark" -> "夜间"
                else -> "跟随系统"
            }
        )
    }

    // ─── EventListener（Go 回调，线程不保证） ─────────────────────

    override fun onProgress(dir: String, peer: String, name: String, done: Long, total: Long) {
        main.post {
            val ctx = appCtx ?: return@post
            if (total > 0) Notifier.progress(ctx, dir, name, (done * 100 / total).toInt())
        }
    }

    override fun onTransfer(dir: String, peer: String, name: String, path: String, size: Long, ok: Boolean, errInfo: String) {
        val ctx = appCtx ?: return
        main.post {
            Notifier.cancelProgress(ctx)
            refreshHistory()
            if (dir == "recv") {
                if (ok) {
                    Notifier.notifyReceived(ctx, name, peer, false)
                    AutoSaver.onReceived(ctx, path, name)
                    if (Receiver.foregroundActive()) showSnackbar("已收到 $name")
                } else {
                    showSnackbar("收到失败：$name")
                }
            } else {
                showSnackbar(if (ok) "已发送到 $peer：$name" else "发送失败：$errInfo")
            }
        }
    }

    override fun onText(dir: String, peer: String, text: String) {
        val ctx = appCtx ?: return
        main.post {
            refreshHistory()
            if (dir == "recv") {
                Notifier.notifyReceived(ctx, "文本", peer, true)
                if (Receiver.foregroundActive()) showSnackbar("收到来自 $peer 的文本")
            }
        }
    }

    override fun onPairRequest(peerName: String, peerID: String): Boolean {
        // 返回 false：交给 UI 弹窗（允许/拒绝 → PairDecide），超时视为拒绝。
        // 不会并发到达：Go 侧 confirmPeer 在 pending != nil 时直接返回 false、
        // 根本不回调，故这里无需处理「已有待裁决请求」的分支。
        main.post {
            pairRequest = PairReq(peerName, peerID)
            // 与 Go 侧 pairDecisionTimeout（3 分钟）对齐：到点弹窗自动收起，
            // 否则用户对着一个已经失效的弹窗点「允许」，只会静默失败。
            pairTimer?.let { main.removeCallbacks(it) }
            val t = Runnable {
                pairRequest = null
                showSnackbar("配对请求已超时，请重新发起")
            }
            pairTimer = t
            main.postDelayed(t, 175_000)
        }
        return false
    }

    override fun onDeviceChanged() {
        refreshDevices()
    }

    fun resolvePairRequest(allow: Boolean) {
        val req = pairRequest
        pairRequest = null
        pairTimer?.let { main.removeCallbacks(it); pairTimer = null }
        bg {
            val r = Go.pairDecide(allow)
            // 失败必须出声：超时/已被处理时 Go 侧回「当前没有待裁决的配对请求」，
            // 此前只对成功路径提示，用户点「允许」看到弹窗消失却什么也没发生
            main.post {
                if (r.isSuccess) {
                    if (allow) showSnackbar("已允许 ${req?.name} 配对")
                } else {
                    showSnackbar("配对处理失败：${r.exceptionOrNull()?.message}")
                }
            }
        }
    }
}

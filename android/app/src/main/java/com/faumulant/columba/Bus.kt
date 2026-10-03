package com.faumulant.columba

import android.content.Context
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

    var devices by mutableStateOf<List<Device>>(emptyList())
        private set
    var history by mutableStateOf<List<HistoryEntry>>(emptyList())
        private set
    var self by mutableStateOf<SelfInfo?>(null)
    var pairRequest by mutableStateOf<PairReq?>(null)
        private set
    var snackbar by mutableStateOf<String?>(null)
        private set

    // 系统分享暂存：其他 app「分享到 Columba」的内容（MainActivity 解析写入，
    // 发送面板读取预填后 consumeShare 清空）
    data class SharePayload(val text: String? = null, val uris: List<android.net.Uri> = emptyList())

    var shareIntent by mutableStateOf<SharePayload?>(null)
        private set
    fun pushShare(p: SharePayload) { shareIntent = p }
    fun consumeShare() { shareIntent = null }

    // 外观模式（"system"/"light"/"dark"）：点右上角斑鸠循环切换，Theme.kt 据此生效
    var themeMode by mutableStateOf("system")
        private set

    fun init(ctx: Context) {
        if (appCtx != null) return
        appCtx = ctx.applicationContext
        themeMode = AppSettings.load(appCtx!!).themeMode
        // 任何 Go 调用前先固定数据目录（identity.AppDir 依赖 LOCALAPPDATA，
        // 否则会回退到 cwd 下的 .drop 生成一套错误身份）
        Go.setDataDir(Receiver.dataDir(appCtx!!).absolutePath)
        // 安卓默认主机名是 localhost：身份名是它（或空）时改用机型名
        run {
            val s = Go.selfInfo()
            if (s.name.isBlank() || s.name.equals("localhost", true)) {
                val model = android.os.Build.MODEL.trim().ifBlank { "Android 设备" }
                Go.setDeviceName(model)
            }
        }
        refreshAll()
    }

    fun refreshAll() {
        refreshDevices()
        refreshHistory()
        self = Go.selfInfo()
    }

    fun refreshDevices() { devices = Go.listDevices() }
    fun refreshHistory() { history = Go.history() }

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

    override fun onTransfer(dir: String, peer: String, name: String, path: String, size: Long, ok: Boolean, errInfo: String) {
        val ctx = appCtx ?: return
        main.post {
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
        // 返回 false：交给 UI 弹窗（允许/拒绝 → PairDecide），超时视为拒绝
        main.post {
            if (pairRequest == null) pairRequest = PairReq(peerName, peerID)
            else Go.pairDecide(false) // 已有一个待裁决请求，直接拒绝后来的
        }
        return false
    }

    override fun onDeviceChanged() {
        main.post { refreshDevices() }
    }

    fun resolvePairRequest(allow: Boolean) {
        val req = pairRequest
        pairRequest = null
        // 后台线程：PairDecide 本身不阻塞，但保险起见
        Thread {
            val r = Go.pairDecide(allow)
            if (allow && r.isSuccess) main.post { showSnackbar("已允许 ${req?.name} 配对") }
        }.start()
    }
}

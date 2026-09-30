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

    fun init(ctx: Context) {
        if (appCtx != null) return
        appCtx = ctx.applicationContext
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

    // ─── EventListener（Go 回调，线程不保证） ─────────────────────

    override fun OnTransfer(dir: String, peer: String, name: String, path: String, size: Long, ok: Boolean, errInfo: String) {
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

    override fun OnText(dir: String, peer: String, text: String) {
        val ctx = appCtx ?: return
        main.post {
            refreshHistory()
            if (dir == "recv") {
                Notifier.notifyReceived(ctx, "文本", peer, true)
                if (Receiver.foregroundActive()) showSnackbar("收到来自 $peer 的文本")
            }
        }
    }

    override fun OnPairRequest(peerName: String, peerID: String): Boolean {
        // 返回 false：交给 UI 弹窗（允许/拒绝 → PairDecide），超时视为拒绝
        main.post {
            if (pairRequest == null) pairRequest = PairReq(peerName, peerID)
            else Go.pairDecide(false) // 已有一个待裁决请求，直接拒绝后来的
        }
        return false
    }

    override fun OnDeviceChanged() {
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

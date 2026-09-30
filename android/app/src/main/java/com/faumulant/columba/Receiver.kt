package com.faumulant.columba

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.net.wifi.WifiManager
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import java.io.File

// 接收端生命周期管理：
// - 数据目录固定到应用专属目录（getExternalFilesDir，免存储权限），SetDataDir 之后
//   Go 核心（身份/信任库/历史/ipcache）全部落在这里
// - MulticastLock：组播发现前提，启动发现前获取、停止时释放
// - 前台服务（常驻开）或 Activity 前台（常驻关）两种宿主方式，共用这里
object Receiver {

    // Compose 可观察：设置页/接收页据此显示「接收运行中」
    var running: Boolean by androidx.compose.runtime.mutableStateOf(false)
        private set

    // 前台是否活跃（用于决定是否弹应用内提示；通知由服务/Notifier 负责）
    @Volatile
    var foregroundActive: Boolean = false

    private var mcastLock: WifiManager.MulticastLock? = null

    fun foregroundActive(): Boolean = foregroundActive

    fun dataDir(ctx: Context): File {
        val base = ctx.getExternalFilesDir(null) ?: ctx.filesDir
        return File(base, "data")
    }

    fun recvDir(ctx: Context): File {
        val base = ctx.getExternalFilesDir(null) ?: ctx.filesDir
        return File(base, "Receive")
    }

    // 启动接收端；失败返回错误消息（如端口被占），成功返回 null
    fun ensureStarted(ctx: Context): String? {
        if (running) return null
        val c = ctx.applicationContext
        // 服务进程可能先于 Activity 启动（开机自启/被杀重启），事件中枢先就位
        Bus.init(c)
        val dir = Go.setDataDir(dataDir(c).absolutePath)
        if (dir.isFailure) return dir.exceptionOrNull()?.message
        val s = AppSettings.load(c)
        val r = Go.startReceiver(recvDir(c).absolutePath, s.isAuto, Bus)
        if (r.isFailure) {
            return r.exceptionOrNull()?.message
        }
        acquireMulticastLock(c)
        running = true
        return null
    }

    fun stop(ctx: Context) {
        if (!running) return
        running = false
        Go.stopReceiver()
        releaseMulticastLock()
    }

    @SuppressLint("WifiManagerPotentialLeak")
    private fun acquireMulticastLock(ctx: Context) {
        if (mcastLock?.isHeld == true) return
        try {
            val wifi = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            val lock = wifi.createMulticastLock("columba-mcast")
            lock.setReferenceCounted(false)
            lock.acquire()
            mcastLock = lock
        } catch (_: Throwable) {
            // 无 Wi-Fi 环境（如模拟器以太网）下不阻塞接收，组播发现可能不可用，
            // Go 核心自带子网探测兜底
        }
    }

    private fun releaseMulticastLock() {
        try {
            mcastLock?.let { if (it.isHeld) it.release() }
        } catch (_: Throwable) {
        }
        mcastLock = null
    }

    // ─── 常驻开关 ───────────────────────────────────────────────

    fun startPersistent(ctx: Context) {
        val c = ctx.applicationContext
        val i = Intent(c, ReceiverService::class.java)
        c.startForegroundService(i)
    }

    fun stopPersistent(ctx: Context) {
        val c = ctx.applicationContext
        c.stopService(Intent(c, ReceiverService::class.java))
        stop(c)
    }
}

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

    // 接收端状态（Compose 可观察）：接收页据此决定空态显示什么。
    //
    // Starting 只是内部区分，**界面不给它单独文案**：启动只要几百毫秒，专门显示
    // 「正在启动」对用户没有价值，反而多闪一次。它唯一的用途是让空态在这段窗口里
    // 按正常状态显示（「还没有记录」），而不是先报一句误导性的「接收未运行」。
    // 真正需要告知用户的只有失败——失败会退回 NotStarted 并弹一次错误。
    enum class State { NotStarted, Starting, Running }

    var state: State by androidx.compose.runtime.mutableStateOf(State.NotStarted)
        private set

    // 已就绪 / 启动中（两者都算「正在工作」，界面按同一形态显示）
    val running: Boolean get() = state == State.Running
    val starting: Boolean get() = state == State.Starting

    // started 权威判定：不受 Compose state 的异步写入影响。
    //
    // 为什么不能直接用 running 判：state 经 Bus.onMain 切到主线程才生效，而
    // ensureStarted 在后台线程同步跑完；服务与 Activity 同时启动时，第二个调用
    // 进来读到的 state 仍是 NotStarted，会继续往下走、被 Go 侧以「接收端已在运行」
    // 拒绝，用户就看到「接收端启动失败：接收端已在运行」的误报（真机实测）。
    // 这个字段在 @Synchronized 区内同步读写，是可靠的互斥依据。
    @Volatile
    private var started = false

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

    // 启动接收端；失败返回错误消息（如端口被占），成功返回 null。
    // @Synchronized：onStart（经 Bus.bg）与配对弹窗线程可并发进入，
    // 无锁时 running 检查与 startReceiver 之间存在重入窗口（曾可启动两次）。
    @Synchronized
    fun ensureStarted(ctx: Context): String? {
        if (started) return null // 权威判定，见 started 字段注释
        val c = ctx.applicationContext
        // 先标「启动中」再干活：UI 首帧可能已经渲染过，早一步把状态从
        // NotStarted 切走，避免空态闪一下「接收未运行」
        Bus.onMain { state = State.Starting }
        // 服务进程可能先于 Activity 启动（开机自启/被杀重启），事件中枢先就位
        Bus.init(c)
        val dir = Go.setDataDir(dataDir(c).absolutePath)
        if (dir.isFailure) {
            Bus.onMain { state = State.NotStarted }
            return dir.exceptionOrNull()?.message
        }
        val s = AppSettings.load(c)
        val r = Go.startReceiver(recvDir(c).absolutePath, s.isAuto, Bus)
        if (r.isFailure) {
            val msg = r.exceptionOrNull()?.message ?: "未知错误"
            // 「已在运行」不是故障：另一条路径（前台服务/Activity）已经把它拉起来了，
            // 属于并发启动的正常结果，不该弹「启动失败」吓用户。
            if (msg.contains("已在运行")) {
                started = true
                Bus.onMain { state = State.Running }
                return null
            }
            // 真失败要退回 NotStarted，否则界面会永远停在「正在启动」
            Bus.onMain { state = State.NotStarted }
            return msg
        }
        acquireMulticastLock(c)
        started = true
        // 同上：state 是 Compose state，而 ensureStarted 只从后台线程调用
        Bus.onMain { state = State.Running }
        return null
    }

    @Synchronized
    fun stop(ctx: Context) {
        if (!started) return
        started = false
        // state 是 Compose state，本函数由 onStop（主线程）与后台线程共同调用，
        // 统一经 Bus.onMain 落写，避免并发写快照丢更新
        Bus.onMain { state = State.NotStarted }
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
        try {
            c.startForegroundService(i)
        } catch (e: Exception) {
            // Android 12+ 后台 FGS 启动限制、Android 15+ BOOT_COMPLETED 禁止
            // dataSync 类型前台服务（ForegroundServiceStartNotAllowedException）。
            // 崩溃不如降级：下次打开 App 时由 ensureStarted 路径补启动。
            Bus.showSnackbar("后台启动被系统限制，请打开 App 一次以恢复接收")
        }
    }

    fun stopPersistent(ctx: Context) {
        val c = ctx.applicationContext
        c.stopService(Intent(c, ReceiverService::class.java))
        stop(c)
    }
}

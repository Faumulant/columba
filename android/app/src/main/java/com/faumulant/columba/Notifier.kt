package com.faumulant.columba

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat

// 系统通知：常驻服务通知 + 收到文件/文本通知（点击跳到 App 接收页）
object Notifier {

    const val CH_STATUS = "status"      // 低重要度：常驻“正在等待接收”
    const val CH_TRANSFER = "transfer"  // 默认重要度：传输完成
    const val ID_STATUS = 1
    const val ID_PROGRESS = 90
    private var seq = 100

    fun ensureChannels(ctx: Context) {
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(
            NotificationChannel(CH_STATUS, "接收状态", NotificationManager.IMPORTANCE_MIN).apply {
                description = "常驻接收服务通知"
            }
        )
        nm.createNotificationChannel(
            NotificationChannel(CH_TRANSFER, "传输通知", NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = "收到文件/文本时提醒"
            }
        )
    }

    private fun openAppIntent(ctx: Context): android.app.PendingIntent =
        android.app.PendingIntent.getActivity(
            ctx, 0,
            Intent(ctx, MainActivity::class.java).apply {
                addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
                putExtra("open", "receive")
            },
            android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE
        )

    // 常驻通知上的「打开」：与点通知本体同一条 Activity 入口
    private fun openAppPending(ctx: Context): android.app.PendingIntent = openAppIntent(ctx)

    // 常驻通知上的「关闭」：显式组件的 BroadcastReceiver，在通知栏里直接停接收，
    // 不把 App 拉到前台。requestCode 必须与「打开」不同，否则两个 PendingIntent
    // 会被 FLAG_UPDATE_CURRENT 合并成同一个动作。
    private fun stopPending(ctx: Context): android.app.PendingIntent =
        android.app.PendingIntent.getBroadcast(
            ctx, 1,
            Intent(ctx, StopReceiverAction::class.java),
            android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE
        )

    // 常驻状态通知（前台服务要求）。空闲时「咕咕咕~」+「正在等待接收」。
    //
    // 标题用「咕咕咕~」而不是 "Columba"：通知栏本来就会显示渠道名（接收状态），
    // 标题再写一遍应用名是纯冗余；这句与发送面板底部的手写文案
    // 同源，有辨识度。
    //
    // 只有一个「关闭」按钮：点通知本体已经能进 App（setContentIntent），
    // 再放一个「打开」按钮是重复的。
    fun statusNotification(ctx: Context): Notification =
        NotificationCompat.Builder(ctx, CH_STATUS)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle("咕咕咕~")
            .setContentText("正在等待接收")
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(openAppPending(ctx))
            .addAction(R.drawable.ic_stat_columba, "关闭", stopPending(ctx))
            .build()

    fun cancelStatus(ctx: Context) {
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .cancel(ID_STATUS)
        } catch (_: Exception) {
        }
    }

    // 传输进行中：把常驻状态通知「就地升级」为带进度条的形态，而不是另发一条。
    //
    // 为什么合并：此前 ID_STATUS（正在等待接收）与 ID_PROGRESS（传输进度）是两条
    // 独立通知，传输时通知栏同时挂着两条，信息重复且杂乱。
    // 现在只有一条：空闲时「正在等待接收」，传输中变成「接收中 45%」+ 进度条，
    // 传完自动退回等待态。
    //
    // 节流：Go 端按块回调时频率可达每秒几十次，每次都跨进程 IPC 到
    // system_server——百分比未变或距上次 <200ms 时跳过（100% 强制刷）。
    private var lastProgressAt = 0L
    private var lastProgressPct = -1

    fun progress(ctx: Context, dir: String, name: String, pct: Int) {
        val p = pct.coerceIn(0, 100)
        val now = android.os.SystemClock.uptimeMillis()
        if (p != 100) {
            if (p == lastProgressPct) return
            if (now - lastProgressAt < 200) return
        }
        lastProgressAt = now
        lastProgressPct = p
        val verb = if (dir == "recv") "接收中" else "发送中"
        val n = NotificationCompat.Builder(ctx, CH_STATUS)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle("$verb $name")
            .setContentText("$p%")
            .setProgress(100, p, false)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(openAppPending(ctx))
            .addAction(R.drawable.ic_stat_columba, "关闭", stopPending(ctx))
            .build()
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .notify(ID_STATUS, n)
        } catch (_: SecurityException) {
        }
    }

    // 传输结束：把常驻通知退回「正在等待接收」形态（不取消——它承载前台服务）。
    fun restoreStatus(ctx: Context) {
        lastProgressAt = 0L
        lastProgressPct = -1
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .notify(ID_STATUS, statusNotification(ctx))
        } catch (_: SecurityException) {
        }
    }

    // 兼容旧调用点：传输结束时把常驻通知退回等待态。
    // （合并通知后不再有独立的 ID_PROGRESS，故这里等价于 restoreStatus）
    fun cancelProgress(ctx: Context) {
        restoreStatus(ctx)
    }

    // 自动保存目标失效（接收文件夹被改名/删除、相册写入失败）：历史已显示「收到」，
    // 文件却没落到用户期望的位置——必须出声，否则用户以为收到了。复用 CH_TRANSFER 渠道。
    fun notifySaveFailed(ctx: Context, detail: String) {
        val n: Notification = NotificationCompat.Builder(ctx, CH_TRANSFER)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle("自动保存失败")
            .setContentText(detail)
            .setAutoCancel(true)
            .setContentIntent(openAppIntent(ctx))
            .build()
        try {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.notify(seq++, n)
        } catch (_: SecurityException) {
            // 通知权限未授予：忽略
        }
    }

    fun notifyReceived(ctx: Context, name: String, peer: String, isText: Boolean) {
        if (!AppSettings.load(ctx).persistent) return // 仅常驻模式下走系统通知（前台有 snackbar）
        val title = if (isText) "收到文本" else "收到文件"
        val n: Notification = NotificationCompat.Builder(ctx, CH_TRANSFER)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle(title)
            .setContentText("来自 $peer：$name")
            .setStyle(
                if (isText) NotificationCompat.BigTextStyle().bigText("来自 $peer：\n$name")
                else null
            )
            .setAutoCancel(true)
            .setContentIntent(openAppIntent(ctx))
            .build()
        try {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.notify(seq++, n)
        } catch (_: SecurityException) {
            // 通知权限未授予：忽略（用户可在系统设置里开）
        }
    }
}

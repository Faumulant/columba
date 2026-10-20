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

    // 常驻状态通知（前台服务要求）。带「打开 / 关闭」两个动作：
    // 此前下拉展开后什么都没有，用户出门时想让接收停下只能进设置关开关，
    // 通知栏里取消不掉（前台服务通知被系统标为 ongoing，不允许直接划掉）。
    fun statusNotification(ctx: Context): Notification =
        NotificationCompat.Builder(ctx, CH_STATUS)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle("Columba")
            .setContentText("正在等待接收")
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(openAppPending(ctx))
            .addAction(R.drawable.ic_stat_columba, "打开", openAppPending(ctx))
            .addAction(R.drawable.ic_stat_columba, "关闭", stopPending(ctx))
            .build()

    fun cancelStatus(ctx: Context) {
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .cancel(ID_STATUS)
        } catch (_: Exception) {
        }
    }

    // 传输进行中：常驻进度条（收发共用一条，完成/失败时由 onTransfer 取消）。
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
        val title = if (dir == "recv") "正在接收 $name" else "正在发送 $name"
        val n = NotificationCompat.Builder(ctx, CH_TRANSFER)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle(title)
            .setProgress(100, p, false)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .notify(ID_PROGRESS, n)
        } catch (_: SecurityException) {
        }
    }

    fun cancelProgress(ctx: Context) {
        lastProgressAt = 0L
        lastProgressPct = -1
        try {
            (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .cancel(ID_PROGRESS)
        } catch (_: Exception) {
        }
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

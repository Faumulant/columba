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

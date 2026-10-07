package com.faumulant.columba

import android.app.Service
import android.content.Intent
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat

// 前台服务（dataSync）：常驻模式下宿主 Go 接收端。
// 常驻通知（Android 前台服务硬性要求）：“Columba 正在等待接收”。
class ReceiverService : Service() {

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startAsForeground()
        // ensureStarted 内含 gomobile 同步调用（绑 socket/读信任库），
        // onStartCommand 在主线程执行，必须移出（否则开机/重启路径卡顿）
        Thread {
            val err = Receiver.ensureStarted(this)
            if (err != null) {
                Bus.showSnackbar("接收端启动失败：$err")
                stopSelf()
            }
        }.start()
        return START_STICKY // App 被杀后服务重启，重挂接收端
    }

    override fun onDestroy() {
        // 仅在非常驻模式（被 stopPersistent 连带调用）时由 stop 收尾；
        // 常驻模式下 onDestroy 意味着系统要回收，接收端一并停止
        Receiver.stop(this)
        super.onDestroy()
    }

    private fun startAsForeground() {
        // 进程可能由 BootReceiver 拉起（Activity 从未创建），渠道不一定存在，
        // API 26+ 缺渠道会导致 startForeground 的通知投递失败
        Notifier.ensureChannels(this)
        val n = NotificationCompat.Builder(this, Notifier.CH_STATUS)
            .setSmallIcon(R.drawable.ic_stat_columba)
            .setContentTitle("Columba")
            .setContentText("正在等待接收")
            .setOngoing(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .build()
        ServiceCompat.startForeground(
            this, Notifier.ID_STATUS, n,
            android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
        )
    }
}

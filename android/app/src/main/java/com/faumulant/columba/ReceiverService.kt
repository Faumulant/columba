package com.faumulant.columba

import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.IBinder
import androidx.core.app.ServiceCompat

// 前台服务（specialUse）：常驻模式下宿主 Go 接收端。
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
        // 显式移除前台通知：个别 ROM（荣耀实测）在服务销毁后会把通知留在
        // 通知栏，用户看到"正在等待接收"但其实已经停了
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        Notifier.cancelStatus(this)
        super.onDestroy()
    }

    private fun startAsForeground() {
        // 进程可能由 BootReceiver 拉起（Activity 从未创建），渠道不一定存在，
        // API 26+ 缺渠道会导致 startForeground 的通知投递失败
        Notifier.ensureChannels(this)
        val n = Notifier.statusNotification(this)
        // specialUse 类型位仅 API 34+ 认识，低版本传类型参数会因与清单解析结果
        // 不一致被框架校验拒绝，故只在 34+ 显式传类型
        if (Build.VERSION.SDK_INT >= 34) {
            ServiceCompat.startForeground(
                this, Notifier.ID_STATUS, n,
                android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            )
        } else {
            @Suppress("DEPRECATION")
            startForeground(Notifier.ID_STATUS, n)
        }
    }
}

package com.faumulant.columba

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

// 常驻通知「关闭」按钮的落点：在通知栏里直接停掉后台接收，不打开 App。
//
// 为什么不用 Activity 收这个动作：点通知按钮若走 Activity，会先把 App 拉到
// 前台（用户本来只想关掉接收），而且 Activity 冷启动本身要几百毫秒。
// BroadcastReceiver 在后台就能完成 stopPersistent。
//
// 注意：Android 8+ 隐式广播受限，但 PendingIntent 指定了显式组件
// （Intent(context, StopReceiverAction::class.java)），不受该限制影响。
class StopReceiverAction : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent?) {
        val app = ctx.applicationContext
        // 用户从通知栏关掉接收 = 同时关掉「后台接收」开关，否则下次打开 App
        // onStart 又会按 persistent=true 把它重新拉起来，出现「关不掉」。
        app.saveSettings(AppSettings.load(app).copy(persistent = false))
        // 同步可观察镜像：设置页开关读的是 Bus.persistent（LaunchedEffect 跟随），
        // 不写这里的话开关会停在「开」而服务已经停了。
        Bus.persistent = false
        // 通知由服务 onDestroy 的 stopForeground(STOP_FOREGROUND_REMOVE) 带走。
        // 这里不直接 cancel：stopService 是异步的，服务还在前台时取消通知会被
        // 系统重新发布（前台服务的通知不允许被外部移除）。
        Receiver.stopPersistent(app)
        Bus.showSnackbar("已关闭后台接收")
    }
}

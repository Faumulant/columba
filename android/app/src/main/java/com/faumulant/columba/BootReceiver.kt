package com.faumulant.columba

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

// 开机自启：常驻开关开启时重启接收服务（App 被杀/重启后的恢复路径）
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        if (AppSettings.load(context).persistent) {
            Receiver.startPersistent(context)
        }
    }
}

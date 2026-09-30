package com.faumulant.columba

import android.content.Context
import android.net.Uri

// 用户设置（SharedPreferences）。D3/D4/D5 的三个拍板项都落在：
// saveMode：auto=自动保存到所选文件夹 / manual=手动保存（每条记录给「保存」按钮）
// autoGallery：自动保存模式下收到的图片同时写入系统相册（API 29+ 生效）
// persistent：后台常驻接收（前台服务）
data class AppSettings(
    val saveMode: String,        // "auto" / "manual"
    val autoGallery: Boolean,
    val persistent: Boolean,
    val treeUri: String,         // 自动保存目标文件夹（SAF tree）
) {
    val isAuto: Boolean get() = saveMode == "auto"

    companion object {
        fun load(ctx: Context): AppSettings {
            val p = ctx.getSharedPreferences("columba", Context.MODE_PRIVATE)
            return AppSettings(
                saveMode = p.getString("saveMode", "auto") ?: "auto",
                autoGallery = p.getBoolean("autoGallery", false),
                persistent = p.getBoolean("persistent", false),
                treeUri = p.getString("treeUri", "") ?: "",
            )
        }
    }
}

fun Context.saveSettings(s: AppSettings) {
    getSharedPreferences("columba", Context.MODE_PRIVATE).edit()
        .putString("saveMode", s.saveMode)
        .putBoolean("autoGallery", s.autoGallery)
        .putBoolean("persistent", s.persistent)
        .putString("treeUri", s.treeUri)
        .apply()
}

fun Context.persistTreePermission(uri: Uri) {
    try {
        contentResolver.takePersistableUriPermission(
            uri,
            android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION or
                android.content.Intent.FLAG_GRANT_WRITE_URI_PERMISSION
        )
    } catch (_: SecurityException) {
    }
}

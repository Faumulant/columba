package com.faumulant.columba

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import androidx.documentfile.provider.DocumentFile
import java.io.File
import java.util.concurrent.Executors

// 自动保存（D3/D4）：
// - 自动模式：收到文件后拷贝到用户在设置里选的文件夹（SAF tree，免存储权限）
// - 图片自动入相册：API 29+ 经 MediaStore 写入 Pictures/Columba
// - 手动模式：不动文件，由记录上的「保存」按钮走 SAF（见 MainActivity）
object AutoSaver {

    private val pool = Executors.newSingleThreadExecutor()

    fun onReceived(ctx: Context, path: String, name: String) {
        val s = AppSettings.load(ctx)
        if (!s.isAuto) return
        pool.execute {
            try {
                val cat = HistoryEntry.categoryOfName(name)
                if (cat == HistoryEntry.Category.IMAGE && s.autoGallery && Build.VERSION.SDK_INT >= 29) {
                    saveToGallery(ctx, path, name)
                }
                if (s.treeUri.isNotEmpty()) {
                    copyToTree(ctx, Uri.parse(s.treeUri), path, name)
                }
            } catch (e: Exception) {
                Bus.showSnackbar("自动保存失败：${e.message}")
            }
        }
    }

    // MediaStore 入相册（API 29+，无需权限）
    fun saveToGallery(ctx: Context, path: String, name: String): Boolean {
        if (Build.VERSION.SDK_INT < 29) return false
        val resolver = ctx.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.Images.Media.DISPLAY_NAME, name)
            put(MediaStore.Images.Media.MIME_TYPE, mimeOf(name))
            put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/Columba")
            put(MediaStore.Images.Media.IS_PENDING, 1)
        }
        val uri = resolver.insert(
            MediaStore.Images.Media.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY), values
        ) ?: return false
        resolver.openOutputStream(uri)?.use { out ->
            File(path).inputStream().use { it.copyTo(out) }
        } ?: return false
        val done = ContentValues().apply { put(MediaStore.Images.Media.IS_PENDING, 0) }
        resolver.update(uri, done, null, null)
        return true
    }

    // SAF tree 拷贝（自动模式的目标文件夹）
    fun copyToTree(ctx: Context, treeUri: Uri, path: String, name: String) {
        val tree = DocumentFile.fromTreeUri(ctx, treeUri) ?: return
        if (!tree.canWrite()) return
        val df = tree.createFile(mimeOf(name), name) ?: return
        ctx.contentResolver.openOutputStream(df.uri)?.use { out ->
            File(path).inputStream().use { it.copyTo(out) }
        }
    }

    fun mimeOf(name: String): String = mimeForName(name)
}

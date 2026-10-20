package com.faumulant.columba

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import androidx.documentfile.provider.DocumentFile
import com.faumulant.columba.ui.mimeForName
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
                    if (!saveToGallery(ctx, path, name)) {
                        warnSaveFailure(ctx, "图片写入相册失败：$name")
                    }
                }
                if (s.treeUri.isNotEmpty() && !copyToTree(ctx, Uri.parse(s.treeUri), path, name)) {
                    warnSaveFailure(ctx, "接收文件夹不可用，请到设置重新选择接收文件夹")
                }
            } catch (e: Exception) {
                Bus.showSnackbar("自动保存失败：${e.message}")
            }
        }
    }

    // 保存失败提示：非常驻/后台场景 Activity 可能已销毁，snackbar 会丢，
    // 故前台走 snackbar、其余走系统通知，两条路都保证用户看得见
    private fun warnSaveFailure(ctx: Context, detail: String) {
        if (Receiver.foregroundActive()) Bus.showSnackbar("自动保存失败：$detail")
        else Notifier.notifySaveFailed(ctx, detail)
    }

    // MediaStore 入相册（API 29+，无需权限）。
    // 失败路径必须删除条目：insert 成功（IS_PENDING=1）后拷贝中断会留下
    // 永久 PENDING 幽灵条目（相册不可见占位 + 占用 DISPLAY_NAME）。
    fun saveToGallery(ctx: Context, path: String, name: String): Boolean {
        if (Build.VERSION.SDK_INT < 29) return false
        val resolver = ctx.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.Images.Media.DISPLAY_NAME, name)
            put(MediaStore.Images.Media.MIME_TYPE, mimeForName(name))
            put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/Columba")
            put(MediaStore.Images.Media.IS_PENDING, 1)
        }
        val uri = resolver.insert(
            MediaStore.Images.Media.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY), values
        ) ?: return false
        val ok = try {
            resolver.openOutputStream(uri)?.use { out ->
                File(path).inputStream().use { it.copyTo(out) }
            } != null
        } catch (e: Exception) {
            false
        }
        if (!ok) {
            resolver.delete(uri, null, null)
            return false
        }
        val done = ContentValues().apply { put(MediaStore.Images.Media.IS_PENDING, 0) }
        resolver.update(uri, done, null, null)
        return true
    }

    // SAF tree 拷贝（自动模式的目标文件夹）。
    // 失败时删除已创建的 0 字节/半截文件，避免目标文件夹残留垃圾。
    // 返回 false（树失效/不可写/拷贝中断）时调用方必须提示，不可静默。
    fun copyToTree(ctx: Context, treeUri: Uri, path: String, name: String): Boolean {
        val tree = DocumentFile.fromTreeUri(ctx, treeUri) ?: return false
        if (!tree.canWrite()) return false
        val df = tree.createFile(mimeForName(name), name) ?: return false
        val ok = try {
            ctx.contentResolver.openOutputStream(df.uri)?.use { out ->
                File(path).inputStream().use { it.copyTo(out) }
            } != null
        } catch (e: Exception) {
            false
        }
        if (!ok) df.delete()
        return ok
    }
}

package com.faumulant.columba.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Notes
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.faumulant.columba.HistoryEntry
import java.text.DateFormat
import java.util.Date

// 与桌面面板同构的小部件：方向胶囊 / 分类图标 / 时间与大小格式化
// 黑白单色体系：「收到」实底、「发出」描边；分类图标不再按类别上色。

@Composable
fun DirectionPill(dir: String) {
    val recv = dir == "recv"
    if (recv) {
        Box(
            Modifier
                .background(MaterialTheme.colorScheme.onSurface.copy(alpha = 0.14f), RoundedCornerShape(999.dp))
                .padding(horizontal = 8.dp, vertical = 2.dp)
        ) {
            Text(
                "收到",
                color = MaterialTheme.colorScheme.onSurface,
                fontSize = 10.sp,
                style = MaterialTheme.typography.labelMedium,
            )
        }
    } else {
        Box(
            Modifier
                .border(1.dp, MaterialTheme.colorScheme.onSurface.copy(alpha = 0.22f), RoundedCornerShape(999.dp))
                .padding(horizontal = 8.dp, vertical = 2.dp)
        ) {
            Text(
                "发出",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                fontSize = 10.sp,
                style = MaterialTheme.typography.labelMedium,
            )
        }
    }
}

// 分类图标盒：40dp 圆角浅底 + 单色图标
@Composable
fun CategoryIcon(entry: HistoryEntry, size: Int = 28) {
    val icon = iconOf(entry)
    Box(
        Modifier
            .size(size.dp)
            .background(MaterialTheme.colorScheme.onSurface.copy(alpha = 0.06f), RoundedCornerShape(size / 3)),
        contentAlignment = Alignment.Center,
    ) {
        Icon(
            icon, null,
            tint = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.size((size - 14).dp),
        )
    }
}

fun iconOf(entry: HistoryEntry): ImageVector = when (entry.category) {
    HistoryEntry.Category.AUDIO -> Icons.Filled.MusicNote
    HistoryEntry.Category.IMAGE -> Icons.Filled.Image
    HistoryEntry.Category.VIDEO -> Icons.Filled.Movie
    HistoryEntry.Category.TEXT -> Icons.AutoMirrored.Filled.Notes
    else -> Icons.Filled.Description
}

val dirIcon: ImageVector = Icons.Filled.ArrowDownward
val sendIcon: ImageVector = Icons.Filled.ArrowUpward

fun fmtTime(iso: String): String = try {
    DateFormat.getDateTimeInstance(DateFormat.SHORT, DateFormat.SHORT)
        .parse(iso)?.let { DateFormat.getTimeInstance(DateFormat.SHORT).format(it) } ?: ""
} catch (e: Exception) {
    iso.take(16).replace('T', ' ')
}

// 完整时间（详情里用）
fun fmtTimeFull(iso: String): String = try {
    val d = DateFormat.getDateTimeInstance(DateFormat.SHORT, DateFormat.SHORT).parse(iso)
    if (d != null) DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(d.time)) else iso
} catch (e: Exception) {
    iso
}

// 部分 ROM（荣耀 MagicOS/Android 16 实测）的 MimeTypeMap 对常见扩展名也返回
// null，这里内置兜底表；命中 octet-stream 的冷门类型由调用方提示不可打开。
private val MIME_FALLBACK = mapOf(
    "jpg" to "image/jpeg", "jpeg" to "image/jpeg", "png" to "image/png",
    "gif" to "image/gif", "webp" to "image/webp", "bmp" to "image/bmp",
    "heic" to "image/heic", "svg" to "image/svg+xml",
    "mp4" to "video/mp4", "mkv" to "video/x-matroska", "mov" to "video/quicktime",
    "avi" to "video/x-msvideo", "webm" to "video/webm",
    "mp3" to "audio/mpeg", "flac" to "audio/flac", "wav" to "audio/x-wav",
    "m4a" to "audio/mp4", "ogg" to "audio/ogg", "aac" to "audio/mp4a-latm",
    "pdf" to "application/pdf", "txt" to "text/plain",
    "zip" to "application/zip", "doc" to "application/msword", "docx" to
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "xls" to "application/vnd.ms-excel", "xlsx" to
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
)

// 按文件名解析 MIME 类型：系统表 → 内置兜底表 → 通用二进制
fun mimeForName(name: String): String {
    val ext = name.substringAfterLast('.', "").lowercase()
    val sys = if (ext.isNotEmpty()) android.webkit.MimeTypeMap.getSingleton()
        .getMimeTypeFromExtension(ext) else null
    return sys ?: MIME_FALLBACK[ext] ?: "application/octet-stream"
}

fun fmtSize(bytes: Long): String {
    if (bytes <= 0) return ""
    val units = arrayOf("B", "KB", "MB", "GB")
    var v = bytes.toDouble()
    var i = 0
    while (v >= 1024 && i < units.size - 1) { v /= 1024; i++ }
    return if (i == 0) "${bytes}B" else String.format("%.1f%s", v, units[i])
}

@Composable
fun EllipsisText(text: String, modifier: Modifier = Modifier, maxLines: Int = 1, fontSize: Int = 15) {
    Text(
        text, modifier = modifier,
        maxLines = maxLines, overflow = TextOverflow.Ellipsis,
        fontSize = fontSize.sp, color = MaterialTheme.colorScheme.onSurface
    )
}

// ─── 统一弹窗（与桌面面板同一设计语言）────────────────────────────
// 容器：16dp 圆角 / card 底 / hairline 描边；标题左对齐 16sp semibold；
// 按钮三态：pri = 反转实心，ghost = hairline 描边，danger = 陶土色文字。

@Composable
fun AppDialog(
    onDismiss: () -> Unit,
    title: String,
    modifier: Modifier = Modifier,
    content: @Composable ColumnScope.() -> Unit,
    buttons: @Composable RowScope.() -> Unit,
) {
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Surface(
            modifier = modifier
                .padding(horizontal = 22.dp)
                .fillMaxWidth()
                .widthIn(max = 380.dp)
                .imePadding(),
            shape = RoundedCornerShape(16.dp),
            color = MaterialTheme.colorScheme.surfaceVariant,
            border = BorderStroke(1.dp, HairlineColor),
        ) {
            Column(Modifier.padding(horizontal = 20.dp, vertical = 20.dp)) {
                Text(
                    title,
                    fontSize = 16.sp, fontWeight = FontWeight.SemiBold,
                    color = MaterialTheme.colorScheme.onSurface,
                )
                Spacer(Modifier.height(10.dp))
                content()
                Spacer(Modifier.height(20.dp))
                Row(
                    Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.End,
                    verticalAlignment = Alignment.CenterVertically,
                ) { buttons() }
            }
        }
    }
}

// 弹窗按钮三态：pri 反转实心 / ghost hairline 描边 / danger 陶土文字
@Composable
fun DlgButton(label: String, kind: String = "ghost", enabled: Boolean = true, onClick: () -> Unit) {
    when (kind) {
        "pri" -> Button(
            onClick = onClick, enabled = enabled,
            shape = RoundedCornerShape(10.dp),
            colors = ButtonDefaults.buttonColors(
                containerColor = MaterialTheme.colorScheme.primary,
                contentColor = MaterialTheme.colorScheme.onPrimary,
            ),
        ) { Text(label, fontSize = 13.sp, fontWeight = FontWeight.SemiBold) }

        "danger" -> TextButton(
            onClick = onClick, enabled = enabled,
            colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
        ) { Text(label, fontSize = 13.sp, fontWeight = FontWeight.SemiBold) }

        else -> OutlinedButton(
            onClick = onClick, enabled = enabled,
            shape = RoundedCornerShape(10.dp),
            border = BorderStroke(1.dp, HairlineColor),
            colors = ButtonDefaults.outlinedButtonColors(
                containerColor = Color.Transparent,
                contentColor = MaterialTheme.colorScheme.onSurface,
            ),
        ) { Text(label, fontSize = 13.sp) }
    }
}

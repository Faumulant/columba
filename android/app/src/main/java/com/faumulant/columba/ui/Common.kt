package com.faumulant.columba.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
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
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
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

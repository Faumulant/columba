package com.faumulant.columba.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material.icons.filled.Notes
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.HistoryEntry
import java.text.DateFormat
import java.util.Date

// 与桌面面板同构的小部件：方向胶囊 / 分类图标 / 时间与大小格式化

@Composable
fun DirectionPill(dir: String) {
    val recv = dir == "recv"
    val bg = if (recv) Color(0xFFDCFCE7) else Color(0xFFDBEAFE)
    val fg = if (recv) Color(0xFF15803D) else Color(0xFF1D4ED8)
    Box(
        Modifier
            .background(bg, RoundedCornerShape(999.dp))
            .padding(horizontal = 8.dp, vertical = 2.dp)
    ) {
        Text(if (recv) "收到" else "发出", color = fg, fontSize = 11.sp)
    }
}

@Composable
fun CategoryIcon(entry: HistoryEntry, size: Int = 28) {
    val (icon, tint) = iconOf(entry)
    Box(
        Modifier.size(size.dp),
        contentAlignment = Alignment.Center
    ) {
        Icon(icon, null, tint = tint, modifier = Modifier.size((size - 6).dp))
    }
}

fun iconOf(entry: HistoryEntry): Pair<ImageVector, Color> = when (entry.category) {
    HistoryEntry.Category.AUDIO -> Icons.Filled.MusicNote to Color(0xFF9333EA)
    HistoryEntry.Category.IMAGE -> Icons.Filled.Image to Color(0xFF0E7490)
    HistoryEntry.Category.VIDEO -> Icons.Filled.Movie to Color(0xFFB45309)
    HistoryEntry.Category.TEXT -> Icons.Filled.Notes to Color(0xFF475569)
    else -> Icons.Filled.Description to Color(0xFF475569)
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

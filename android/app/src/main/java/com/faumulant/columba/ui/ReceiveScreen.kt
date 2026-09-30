package com.faumulant.columba.ui

import android.graphics.BitmapFactory
import android.widget.Toast
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.filled.Save
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.AppSettings
import com.faumulant.columba.Bus
import com.faumulant.columba.Go
import com.faumulant.columba.HistoryEntry
import com.faumulant.columba.Receiver
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

// 接收页：顶部目标设备下拉 + 五类 tabs + 记录列表（图片类带缩略图）
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ReceiveScreen(
    selectedTargetId: String?,
    onSelectTarget: (com.faumulant.columba.Device) -> Unit,
    onSaveRecord: (HistoryEntry) -> Unit,
) {
    var cat by remember { mutableIntStateOf(0) }
    var detail by remember { mutableStateOf<HistoryEntry?>(null) }
    var toDelete by remember { mutableStateOf<HistoryEntry?>(null) }
    val cats = HistoryEntry.Category.entries

    Column(Modifier.fillMaxSize()) {
        Row(
            Modifier.fillMaxWidth().padding(top = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            DeviceDropdown(selectedId = selectedTargetId, onSelect = onSelectTarget)
        }
        ScrollableTabRow(selectedTabIndex = cat, edgePadding = 8.dp) {
            for ((i, c) in cats.withIndex()) {
                Tab(selected = cat == i, onClick = { cat = i }, text = { Text(c.label) })
            }
        }
        val list = remember(cat, Bus.history) {
            val wanted = cats[cat]
            Bus.history.filter { it.category == wanted }.sortedByDescending { it.time }
        }
        if (list.isEmpty()) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Text(
                    if (Receiver.running) "还没有记录\n等电脑发点东西过来吧"
                    else "接收未运行\n打开 App 或开启后台常驻",
                    color = MaterialTheme.colorScheme.outline,
                    fontSize = 14.sp,
                    lineHeight = 22.sp,
                )
            }
        } else {
            LazyColumn(Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(vertical = 6.dp)) {
                items(list, key = { it.id }) { e ->
                    RecordCard(
                        entry = e,
                        onClick = { detail = e },
                        onLongPress = { toDelete = e },
                        onSave = { onSaveRecord(e) },
                    )
                }
            }
        }
    }

    detail?.let { e ->
        DetailDialog(
            entry = e,
            onClose = { detail = null },
            onSave = {
                detail = null
                onSaveRecord(e)
            },
        )
    }

    toDelete?.let { e ->
        AlertDialog(
            onDismissRequest = { toDelete = null },
            title = { Text("删除记录") },
            text = { Text("删除这条记录？（不影响已保存的文件）") },
            confirmButton = {
                TextButton(onClick = {
                    toDelete = null
                    val ids = listOf(e.id)
                    Thread { val r = Go.deleteHistory(ids); Bus.refreshHistory() }.start()
                }) { Text("删除") }
            },
            dismissButton = { TextButton(onClick = { toDelete = null }) { Text("取消") } },
        )
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RecordCard(entry: HistoryEntry, onClick: () -> Unit, onLongPress: () -> Unit, onSave: () -> Unit) {
    val s = AppSettings.load(LocalContext.current)
    Surface(
        Modifier
            .fillMaxWidth()
            .padding(horizontal = 8.dp, vertical = 3.dp)
            .clip(RoundedCornerShape(10.dp))
            .combinedClickable(onClick = onClick, onLongClick = onLongPress),
        color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        shape = RoundedCornerShape(10.dp),
    ) {
        Row(Modifier.padding(horizontal = 10.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            if (entry.category == HistoryEntry.Category.IMAGE && !entry.isText && entry.path.isNotEmpty()) {
                Thumb(entry.path, 40)
            } else {
                CategoryIcon(entry, 34)
            }
            Column(Modifier.weight(1f).padding(horizontal = 10.dp)) {
                if (entry.isText) {
                    Text(
                        entry.text.replace('\n', ' '),
                        maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                        fontSize = 15.sp,
                    )
                } else {
                    Text(
                        entry.name, maxLines = 1,
                        overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                        fontSize = 15.sp,
                    )
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    DirectionPill(entry.dir)
                    Spacer(Modifier.width(8.dp))
                    Text(
                        fmtTime(entry.time) + (if (entry.size > 0 && !entry.isText) " · " + fmtSize(entry.size) else ""),
                        fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                    )
                }
            }
            if (entry.status == "ok") {
                Icon(Icons.Filled.CheckCircle, null, tint = Color(0xFF22C55E), modifier = Modifier.size(20.dp))
            } else {
                Icon(Icons.Filled.ErrorOutline, null, tint = Color(0xFFEF4444), modifier = Modifier.size(20.dp))
            }
            // 手动保存模式：收到的文件给「保存」按钮（D3）
            if (!entry.isText && entry.isRecv && entry.status == "ok" && !s.isAuto) {
                TextButton(onClick = onSave, modifier = Modifier.padding(start = 2.dp)) {
                    Text("保存")
                }
            }
        }
    }
}

// 图片缩略图：本地文件存在才显示（降采样解码，避免大图 OOM）
@Composable
fun Thumb(path: String, sizeDp: Int) {
    val ctx = LocalContext.current
    val bmp by produceState<android.graphics.Bitmap?>(initialValue = null, path) {
        value = withContext(Dispatchers.IO) {
            try {
                val f = java.io.File(path)
                if (!f.exists()) return@withContext null
                val opts = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                BitmapFactory.decodeFile(path, opts)
                val sample = maxOf(1, maxOf(opts.outWidth, opts.outHeight) / (sizeDp * 4))
                BitmapFactory.decodeFile(
                    path,
                    BitmapFactory.Options().apply { inSampleSize = sample }
                )
            } catch (e: Exception) {
                null
            }
        }
    }
    val b = bmp
    if (b != null) {
        Image(
            b.asImageBitmap(), null,
            contentScale = ContentScale.Crop,
            modifier = Modifier.size(sizeDp.dp).clip(RoundedCornerShape(6.dp)),
        )
    } else {
        CategoryIcon(HistoryEntry(name = path.substringAfterLast('/')), sizeDp)
    }
}

@Composable
private fun DetailDialog(entry: HistoryEntry, onClose: () -> Unit, onSave: () -> Unit) {
    val ctx = LocalContext.current
    val s = AppSettings.load(ctx)
    AlertDialog(
        onDismissRequest = onClose,
        title = { Text(if (entry.isText) "文本" else entry.name, fontSize = 17.sp) },
        text = {
            Column {
                Text("来自 ${entry.peer} · ${fmtTimeFull(entry.time)}", fontSize = 12.sp, color = MaterialTheme.colorScheme.outline)
                Spacer(Modifier.height(8.dp))
                if (entry.isText) {
                    Surface(
                        color = MaterialTheme.colorScheme.surfaceVariant,
                        shape = RoundedCornerShape(8.dp),
                    ) {
                        Text(
                            entry.text,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 13.sp,
                            lineHeight = 20.sp,
                            modifier = Modifier.fillMaxWidth().padding(10.dp),
                        )
                    }
                    Spacer(Modifier.height(6.dp))
                    TextButton(onClick = {
                        val cm = ctx.getSystemService(android.content.Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
                        cm.setPrimaryClip(android.content.ClipData.newPlainText("text", entry.text))
                        Toast.makeText(ctx, "已复制", Toast.LENGTH_SHORT).show()
                    }) {
                        Icon(Icons.Filled.ContentCopy, null, modifier = Modifier.size(16.dp))
                        Text("复制全文", modifier = Modifier.padding(start = 4.dp))
                    }
                } else {
                    if (entry.path.isNotEmpty()) Text("已存到 ${entry.path}", fontSize = 12.sp, color = MaterialTheme.colorScheme.outline)
                    if (entry.error.isNotEmpty()) Text("错误：${entry.error}", fontSize = 12.sp, color = Color(0xFFEF4444))
                }
            }
        },
        confirmButton = {
            if (!entry.isText && entry.isRecv && entry.status == "ok" && !s.isAuto) {
                TextButton(onClick = onSave) { Text("保存到…") }
            } else {
                TextButton(onClick = onClose) { Text("关闭") }
            }
        },
        dismissButton = if (!entry.isText && entry.isRecv && entry.status == "ok" && !s.isAuto) {
            { TextButton(onClick = onClose) { Text("关闭") } }
        } else null,
    )
}

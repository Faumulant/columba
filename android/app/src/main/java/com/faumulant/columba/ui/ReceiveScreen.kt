package com.faumulant.columba.ui

import android.graphics.BitmapFactory
import android.widget.Toast
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Launch
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.outlined.Inbox
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
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
import androidx.compose.ui.draw.rotate
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

// 接收页：顶部接收信标 + 商标斑鸠 / 五分类等宽 tabs / 记录列表（图片类带缩略图）
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ReceiveScreen(
    onSaveRecord: (HistoryEntry) -> Unit,
) {
    var cat by remember { mutableIntStateOf(0) }
    var detail by remember { mutableStateOf<HistoryEntry?>(null) }
    var toDelete by remember { mutableStateOf<HistoryEntry?>(null) }
    val cats = HistoryEntry.Category.entries

    Column(Modifier.fillMaxSize()) {
        // 头部：连接信标（左，动=有设备在线 / 静=无设备在线）+ 斑鸠商标（右，点按切换外观）
        Row(
            Modifier.fillMaxWidth().padding(top = 4.dp, start = 18.dp, end = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            ReceiveBeacon()
            Spacer(Modifier.weight(1f))
            Image(
                painter = dovePainter(),
                contentDescription = "切换日间/夜间外观",
                modifier = Modifier
                    .size(28.dp)
                    .rotate(-8f)
                    .clickable { Bus.cycleTheme() },
            )
        }

        // 五分类等宽 tabs：选中 = 半粗 + 短下划线，未选中弱灰（361dp 一屏全显）
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 2.dp),
        ) {
            for ((i, c) in cats.withIndex()) {
                CategoryTab(
                    label = c.label,
                    selected = cat == i,
                    onClick = { cat = i },
                    modifier = Modifier.weight(1f),
                )
            }
        }

        // 分类切换：淡入 + 轻微上移，180ms，不抢戏
        AnimatedContent(
            targetState = cat,
            modifier = Modifier.fillMaxWidth().weight(1f),
            transitionSpec = {
                (fadeIn(tween(180)) + slideInVertically(tween(180)) { it / 24 }) togetherWith
                    fadeOut(tween(120))
            },
            label = "category",
        ) { currentCat ->
            val list = remember(currentCat, Bus.history) {
                Bus.history.filter { it.category == cats[currentCat] }.sortedByDescending { it.time }
            }
            if (list.isEmpty()) {
                EmptyState()
            } else {
                LazyColumn(
                    Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 12.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
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

@Composable
private fun CategoryTab(label: String, selected: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier) {
    Column(
        modifier.clickable(onClick = onClick).padding(vertical = 10.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            label,
            fontSize = 13.sp,
            style = if (selected) MaterialTheme.typography.titleSmall else MaterialTheme.typography.bodyMedium,
            color = if (selected) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.outline,
        )
        Spacer(Modifier.height(5.dp))
        Box(
            Modifier
                .width(18.dp)
                .height(2.dp)
                .background(
                    if (selected) MaterialTheme.colorScheme.onSurface else Color.Transparent,
                    RoundedCornerShape(1.dp),
                )
        )
    }
}

// 空态：细线收件箱 + 两行灰文案 + 手写体点缀（接收运行中时）
@Composable
private fun EmptyState() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Icon(
                Icons.Outlined.Inbox, null,
                tint = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.28f),
                modifier = Modifier.size(52.dp),
            )
            Spacer(Modifier.height(14.dp))
            if (Receiver.running) {
                Text(
                    "还没有记录",
                    fontSize = 15.sp,
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    "等电脑发点东西过来吧",
                    fontSize = 12.sp,
                    color = MaterialTheme.colorScheme.outline,
                )
                Spacer(Modifier.height(10.dp))
                Text(
                    "waiting for a drop…",
                    fontFamily = HandEn,
                    fontSize = 19.sp,
                    color = MaterialTheme.colorScheme.outline.copy(alpha = 0.8f),
                    modifier = Modifier.rotate(-3f),
                )
            } else {
                Text(
                    "接收未运行",
                    fontSize = 15.sp,
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    "打开 App 或开启后台常驻",
                    fontSize = 12.sp,
                    color = MaterialTheme.colorScheme.outline,
                )
            }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RecordCard(entry: HistoryEntry, onClick: () -> Unit, onLongPress: () -> Unit, onSave: () -> Unit) {
    val s = AppSettings.load(LocalContext.current)
    Surface(
        Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .combinedClickable(onClick = onClick, onLongClick = onLongPress),
        color = MaterialTheme.colorScheme.surfaceVariant,
        shape = RoundedCornerShape(14.dp),
        border = androidx.compose.foundation.BorderStroke(1.dp, HairlineColor),
    ) {
        Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            if (entry.category == HistoryEntry.Category.IMAGE && !entry.isText && entry.path.isNotEmpty()) {
                Thumb(entry.path, 40)
            } else {
                CategoryIcon(entry, 40)
            }
            Column(Modifier.weight(1f).padding(horizontal = 12.dp)) {
                if (entry.isText) {
                    Text(
                        entry.text.replace('\n', ' '),
                        maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                        fontSize = 14.sp,
                        style = MaterialTheme.typography.bodyLarge,
                    )
                } else {
                    Text(
                        entry.name, maxLines = 1,
                        overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                        fontSize = 14.sp,
                        style = MaterialTheme.typography.bodyLarge,
                    )
                }
                Spacer(Modifier.height(4.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    DirectionPill(entry.dir)
                    Spacer(Modifier.width(8.dp))
                    Text(
                        fmtTime(entry.time) + (if (entry.size > 0 && !entry.isText) " · " + fmtSize(entry.size) else "") +
                            if (entry.status != "ok") " · 传输中断" else "",
                        fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
                    )
                }
            }
            if (entry.status == "ok") {
                Icon(Icons.Filled.CheckCircle, null, tint = SuccessColor, modifier = Modifier.size(18.dp))
            } else {
                Icon(Icons.Filled.ErrorOutline, null, tint = MaterialTheme.colorScheme.error, modifier = Modifier.size(18.dp))
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
            modifier = Modifier.size(sizeDp.dp).clip(RoundedCornerShape(10.dp)),
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
                Text("来自 ${entry.peer} · ${fmtTimeFull(entry.time)}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Spacer(Modifier.height(8.dp))
                if (entry.isText) {
                    Surface(
                        color = MaterialTheme.colorScheme.surfaceVariant,
                        shape = RoundedCornerShape(10.dp),
                        border = androidx.compose.foundation.BorderStroke(1.dp, HairlineColor),
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
                    if (entry.path.isNotEmpty()) Text("已存到 ${entry.path}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    if (entry.error.isNotEmpty()) Text("错误：${entry.error}", fontSize = 12.sp, color = MaterialTheme.colorScheme.error)
                    if (entry.status == "ok" && entry.path.isNotEmpty()) {
                        Spacer(Modifier.height(4.dp))
                        TextButton(onClick = {
                            val f = java.io.File(entry.path)
                            val uri = androidx.core.content.FileProvider.getUriForFile(
                                ctx, "com.faumulant.columba.fileprovider", f
                            )
                            val mime = android.webkit.MimeTypeMap.getSingleton()
                                .getMimeTypeFromExtension(f.extension.lowercase())
                                ?: "application/octet-stream"
                            val i = android.content.Intent(android.content.Intent.ACTION_VIEW)
                                .setDataAndType(uri, mime)
                                .addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
                            try {
                                ctx.startActivity(i)
                            } catch (e: android.content.ActivityNotFoundException) {
                                Toast.makeText(ctx, "手机上没有能打开这类文件的应用", Toast.LENGTH_SHORT).show()
                            } catch (e: Exception) {
                                // 诊断模式：把真实异常亮出来，定位后换回友好提示
                                Toast.makeText(ctx, "打开失败：${e.javaClass.simpleName} ${e.message ?: ""}", Toast.LENGTH_LONG).show()
                            }
                        }) {
                            Icon(Icons.Filled.Launch, null, modifier = Modifier.size(16.dp))
                            Text("打开", modifier = Modifier.padding(start = 4.dp))
                        }
                    }
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

// 左上角连接信标：呼吸点 + 扩散波纹（发丝线级）。动 = 有配对设备在线
// （哑光灰绿，随时可发），静 = 无设备在线（灰点，纹丝不动）——用户最初
// 提的「显示是否连接」，数据源 = 配对设备的在线状态（每 2 秒比对）。
@Composable
private fun ReceiveBeacon(modifier: Modifier = Modifier) {
    val active = Bus.devices.any { it.online }
    val dot = if (active) SuccessColor else MaterialTheme.colorScheme.outline
    val t = rememberInfiniteTransition(label = "beacon").animateFloat(
        initialValue = 0f, targetValue = 1f,
        animationSpec = infiniteRepeatable(tween(3000, easing = LinearEasing)),
        label = "beaconT",
    ).value
    Canvas(modifier.size(36.dp)) {
        val dotR = 4.2.dp.toPx()
        if (active) {
            // 两圈波纹：半周期错相，向外扩散并渐隐
            for (off in listOf(0f, 0.5f)) {
                val p = (t + off) % 1f
                drawCircle(
                    color = dot,
                    alpha = 0.30f * (1f - p),
                    radius = dotR + p * (15.5.dp.toPx() - dotR),
                    style = Stroke(width = 1.2.dp.toPx()),
                )
            }
        }
        // 呼吸：半径 ±8%；未接收时完全静止
        val breathe = if (active) 1f + 0.08f * kotlin.math.sin(t * 2f * Math.PI.toFloat()) else 1f
        drawCircle(color = dot, radius = dotR * breathe)
    }
}

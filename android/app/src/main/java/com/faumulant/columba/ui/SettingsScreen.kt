package com.faumulant.columba.ui

import android.Manifest
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.AppSettings
import com.faumulant.columba.Bus
import com.faumulant.columba.Go
import com.faumulant.columba.Receiver
import com.faumulant.columba.saveSettings

// 设置页：设备管理（配对）/ 接收存储（D3/D4）/ 后台常驻（D5）/ 关于
@Composable
fun SettingsScreen() {
    val ctx = LocalContext.current
    var settings by remember { mutableStateOf(AppSettings.load(ctx)) }
    var showAddDevice by remember { mutableStateOf(false) }
    var removeDev by remember { mutableStateOf<com.faumulant.columba.Device?>(null) }
    val self = Bus.self

    fun update(s: AppSettings) {
        settings = s
        ctx.saveSettings(s)
    }

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)
    ) {
        Card {
            Text("设备管理", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(6.dp))
            val devices = Bus.devices
            if (devices.isEmpty()) {
                Text("还没有配对设备", fontSize = 13.sp, color = MaterialTheme.colorScheme.outline)
            }
            for (d in devices) {
                Row(
                    Modifier.fillMaxWidth().padding(vertical = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    OnlineDot(d.online)
                    Column(Modifier.weight(1f).padding(start = 10.dp)) {
                        Text(d.name.ifBlank { "设备" }, fontSize = 15.sp)
                        Text(
                            if (d.online) "在线" else "离线",
                            fontSize = 11.sp,
                            color = if (d.online) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline,
                        )
                    }
                    Icon(
                        Icons.Filled.Delete, "移除",
                        tint = MaterialTheme.colorScheme.outline,
                        modifier = Modifier
                            .size(20.dp)
                            .clickable { removeDev = d },
                    )
                }
            }
            HorizontalDivider(Modifier.padding(vertical = 8.dp))
            Button(onClick = { showAddDevice = true }, modifier = Modifier.fillMaxWidth()) {
                Icon(Icons.Filled.Add, null, modifier = Modifier.size(18.dp))
                Text("添加设备", modifier = Modifier.padding(start = 6.dp))
            }
        }

        Card {
            Text("接收存储", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(8.dp))
            Row {
                FilterChip(
                    selected = settings.isAuto,
                    onClick = { update(settings.copy(saveMode = "auto")) },
                    label = { Text("自动保存") },
                )
                Spacer(Modifier.width(10.dp))
                FilterChip(
                    selected = !settings.isAuto,
                    onClick = { update(settings.copy(saveMode = "manual")) },
                    label = { Text("手动保存") },
                )
            }
            if (settings.isAuto) {
                val treeName = remember(settings.treeUri) { treeDisplayName(settings.treeUri) }
                Row(
                    Modifier.fillMaxWidth().clickable {
                        // SAF 选择接收文件夹（launcher 定义在本组合函数下方）
                        folderPickRequested = true
                    }.padding(vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(Icons.Filled.Folder, null, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(20.dp))
                    Column(Modifier.padding(start = 10.dp)) {
                        Text("接收文件夹", fontSize = 14.sp)
                        Text(
                            treeName ?: "点击选择（收到的文件自动存到这里）",
                            fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                        )
                    }
                }
                Row(
                    Modifier.fillMaxWidth().padding(vertical = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text("图片自动入相册", fontSize = 14.sp)
                        Text(
                            if (Build.VERSION.SDK_INT >= 29) "收到的图片同时写入系统相册"
                            else "需要 Android 10 以上",
                            fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                        )
                    }
                    Switch(
                        checked = settings.autoGallery && Build.VERSION.SDK_INT >= 29,
                        enabled = Build.VERSION.SDK_INT >= 29,
                        onCheckedChange = { update(settings.copy(autoGallery = it)) },
                    )
                }
                Text(
                    "收到的文件先进入应用目录，再自动拷贝到所选文件夹；原始记录可在应用信息里清除",
                    fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
                )
            } else {
                Text(
                    "每条记录给「保存」按钮，由你选择保存位置；收到的文件暂存应用目录",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
            }
        }

        Card {
            Text("后台常驻", style = MaterialTheme.typography.titleMedium)
            Row(
                Modifier.fillMaxWidth().padding(vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(Modifier.weight(1f)) {
                    Text("后台接收", fontSize = 14.sp)
                    Text(
                        if (Receiver.running) "接收运行中" else "接收未运行",
                        fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                    )
                }
                Switch(
                    checked = settings.persistent,
                    onCheckedChange = { on ->
                        if (on) {
                            requestNotifPerm = true
                            update(settings.copy(persistent = true))
                            Receiver.startPersistent(ctx)
                        } else {
                            update(settings.copy(persistent = false))
                            Receiver.stopPersistent(ctx)
                        }
                    },
                )
            }
            TextButton(onClick = { openBatteryIgnore(ctx) }) {
                Text("电池优化白名单（避免 Doze 延迟接收）", fontSize = 13.sp)
            }
        }

        Card {
            Text("关于", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(6.dp))
            Text("Columba（飞鸠）· 配对一次，即选即发", fontSize = 13.sp)
            Text(
                "本机：${self?.name ?: ""}（${self?.displayId ?: ""}）",
                fontSize = 12.sp, fontFamily = FontFamily.Monospace,
                color = MaterialTheme.colorScheme.outline,
                modifier = Modifier.padding(top = 4.dp),
            )
            Text(
                "版本 ${com.faumulant.columba.BuildConfig.VERSION_NAME} · 仅限局域网",
                fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                modifier = Modifier.padding(top = 2.dp),
            )
        }
        Spacer(Modifier.height(24.dp))
    }

    // 通知权限：常驻开启时引导授予（Android 13+）
    val notifPerm = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {}
    if (requestNotifPerm) {
        requestNotifPerm = false
        if (Build.VERSION.SDK_INT >= 33) {
            notifPerm.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    // SAF 接收文件夹选择（自动保存模式）
    val folderPicker = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenDocumentTree()
    ) { uri: Uri? ->
        if (uri != null) {
            ctx.persistTreePermission(uri)
            update(settings.copy(treeUri = uri.toString()))
        }
    }
    if (folderPickRequested) {
        folderPickRequested = false
        folderPicker.launch(null)
    }

    if (showAddDevice) {
        AddDeviceDialog(
            onDismiss = { showAddDevice = false },
            onPaired = {
                showAddDevice = false
                Bus.refreshAll()
            },
        )
    }

    removeDev?.let { d ->
        AlertDialog(
            onDismissRequest = { removeDev = null },
            title = { Text("移除设备") },
            text = { Text("移除「${d.name}」？之后需要重新配对。") },
            confirmButton = {
                TextButton(onClick = {
                    removeDev = null
                    Thread {
                        Go.removeDevice(d.id)
                        Bus.refreshDevices()
                    }.start()
                }) { Text("移除") }
            },
            dismissButton = { TextButton(onClick = { removeDev = null }) { Text("取消") } },
        )
    }
}

// 跨组合函数传递的局部开关（页内一次性的 SAF/权限请求触发器）
private var folderPickRequested by mutableStateOf(false)
private var requestNotifPerm by mutableStateOf(false)

private fun treeDisplayName(treeUri: String): String? {
    if (treeUri.isEmpty()) return null
    return try {
        val u = Uri.parse(treeUri)
        // tree uri 形如 content://.../tree/primary%3ADocuments%2FColumba
        val doc = u.lastPathSegment ?: return null
        java.net.URLDecoder.decode(doc.substringAfterLast(':'), "UTF-8").ifBlank { null }
    } catch (e: Exception) {
        null
    }
}

private fun openBatteryIgnore(ctx: android.content.Context) {
    try {
        val pm = ctx.getSystemService(android.content.Context.POWER_SERVICE) as PowerManager
        val intent = if (!pm.isIgnoringBatteryOptimizations(ctx.packageName)) {
            Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
        } else {
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:${ctx.packageName}"))
        }
        ctx.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    } catch (_: Exception) {
    }
}

@Composable
private fun Card(content: @Composable () -> Unit) {
    Surface(
        Modifier.fillMaxWidth().padding(vertical = 6.dp),
        color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.45f),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(Modifier.padding(14.dp)) { content() }
    }
}

// 添加设备：本机出码（PairStart）+ 输入对方配对码（PairJoin）
@Composable
private fun AddDeviceDialog(onDismiss: () -> Unit, onPaired: () -> Unit) {
    val ctx = LocalContext.current
    var myCode by remember { mutableStateOf<String?>(null) }
    var myCodeErr by remember { mutableStateOf<String?>(null) }
    var input by remember { mutableStateOf("") }
    var joining by remember { mutableStateOf(false) }
    var joinMsg by remember { mutableStateOf<String?>(null) }

    AlertDialog(
        onDismissRequest = { if (!joining) onDismiss() },
        title = { Text("添加设备") },
        text = {
            Column {
                Text("方式一：本机出码（在电脑端输入）", fontSize = 13.sp, color = MaterialTheme.colorScheme.outline)
                Spacer(Modifier.height(4.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (myCode != null) {
                        Text(
                            myCode!!, fontSize = 26.sp, fontFamily = FontFamily.Monospace,
                            letterSpacing = 4.sp, color = MaterialTheme.colorScheme.primary,
                        )
                    }
                    TextButton(onClick = {
                        // PairStart 要求接收端在运行
                        val err = Receiver.ensureStarted(ctx)
                        if (err != null) {
                            myCodeErr = err
                        } else {
                            Thread {
                                val r = Go.pairStart()
                                myCodeErr = r.exceptionOrNull()?.message
                                myCode = r.getOrNull() ?: myCode
                            }.start()
                        }
                    }) {
                        Text(if (myCode == null) "生成配对码" else "重新生成")
                    }
                }
                if (myCodeErr != null) {
                    Text(myCodeErr!!, fontSize = 12.sp, color = MaterialTheme.colorScheme.error)
                }
                HorizontalDivider(Modifier.padding(vertical = 10.dp))
                Text("方式二：输入对方显示的配对码", fontSize = 13.sp, color = MaterialTheme.colorScheme.outline)
                Spacer(Modifier.height(4.dp))
                OutlinedTextField(
                    value = input,
                    onValueChange = { input = it.filter { ch -> ch.isLetterOrDigit() } },
                    placeholder = { Text("6 位配对码") },
                    singleLine = true,
                    enabled = !joining,
                )
                if (joinMsg != null) {
                    Text(joinMsg!!, fontSize = 12.sp, color = MaterialTheme.colorScheme.error)
                }
                if (joining) {
                    Text("正在配对（最长 90 秒）…", fontSize = 12.sp, color = MaterialTheme.colorScheme.outline)
                }
            }
        },
        confirmButton = {
            Button(
                enabled = !joining && input.isNotBlank(),
                onClick = {
                    joining = true
                    joinMsg = null
                    val code = input.trim()
                    Thread {
                        // 先确保发现监听在跑（在线状态与目标解析更快命中）
                        Receiver.ensureStarted(ctx)
                        val r = Go.pairJoin(code)
                        joining = false
                        if (r.isSuccess) {
                            joinMsg = null
                            onPaired()
                        } else {
                            joinMsg = r.exceptionOrNull()?.message
                        }
                    }.start()
                },
            ) { Text("加入") }
        },
        dismissButton = { TextButton(onClick = { if (!joining) onDismiss() }) { Text("取消") } },
    )
}

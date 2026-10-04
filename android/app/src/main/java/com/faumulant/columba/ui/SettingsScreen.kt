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
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.NorthEast
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.FilterChipDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.AppSettings
import com.faumulant.columba.Bus
import com.faumulant.columba.Go
import com.faumulant.columba.Receiver
import com.faumulant.columba.persistTreePermission
import com.faumulant.columba.saveSettings

// 设置页：设备管理（配对）/ 接收存储（D3/D4）/ 后台常驻（D5）/ 关于（含本机改名）
@Composable
fun SettingsScreen() {
    val ctx = LocalContext.current
    var settings by remember { mutableStateOf(AppSettings.load(ctx)) }
    var showAddDevice by remember { mutableStateOf(false) }
    var removeDev by remember { mutableStateOf<com.faumulant.columba.Device?>(null) }
    var showRename by remember { mutableStateOf(false) }
    var aliasEdit by remember { mutableStateOf<com.faumulant.columba.Device?>(null) }
    val self = Bus.self

    fun update(s: AppSettings) {
        settings = s
        ctx.saveSettings(s)
    }

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)
    ) {
        // 头部：标题 + 手写体点缀（不影响阅读的边角）
        Row(
            Modifier.fillMaxWidth().padding(top = 10.dp, bottom = 6.dp, start = 4.dp, end = 4.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("设置", fontSize = 20.sp, style = MaterialTheme.typography.titleLarge)
            Text(
                "columba",
                fontFamily = HandEn,
                fontSize = 20.sp,
                color = MaterialTheme.colorScheme.outline,
                modifier = Modifier.rotate(-3f),
            )
        }

        Card {
            Text("设备管理", style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(10.dp))
            val devices = Bus.devices
            if (devices.isEmpty()) {
                Text("还没有配对设备", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            for (d in devices) {
                Row(
                    Modifier.fillMaxWidth().padding(vertical = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    OnlineDot(d.online, size = 7)
                    Column(Modifier.weight(1f).padding(start = 10.dp)) {
                        Text(d.name.ifBlank { "设备" }, fontSize = 14.sp, style = MaterialTheme.typography.bodyLarge)
                        Text(
                            if (d.online) "在线" else "离线",
                            fontSize = 11.sp,
                            color = if (d.online) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.outline,
                        )
                    }
                    Icon(
                        Icons.Filled.Edit, "改备注名",
                        tint = MaterialTheme.colorScheme.outline,
                        modifier = Modifier
                            .size(18.dp)
                            .clickable { aliasEdit = d },
                    )
                    Spacer(Modifier.width(14.dp))
                    Icon(
                        Icons.Filled.Delete, "移除",
                        tint = MaterialTheme.colorScheme.outline,
                        modifier = Modifier
                            .size(18.dp)
                            .clickable { removeDev = d },
                    )
                }
            }
            HorizontalDivider(Modifier.padding(vertical = 10.dp), color = HairlineColor)
            OutlinedButton(
                onClick = { showAddDevice = true },
                modifier = Modifier.fillMaxWidth().height(44.dp),
                shape = RoundedCornerShape(12.dp),
                border = androidx.compose.foundation.BorderStroke(1.dp, MaterialTheme.colorScheme.onSurface.copy(alpha = 0.16f)),
            ) {
                Icon(Icons.Filled.Add, null, modifier = Modifier.size(14.dp))
                Text("添加设备", fontSize = 13.sp, modifier = Modifier.padding(start = 7.dp))
            }
        }

        Card {
            Text("接收存储", style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(10.dp))
            Row {
                FilterChip(
                    selected = settings.isAuto,
                    onClick = { update(settings.copy(saveMode = "auto")) },
                    label = { Text("自动保存", fontSize = 12.sp) },
                    colors = monochromeChipColors(),
                )
                Spacer(Modifier.width(10.dp))
                FilterChip(
                    selected = !settings.isAuto,
                    onClick = { update(settings.copy(saveMode = "manual")) },
                    label = { Text("手动保存", fontSize = 12.sp) },
                    colors = monochromeChipColors(),
                )
            }
            if (settings.isAuto) {
                val treeName = remember(settings.treeUri) { treeDisplayName(settings.treeUri) }
                Spacer(Modifier.height(10.dp))
                Surface(
                    Modifier.fillMaxWidth().clickable {
                        // SAF 选择接收文件夹（launcher 定义在本组合函数下方）
                        folderPickRequested = true
                    },
                    color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.05f),
                    shape = RoundedCornerShape(10.dp),
                ) {
                    Row(
                        Modifier.padding(horizontal = 12.dp, vertical = 9.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(Icons.Filled.Folder, null, tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(15.dp))
                        Column(Modifier.padding(start = 8.dp)) {
                            Text("接收文件夹", fontSize = 12.sp)
                            Text(
                                treeName ?: "点击选择（收到的文件自动存到这里）",
                                fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                }
                Row(
                    Modifier.fillMaxWidth().padding(vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text("图片自动入相册", fontSize = 13.sp)
                        if (Build.VERSION.SDK_INT < 29) {
                            Text(
                                "需要 Android 10 以上",
                                fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
                            )
                        }
                    }
                    Switch(
                        checked = settings.autoGallery && Build.VERSION.SDK_INT >= 29,
                        enabled = Build.VERSION.SDK_INT >= 29,
                        onCheckedChange = { update(settings.copy(autoGallery = it)) },
                        colors = monochromeSwitchColors(),
                    )
                }
            } else {
                Text(
                    "收到的文件暂存应用目录，点记录上的「保存」另存",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
            }
        }

        Card {
            Text("后台常驻", style = MaterialTheme.typography.titleSmall)
            Row(
                Modifier.fillMaxWidth().padding(vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(Modifier.weight(1f)) {
                    Text("后台接收", fontSize = 13.sp)
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
                    colors = monochromeSwitchColors(),
                )
            }
            Row(
                Modifier.clickable { openBatteryIgnore(ctx) }.padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    "电池优化白名单（防止休眠时漏收）",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Icon(
                    Icons.Outlined.NorthEast, null,
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(start = 6.dp).size(12.dp),
                )
            }
        }

        Card {
            Text("关于", style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(10.dp))
            Text("Columba（飞鸠）· v${com.faumulant.columba.BuildConfig.VERSION_NAME}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.height(8.dp))
            // 本机名 + 改名入口（调 Go.setDeviceName，广播身份即时更新）
            Row(
                Modifier.fillMaxWidth().clickable { showRename = true },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(Modifier.weight(1f)) {
                    Text("本机名", fontSize = 11.sp, color = MaterialTheme.colorScheme.outline)
                    Text(
                        self?.name?.ifBlank { "未命名" } ?: "未命名",
                        fontSize = 13.sp, style = MaterialTheme.typography.bodyLarge,
                    )
                }
                Icon(
                    Icons.Outlined.Edit, "修改本机名",
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.size(16.dp),
                )
            }
            Spacer(Modifier.height(8.dp))
            Text(
                "不经任何服务器，仅局域网点对点直连",
                fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
            )
            Spacer(Modifier.height(8.dp))
            Row(
                Modifier
                    .fillMaxWidth()
                    .clickable {
                        try {
                            ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://github.com/Faumulant/columba")))
                        } catch (_: Exception) {
                        }
                    }
                    .padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text("GitHub", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Icon(
                    Icons.Outlined.NorthEast, null,
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(start = 6.dp).size(12.dp),
                )
            }
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

    if (showRename) {
        RenameDialog(
            initial = self?.name ?: "",
            onDismiss = { showRename = false },
        )
    }

    removeDev?.let { d ->
        AppDialog(
            onDismiss = { removeDev = null },
            title = "移除设备",
            content = {
                Text(
                    "移除「${d.name}」？之后需要重新配对。",
                    fontSize = 13.sp, lineHeight = 19.sp,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            },
            buttons = {
                DlgButton("取消", "ghost") { removeDev = null }
                Spacer(Modifier.width(10.dp))
                DlgButton("移除设备", "danger") {
                    removeDev = null
                    Thread {
                        Go.removeDevice(d.id)
                        Bus.refreshDevices()
                    }.start()
                }
            },
        )
    }

    aliasEdit?.let { d ->
        AliasDialog(
            deviceName = d.name,
            onDismiss = { aliasEdit = null },
            onSave = { alias ->
                aliasEdit = null
                Thread {
                    Go.setAlias(d.id, alias)
                    Bus.refreshDevices()
                }.start()
            },
        )
    }
}

// 备注名弹窗：仅本机生效（优先于对方自报名显示），留空 = 清除备注恢复原名
@Composable
private fun AliasDialog(deviceName: String, onDismiss: () -> Unit, onSave: (String) -> Unit) {
    var text by remember { mutableStateOf("") }
    AppDialog(
        onDismiss = onDismiss,
        title = "修改备注名",
        content = {
            Text("给「$deviceName」的备注仅自己可见；留空清除。", fontSize = 13.sp)
            Spacer(Modifier.height(12.dp))
            OutlinedTextField(
                value = text,
                onValueChange = { text = it },
                modifier = Modifier.fillMaxWidth(),
                placeholder = { Text(deviceName) },
                singleLine = true,
                shape = RoundedCornerShape(10.dp),
            )
        },
        buttons = {
            DlgButton("取消", "ghost") { onDismiss() }
            Spacer(Modifier.width(10.dp))
            DlgButton("保存", "pri") { onSave(text.trim()) }
        },
    )
}

// 本机改名：写入身份并刷新 Bus.self（其他设备看到的名字随之更新）
@Composable
private fun RenameDialog(initial: String, onDismiss: () -> Unit) {
    var name by remember { mutableStateOf(initial) }
    var err by remember { mutableStateOf<String?>(null) }
    AppDialog(
        onDismiss = onDismiss,
        title = "修改本机名",
        content = {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it; err = null },
                singleLine = true,
                placeholder = { Text("给其他设备看的名字") },
                shape = RoundedCornerShape(10.dp),
            )
            if (err != null) {
                Spacer(Modifier.height(6.dp))
                Text(err!!, fontSize = 12.sp, color = MaterialTheme.colorScheme.error)
            }
        },
        buttons = {
            DlgButton("取消", "ghost") { onDismiss() }
            Spacer(Modifier.width(10.dp))
            DlgButton("保存", "pri") {
                val n = name.trim()
                if (n.isEmpty()) {
                    err = "名字不能为空"
                    return@DlgButton
                }
                Thread {
                    val r = Go.setDeviceName(n)
                    if (r.isSuccess) {
                        Bus.refreshAll()
                        Bus.showSnackbar("本机名已更新：$n")
                        onDismiss()
                    } else {
                        err = r.exceptionOrNull()?.message ?: "改名失败"
                    }
                }.start()
            }
        },
    )
}

// 单色 Chip：选中 = 反转实底，未选 = 描边
@Composable
private fun monochromeChipColors() = FilterChipDefaults.filterChipColors(
    containerColor = androidx.compose.ui.graphics.Color.Transparent,
    labelColor = MaterialTheme.colorScheme.onSurfaceVariant,
    selectedContainerColor = MaterialTheme.colorScheme.primary,
    selectedLabelColor = MaterialTheme.colorScheme.onPrimary,
)

// 单色 Switch：开 = 反转实底黑点，关 = 描边灰
@Composable
private fun monochromeSwitchColors() = SwitchDefaults.colors(
    checkedTrackColor = MaterialTheme.colorScheme.primary,
    checkedThumbColor = MaterialTheme.colorScheme.onPrimary,
    checkedBorderColor = androidx.compose.ui.graphics.Color.Transparent,
    uncheckedTrackColor = androidx.compose.ui.graphics.Color.Transparent,
    uncheckedThumbColor = MaterialTheme.colorScheme.onSurfaceVariant,
    uncheckedBorderColor = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.22f),
)

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
        Modifier.fillMaxWidth().padding(vertical = 5.dp),
        color = MaterialTheme.colorScheme.surfaceVariant,
        shape = RoundedCornerShape(16.dp),
        border = androidx.compose.foundation.BorderStroke(1.dp, HairlineColor),
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

    AppDialog(
        onDismiss = { if (!joining) onDismiss() },
        title = "添加设备",
        content = {
            Column {
                Text("方式一：本机出码（在电脑端输入）", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
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
                Text("方式二：输入对方显示的配对码", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
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
                    Text("正在配对…", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        },
        buttons = {
            DlgButton("取消", "ghost", enabled = !joining) { onDismiss() }
            Spacer(Modifier.width(10.dp))
            DlgButton("加入", "pri", enabled = !joining && input.isNotBlank()) {
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
            }
        },
    )
}

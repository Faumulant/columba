package com.faumulant.columba

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Drafts
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.ui.ColumbaTheme
import com.faumulant.columba.ui.ReceiveScreen
import com.faumulant.columba.ui.SendSheet
import com.faumulant.columba.ui.SettingsScreen

class MainActivity : ComponentActivity() {

    private var openReceiveTab = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        openReceiveTab = intent?.getStringExtra("open") == "receive"
        Bus.init(applicationContext)
        Notifier.ensureChannels(this)
        setContent { ColumbaTheme { AppRoot() } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        openReceiveTab = intent.getStringExtra("open") == "receive"
    }

    override fun onStart() {
        super.onStart()
        Receiver.foregroundActive = true
        val s = AppSettings.load(this)
        if (s.persistent) {
            Receiver.startPersistent(this)
        } else {
            val err = Receiver.ensureStarted(this)
            if (err != null) Bus.showSnackbar("接收端启动失败：$err")
        }
    }

    override fun onStop() {
        Receiver.foregroundActive = false
        val s = AppSettings.load(this)
        if (!s.persistent) Receiver.stop(this)
        super.onStop()
    }

    @Composable
    private fun AppRoot() {
        var tab by remember { mutableStateOf(Tab.RECEIVE) }

        var selectedTarget by remember { mutableStateOf<String?>(null) }
        var showSend by remember { mutableStateOf(false) }
        val snackbar = remember { SnackbarHostState() }
        LaunchedEffect(Bus.snackbar) {
            Bus.snackbar?.let {
                snackbar.showSnackbar(it)
                Bus.clearSnackbar()
            }
        }

        // 手动保存（D3）：SAF 创建文档
        var pendingSave by remember { mutableStateOf<HistoryEntry?>(null) }
        val saveLauncher = rememberLauncherForActivityResult(
            ActivityResultContracts.CreateDocument("application/octet-stream")
        ) { uri: Uri? ->
            val entry = pendingSave
            if (uri != null && entry != null && entry.path.isNotEmpty()) {
                Thread {
                    try {
                        contentResolver.openOutputStream(uri)?.use { out ->
                            java.io.File(entry.path).inputStream().use { it.copyTo(out) }
                        }
                        Bus.showSnackbar("已保存：${entry.name}")
                    } catch (e: Exception) {
                        Bus.showSnackbar("保存失败：${e.message}")
                    }
                }.start()
            }
            pendingSave = null
        }
        val onSaveRecord: (HistoryEntry) -> Unit = { entry ->
            pendingSave = entry
            saveLauncher.launch(entry.name.ifBlank { "file" })
        }

        Scaffold(
            snackbarHost = { SnackbarHost(snackbar) },
            bottomBar = {
                BottomBar(
                    tab = tab,
                    onTab = { tab = it },
                    onFab = { showSend = true },
                )
            },
        ) { padding ->
            Surface(Modifier.fillMaxSize().padding(padding), color = MaterialTheme.colorScheme.surface) {
                when (tab) {
                    Tab.RECEIVE -> ReceiveScreen(
                        selectedTargetId = selectedTarget,
                        onSelectTarget = { d ->
                            selectedTarget = d.id
                            Thread { Go.setTarget(d.id) }.start()
                        },
                        onSaveRecord = onSaveRecord,
                    )
                    Tab.SETTINGS -> SettingsScreen()
                }
            }
        }

        if (showSend) {
            SendSheet(
                selectedId = selectedTarget,
                onSelectTarget = { d ->
                    selectedTarget = d.id
                    Thread { Go.setTarget(d.id) }.start()
                },
                onDismiss = { showSend = false },
            )
        }

        // 配对请求弹窗（OnPairRequest → 允许/拒绝 → PairDecide）
        Bus.pairRequest?.let { req ->
            AlertDialog(
                onDismissRequest = { Bus.resolvePairRequest(false) },
                title = { Text("配对请求") },
                text = { Text("设备「${req.name}」请求与本机配对，允许吗？") },
                confirmButton = {
                    TextButton(onClick = { Bus.resolvePairRequest(true) }) { Text("允许") }
                },
                dismissButton = {
                    TextButton(onClick = { Bus.resolvePairRequest(false) }) { Text("拒绝") }
                },
            )
        }
    }
}

private enum class Tab { RECEIVE, SETTINGS }

// 底栏三段：接收（信封开合过渡）/ 圆形 FAB（凸出）/ 设置
@Composable
private fun BottomBar(tab: Tab, onTab: (Tab) -> Unit, onFab: () -> Unit) {
    Surface(color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.6f)) {
        Row(Modifier.fillMaxWidth().height(72.dp)) {
            BottomItem(
                Modifier.weight(1f),
                label = "接收",
                selected = tab == Tab.RECEIVE,
                onClick = { onTab(Tab.RECEIVE) },
            ) { selected ->
                // 信封动画：未选中=闭合，选中=开盖（D5 打磨项的前置简单版）
                AnimatedContent(
                    targetState = selected,
                    transitionSpec = { (fadeIn() + scaleIn(initialScale = 0.7f)) togetherWith (fadeOut() + scaleOut(targetScale = 0.7f)) },
                    label = "envelope",
                ) { sel ->
                    Icon(
                        if (sel) Icons.Filled.Drafts else Icons.Filled.Email,
                        contentDescription = "接收",
                        modifier = Modifier.size(26.dp),
                        tint = if (sel) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline,
                    )
                }
            }
            Box(Modifier.weight(1f), contentAlignment = Alignment.Center) {
                FloatingActionButton(
                    onClick = onFab,
                    shape = CircleShape,
                    containerColor = MaterialTheme.colorScheme.primary,
                    contentColor = MaterialTheme.colorScheme.onPrimary,
                    modifier = Modifier.offset(y = (-14).dp).size(58.dp),
                ) {
                    Icon(Icons.Filled.Add, "发送", modifier = Modifier.size(28.dp))
                }
            }
            BottomItem(
                Modifier.weight(1f),
                label = "设置",
                selected = tab == Tab.SETTINGS,
                onClick = { onTab(Tab.SETTINGS) },
            ) { selected ->
                Icon(
                    Icons.Filled.Settings,
                    contentDescription = "设置",
                    modifier = Modifier.size(26.dp),
                    tint = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline,
                )
            }
        }
    }
}

@Composable
private fun BottomItem(
    modifier: Modifier,
    label: String,
    selected: Boolean,
    onClick: () -> Unit,
    icon: @Composable (Boolean) -> Unit,
) {
    Column(
        modifier
            .clickable(onClick = onClick)
            .padding(top = 10.dp, bottom = 14.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        icon(selected)
        Text(
            label,
            fontSize = 11.sp,
            color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline,
        )
    }
}

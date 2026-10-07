package com.faumulant.columba

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Drafts
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.scale
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.ui.AppDialog
import com.faumulant.columba.ui.ColumbaTheme
import com.faumulant.columba.ui.DlgButton
import com.faumulant.columba.ui.HairlineColor
import com.faumulant.columba.ui.ReceiveScreen
import com.faumulant.columba.ui.SendSheet
import com.faumulant.columba.ui.SettingsScreen

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 沉浸式：状态栏/导航栏透出背景色，消除顶部白条
        enableEdgeToEdge()
        if (intent?.getStringExtra("open") == "receive") Bus.pushOpenReceive()
        Bus.init(applicationContext)
        parseShare(intent)?.let { Bus.pushShare(it) }
        Notifier.ensureChannels(this)
        setContent { ColumbaTheme { AppRoot() } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        if (intent.getStringExtra("open") == "receive") Bus.pushOpenReceive()
        parseShare(intent)?.let { Bus.pushShare(it) }
    }

    // 解析系统分享（SEND / SEND_MULTIPLE）：文本或一个/多个内容 URI；
    // target_id 为 Direct Share 快捷方式带来的直达目标设备
    private fun parseShare(intent: Intent?): Bus.SharePayload? {
        intent ?: return null
        val action = intent.action ?: return null
        if (action != Intent.ACTION_SEND && action != Intent.ACTION_SEND_MULTIPLE) return null
        val targetId = intent.getStringExtra("target_id")?.takeIf { it.isNotBlank() }
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)?.takeIf { it.isNotBlank() }
        val streams: List<Uri> = if (action == Intent.ACTION_SEND_MULTIPLE) {
            @Suppress("DEPRECATION")
            intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM) ?: emptyList()
        } else {
            @Suppress("DEPRECATION")
            listOfNotNull(intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))
        }
        if (text == null && streams.isEmpty()) return null
        return Bus.SharePayload(text = text, uris = streams, targetId = targetId)
    }

    override fun onStart() {
        super.onStart()
        Receiver.foregroundActive = true
        val s = AppSettings.load(this)
        if (s.persistent) {
            Receiver.startPersistent(this)
        } else {
            // ensureStarted 内含 gomobile 同步调用（绑 socket/读信任库），
            // 必须离开主线程（曾在 onStart 直接调用，冷启动卡顿源）
            Bus.bg {
                val err = Receiver.ensureStarted(this)
                if (err != null) Bus.showSnackbar("接收端启动失败：$err")
            }
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

        // 上次选择的发送目标：启动恢复，选择时持久化（左上角下拉与发送面板共用）
        var selectedTarget by remember {
            mutableStateOf(AppSettings.load(applicationContext).lastTargetId.ifBlank { null })
        }
        var showSend by remember { mutableStateOf(false) }
        // 居中反转胶囊 toast（与 PC 面板同语言），2.6 秒自动消失
        LaunchedEffect(Bus.snackbar) {
            if (Bus.snackbar != null) {
                kotlinx.coroutines.delay(2600)
                Bus.clearSnackbar()
            }
        }

        // 保存的设备可能已被移除：列表就绪后校验，失效则清空
        LaunchedEffect(Bus.devices) {
            val list = Bus.devices
            if (list.isNotEmpty() && selectedTarget != null && list.none { it.id == selectedTarget }) {
                selectedTarget = null
            }
        }

        // 系统分享进入：切到接收页并弹出发送面板（内容已预填）；
        // Direct Share 直达目标带 target_id 时预选该设备（防发错人）
        LaunchedEffect(Bus.shareIntent) {
            val share = Bus.shareIntent
            if (share != null) {
                if (share.targetId != null && Bus.devices.any { it.id == share.targetId }) {
                    selectedTarget = share.targetId
                }
                tab = Tab.RECEIVE
                showSend = true
            }
        }

        // 通知点击（"open"="receive"）：切到接收页
        LaunchedEffect(Bus.openReceive) {
            if (Bus.openReceive) {
                tab = Tab.RECEIVE
                Bus.consumeOpenReceive()
            }
        }

        val onSelectTarget: (com.faumulant.columba.Device) -> Unit = { d ->
            selectedTarget = d.id
            Bus.bg {
                Go.setTarget(d.id)
                applicationContext.saveSettings(
                    AppSettings.load(applicationContext).copy(lastTargetId = d.id)
                )
            }
        }

        // 手动保存（D3）：SAF 创建文档
        var pendingSave by remember { mutableStateOf<HistoryEntry?>(null) }
        val saveLauncher = rememberLauncherForActivityResult(
            ActivityResultContracts.CreateDocument("application/octet-stream")
        ) { uri: Uri? ->
            val entry = pendingSave
            if (uri != null && entry != null && entry.path.isNotEmpty()) {
                Bus.bg {
                    try {
                        contentResolver.openOutputStream(uri)?.use { out ->
                            java.io.File(entry.path).inputStream().use { it.copyTo(out) }
                        }
                        Bus.showSnackbar("已保存：${entry.name}")
                    } catch (e: Exception) {
                        Bus.showSnackbar("保存失败：${e.message}")
                    }
                }
            }
            pendingSave = null
        }
        val onSaveRecord: (HistoryEntry) -> Unit = { entry ->
            pendingSave = entry
            saveLauncher.launch(entry.name.ifBlank { "file" })
        }

        Scaffold(
            bottomBar = {
                BottomBar(
                    tab = tab,
                    onTab = { tab = it },
                    onFab = { showSend = true },
                )
            },
        ) { padding ->
            Box(Modifier.fillMaxSize()) {
            Surface(Modifier.fillMaxSize().padding(padding), color = MaterialTheme.colorScheme.surface) {
                // 页面切换：交叉淡化 200ms，克制不抢戏
                AnimatedContent(
                    targetState = tab,
                    transitionSpec = { fadeIn(tween(200)) togetherWith fadeOut(tween(160)) },
                    label = "page",
                ) { t ->
                    when (t) {
                        Tab.RECEIVE -> ReceiveScreen(
                            onSaveRecord = onSaveRecord,
                        )
                        Tab.SETTINGS -> SettingsScreen()
                    }
                }
            }
            // 居中反转胶囊 toast（与 PC 面板同语言）
            Bus.snackbar?.let { msg ->
                Box(
                    Modifier.fillMaxSize().padding(bottom = 120.dp),
                    contentAlignment = Alignment.Center,
                ) {
                    Surface(
                        shape = RoundedCornerShape(12.dp),
                        color = MaterialTheme.colorScheme.inverseSurface,
                        shadowElevation = 6.dp,
                    ) {
                        Text(
                            msg,
                            color = MaterialTheme.colorScheme.inverseOnSurface,
                            fontSize = 13.sp,
                            modifier = Modifier.padding(horizontal = 18.dp, vertical = 12.dp),
                        )
                    }
                }
            }
            }
        }

        if (showSend) {
            SendSheet(
                selectedId = selectedTarget,
                onSelectTarget = onSelectTarget,
                shared = Bus.shareIntent,
                onDismiss = { showSend = false; Bus.consumeShare() },
            )
        }

        // 配对请求弹窗（OnPairRequest → 允许/拒绝 → PairDecide）
        Bus.pairRequest?.let { req ->
            AppDialog(
                onDismiss = { Bus.resolvePairRequest(false) },
                title = "配对请求",
                content = {
                    Text(
                        "设备「${req.name}」请求与本机配对，允许吗？",
                        fontSize = 13.sp, lineHeight = 19.sp,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                },
                buttons = {
                    DlgButton("拒绝", "ghost") { Bus.resolvePairRequest(false) }
                    Spacer(Modifier.width(10.dp))
                    DlgButton("允许", "pri") { Bus.resolvePairRequest(true) }
                },
            )
        }
    }
}

private enum class Tab { RECEIVE, SETTINGS }

// 底栏三段：接收（信封开合过渡）/ 圆形 FAB（凸出）/ 设置
// 结构要点：外层 Box 不做任何裁切，FAB 叠在底条之上自然凸出（修复旧版 Surface 裁掉 FAB 顶部的问题）
@Composable
private fun BottomBar(tab: Tab, onTab: (Tab) -> Unit, onFab: () -> Unit) {
    // 底栏测量高度 = 底条 64dp + 手势条；内容区正好排到发丝线为止。
    // FAB 用 offset 悬出测量区、压在滚动内容上——中间没有「死区」，
    // 文字从 FAB 背后滚过、到发丝线被底条截住，日间/夜间都成立。
    Column(Modifier.fillMaxWidth()) {
        Box(
            Modifier.fillMaxWidth()
                .height(64.dp)
                .background(MaterialTheme.colorScheme.surfaceContainerLow)
        ) {
            HorizontalDivider(color = HairlineColor, modifier = Modifier.align(Alignment.TopCenter))
            Row(
                Modifier.fillMaxSize(),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                BottomItem(
                    Modifier.weight(1f),
                    label = "接收",
                    selected = tab == Tab.RECEIVE,
                    onClick = { onTab(Tab.RECEIVE) },
                ) { selected ->
                    // 信封动画：未选中=闭合，选中=开盖（保留原有动效）
                    AnimatedContent(
                        targetState = selected,
                        transitionSpec = { (fadeIn() + scaleIn(initialScale = 0.7f)) togetherWith (fadeOut() + scaleOut(targetScale = 0.7f)) },
                        label = "envelope",
                    ) { sel ->
                        Icon(
                            if (sel) Icons.Filled.Drafts else Icons.Filled.Email,
                            contentDescription = "接收",
                            modifier = Modifier.size(22.dp),
                            tint = if (sel) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.outline,
                        )
                    }
                }
                Spacer(Modifier.weight(1f)) // 中央 FAB 占位
                BottomItem(
                    Modifier.weight(1f),
                    label = "设置",
                    selected = tab == Tab.SETTINGS,
                    onClick = { onTab(Tab.SETTINGS) },
                ) { selected ->
                    Icon(
                        Icons.Filled.Settings,
                        contentDescription = "设置",
                        modifier = Modifier.size(22.dp),
                        tint = if (selected) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.outline,
                    )
                }
            }
            // 凸出 FAB：上探 28dp 悬在内容上，按压 ×0.92 弹簧回弹
            PressFab(
                onClick = onFab,
                modifier = Modifier.align(Alignment.TopCenter).offset(y = (-28).dp),
            )
        }
        // 手势条区域与底条同色，视觉连续到底
        Spacer(
            Modifier.fillMaxWidth()
                .background(MaterialTheme.colorScheme.surfaceContainerLow)
                .navigationBarsPadding()
        )
    }
}

@Composable
private fun PressFab(onClick: () -> Unit, modifier: Modifier = Modifier) {
    val interactionSource = remember { MutableInteractionSource() }
    val pressed by interactionSource.collectIsPressedAsState()
    val fabScale by animateFloatAsState(
        targetValue = if (pressed) 0.92f else 1f,
        animationSpec = spring(dampingRatio = Spring.DampingRatioMediumBouncy, stiffness = Spring.StiffnessMedium),
        label = "fabPress",
    )
    FloatingActionButton(
        onClick = onClick,
        shape = CircleShape,
        containerColor = MaterialTheme.colorScheme.primary,
        contentColor = MaterialTheme.colorScheme.onPrimary,
        interactionSource = interactionSource,
        modifier = modifier.size(57.dp).scale(fabScale),
    ) {
        Icon(Icons.Filled.Add, "发送", modifier = Modifier.size(24.dp))
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
        modifier.clickable(onClick = onClick),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        icon(selected)
        Spacer(Modifier.height(3.dp))
        Text(
            label,
            fontSize = 10.sp,
            style = MaterialTheme.typography.labelMedium,
            color = if (selected) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.outline,
        )
    }
}

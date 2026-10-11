package com.faumulant.columba.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import com.faumulant.columba.Bus
import com.faumulant.columba.R

// 黑白单色优雅体系（2026-09 视觉定稿，取代原信使蓝）：
// 不靠彩色表达层级，用明暗与留白；强调色 = 黑白反转（深底白按钮 / 浅底黑按钮）。
// 状态色只保留两粒「哑光」彩色：成功灰绿、失败灰陶土，刻意压低存在感。

private val DarkColors = darkColorScheme(
    primary = Color(0xFFE8E8E6),           // 反转强调：白
    onPrimary = Color(0xFF16171A),
    primaryContainer = Color(0xFFE8E8E6),
    onPrimaryContainer = Color(0xFF16171A),
    secondary = Color(0xFF909095),
    onSecondary = Color(0xFF16171A),
    background = Color(0xFF16171A),
    onBackground = Color(0xFFF0F0EE),
    surface = Color(0xFF16171A),
    onSurface = Color(0xFFF0F0EE),
    surfaceVariant = Color(0xFF1F2125),     // 卡片
    onSurfaceVariant = Color(0xFF909095),   // 次级文字
    surfaceContainerLow = Color(0xFF191B1E),// 底栏
    surfaceContainerHigh = Color(0xFF222428),// 底部弹层
    outline = Color(0xFF6F7076),            // 弱提示文字
    outlineVariant = Color(0xFF2C2E33),     // 发丝线
    error = Color(0xFFC68F82),              // 哑光灰陶土
    onError = Color(0xFF16171A),
    inverseSurface = Color(0xFFE8E8E6),
    inverseOnSurface = Color(0xFF16171A),
    inversePrimary = Color(0xFF1B1C1A),
)

private val LightColors = lightColorScheme(
    primary = Color(0xFF1B1C1A),            // 反转强调：黑
    onPrimary = Color(0xFFF7F7F5),
    primaryContainer = Color(0xFF1B1C1A),
    onPrimaryContainer = Color(0xFFF7F7F5),
    secondary = Color(0xFF66686A),
    onSecondary = Color(0xFFF7F7F5),
    background = Color(0xFFF2F3F2),
    onBackground = Color(0xFF1B1C1A),
    surface = Color(0xFFF2F3F2),
    onSurface = Color(0xFF1B1C1A),
    surfaceVariant = Color(0xFFFFFFFF),     // 卡片
    onSurfaceVariant = Color(0xFF66686A),
    surfaceContainerLow = Color(0xFFFFFFFF),// 底栏
    surfaceContainerHigh = Color(0xFFFFFFFF),// 底部弹层
    outline = Color(0xFF93959A),
    outlineVariant = Color(0xFFE2E4E3),
    error = Color(0xFFA85A4C),
    onError = Color(0xFFF7F7F5),
    inverseSurface = Color(0xFF1B1C1A),
    inverseOnSurface = Color(0xFFF7F7F5),
    inversePrimary = Color(0xFFE8E8E6),
)

// 手写体：中文马善政（「咕咕咕~」）、英文 Caveat（columba / waiting for a drop…）
val HandZh = FontFamily(Font(R.font.ma_shan_zheng))
val HandEn = FontFamily(Font(R.font.caveat))

// 生效暗色：手动模式（点斑鸠切换）优先，跟随系统兜底。
// 全文件所有明暗分支都走这里，保证切换单一来源。
@Composable
fun isAppDark(): Boolean = when (Bus.themeMode) {
    "light" -> false
    "dark" -> true
    else -> isSystemInDarkTheme()
}

// 设计稿之外的语义色：成功（哑光灰绿）。失败直接用 colorScheme.error。
val SuccessColor: Color
    @Composable get() = if (isAppDark()) Color(0xFF8FAE8B) else Color(0xFF5F7A5B)

// 发丝边框（卡片/描边按钮共用）：比 outlineVariant 更轻一点
val HairlineColor: Color
    @Composable get() = if (isAppDark()) Color(0x14FFFFFF) else Color(0x14000000)

// 商标斑鸠：深底白鸽 / 浅底黑鸽（透明剪影，res/drawable-nodpi）
@Composable
fun dovePainter(): Painter =
    painterResource(if (isAppDark()) R.drawable.dove_white else R.drawable.dove_black)

@Composable
fun ColumbaTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (isAppDark()) DarkColors else LightColors,
        content = content,
    )
}

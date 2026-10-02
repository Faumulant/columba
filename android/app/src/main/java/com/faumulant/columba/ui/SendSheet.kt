package com.faumulant.columba.ui

import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.AttachFile
import androidx.compose.material.icons.filled.NorthEast
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.Bus
import com.faumulant.columba.Device
import com.faumulant.columba.Go
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

// FAB 发送面板：目标设备 + 文件（系统选择器）/ 文本
// 失败原因内联在面板里（不再依赖会被遮罩盖住的 snackbar）
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SendSheet(
    selectedId: String?,
    onSelectTarget: (Device) -> Unit,
    onDismiss: () -> Unit,
) {
    val ctx = LocalContext.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    var text by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var pickedNames by remember { mutableStateOf<List<String>>(emptyList()) }
    var sendError by remember { mutableStateOf<String?>(null) }

    val filePicker = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenMultipleDocuments()
    ) { uris: List<Uri> ->
        if (uris.isEmpty()) return@rememberLauncherForActivityResult
        pickedNames = uris.map { it.displayName(ctx) }
        sendError = null
        busy = true
        scope.launch(Dispatchers.IO) {
            try {
                val paths = ArrayList<String>(uris.size)
                // 每次发送独立子目录：保留原始文件名（Go 端按文件名传输），
                // 时间戳目录避免多次发送同名互相覆盖
                val dir = java.io.File(java.io.File(ctx.cacheDir, "sends"), System.nanoTime().toString())
                dir.mkdirs()
                for (u in uris) {
                    val name = sanitizeName(u.displayName(ctx))
                    var f = java.io.File(dir, name)
                    var n = 1
                    while (f.exists()) { // 同名去重
                        f = java.io.File(dir, "($n) $name")
                        n++
                    }
                    ctx.contentResolver.openInputStream(u)?.use { input ->
                        f.outputStream().use { input.copyTo(it) }
                    } ?: continue
                    paths.add(f.absolutePath)
                }
                val r = Go.sendFiles(selectedId ?: "", paths)
                withContext2 {
                    if (r.isSuccess) onDismiss() else sendError = "发送失败：${r.exceptionOrNull()?.message}"
                }
            } catch (e: Exception) {
                withContext2 { sendError = "发送失败：${e.message}" }
            } finally {
                withContext2 { busy = false }
            }
        }
    }

    ModalBottomSheet(
        onDismissRequest = { if (!busy) onDismiss() },
        containerColor = MaterialTheme.colorScheme.surfaceContainerHigh,
    ) {
        Column(
            Modifier.fillMaxWidth().padding(horizontal = 20.dp).padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            // 目标行：标签在左，设备选择在右
            Row(
                Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text("发送至", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                DeviceDropdown(selectedId = selectedId, onSelect = onSelectTarget)
            }

            // 次按钮：描边 + 文字（原纯图标 FilledIconButton 层级不清）
            OutlinedButton(
                onClick = { filePicker.launch(arrayOf("*/*")) },
                enabled = !busy,
                modifier = Modifier.fillMaxWidth().height(46.dp),
                shape = RoundedCornerShape(12.dp),
                border = androidx.compose.foundation.BorderStroke(1.dp, MaterialTheme.colorScheme.onSurface.copy(alpha = 0.16f)),
            ) {
                Icon(Icons.Filled.AttachFile, null, modifier = Modifier.size(16.dp))
                Text("选择文件", fontSize = 13.sp, modifier = Modifier.padding(start = 8.dp))
            }
            if (pickedNames.isNotEmpty()) {
                Text(
                    "已选：${pickedNames.joinToString("、")}",
                    fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
                )
            }

            OutlinedTextField(
                value = text,
                onValueChange = { text = it; sendError = null },
                modifier = Modifier.fillMaxWidth(),
                placeholder = { Text("说点什么…（可选）", color = MaterialTheme.colorScheme.outline) },
                minLines = 3,
                maxLines = 6,
                enabled = !busy,
                shape = RoundedCornerShape(12.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.3f),
                    unfocusedBorderColor = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.1f),
                ),
            )

            // 主按钮：实心反转色，发送文本
            Button(
                onClick = {
                    val t = text.trim()
                    if (t.isEmpty()) return@Button
                    sendError = null
                    busy = true
                    scope.launch(Dispatchers.IO) {
                        val r = Go.sendText(selectedId ?: "", t)
                        launch(Dispatchers.Main) {
                            busy = false
                            if (r.isSuccess) {
                                text = ""
                                onDismiss()
                            } else sendError = "发送失败：${r.exceptionOrNull()?.message}"
                        }
                    }
                },
                enabled = !busy && text.isNotBlank(),
                modifier = Modifier.fillMaxWidth().height(48.dp),
                shape = RoundedCornerShape(12.dp),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(20.dp), strokeWidth = 2.dp,
                        color = MaterialTheme.colorScheme.onPrimary,
                    )
                } else {
                    Text("发送", fontSize = 14.sp, style = MaterialTheme.typography.titleSmall)
                    Icon(
                        Icons.Filled.NorthEast, null,
                        modifier = Modifier.padding(start = 6.dp).size(15.dp),
                    )
                }
            }

            // 失败内联提示：面板内可见，不被遮罩遮挡
            if (sendError != null) {
                Text(sendError!!, fontSize = 11.sp, color = MaterialTheme.colorScheme.error)
            }

            // 斑鸠叫声，手写体落款（纯点缀）
            Text(
                "咕咕咕~",
                fontFamily = HandZh,
                fontSize = 16.sp,
                color = MaterialTheme.colorScheme.outline.copy(alpha = 0.7f),
                modifier = Modifier.fillMaxWidth().rotate(-2f),
                textAlign = androidx.compose.ui.text.style.TextAlign.Center,
            )
        }
    }
}

private fun Uri.displayName(ctx: android.content.Context): String {
    try {
        ctx.contentResolver.query(this, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                val i = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (i >= 0) {
                    val n = c.getString(i)
                    if (!n.isNullOrBlank()) return n
                }
            }
        }
    } catch (_: Exception) {
    }
    return "file_${System.currentTimeMillis()}"
}

private fun sanitizeName(n: String): String =
    n.replace('/', '_').replace('\\', '_').ifBlank { "file" }

// 小工具：在 IO 协程里回到主线程更新 UI
fun withContext2(block: () -> Unit) {
    android.os.Handler(android.os.Looper.getMainLooper()).post(block)
}

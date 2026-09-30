package com.faumulant.columba.ui

import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.AttachFile
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.Bus
import com.faumulant.columba.Device
import com.faumulant.columba.Go
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

// FAB 发送面板：目标设备下拉 + 文件（系统选择器）/文本，箭头 = 发送（邮件隐喻）
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

    val filePicker = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenMultipleDocuments()
    ) { uris: List<Uri> ->
        if (uris.isEmpty()) return@rememberLauncherForActivityResult
        pickedNames = uris.map { it.displayName(ctx) }
        busy = true
        scope.launch(Dispatchers.IO) {
            try {
                val paths = ArrayList<String>(uris.size)
                val dir = java.io.File(ctx.cacheDir, "sends").apply { mkdirs() }
                for (u in uris) {
                    val name = sanitizeName(u.displayName(ctx))
                    val f = java.io.File(dir, "${System.nanoTime()}_$name")
                    ctx.contentResolver.openInputStream(u)?.use { input ->
                        f.outputStream().use { input.copyTo(it) }
                    } ?: continue
                    paths.add(f.absolutePath)
                }
                val r = Go.sendFiles(selectedId ?: "", paths)
                withContext2 {
                    if (r.isSuccess) onDismiss() else Bus.showSnackbar("发送失败：${r.exceptionOrNull()?.message}")
                }
            } catch (e: Exception) {
                Bus.showSnackbar("发送失败：${e.message}")
            } finally {
                busy = false
            }
        }
    }

    ModalBottomSheet(onDismissRequest = { if (!busy) onDismiss() }) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 20.dp).padding(bottom = 28.dp)) {
            Text("发送到", fontSize = 13.sp, color = MaterialTheme.colorScheme.outline)
            DeviceDropdown(selectedId = selectedId, onSelect = onSelectTarget)
            Spacer(Modifier.height(10.dp))

            OutlinedTextField(
                value = text,
                onValueChange = { text = it },
                modifier = Modifier.fillMaxWidth(),
                placeholder = { Text("输入要发送的文本") },
                minLines = 2,
                maxLines = 6,
                enabled = !busy,
            )
            if (pickedNames.isNotEmpty()) {
                Text(
                    "文件：${pickedNames.joinToString("、")}",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.outline,
                    modifier = Modifier.padding(top = 6.dp),
                )
            }
            Spacer(Modifier.height(14.dp))
            Row(
                Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                FilledIconButton(onClick = { filePicker.launch(arrayOf("*/*")) }, enabled = !busy) {
                    Icon(Icons.Filled.AttachFile, "选择文件")
                }
                FilledIconButton(
                    onClick = {
                        val t = text.trim()
                        if (t.isEmpty()) return@FilledIconButton
                        busy = true
                        scope.launch(Dispatchers.IO) {
                            val r = Go.sendText(selectedId ?: "", t)
                            launch(Dispatchers.Main) {
                                busy = false
                                if (r.isSuccess) {
                                    text = ""
                                    onDismiss()
                                } else Bus.showSnackbar("发送失败：${r.exceptionOrNull()?.message}")
                            }
                        }
                    },
                    enabled = !busy && text.isNotBlank(),
                    shape = CircleShape,
                    colors = IconButtonDefaults.filledIconButtonColors(
                        containerColor = MaterialTheme.colorScheme.primary,
                    ),
                    modifier = Modifier.size(52.dp),
                ) {
                    if (busy) {
                        CircularProgressIndicator(
                            modifier = Modifier.size(22.dp), strokeWidth = 2.dp,
                            color = MaterialTheme.colorScheme.onPrimary,
                        )
                    } else {
                        Icon(Icons.Filled.ArrowUpward, "发送", modifier = Modifier.size(24.dp))
                    }
                }
            }
            Spacer(Modifier.height(4.dp))
            Text(
                "文件走系统选择器，文本即输即发；未选目标时先在左上角选择",
                fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
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

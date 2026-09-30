package com.faumulant.columba.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.Bus
import com.faumulant.columba.Device

// 顶部目标设备下拉（发送目标）：在线点 + 设备名 + 下拉箭头，单色表达
@Composable
fun DeviceDropdown(
    selectedId: String?,
    onSelect: (Device) -> Unit,
    modifier: Modifier = Modifier,
) {
    var open by remember { mutableStateOf(false) }
    val devices = Bus.devices
    val sel = devices.firstOrNull { it.id == selectedId }

    Box(modifier) {
        Row(
            Modifier
                .clickable { open = true; Bus.refreshDevices() }
                .padding(horizontal = 10.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            OnlineDot(sel?.online == true, size = 7)
            Text(
                sel?.name?.ifBlank { null } ?: "未选择设备",
                fontSize = 15.sp,
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.onSurface,
                modifier = Modifier.padding(start = 8.dp, end = 2.dp),
            )
            Icon(
                Icons.Filled.ArrowDropDown, null,
                tint = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.size(16.dp),
            )
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            if (devices.isEmpty()) {
                Text(
                    "还没有已配对设备\n去 设置 → 设备管理 添加",
                    fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
            }
            for (d in devices) {
                DropdownMenuItem(
                    text = {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            OnlineDot(d.online)
                            Text(d.name.ifBlank { "设备" }, modifier = Modifier.padding(start = 8.dp))
                        }
                    },
                    onClick = { open = false; onSelect(d) },
                )
            }
        }
    }
}

// 单色在线点：在线 = 高亮实心，离线 = 弱灰
@Composable
fun OnlineDot(online: Boolean, size: Int = 8) {
    Box(
        Modifier
            .size(size.dp)
            .background(
                if (online) MaterialTheme.colorScheme.onSurface
                else MaterialTheme.colorScheme.onSurface.copy(alpha = 0.25f),
                CircleShape
            )
    )
}

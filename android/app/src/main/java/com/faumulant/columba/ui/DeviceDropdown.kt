package com.faumulant.columba.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.DeviceHub
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.faumulant.columba.Bus
import com.faumulant.columba.Device

// 左上角小型目标设备下拉（发送目标；与桌面顶栏同构），带在线状态点
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
            Icon(
                Icons.Filled.DeviceHub, null,
                tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(18.dp)
            )
            Text(
                sel?.name?.ifBlank { null } ?: "未选择设备",
                fontSize = 14.sp,
                modifier = Modifier.padding(start = 6.dp, end = 2.dp),
            )
            Icon(Icons.Filled.ArrowDropDown, null, modifier = Modifier.size(16.dp))
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            if (devices.isEmpty()) {
                Text(
                    "还没有已配对设备\n去 设置 → 设备管理 添加",
                    fontSize = 13.sp, color = MaterialTheme.colorScheme.outline,
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

@Composable
fun OnlineDot(online: Boolean, size: Int = 8) {
    Box(
        Modifier
            .size(size.dp)
            .background(
                if (online) Color(0xFF22C55E) else Color(0xFFCBD5E1),
                CircleShape
            )
    )
}

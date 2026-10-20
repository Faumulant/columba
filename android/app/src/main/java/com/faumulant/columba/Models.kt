package com.faumulant.columba

import org.json.JSONArray
import org.json.JSONObject

// JSON 模型：对应 core/mobileapi 的 JSON 输出与 core/history.Entry。
// 用 Android 内置 org.json 解析，不引入额外序列化依赖。

data class Device(val id: String, val name: String, val online: Boolean)

data class SelfInfo(val name: String, val id: String, val displayId: String) {
    companion object {
        fun parse(json: String): SelfInfo {
            val o = JSONObject(json)
            return SelfInfo(o.optString("name"), o.optString("id"), o.optString("display_id"))
        }
    }
}

// 配对设备上限：limit 已按解锁状态解析（解锁后为极大值），unlocked 供 UI 决定
// 是否显示「解除上限」入口。标记文件与桌面端共用，两端一次解锁。
data class DeviceLimit(val limit: Int, val unlocked: Boolean) {
    companion object {
        fun parse(json: String): DeviceLimit {
            val o = JSONObject(json)
            return DeviceLimit(o.optInt("limit", 3), o.optBoolean("unlocked"))
        }
    }
}

data class HistoryEntry(
    val id: String = "",
    val time: String = "",
    val dir: String = "recv",   // "recv" / "send"
    val peer: String = "",
    val name: String = "",
    val size: Long = 0,
    val status: String = "ok",  // "ok" / "failed"
    val path: String = "",
    val error: String = "",
    val kind: String = "",      // "text" 或空（文件）
    val text: String = "",
) {
    val isText: Boolean get() = kind == "text"
    val isRecv: Boolean get() = dir == "recv"

    // 分类：与桌面面板 catOf() 完全同构（文件/音频/图片/视频/文本 五类）
    val category: Category
        get() = when {
            isText -> Category.TEXT
            else -> categoryOfName(name)
        }

    enum class Category(val label: String) {
        FILE("文件"), AUDIO("音频"), IMAGE("图片"), VIDEO("视频"), TEXT("文本")
    }

    companion object {
        fun categoryOfName(name: String): Category {
            val ext = name.substringAfterLast('.', "").lowercase()
            return when (ext) {
                "mp3", "flac", "wav", "m4a", "ogg", "aac" -> Category.AUDIO
                "jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "heic" -> Category.IMAGE
                "mp4", "mkv", "mov", "avi", "webm", "flv", "wmv" -> Category.VIDEO
                else -> Category.FILE
            }
        }

        fun parseList(json: String): List<HistoryEntry> {
            val arr = JSONArray(json)
            val out = ArrayList<HistoryEntry>(arr.length())
            for (i in 0 until arr.length()) {
                val o = arr.getJSONObject(i)
                out.add(
                    HistoryEntry(
                        id = o.optString("id"),
                        time = o.optString("time"),
                        dir = o.optString("dir"),
                        peer = o.optString("peer"),
                        name = o.optString("name"),
                        size = o.optLong("size"),
                        status = o.optString("status"),
                        path = o.optString("path"),
                        error = o.optString("error"),
                        kind = o.optString("kind"),
                        text = o.optString("text"),
                    )
                )
            }
            return out
        }
    }
}

// 设备列表解析结果：Go 侧读取失败时返回 [{"error":"…"}]（而非空数组），
// 用来区分「真的没有配对设备」与「信任库读不出来」——后者必须提示用户，
// 否则界面显示「还没有配对设备」，用户会以为数据丢了并重新配对。
class DeviceList(val devices: List<Device>, val error: String?) {
    companion object {
        fun parse(json: String): DeviceList {
            val arr = JSONArray(json)
            // 失败形态是单个带 error 字段的对象
            if (arr.length() == 1) {
                val o = arr.getJSONObject(0)
                if (o.has("error")) return DeviceList(emptyList(), o.optString("error"))
            }
            val out = ArrayList<Device>(arr.length())
            for (i in 0 until arr.length()) {
                val o = arr.getJSONObject(i)
                out.add(Device(o.optString("id"), o.optString("name"), o.optBoolean("online")))
            }
            return DeviceList(out, null)
        }
    }
}

// Go 端批量参数用 JSON 字符串数组（gomobile 不支持 []string）
fun jsonArrayOf(items: List<String>): String {
    val arr = JSONArray()
    for (s in items) arr.put(s)
    return arr.toString()
}

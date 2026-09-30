package com.faumulant.columba

import mobileapi.Mobileapi

// core/mobileapi（gomobile AAR）的薄封装：
// - gobind 把 Go 的 (T, error) 双返回值映射为「返回 T + 抛异常」，这里统一转成 Result
// - 名字保持 Go 原名（Mobileapi.setDataDir 等），便于与 core/mobileapi 对照
object Go {

    private fun <T> call(block: () -> T): Result<T> = try {
        Result.success(block())
    } catch (e: Throwable) {
        // gobind 包装的 Go 错误消息在异常 message 里（常带前缀），剥掉影响可读性的部分
        Result.failure(RuntimeException(e.readableMessage(), e.cause ?: e))
    }

    private fun Throwable.readableMessage(): String {
        var m = message ?: ""
        // gobind 异常形如 "mobileapi:xxx: 实际消息"，保留中文消息本体
        val idx = m.lastIndexOf(": ")
        if (idx in 0 until m.length - 2) m = m.substring(idx + 2)
        return m
    }

    // ─── 初始化 ────────────────────────────────────────────────

    fun setDataDir(dir: String): Result<Unit> = call { Mobileapi.setDataDir(dir) }
    fun setPort(port: Int): Result<Unit> = call { Mobileapi.setPort(port.toLong()) }
    fun selfInfo(): SelfInfo = try {
        SelfInfo.parse(Mobileapi.selfInfo())
    } catch (e: Throwable) {
        SelfInfo("", "", "")
    }

    // ─── 生命周期 ──────────────────────────────────────────────

    fun startReceiver(saveDir: String, autoSave: Boolean, listener: mobileapi.EventListener): Result<Unit> =
        call { Mobileapi.startReceiver(saveDir, autoSave, listener) }

    fun stopReceiver() = try { Mobileapi.stopReceiver() } catch (_: Throwable) {}

    fun startDiscovery(listener: mobileapi.EventListener?): Result<Unit> =
        call { if (listener == null) Mobileapi.startDiscovery(null) else Mobileapi.startDiscovery(listener) }

    fun stopDiscovery() = try { Mobileapi.stopDiscovery() } catch (_: Throwable) {}

    // ─── 设备与配对 ────────────────────────────────────────────

    fun listDevices(): List<Device> = try {
        parseDevices(Mobileapi.listDevices())
    } catch (e: Throwable) {
        emptyList()
    }

    fun pairStart(): Result<String> = call { Mobileapi.pairStart() }
    fun pairJoin(code: String): Result<String> = call { Mobileapi.pairJoin(code) }
    fun pairDecide(allow: Boolean): Result<Unit> = call { Mobileapi.pairDecide(allow) }
    fun removeDevice(id: String): Result<Unit> = call { Mobileapi.removeDevice(id) }

    // ─── 发送 ──────────────────────────────────────────────────

    fun setTarget(targetId: String): Result<Unit> = call { Mobileapi.setTarget(targetId) }
    fun sendText(targetId: String, text: String): Result<Unit> = call { Mobileapi.sendText(targetId, text) }
    fun sendFiles(targetId: String, paths: List<String>): Result<Unit> =
        call { Mobileapi.sendFiles(targetId, jsonArrayOf(paths)) }

    // ─── 历史与设置 ────────────────────────────────────────────

    fun history(): List<HistoryEntry> = try {
        HistoryEntry.parseList(Mobileapi.historyJSON())
    } catch (e: Throwable) {
        emptyList()
    }

    fun deleteHistory(ids: List<String>): Result<Unit> = call { Mobileapi.deleteHistory(jsonArrayOf(ids)) }
    fun setSaveDir(dir: String): Result<Unit> = call { Mobileapi.setSaveDir(dir) }
}

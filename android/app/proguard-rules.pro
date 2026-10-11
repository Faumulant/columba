# gomobile 绑定层通过 JNI 反射调用，避免被裁剪
-keep class mobileapi.** { *; }
-keep class go.** { *; }
# Go 侧事件回调（OnDeviceChanged/OnProgress/OnTransfer/OnPairRequest）经 JNI
# 按「方法名」在监听器实现类上反射查找——R8 重命名/移除这些方法会让事件链
# 静默断裂（症状：传输正常但信标/在线状态/进度通知永不更新）。
# 实测：R8 版在线感知失效，真机复现后补此规则（2026-10-11）。
-keep class * implements mobileapi.EventListener { *; }

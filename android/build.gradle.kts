// Columba (Drop 安卓端) 根构建脚本。
// 版本组合（互相兼容，gomobile 管道要求）：
//   Gradle 8.9 + AGP 8.7.3 + Kotlin 2.0.21（Compose 编译器随 Kotlin 插件内置）
plugins {
    id("com.android.application") version "8.7.3" apply false
    id("org.jetbrains.kotlin.android") version "2.0.21" apply false
    id("org.jetbrains.kotlin.plugin.compose") version "2.0.21" apply false
}

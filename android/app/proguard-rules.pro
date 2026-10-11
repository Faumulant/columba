# gomobile 绑定层通过 JNI 反射调用，避免被裁剪
-keep class mobileapi.** { *; }
-keep class go.** { *; }

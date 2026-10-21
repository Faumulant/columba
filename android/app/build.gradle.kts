plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "com.faumulant.columba"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.faumulant.columba"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // 日常安装走 release 形态（R8 收缩 + 资源裁剪）：签名复用 debug
            // 密钥，与 debug 包同签名可原地覆盖升级，无需额外密钥管理
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    // ABI 分包：完整包（四架构，约 36MB）+ 每架构一个单独包。
    //
    // 为什么不用 abiFilters 只留 arm64：那样会**丢掉**完整包，模拟器就没法装了。
    // splits 是「完整包 + 每架构一个」并存，isUniversalApk=true 正是保留完整包。
    // 对外只发 arm64-v8a 那个（约 12MB），其余留在本地自用。
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a", "armeabi-v7a", "x86", "x86_64")
            isUniversalApk = true
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        compose = true
        buildConfig = true
    }
    packaging {
        // gomobile AAR 内的 JNI so 不需要去重处理，但元数据冲突需要排除
        resources.excludes += "META-INF/AL2.0"
        resources.excludes += "META-INF/LGPL2.1"
    }
}

dependencies {
    // gomobile bind 产物（core/mobileapi 桥接层）
    implementation(files("libs/mobileapi.aar"))

    val composeBom = platform("androidx.compose:compose-bom:2024.09.03")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.activity:activity-compose:1.9.2")
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.6")
    implementation("androidx.documentfile:documentfile:1.0.1")
    debugImplementation("androidx.compose.ui:ui-tooling")
}

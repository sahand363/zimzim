plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.sahand.zimzim"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.sahand.zimzim"
        minSdk = 26
        targetSdk = 34
        versionCode = 3
        versionName = "1.1.1"
    }

    // Release key comes from GitHub secrets (ZZ_KEYSTORE_B64 / ZZ_KEYSTORE_PASS) and never lives in the repo.
    // Until those secrets exist, builds fall back to the old committed key so updates still install.
    val ks = System.getenv("ZZ_KEYSTORE")
    val ksPass = System.getenv("ZZ_KEYSTORE_PASS")
    signingConfigs {
        create("release") {
            if (!ks.isNullOrBlank() && !ksPass.isNullOrBlank()) {
                storeFile = file(ks)
                storeType = "pkcs12"
                storePassword = ksPass
                keyAlias = "zimzim"
                keyPassword = ksPass
            } else {
                storeFile = rootProject.file("zimzim.p12")
                storeType = "pkcs12"
                storePassword = "android"
                keyAlias = "androiddebugkey"
                keyPassword = "android"
            }
        }
    }
    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("release")
        }
    }
    buildFeatures { buildConfig = true }
    lint { checkReleaseBuilds = false; abortOnError = false }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {}

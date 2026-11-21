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
        versionCode = 4
        versionName = "1.2.0"
    }

    // Release key comes only from GitHub secrets (ZZ_KEYSTORE_B64 / ZZ_KEYSTORE_PASS); it never lives in the repo.
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
            }
        }
    }
    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
            if (!ks.isNullOrBlank()) signingConfig = signingConfigs.getByName("release")
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

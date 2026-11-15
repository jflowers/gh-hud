plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

// A release (make android-release) takes its version from the tag,
// -PghhudVersion=1.2.3, and its signing key from the environment:
// ANDROID_KEYSTORE (an absolute path to the .jks), ANDROID_KEYSTORE_PASSWORD,
// ANDROID_KEY_ALIAS and ANDROID_KEY_PASSWORD. Every release is signed with
// that one key, so a phone updates from one to the next. Debug builds keep
// the machine's debug key and version 0.1 (1).
val releaseVersion: String? = providers.gradleProperty("ghhudVersion").orNull
val releaseKeystore: String? = System.getenv("ANDROID_KEYSTORE")?.takeIf { it.isNotEmpty() }

// 1.2.3 -> 1002003, so each release's versionCode is higher than the last.
fun versionCodeOf(v: String): Int {
    val parts = v.substringBefore('-').split('.').map { it.toIntOrNull() ?: 0 } + listOf(0, 0, 0)
    return maxOf(2, parts[0] * 1_000_000 + parts[1] * 1_000 + parts[2])
}

android {
    namespace = "io.github.jflowers.ghhud"
    compileSdk = 37

    defaultConfig {
        applicationId = "io.github.jflowers.ghhud"
        minSdk = 29
        targetSdk = 37
        versionCode = releaseVersion?.let(::versionCodeOf) ?: 1
        versionName = releaseVersion ?: "0.1"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    // Java 21 bytecode, as :core builds it, so the app can inline the
    // generated message builders (snapshot { … }) from it.
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_21
        targetCompatibility = JavaVersion.VERSION_21
    }

    // The device tests read the clients' shared fixtures (testdata/clients).
    sourceSets["androidTest"].assets.srcDir(rootProject.file("../testdata/clients"))

    buildFeatures {
        compose = true
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            // Unsigned without ANDROID_KEYSTORE; make android-release refuses that.
            signingConfig = if (releaseKeystore != null) signingConfigs.getByName("release") else null
        }
    }
}

dependencies {
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.material3)
    implementation(libs.compose.ui)
    implementation(libs.compose.material.icons.core)
    implementation(libs.activity.compose)
    implementation(libs.lifecycle.viewmodel.compose)
    implementation(libs.lifecycle.runtime.compose)
    implementation(project(":core"))

    testImplementation(libs.junit)

    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation(libs.compose.ui.test.junit4)
    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.espresso.core)
    androidTestImplementation(libs.okhttp.mockwebserver)
    debugImplementation(libs.compose.ui.test.manifest)
}

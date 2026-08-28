# Android build status

This directory contains Novera's experimental Android build. Its build stack is
kept current, but production packaging remains deliberately disabled until the
mobile host is synchronized with Wails v3.0.0-beta.15 and receives a dedicated
security review. The existing `package` and `package:fat` tasks therefore fail
closed; do not replace them with debug-key or unsigned release fallbacks.

## Toolchain baseline

The validated configuration baseline as of 2026-08-28 is:

| Component | Version |
| --- | --- |
| Android Gradle Plugin | 9.3.2 |
| Gradle wrapper | 9.7.1 |
| Java source/target | 17 |
| Android compile SDK | 37 |
| Android target SDK | 36 |
| Android minimum SDK | 24 |
| Android Build Tools | 37.0.0 |
| Android NDK | 29.0.14206865 (r29) |
| AndroidX Activity | 1.13.0 |
| AndroidX AppCompat | 1.8.0 |
| AndroidX Core | 1.19.0 |
| AndroidX WebKit | 1.17.0 |
| Material Components | 1.14.0 |

AGP 9.3 supports API 37 and requires Gradle 9.5 or newer and JDK 17 or
newer. The wrapper uses the current Gradle 9.7.1 distribution and pins its
published SHA-256 digest. Android dependencies use current stable releases
compatible with the API-24 floor.

Primary compatibility references:

- [Android Gradle Plugin 9.3 release notes](https://developer.android.com/build/releases/agp-9-3-0-release-notes)
- [Gradle current release metadata](https://services.gradle.org/versions/current)
- [Android NDK downloads](https://developer.android.com/ndk/downloads)
- [AndroidX WebKit releases](https://developer.android.com/jetpack/androidx/releases/webkit)
- [Google Play target API requirements](https://developer.android.com/google/play/requirements/target-sdk)

## Target-SDK policy

The app compiles against API 37 but intentionally targets API 36. Targeting API
36 satisfies Google Play's 2026 requirement and enables the already-handled
edge-to-edge and predictive-back behavior changes. Targeting API 37 is deferred:
Android 17 requires runtime `ACCESS_LOCAL_NETWORK` consent for local-network
access, and Novera does not yet have the product flow needed to explain and time
that permission request. Do not add a generic startup prompt as a shortcut.

## Wails mobile-host blocker

The checked-in Java host predates the expanded Wails v3.0.0-beta.15 Android
template. It does not yet contain that template's lifecycle, main-thread,
dialog, file-picker, mobile API, and Go overlay integration. Importing the
template wholesale would also import packaging behavior that is incompatible
with Novera's fail-closed signing policy. Treat host synchronization and its
security review as a separate migration before claiming a runnable or
production-ready Android package. That migration should also replace the
process-wide `addJavascriptInterface` bridge with an origin-scoped WebKit
message listener once the Wails host protocol supports it. Until then, the
WebView blocks every non-Wails resource; top-level external links are opened by
the system browser instead.

## Local validation

Install JDK 17 or newer and the exact SDK, Build Tools, and NDK revisions above,
accept the Android SDK licenses locally, then run from this directory:

```powershell
.\gradlew.bat --version
.\gradlew.bat help --warning-mode all
.\gradlew.bat :app:dependencies --configuration debugRuntimeClasspath --warning-mode all
.\gradlew.bat :app:compileDebugJavaWithJavac --warning-mode all
```

The dependency installer at `scripts/deps/install_deps.go` checks the same
versions and rejects missing or mismatched tools. A successful Gradle compile
only validates this bounded host; it does not clear the Wails host/security
blocker above.

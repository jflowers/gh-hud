# gh-hud — Phase 4 plan: the Android app

A native client of the hub (`PLAN-serve.md`) for the phone: what needs
you, approvals, and notifications when you're away from the laptop.
Decided: a native app (Kotlin and Jetpack Compose), not a web app.

## Constraints that shape it

- **The phone isn't on the hub's machine.** The hub listens on a unix
  socket and loopback TCP only (3a). The phone needs a listener on the
  tailnet, with an identity check instead of the per-start token, since
  the phone can't read `hub.json` (4b).
- **No Android Studio.** As with the Mac app, everything builds from the
  command line: `make android` (lint, JVM tests, debug APK) through the
  Gradle wrapper, with the JDK and the SDK command-line tools pinned in
  the repo's `mise.toml` (4a). mise keeps installs per machine, not per
  checkout, so the pins cost about 900 MB once on each machine with a
  gh-hud checkout, plus about 1 GB of Gradle cache.
- **Android stops background network.** A subscription lives only while
  the app is open. Notifications when it's closed need a push channel
  (4d); a foreground service can't hold the stream all day (Android 15
  caps `dataSync` services at 6 hours a day).
- **Device tests need a phone.** CI (Ubuntu) runs lint, the JVM tests
  and the build. Instrumented tests run locally on a phone over adb
  (USB, or `adb connect` over Wi-Fi or the tailnet): `make
  android-device-test`. No self-hosted runner: this repo is public, so
  fork PRs could run code on it.

## Shape

- `android/` in this repo, so the app, the hub and the Mac app change
  together and the Kotlin client is generated from the same `proto/`.
- `:core` (a plain Kotlin/JVM module): the generated messages and
  Connect-Kotlin client, the connection (subscribe, reconnect with
  backoff), and view-model logic, the counterpart of `GHHudKit`. Its
  tests run on the JVM, in CI, with no device.
- `:app` (Android): Compose screens over `:core`, notifications, and the
  hub address and pairing. Compose UI tests run on the phone.
- Codegen: `buf generate` with buf's hosted plugins (protobuf Java and
  Kotlin, lite runtime; connect-kotlin), pinned like the Swift ones,
  output committed under `android/core/src/main/gen`, covered by `make
  proto-check`.
- Versions in `android/gradle/libs.versions.toml`; Gradle 9.9.1 pinned
  by the wrapper with its SHA-256. minSdk 29, target and compile 37.
- Signing: debug builds only until the app is shared. A release key and
  Play distribution are out of scope.

## Sequence

1. **4a, the toolchain.** *(#62)* The mise pins, the Gradle project, the
   Make targets (`android-sdk`, `android`, `android-install`,
   `android-device-test`), CI (`.github/workflows/android.yml`, with the
   debug APK as an artifact), and a placeholder screen with a JVM test
   and a device test.
2. **4b, Go: reachable from the phone.** A tsnet listener in the hub,
   off unless `[hub] tailnet` names a hostname (say `gh-hud`). tsnet
   makes the hub its own tailnet node, so it works whether or not the
   Mac's Tailscale is running; its state lives in the state directory,
   and the first start prints a login URL (or uses `TS_AUTHKEY`).
   Requests must come from the hub owner's own tailnet login
   (`LocalClient.WhoIs`); tagged and shared nodes are refused. HTTPS
   with the tailnet's certificate when the tailnet allows it (Android
   refuses cleartext by default), otherwise HTTP with a cleartext
   exception for that one host. Only the hub that owns dispatch (not a
   follower) joins. Tests: identity check, follower stays off, `gh-hud
   serve` end to end over a test tailnet (`tstest/integration`) if that
   stays cheap, otherwise over a fake `WhoIs`.
3. **4c, what needs you.** The phone's counterpart of the menu bar app:
   the hub's state, the sections (reviews requested, your PRs ready,
   agents blocked on you) newest first, tap to open the PR in the
   browser, pull to refresh. Then approvals (proposals, epic plans,
   gates) with Approve, Reject and Clear, as in 3b.
4. **4d, notifications.** Alerts as Android notifications while the app
   is closed. To decide then:
   - *FCM*: reliable and battery-friendly; needs a Firebase project and
     a service-account key on the hub. The push carries only "something
     changed", and the app reads the details over the tailnet, so no
     PR content goes through Google.
   - *UnifiedPush* (ntfy): no Google account, but another app on the
     phone, and ntfy.sh relays through FCM anyway unless self-hosted.

   Either way the hub keeps the mute it gives native notifications (3a)
   so the laptop and the phone don't both buzz for the same thing.
5. **4e, later.** The agents list and Watch, and the queue, if the phone
   turns out to need them. The full dashboard stays on the Mac.

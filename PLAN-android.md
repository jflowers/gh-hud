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

1. **4a, the toolchain.** *(done, #62)* The mise pins, the Gradle project, the
   Make targets (`android-sdk`, `android`, `android-install`,
   `android-device-test`), CI (`.github/workflows/android.yml`, with the
   debug APK as an artifact), and a placeholder screen with a JVM test
   and a device test.
2. **4b, Go: reachable from the phone.** *(done, #64)* A tsnet listener in the hub,
   off unless `[hub] tailnet` names a hostname (say `gh-hud`). tsnet
   makes the hub its own tailnet node, so it works whether or not the
   Mac's Tailscale is running; its state lives in `tailnet/` under the
   state directory, and the first start logs a login URL (or uses
   `TS_AUTHKEY`). Requests must come from the hub owner's own tailnet
   login (`WhoIs`); tagged devices and devices shared in by other people
   are refused, and a hub whose own node is tagged refuses to serve.
   HTTPS on the tailnet's certificate when the tailnet has HTTPS on
   (Android refuses cleartext by default), otherwise HTTP. Like the TCP
   listener, it runs on whichever hub holds the socket. Tests: the
   identity check against a fake `WhoIs`, and the owner's device served
   and another user's refused on a test tailnet (`testcontrol`), with
   HTTP since a test tailnet can't issue certificates. As built: about
   20 MB more binary (tailscale.com).
3. **4c, what needs you.** *(this PR)* The phone's counterpart of the
   menu bar app: the hub's state, the sections (agents blocked on you,
   proposals, epic plans and gates with Approve, Reject and Clear,
   reviews requested, your PRs ready) newest first, tap to open on
   GitHub, pull to refresh, and the hub's address under **Hub**.

   As built: Kotlin is generated with buf's hosted plugins (protobuf
   Java and Kotlin lite v36.2, connect-kotlin v0.10.0) into
   `android/core/src/main/gen`; the proto gained `java_package` and
   `java_multiple_files` (Go and Swift output unchanged). `:core` holds
   `Attention` (a port of the Mac's) and `HubConnection`; its tests run
   it against MockWebServer speaking the Connect protocol. The screen is
   stateless (`AttentionScreen`), tested on the phone. The subscription
   runs only while the app is on screen. `make android-dev-hub` points
   the phone at the Mac's loopback hub over USB (`adb reverse`), so
   development needs no tailnet.

   Found on the way: the hub refused calls at the HTTP level, which
   connect-kotlin can't read on a stream ("unexpected content-type").
   The token and tailnet checks are now a Connect interceptor, so a
   refusal is a Connect error on every kind of call.
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

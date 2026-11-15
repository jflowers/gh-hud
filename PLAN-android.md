# gh-hud — Phase 4 plan: the Android app

A native client of the hub (`PLAN-serve.md`) for the phone: what needs
you, approvals, and notifications when you're away from the laptop, and
then (4e) about everything the Mac app shows.
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
  the app is open, unless a foreground service holds it (4d). Android 15
  caps `dataSync` services at 6 hours a day, but not `specialUse` ones.
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
4. **4d, notifications.** Done: alerts as Android notifications while the
   app is closed. Weighed against FCM (a Firebase project and a
   service-account key on the hub), UnifiedPush/ntfy (another app on the
   phone) and a WorkManager poll (15 minutes or more late), we chose to
   keep the subscription open ourselves: no third party, notifications
   within seconds.

   As built: "Notify me" (under **Hub**) starts `HubService`, a
   `specialUse` foreground service, again at boot. The screen and the
   service share one connection (`Hub`), up while either holds it.
   Android shows the service as a silent, minimum-importance
   notification that says whether the hub is there ("3 need you", "Lost
   the hub: …"). It posts the hub's alerts as the Mac app does (tap to
   open the PR), and approvals that start waiting on you (`ApprovalWatch`
   in `:core`: proposals, epic plans, gates; tap to open the app); an
   approval's notification goes when it stops waiting. Over HTTPS, HTTP/2
   pings every minute find a connection that died quietly. Alerts sent
   while the phone was off the tailnet are missed; the screen still
   shows what waits.

   The phone doesn't ask the hub to mute its own (osascript)
   notifications: it's always subscribed, so that would silence the Mac
   whenever its app isn't running. You get alerts on both, as with a chat
   app.
5. **4e, the rest of the Mac app.** What needs you isn't enough on its
   own: the phone should show about everything the Mac app does. Below.

## 4e: about everything the Mac app shows

The Mac app has the menu (what needs you, which the phone has) and the
dashboard: the item filters (the TUI's tabs) as a table, and the Plan,
Agents, Workspaces and Fleet panes, with commands on each. The phone
gets the same information and the same commands, laid out for a phone.
Two things it can't copy as they are: tables with ten columns, and
commands that open something on the Mac (Watch, Open Transcript, Show in
Finder).

### Layout

A bottom bar with five places, each a list; tapping a row opens its
detail, where its commands are:

- **Inbox**: what needs you, as now. The app opens here.
- **Items**: the dashboard's filters (Review requested, Mine, Issues,
  CI, Notifications, Ready, …) as chips across the top, with counts.
  Each row is two lines: the title, then the TUI's cluster in small
  type (kind, marker ◆, CI/review/merge glyphs, threads, last actor,
  age, the run tag "fix ● 5m" and what's next "▸review"). The search
  field takes the Mac's column terms (`repo:acme -author:dependabot`)
  and keeps them as chips; a sort menu replaces clickable headers.
- **Plan**: open cases (with their proposals), enrolled epics (plan,
  pieces, gates), then what starts next, running, waiting, stopped and
  merging. Your queue reorders by drag or Move up/down.
- **Agents**: sessions and runs (Live Only and Show stale, as on the
  Mac), and a Workspaces tab within it for the checkouts a dispatch
  could use.
- **Fleet**: the rollout target and its CI, a row per machine, and the
  four controls.

Details are full screens, not side panes:

- **Item**: everything the row has, untruncated, plus its runs.
  Commands: Open on GitHub, Start agent… and Add to queue… (the hub's
  task choices, in a bottom sheet), Re-check run, Copy link.
- **Session or run**: the Mac's labelled facts (outcome, took,
  directory, ref). Commands: Re-check, Open on GitHub, Copy session ID.
- **Epic**: the plan's pieces and gates. Approve plan, Clear gate,
  Reread, Drop (confirmed).
- **Case**: the check, the PRs, the proposal. Approve or Reject.

Long-press on a row offers the same commands and the column filters
("Show only repo acme/r", "Hide author dependabot") the Mac's context
menu does. A command's result shows in a snackbar; a failure stays until
dismissed, as the Mac's notice bar. Commands are off, with the reason,
when the hub has no dispatcher or another gh-hud owns dispatch.

### The logic goes to `:core`, tested against the Mac's

Almost everything the Mac shows is worked out by pure functions in
`GHHudKit`: `ItemList`, `Filters`, `Sorting`, `Agents`, `Plan`,
`Fleet` (about 1,300 lines), checked by `GHHudChecks` (189 checks).
They port to Kotlin in `:core` the way `Attention` did, with JVM tests.

So the two can't drift, the checks move to shared fixtures:
`testdata/clients/` holds snapshots (as protobuf JSON) and what each
view should make of them (rows, counts, sort orders, filter results).
`GHHudChecks` and the `:core` tests both read them; a change to what a
view shows changes the fixture once, and both clients follow. Checks
that are specific to one client stay in it.

### Watching an agent from the phone

On the Mac, Watch opens the agent's herdr pane and Open Transcript its
opencode session, both on the hub's machine. The phone needs the
content instead: a hub call that returns a session's recent output
(herdr `pane read`) and streams what follows, and one that returns a
run's transcript as text. To decide when we get there: which of those
the dispatcher can already give (its runners are the merge train's,
`internal/dispatch`), and whether answering a blocked agent from the
phone (sending it a line) is wanted, which would make the phone a
remote control rather than a viewer.

### Sequence

1. **4e1, `:core` ports and shared fixtures.** *(done)* `ItemList`, `Filters`,
   `Sorting`, `Agents`, `Plan`, `Fleet` in Kotlin; the fixtures and
   `GHHudChecks` reading them. No new screens. The biggest step, and it
   makes the rest mostly UI.

   As built: the fixtures are snapshots written as protobuf JSON
   (`testdata/clients/*.json`); a Go test keeps a binary copy of each,
   since the Android app's lite protobuf runtime can't read JSON. Rather
   than move the Mac's checks into data, each client renders everything
   its views make of a snapshot (rows, counts, searches, sort orders,
   plan, epics, fleet, what needs you) as `key = value` lines, and both
   must match `<name>.views.txt` exactly. `make client-fixtures` rewrites
   the files from the Android code and runs the Mac checks against them;
   the first run found one difference (the "unknown" sort key's size),
   fixed. The Mac's own checks stay as they were.
2. **4e2, the shell and Items.** *(done)* The bottom bar, the Items list with
   filters, search, sort and column chips, the item detail, and its
   commands (start an agent, queue, re-check).

   As built: the bottom bar has Inbox and Items; each later step adds
   its place. Sort is a menu (Hub's order, Needs you, Status, Number,
   Threads, Newest, and Reverse), the Mac's column sorts as `ItemSort` in
   `:core`. Every change to the list's view goes through one function
   that applies it to the view as it is then: the search field reports
   its text late, and with a captured copy it undid the chips its own
   enter had just made (the device tests caught it, intermittently).
   Watch and Open Transcript wait for 4e6.
3. **4e3, Plan.** Cases, epics and the queue, with every command the
   Mac's Plan pane has, Enroll epic included.
4. **4e4, Agents and Workspaces**, without Watch.
5. **4e5, Fleet**, with pause/resume, check now, skip wait (confirmed)
   and retry.
6. **4e6, watching from the phone.** The hub calls above, then a
   session screen that follows its output. Planned when 4e5 is done.

Each step is a PR with its JVM tests and device tests, tried on the
phone (wireless debugging) before review.


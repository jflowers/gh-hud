# gh-hud — Phase 3 plan: the Mac app

A native client of the hub (`PLAN-serve.md`): what needs you at a glance
in the menu bar, native notifications, and later a full window.

## Constraints that shape it

- **No Xcode on the build machine**, only the Command Line Tools (Swift
  6.4, the macOS SDK with SwiftUI). So the app is a Swift package built
  with `swift build`; `make mac` wraps the binary in an `.app` bundle
  (Info.plist, icon) and signs it. No `.xcodeproj` to maintain.
- **URLSession can't open a unix socket.** The hub therefore also listens
  on loopback TCP behind a per-start token and writes the URL and token
  to `hub.json` in the state directory (3a). The phone will need TCP
  anyway (Phase 4, over tailscale).
- **Notifications twice.** A client that posts notifications itself
  subscribes with `native_notifications`, and the hub mutes its
  osascript notifier while that subscription lasts (3a).

## Shape

- `macos/` in this repo, so the app and the hub change together and the
  Swift client is generated from the same `proto/`.
- `GHHudKit` (library): the generated Connect-Swift client and messages,
  hub discovery (`~/.local/state/gh-hud/hub.json`), a connection that
  subscribes, reconnects with backoff, and re-reads `hub.json` when the
  hub restarts (its token changes), and view-model logic that is
  testable without UI.
- `GHHud` (executable): a SwiftUI `MenuBarExtra` app (`LSUIElement`, no
  Dock icon) over `GHHudKit`.
- Codegen: `buf generate` with the Swift plugins (swift-protobuf and
  connect-swift), output committed under `macos/Sources/GHHudKit/Gen`,
  and covered by `make proto-check`.
- Signing: `make mac` signs with your Developer ID when `MAC_SIGN_ID` is
  set, ad hoc otherwise. Notarizing waits until the app is shared.

## Sequence

1. **3a, Go: reachable from the Mac.** *(done, #29)* `[hub] listen` (loopback only,
   default `127.0.0.1:0`), the bearer token, `hub.json`, and
   `native_notifications` muting. Tests: token required, hub.json 0600
   and removed on exit (only by the hub that wrote it), loopback-only
   listen, muting held for exactly the subscription, and `gh-hud serve`
   end to end over TCP.
2. **3b, the menu bar app.** *(done)*
   - The icon shows how many things need you: reviews requested, your
     PRs ready for a maintainer, and agents blocked on you.
   - The menu lists them by section, newest activity first; a click
     opens the PR in the browser. It also shows the hub's state
     (connected, refreshing, follower, lost) and has Refresh.
   - Alerts become native notifications (`UNUserNotificationCenter`);
     clicking one opens its PR or run. The app only asks the hub to mute
     its own notifications once you've allowed the app's.

   As built: SwiftUI's `@State` and the XCTest / Swift Testing macros
   need Xcode, so the app keeps view state in `ObservableObject`s and its
   checks are an executable (`make mac-check`). Swift code is generated
   with buf's hosted plugins (swift-protobuf 1.39 generator, 1.40
   library; connect-swift 1.2.3). The subscription uses its own
   URLSession with a long timeout, because an idle hub sends nothing.
   `--snapshot` renders the menu with ImageRenderer, which can't draw
   scroll views or system buttons, so it gets a plain layout.
3. **3c, the window.** The dashboard's tabs as a native window: the list
   with filters and search, agents, workspaces, the plan and your queue,
   and the fleet, with the same commands as the TUI (dispatch, queue,
   watch, re-check, fleet controls).

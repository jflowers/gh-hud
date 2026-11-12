# gh-hud — Phase 1 plan: extract the hub from the TUI

Status: proposed (not yet approved).

## Why

Today the TUI process *is* the hub. `ui.Model` owns the fetch loop, the
agents/workspaces polls, the auto pass (auto-merge + auto-dispatch), the
notification dedupe, and the fleet snapshot, all driven by Bubble Tea's 1 s
`tickMsg` (`internal/ui/app.go`, `Update` → `case tickMsg`). Closing the TUI
stops all of it, and no other client (a Mac app, an Android app over the
tailnet) can share it.

Phase 1 moves every non-presentation responsibility into a new
`internal/hub` package behind an interface, with **no user-visible behavior
change**. The TUI becomes the first client of that interface, in-process.
Phase 2 (API + `gh-hud serve`) then only has to put that interface on the
wire.

### Non-goals (Phase 1)

- No network API, no daemon, no new binary modes.
- No change to owner/follower semantics: each gh-hud process still builds
  its own hub, and the existing `dispatch.lock` decides who owns dispatch.
- `--once` and `--snapshot` keep calling `model.FetchAll` directly.
- No wire types yet: the interface uses `model.Item`, `dispatch.Run`, etc.
  as they are.

## What leaves `internal/ui`

Everything below is in `internal/ui` today but isn't presentation.

| Responsibility | Today | Moves to |
|---|---|---|
| Refresh cadence (`RefreshSecs`), in-flight guard | `app.go` `tickMsg`, `fetchCmd`, `fetching`, `lastFetch` | `hub` loop |
| Fetch error state | `app.go` `fetchedMsg` → `fetchErr` | `Snapshot.FetchErr` |
| Agents poll every 30 s (sessions, runs, usage, follower) | `app.go` `fetchAgents`, `agentsMsg` | `hub` loop |
| Workspaces poll (`wsRefresh`) + loading guard | `workspaces.go` `fetchWorkspaces` | `hub` loop |
| Auto pass after each fetch | `app.go` `autoCmd` (`AutoMerge` + `AutoDispatch`) | `hub` loop |
| Ready / blocked / finished notifications + seen-sets | `app.go` `notifyReady`, `notifyBlocked`; `runs.go` `notifyFinished`; `readySeen`, `blockedSeen`, `finishedSeen` | `hub/alerts.go` |
| `osascript` notification sink | `app.go` `desktopNotify` | `internal/notify` |
| Fleet snapshot refresh | `app.go` `refreshFleetSnap` on every tick | `hub` loop |
| Fleet `Run` start (owner only) | `cmd/gh-hud/main.go` | `hub.Run` |
| Pipeline stage per row | `view.go` `rc.stage` → `m.disp.Stage(it, runs)` on every render | computed once per snapshot |
| Dispatch / menu / fleet controls / reinspect | direct `m.disp.*` and `m.fleet.*` calls | `hub.Backend` methods |

## What stays in `internal/ui`

Cursor, offset, hover, filter, search, help, and the tab and modal modes;
`visible()` / `visibleAgents()` filtering; restoring the cursor by URL;
every renderer, glyph and color; `gestureOf` and mouse hit-testing; the
`s s` double-press confirmation for skipping the drain wait; flash display.

Clipboard and `open` (`internal/clipboard`) also stay client-side: they act
on the machine the *viewer* sits at, which in Phase 3+ is not the hub's.

Host-local actions are the awkward middle: `Watch`, `WatchRun` and
`OpenTranscript` open a terminal on the machine the agent runs on. In Phase
1 they go through `Backend` like everything else. Phase 2 marks them
host-only, so a remote client can show them but only a client on the hub
host can run them.

## The `hub` package

```go
package hub

// Backend is everything a client needs. *Hub implements it in-process;
// Phase 2 adds a remote implementation with the same contract, which is
// why every method takes a ctx and returns an error even where the
// in-process version can't fail.
type Backend interface {
    // State
    Snapshot() Snapshot
    Subscribe(ctx context.Context) (<-chan Snapshot, <-chan Event)

    // Triggers (non-blocking; the result arrives as a snapshot/event)
    Refresh(ctx context.Context) error
    RefreshAgents(ctx context.Context) error
    RefreshWorkspaces(ctx context.Context) error

    // Dispatch
    Actions(ctx context.Context, url string) ([]dispatch.MenuChoice, error)
    Dispatch(ctx context.Context, task dispatch.Task, url string) (dispatch.Record, error)
    Reinspect(ctx context.Context, runID string) (dispatch.Run, error)

    // Fleet
    FleetSetPaused(ctx context.Context, paused bool) error
    FleetApplyNow(ctx context.Context) error
    FleetSkipWait(ctx context.Context) error
    FleetRetry(ctx context.Context) error

    // Host-local (open a terminal on the agent's machine)
    WatchSession(ctx context.Context, sessionID string) error
    WatchRun(ctx context.Context, runID string) error
    OpenTranscript(ctx context.Context, runID string) error
}
```

Commands address items by **URL** and runs/sessions by **ID**, never by
passing a `model.Item` back in. The hub looks the item up in its current
snapshot. That keeps the contract serializable and stops clients from
dispatching on stale item data.

```go
type Snapshot struct {
    Seq       uint64    // monotonic; clients drop out-of-order snapshots
    User      string
    Items     []model.Item              // already ranked (model.SortItems)
    Stages    map[string]dispatch.Stage // by Item.URL; empty when dispatch is off
    FetchedAt time.Time
    Fetching  bool
    FetchErr  string   // fatal (no user); the TUI shows its error screen
    Warnings  []string // per-section errors

    Dispatch  bool // dispatcher configured
    Follower  string // owner pid when this hub only follows ("?" if unknown)
    Sessions  []dispatch.Session
    Runs      []dispatch.Run // newest first
    Usage     []dispatch.MachineUsage
    AgentsAt  time.Time

    Workspaces        []dispatch.CheckoutHealth
    WorkspacesAt      time.Time
    WorkspacesLoading bool

    Fleet *fleet.Snapshot // nil when [fleet] is off
}

type EventKind int
const (
    EventNotice EventKind = iota // a status-line flash: auto-pass results, dispatch done/failed
    EventAlert                   // something that warrants a desktop notification
)

type Event struct {
    Kind  EventKind
    Text  string
    Alert *Alert // set for EventAlert
}

// Alert is structured so each client can render it natively later
// (UserNotifications on macOS, a push on Android). The osascript sink
// formats it with the same text the TUI produces today.
type Alert struct {
    Type    AlertType // Ready, Blocked, RunFinished
    Ref     string
    Run     *dispatch.Run     // RunFinished
    Session *dispatch.Session // Blocked
}
```

### Loop

`hub.Run(ctx)` replaces the work done on the TUI tick. One goroutine per
cadence, each with its own in-flight guard, and the same timeouts as today:

- **fetch**, every `RefreshSecs` or when triggered. On success it runs the
  alert pass, precomputes `Stages`, publishes, then starts the auto pass
  (90 s timeout), followed by an agents refresh.
- **agents**, every 30 s or when triggered: a 30 s timeout for the session
  scan and a separate 20 s timeout for runs and usage. Blocked and finished
  alerts fire only when `Follower == ""`, as today.
- **workspaces**, every `wsRefresh` (moves from `ui`) or when triggered,
  with a 60 s timeout.
- **fleet**: the hub starts `fleet.Coordinator.Run` when it owns dispatch,
  and copies `Snapshot()` every second while anyone is subscribed.

Publishing replaces `h.snap` under a mutex, bumps `Seq`, and sends to each
subscriber's 1-buffered snapshot channel, **latest wins** (a slow client
drops intermediate snapshots, never the newest). Events use a separate
small buffered channel. Notices may be dropped when it's full; alerts go
to the `Notifier` synchronously in the hub, not through the channel, so a
slow client can't swallow one.

`Stages` must be computed after both the fetch and the agents result have
landed (`Stage` needs the runs). Recompute it on every publish. It does no
I/O.

## Bubble Tea wiring

- `ui.New(cfg, backend hub.Backend)`. The `disp`, `idx`, `fleet`, `client`,
  `res`, `agents`, `runs`, `machineUsage`, `ws*`, `fleetSnap`, `fetching`,
  `lastFetch` and `*Seen` fields go away, replaced by one `snap hub.Snapshot`.
- `Init` subscribes and returns `waitSnapshot(ch)` and `waitEvent(ch)` cmds.
  Each re-arms itself after delivering (the standard Bubble Tea channel
  pattern).
- `snapshotMsg` stores the snapshot, restores the cursor by URL, clamps the
  selections and refreshes `runView`. `eventMsg` flashes.
- The 1 s `tickMsg` stays only to re-render ages and expire flashes.
- `r` / `6` / `9` call `backend.Refresh*`. `a` calls `backend.Actions`
  (fast, cached view). Enter in the menu calls `backend.Dispatch` inside a
  `tea.Cmd`, as today.

## Sequence (each PR builds, vets and passes `go test ./...` on its own)

1. **`internal/notify` + `hub/alerts.go`.** *(done)* `desktopNotify`
   becomes `notify.Notifier` (`Notify(title, body)`, the same shape as
   `fleet.Notifier`), implemented by `notify.OSAScript` and, for tests,
   `notify.Func`. `notifyReady`, `notifyBlocked` and `notifyFinished` become
   `hub.Alerter`'s `Items`, `Sessions` and `Runs`. It's stateful: it keeps
   the seen sets the UI used to hold, and takes `now` as an argument so it
   can be tested. The run status glyph, words and location live on
   `dispatch.RunStatus` / `dispatch.Run`, so the alert text and the TUI
   share them. The UI calls the alerter, so behavior is unchanged. The one
   difference is that ready alerts now fire in rank order rather than
   map order.
2. **`hub.Hub` owns the loops and the snapshot.** Add `New`, `Run`,
   `Snapshot`, `Subscribe` and the `Refresh*` triggers. `main.go` builds the
   hub and stops starting the fleet itself. The UI switches from
   `fetchCmd`, `fetchAgents`, `fetchWorkspaces`, `autoCmd` and its tick
   logic to the subscription. Stages are precomputed. This PR carries the
   real risk: test it by running owner and follower side by side.
3. **Commands through `Backend`.** Dispatch, actions, reinspect, the fleet
   controls and the host-local actions. Remove the `*dispatch.Dispatcher`
   and `*fleet.Coordinator` fields from `ui.Model`. Add
   `hub/hubtest.Fake` and move the UI tests that build `&Model{...}` with
   raw fields onto it.
4. **Guard the boundary.** Add a test that fails if `internal/ui` imports
   `os/exec`, or references `dispatch.Dispatcher` or `fleet.Coordinator`.
   Update the Architecture section of `PLAN.md` and `AGENTS.md`.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`.
- `hub` unit tests with a fake fetcher, dispatcher and clock covering:
  refresh cadence, the in-flight guard, the auto pass running once per
  fetch, latest-wins delivery to a slow subscriber, alerts firing once and
  only when we own dispatch, and `Stages` recomputed after agents land.
- Manual: two gh-hud instances on one state dir, where the second shows
  `follower` and never notifies or dispatches. Exit the first, and the
  second takes over (dispatch and the fleet start). Run the iTerm2 click
  checklist from `PLAN.md`. Run `--once` and `--snapshot 160x40` unchanged.

## Decisions for Jay

1. **Interface types.** Phase 1 reuses `model.Item` / `dispatch.*` in
   `Backend`. Recommended: yes. Phase 2 introduces proto types and a
   mapping layer, and that's where presentation leaks get fixed (for
   example `Item.Review`, `Bot` and `Merge` are emoji strings and should
   become enums).
2. **Follower takeover.** Today a follower's dispatcher takes over when the
   owner exits. Keep that in Phase 1 (recommended), and replace it in
   Phase 2 with "followers attach to the owner's socket".
3. **Who posts notifications once the Mac app exists.** Proposal: the hub
   always emits `Alert`s, and the osascript sink is turned off when a
   native client is subscribed. That's Phase 3, so nothing needs deciding
   now beyond keeping `Alert` structured.

## How Phase 2 plugs in

`gh-hud serve` runs a `hub.Hub` with no TUI and serves `Backend` over
Connect-RPC (unix socket, and later `tsnet`). `Subscribe` becomes a
server stream. `gh-hud` with no flags tries the socket first and falls back
to an in-process hub, so `ui` doesn't change.

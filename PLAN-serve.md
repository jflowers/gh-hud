# gh-hud — Phase 2 plan: the hub on the wire

Phase 1 (`PLAN-hub.md`) made the TUI a client of `hub.Backend`. Phase 2
puts that interface on a socket, so the hub can outlive the TUI and the
Mac and Android apps (Phases 3 and 4) can share it.

## Shape

- **Schema:** `proto/ghhud/v1/hub.proto`, one `HubService`: `GetSnapshot`,
  a server-streaming `Subscribe` (snapshots and events), and one RPC per
  `Backend` command. Generated Go lives in `internal/api/gen` and is
  committed; `make proto` regenerates it with pinned buf and plugin
  versions, and CI fails when it is stale. Swift and Kotlin clients
  generate from the same file.
- **Transport:** Connect-RPC over HTTP/2 cleartext on a unix socket,
  `hub.sock` in the state directory (0700 dir, 0600 socket, so only you
  can reach it). Phase 4 adds a tsnet listener for the phone.
- **`internal/api`:** converters between the hub's Go types and the wire
  types; `Server` (a `HubServiceHandler` over any `hub.Backend`); and
  `Client`, which *is* a `hub.Backend`, so `ui.New` takes it unchanged.

## Wire decisions

- Clients name things by stable keys, as `Backend` does: items by URL,
  runs and sessions by ID, queue entries by task + ref.
- Open sets are strings (tasks, run statuses, stage kinds, fleet states);
  closed sets are enums with an `UNSPECIFIED` zero. Go enums whose zero
  means something go on the wire as Go value + 1.
- `Item.review`, `bot_review` and `merge` stay the TUI's glyph strings for
  now. They become enums when the Mac app needs to draw them natively,
  which is when we'll know the right cases.
- `Subscribe` skips a snapshot that differs from the last one sent only
  in `seq`, so an idle hub sends nothing although it publishes every
  second.
- The client keeps its own snapshot sequence: it follows the hub's, and
  keeps increasing across a hub restart (which starts again at 1), so the
  TUI's "drop older snapshots" rule never freezes it. A dropped
  subscription reconnects with backoff and says so in a notice.
- Errors: `hub.ErrNoDispatch` / `ErrNoFleet` travel as
  `FAILED_PRECONDITION` and come back as the same sentinels; anything
  else comes back as the hub's own message, ready to show.
- `Dispatch` returns where the run started (task, ref, workspace,
  machine, runner, dir), not the whole internal record.

## Keeping the wire complete

`api.TestRoundTrip` fills every exported field of `hub.Snapshot` (and so
`model.Item`, `dispatch.Run`, `dispatch.Session`, …), `hub.Event` and
`dispatch.MenuChoice` by reflection, sends them through proto bytes and
back, and names any field that didn't survive. **A field added to those
types needs a proto field and a line in `internal/api/convert.go` in the
same PR** (`make proto` regenerates). That is deliberate: otherwise a
remote TUI would silently lose it.

## Sequence

1. **API library (2a).** *(done, #26)* Schema, generated code, converters, server,
   client, unix-socket transport, and tests over a real socket: every
   command, error mapping, snapshot dedupe, events, a hub restart, a
   stale socket, and a real hub end to end. No behavior change: nothing
   serves yet.
2. **`gh-hud serve` and attach (2b).** *(done)*
   - `gh-hud serve` runs a hub with no TUI and serves the socket.
   - `gh-hud` first tries the socket: if a hub answers, the TUI attaches
     to it. If not, it builds its hub in-process as today *and* serves
     the socket, so a second `gh-hud` (or the Mac app) attaches to the
     first instead of becoming a follower.
   - Follower mode stays as the fallback for an owner without a socket
     (an older gh-hud, or `--no-attach`), and goes away a release later.
   - `--once` and `--snapshot` no longer build a dispatcher, so they never
     take the dispatch lock.
   - A launchd agent (`gh-hud serve`, `KeepAlive`) is documented, not
     installed, so the hub can run without a terminal.
   - Decided: when the TUI that hosts the hub quits while another client
     is attached, the hub goes with it and nothing takes over on its own;
     the other client shows "lost the hub" and reconnects to whatever hub
     starts next. `gh-hud serve` under launchd keeps one up.
3. **Later in Phase 2:** `buf breaking` against master in CI once a
   client outside this repo depends on the schema.

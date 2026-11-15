# The clients' shared fixtures

The Mac app (`macos/Sources/GHHudKit`) and the Android app
(`android/core`) work out what they show from a hub snapshot with the
same logic, written twice: the item list and its filters, search and sort
orders, agents, workspaces, the fleet, the plan, and what needs you.
These fixtures keep the two in step.

Each fixture is a snapshot and what the views make of it:

- `<name>.json`: the snapshot, as protobuf JSON (`ghhud.v1.Snapshot`).
  Edit this one. The views are rendered at the snapshot's `fetchedAt`.
- `<name>.binpb`: the same snapshot in binary, which both clients read
  (the Android app's protobuf runtime can't read JSON). Generated.
- `<name>.views.txt`: every view of the snapshot, one `key = value` line
  each. Generated, and reviewed like code: a change here is a change to
  what both apps show.

Both clients render the views text from the binary snapshot:
`Views.render` in `android/core/src/test/.../Views.kt` and in
`macos/Sources/GHHudChecks/Views.swift`. Each must produce the file
exactly. The Android tests (`make android`) and the Mac checks (`make
mac-check`) fail when they don't; `go test ./internal/api` fails when a
`.binpb` is older than its JSON.

## Changing what a view shows

1. Change the logic in both clients, and the views text in both
   `Views` if it shows something new.
2. `make client-fixtures`: rewrites the binary snapshots, renders the
   views with the Android code, and runs the Mac checks against them.
3. If the Mac checks fail, the two clients disagree: the message names
   the first line that differs.

## Adding a fixture

Add `<name>.json` and run `make client-fixtures`. The views text format:
strings in double quotes (with `\`, `"` and newlines escaped), `nil` for
absent, lists as `["a", "b"]`, times as Unix seconds, durations as the
apps show them ("1h05"). Sort orders (`items.sort.ageKey`) are ascending,
with ties in list order.

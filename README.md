# gh-hud

A terminal HUD for GitHub things that need your attention: PRs awaiting your
review, your own PRs, issues, failed Actions runs, and unread notifications —
merged into one attention-ranked list. Built for iTerm2.

## Install

Requirements: macOS (clipboard and browser go through `pbcopy` / `open`),
Go 1.26+, and the [GitHub CLI](https://cli.github.com) logged in with
`gh auth login`. gh-hud talks to GitHub only through `gh`, reusing its auth;
it stores no tokens.

```
go install github.com/jflowers/gh-hud/cmd/gh-hud@latest
gh-hud            # interactive dashboard
gh-hud --once     # one-shot table with hyperlinks, then exit
gh-hud serve      # the hub alone, no dashboard (see "The hub")
```

From a checkout: `go install ./cmd/gh-hud` (or `go build -o gh-hud ./cmd/gh-hud`).

## Interactions (iTerm2)

| Action | Result |
|---|---|
| click row (or ⌘-click) | open in browser |
| ⌘-right-click | copy the URL to the clipboard |
| ⇧-click | copy `owner/repo#N` to the clipboard |

iTerm2 only forwards some modified clicks to apps. ⌥-click is reserved for
text selection, and ⌃-click / plain right-click open context menus, so those
never reach gh-hud. iTerm2 reports ⌘ as the meta bit, which is how
⌘-right-click works. Rebind with `url_click` / `ref_click` (choices:
`cmd+right`, `shift+click`, `shift+right`, `ctrl+right`, `cmd+click`). Run with
`GH_HUD_MOUSELOG=/tmp/mouse.log gh-hud` to log what your terminal actually
sends (clicks do nothing in that mode).

Keys: `j/k` move · `enter/o` open · `y` copy `owner/repo#N` · `Y` copy URL ·
`r` refresh · `a` agent actions · `+` queue an agent task · `p` Plan ·
`i` row's last run · `6` Agents tab · `9` Workspaces tab · `F` fleet rollout · `1-5`/`7-8` filter section ·
`0` all · `/` search · `?` help · `q` quit.

## Sections (header tabs, keys 0-9)

Tabs `6` (Agents), `9` (Workspaces) and `p` (Plan) are views of their own,
described under [Agent dispatch](#agent-dispatch) and
[Plan and your queue](#plan-and-your-queue); the rest filter the dashboard.

- **Review** — open PRs in your orgs where your review is requested or you're assigned
- **Mine** — your open PRs in your orgs (set `my_prs_everywhere = true` for all of GitHub)
- **Issues** — open issues assigned to you or mentioning you
- **CI** — failing checks on open PRs, plus failed runs on `watch_repos`
- **Inbox** — unread notifications (mentions, comments, review requests, CI activity)
- **Ready** (7) — your PRs that are done on your side and wait only on a human
  maintainer: not draft, checks green or absent, no conflicts, no unresolved
  threads, not yet approved, you acted last. Marked `◆` green; a macOS desktop
  notification fires when a PR newly enters this state (`notify = true`).
- **Queue** (8) — PRs ready for an agent review: updated within
  `queue_window_hours`, checks settled, no conflicts, and not a chore (a title
  starting `chore:` or `chore(scope):`). That's other humans' PRs
  where the author acted last, plus your own `unbound-force` PRs until
  `uf-divisor` has reviewed the head commit
  (requires `orgs`; the sweep is disabled without them). With
  `[dispatch.review] auto = true` these drain automatically as agent slots
  free up (see [Configuring dispatch](#configuring-dispatch)).

Notifications merge into matching PR/issue rows as a colored reason tag
(`mention`, `review`, ...) instead of duplicating them. Rows are ranked by an
urgency score (failing CI, changes requested, waiting on you, human review
threads, mentions, freshness). Assigned issues with no activity for a day and
nothing pinging you are badged `later` and sink below the rest.

## Columns

`kind` badge · `◆` waiting on you · `state` = CI / review / merge glyphs ·
`#N` · `repo` (long owners abbreviated, e.g. `uf/`) · `title` + reason
tags · `✎` unresolved threads (yellow = human, dim = bot-only) · `who` last
actor (`you` or a name) · `age`.

Glyphs: `✓` passing/approved · `✗` failing/changes requested · `●` running/mixed
· `!` you requested changes · `○` no checks · `⊘` merge conflict. Press `?` in
the app for the full legend.

## The hub

Refreshing GitHub, dispatching agents, auto-merge, alerts and fleet
rollouts all run in gh-hud's *hub*. The dashboard is a client of it, and
there is one hub per state directory (`~/.local/state/gh-hud`, or
`--state-dir`), reachable on the unix socket `hub.sock` there (only you
can open it).

- `gh-hud` attaches to the hub on the socket when one answers. Otherwise
  it runs the hub itself and serves the socket, so the next `gh-hud`
  attaches to it: open as many dashboards as you like, and they share one
  refresh and one dispatcher.
- Quitting the `gh-hud` that runs the hub stops it. Attached dashboards
  say `lost the hub; reconnecting…` and pick up the next hub that starts
  on the socket; nothing takes over on its own.
- `gh-hud serve` runs the hub with no dashboard and logs its notices and
  alerts to stderr. It refuses to start while another hub serves the
  socket. Run it under launchd to keep a hub up without a terminal.
- Apps that can't open a unix socket (the Mac app) reach the hub on
  loopback TCP instead: the hub picks a free port, makes a new random
  token each time it starts, and writes both to `hub.json` in the state
  directory (readable only by you). Every request must carry the token;
  the hub refuses non-loopback addresses. `[hub] listen` sets the address
  (`"127.0.0.1:0"` by default); `listen = ""` turns TCP off.
- A client that shows alerts as native notifications says so when it
  subscribes, and the hub stops posting its own osascript notifications
  while it's connected, so you aren't notified twice.
- `gh-hud --no-attach` runs a hub in-process even when one is serving
  (it follows the serving hub's dispatch, read-only); for debugging.

### Running the hub under launchd

Save as `~/Library/LaunchAgents/gh-hud.hub.plist`, with your username in
the paths. The hub runs `gh`, `git`, `ssh`, `herdr` and `opencode`, so
`PATH` has to find them: launchd's default doesn't include Homebrew or
`~/go/bin`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>gh-hud.hub</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/go/bin/gh-hud</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/Users/YOU/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/gh-hud.log</string>
</dict>
</plist>
```

```
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/gh-hud.hub.plist
launchctl kickstart -k gui/$(id -u)/gh-hud.hub   # restart, e.g. after go install
launchctl bootout gui/$(id -u)/gh-hud.hub        # stop and unload
tail -f ~/Library/Logs/gh-hud.log
```

With `KeepAlive`, launchd restarts the hub if it exits. While a dashboard
runs its own hub, `gh-hud serve` exits with "already serving" and launchd
retries every 10 seconds, taking over once that dashboard quits. Start
your dashboards after the agent to attach to it from the start.

The hub reads its config when it starts: restart it after editing
`config.toml`.

## Mac app

A menu bar app that shows what needs you, from the hub: reviews requested
of you, your PRs ready for a maintainer, and agents waiting on you. The
icon shows the count; the menu lists them (click one to open it on
GitHub) with the hub's state and a Refresh button. Its source is in
`macos/` and it needs a hub running (see "The hub") with TCP on, which is
the default.

```
make mac                                   # build/gh-hud.app, signed ad hoc
make mac MAC_SIGN_ID="Developer ID Application: Your Name (TEAMID)"
cp -R build/gh-hud.app /Applications/ && open /Applications/gh-hud.app
```

A Developer ID build only opens on other Macs once Apple has notarized it.
`make mac-notarize` builds, notarizes and staples in one step. It needs a
notarytool keychain profile, stored once from an App Store Connect API key:

```
xcrun notarytool store-credentials gh-hud-notary --key AuthKey_XXXX.p8 --key-id <KEY_ID> --issuer <ISSUER_ID>
make mac-notarize MAC_SIGN_ID="Developer ID Application: Your Name (TEAMID)"
```

`MAC_NOTARY_PROFILE` names another profile. A build signed with a
Developer ID now carries a secure timestamp, which notarization requires.

It builds with the Command Line Tools alone (no Xcode). Add it to
System Settings → General → Login Items to start it with your Mac.

- **Notifications.** On first launch it asks to show notifications. Once
  you allow them it shows the hub's alerts itself (clicking one opens
  the PR) and the hub stops posting its own while the app is connected.
  If you don't, the hub keeps notifying as before.
- **Finding the hub.** It reads `hub.json` from the hub's state directory
  (`~/.local/state/gh-hud`, or `GH_HUD_STATE_DIR`), and reads it again
  whenever it loses the hub, so it follows the hub across restarts.
- **Checks.** `make mac-check` runs the app's checks (`swift run
  GHHudChecks`; XCTest needs Xcode). `swift run GHHudChecks --live <state
  dir>` from `macos/` also connects to a running hub.
- `build/gh-hud.app/Contents/MacOS/GHHud --snapshot menu.png` renders the
  menu to an image once the hub answers, for screenshots.

## Agent dispatch

`a` on a row proposes dispatching an autonomous opencode session against a
ghws checkout of that repo (workspace root: `$GHWS_ROOT`, default
`~/Projects/github`; instance whose branch matches the PR's head branch is
preferred). The offered tasks follow the row's signals (see
[Tasks](#tasks) for each one's trigger and command): merge conflict →
rebase, a failing required check → fix (see
[Coordination](#coordination)), changes requested on your PR → address feedback,
PR in the review queue → review, issue → implement. Your own PRs also offer
review on request (never auto-dispatched outside `unbound-force`;
`/uf.review-pr` posts the verdict as a comment, since GitHub rejects approving
your own PR; see [Review identity](#review-identity) for `unbound-force`). Tasks
can also start themselves with `auto` (see
[Configuring dispatch](#configuring-dispatch)). The
dispatcher never assumes
workspace state: it refuses busy checkouts (a live herdr agent or a foreign
opencode session updated within `busy_window_secs`), applies your `dirty`
policy to uncommitted files, and fast-forward syncs the default branch to
`upstream` before launching. It also fast-forwards every stealth overlay
the checkout links into (the `.uf/<repo>` clones behind `.opencode`,
`openspec`, …) onto its upstream; it never pushes one, and an overlay
with uncommitted changes or unpushed commits refuses the dispatch. PR
tasks (rebase, ci, feedback) then check the PR head out with
`gh pr checkout --force`, so the agent starts on the PR exactly as it is
on GitHub; a local PR branch holding commits no remote has is refused
rather than reset.

When a run ends, gh-hud reads its outcome and the agent's final message
from the opencode transcript and inspects the checkout and its overlays.
A run that left nothing behind is returned to the default branch. One
that left uncommitted files or unpushed commits is marked `!` (left work)
and kept as it is: nothing is stashed, reset or backed up silently, and
the next dispatch into that checkout is refused naming the run that left
it. Each PR/issue row shows its latest run as a tag (`feedback ✓ 12m`;
`✗` failed, `!` left work, `?` outcome unknown, `●` running, `◆` blocked;
failures and leftovers also take the attention slot), and with `notify`
every finished run posts a desktop notification. `i` opens the row's
latest run: where it ran, how long it took, the outcome and failure
reason, what it pushed, what it left behind, and the agent's final
message; there `w` watches a live run, `t` opens the transcript
(`opencode -s <id>` on its machine), `y` copies the session id, and `r`
re-checks the checkout after you've committed, pushed or discarded the
leftovers.

Dewey learnings (`.uf/dewey/learnings/*`, written by agent sessions after
most runs) are the exception: untracked learnings never count as dirty or
as left work, since a new uniquely named file can't conflict with a
checkout or a fast-forward (a modified tracked learning still does). After
a clean run, gh-hud commits a checkout's untracked learnings on
`chore/dewey-learnings-<label>-<date>` cut from `upstream/<default>`,
pushes it to `origin` and opens a PR on the upstream repo — at most one a
day per checkout, shown under Pushed in the run view. A failed push puts
the files back; set `learnings_pr = false` to only ignore them. Learnings
inside a stealth overlay are ignored but not PR'd.

By default (`runner = "herdr"`) dispatch goes through a local herdr
server: one herdr workspace per ghws workspace (created lazily, reused,
never closed), one tab per dispatch labeled `<task> <repo>#<num>`, agent
run in that tab's pane. If the server can't be reached, dispatch falls
back to a detached local `opencode run --command …` with the configured
agent — except with `dispatch_mode = "tui"`, where the dispatch is
refused instead, since `opencode run` has no question tool. Set
`runner = "local"` to skip herdr entirely.

With the default `dispatch_mode = "run"` sessions run headless, so the
configured `agent` (default `autonomy`, from
[opencode-auto-question](https://github.com/jflowers/opencode-auto-question))
must not depend on a human: under `opencode run` a permission `ask` is
auto-rejected and ends the run, while `deny` returns an error the agent can
work around. The `build` agent inherits interactive `ask` rules.
Dispatch panes also export `OPENCODE_PERMISSION` with `external_directory`
denied outside the temp dirs. opencode merges it over the config files, so
it reaches subagents the dispatched agent starts (review-council reviewers
don't inherit the autonomy agent's rules) and an outside path is an error
rather than a prompt; sessions you start yourself keep the config's `ask`.

### Review identity

`review` tasks on your own `unbound-force` PRs run as the `uf-divisor` machine
account, auto or by hand with `a`. Everything else runs as your own `gh`
login, including reviews of other people's `unbound-force` PRs.

| Task | your `unbound-force` PR | anything else |
| --- | --- | --- |
| `review` | `uf-divisor` | you |
| `feedback`, `ci`, `rebase`, `issue` | you | you |

Your `unbound-force` PRs enter the Queue until `uf-divisor` has reviewed the
head commit, so each new push gets one review. Review and feedback feed each other, so each stops auto-dispatching on a PR
after 2 rounds, counting runs you started by hand. gh-hud shows a notice
once, and `a` still starts another round. Set `max_rounds` in the task's
table to change it, or a negative value for no limit. Only one agent works
on a PR at a time, on any machine, so a review never starts while a
feedback run is still pushing (see [Coordination](#coordination)).

Chore PRs skip the Queue, so
they only get a review if you start one with `a`; on a branch that requires
an approval, they won't auto-merge without one. No task auto-dispatches on a
chore PR, so a chore with failing CI or a conflict also waits for you. With `[dispatch.review] auto =
true` they drain like any other Queue row.

The dispatch shell exports `GH_TOKEN` from `UF_DIVISOR_GH_TOKEN`, looking in
`~/.config/agent-env` when the shell doesn't already have it (a hub pane only
has what the herdr server started with, so a new token needs no server
restart). gh-hud only ever handles the variable's name: the value stays out
of pane transcripts, argv and logs.

A review never falls back to your account. A machine without the token
refuses before touching the checkout and placement tries the next one. If none
can take it, `a` shows the error and an auto dispatch flashes it once per PR,
retrying as usual.

The bot posts the council's real verdict, so its `APPROVE` counts toward
required approvals (GitHub only stops you approving your own PR, not
`uf-divisor`). It never merges: a `gh` guard first on `PATH`
(`~/.local/share/gh-hud/guard/gh`, rewritten on each such dispatch) refuses
`gh pr merge`, the merge API and GraphQL merge mutations.

This is a convention, not a security boundary: any agent can read the token
from `agent-env`, and the guard only sees `gh`, so `curl` would get past it.

`opencode run` also can't ask questions: it denies the question tool, so a
headless agent that needs a decision guesses or fails. `dispatch_mode =
"tui"` (herdr runner only; per machine with `[[machines]] dispatch_mode`)
runs the agent as an interactive opencode TUI in its herdr pane instead,
started with `--prompt "/<command> <args>"`, so it can ask and the
auto-question plugin can answer. The TUI never exits on its own, so gh-hud
reads completion from herdr's agent state: live while working or blocked,
finished on `done` (or idle after working). On finish it links the opencode
session, marks the dispatch failed when the transcript doesn't end in a
clean assistant reply (a `?` yellow marks a dispatch whose transcript
couldn't be read, with the reason on the selected row), and closes the pane (the transcript stays in
opencode: `opencode -s <id>`). A short block is auto-question deciding
(up to its 120s handler timeout); when a dispatched agent stays blocked
longer than `blocked_notify_secs` (default 180, needs `notify`), the
question went to a human and gh-hud sends a desktop notification naming the
machine and pane to attach to.

Dispatch only targets ghws `@` instance workspaces (never the base
human-owned clones); set `dispatch_instances_only = false` to also allow
base workspaces. With `[[machines]]` configured, placement spreads across
the hub and the workers: a machine qualifies when it is enabled and
reachable (`herdr machine status`), allows the task (per-machine `tasks`
allowlist), has a free slot (`max_concurrent`), and has a matching idle
checkout under its `ghws_root`. Machines are ranked by free-slot fraction
(ties: local first, then config order) and busy or unreachable candidates
are skipped in favor of the next one. A PR/issue is never live on two
machines at once. Workers are herdr-managed machines saved with
`herdr machine add` and reached over ssh (key auth); gh-hud never starts a
remote herdr server itself.

Each worker needs, before gh-hud can place work on it: a running herdr
server (e.g. a systemd user unit), the same opencode/uf toolchain and
provider credentials as the hub, `gh` auth for pushes, and ghws `@`
instances under its `ghws_root` (`ghws` runs headless on Linux). Instance
names must be unique across machines so sessions and records never collide;
ghws's per-machine name themes (`GHWS_NAME_THEME`) handle that. The
author's fleet setup (provisioning, toolchain pins, secrets, runbooks) lives
in their private dotfiles repo under `docs/fleet.md`.

`6` opens the Agents tab (its count is the number of running agents): a
full-width table of agent sessions across workspaces and machines — task,
PR/issue with its title, machine, workspace, status, how long the run
took, and age — with the selected session's detail below it (outcome and
failure reason, what it left behind and pushed, the agent's final
message). By default it lists sessions gh-hud dispatched, live/blocked/
failed ones, and anything touched in the last 24h (`a` toggles the stale
rest): `enter` watches one (herdr
sessions focus the workspace and attach the herdr TUI in a Ghostty window
via `herdr_watch_cmd`; remote ones attach with `herdr --machine <label>`;
local runner sessions tail the session log via
`agent_watch_cmd`), `i` opens the full run view, `t` the transcript, `y`
copies the session id, and the other tab keys switch back to the
dashboard. opencode keys sessions by repo, so the hub lists each repo
once (not once per checkout) and reuses a listing for two minutes. A row
in the dashboard shows its own run in the attention slot: `●` teal =
running, `◆` orange = blocked on a question or approval. The header shows
per-machine slot usage (`local 1/2 · lap1 0/2 · lap2 —`, `—` =
unreachable or disabled).

`9` opens the Workspaces tab (its count is the number of ready
checkouts): every checkout a dispatch could use (`@` instances only with
`dispatch_instances_only`) on the hub and each worker, with what the
next run there would hit. A checkout is ready unless an agent is running
in it, it has uncommitted files (untracked dewey learnings don't count),
it has no `upstream` remote, or its default branch has diverged from
upstream (preflight only fast-forwards). Being on another branch is fine:
preflight switches to the default branch. Notes show what cleaning up
would lose (commits on no remote, stashes) and learnings waiting for the
daily chore PR; the selected checkout's detail lists the files and
branches. One script per machine reads every checkout (no fetch, so the
upstream column is as of each checkout's last fetch), every 2 minutes and
on `r`; `y` copies the checkout's dir.

### Coordination

gh-hud moves a PR toward merge one step at a time and keeps agents from
competing over it:

- **One dispatcher.** The hub (see "The hub") takes an exclusive lock on
  `dispatch.lock` in its state dir and owns dispatch, auto-merge, finishing
  runs and fleet rollouts; dashboards attached to it share it. A second
  hub on the same state dir (`--no-attach`, or a gh-hud from before the
  socket) is read-only: it shows runs from `state.json`, its header says
  `read-only: dispatch owned by gh-hud pid N`, and `a` is refused. It takes
  over when the owner exits (fleet rollouts need a restart). Workers never
  run gh-hud; the hub places all their work.
- **Reservations.** A dispatch claims its PR/issue and its checkout before
  preflight and holds them until the run is recorded, so a second dispatch
  can't pass the same busy checks during the seconds preflight takes.
- **One auto pass at a time.** A refresh that lands while the previous pass
  is still placing agents skips its pass.
- **One step per PR.** Of the tasks auto-dispatch handles for a PR, only the
  most urgent runs: rebase, then ci, then feedback, then review, and merge
  last. Feedback is due only while a human thread is unresolved or a
  reviewer acted after you; a changes-requested review you already answered
  goes to the review step. A step that can't go on stops the PR at "needs
  you" instead of letting a later step run on a PR that isn't ready for it:
  out of rounds, or its last run on the PR's current state failed, left
  work, or changed nothing. gh-hud flashes that once; `a` still starts any
  task by hand. Tasks without `auto` are skipped, not waited on.
- **Merge train.** Your own PRs in each repo are ordered closest to merge
  first: ready to merge, approved but needing a fix, waiting on review,
  changes requested, draft; then smaller, then older. Auto-dispatch gives
  free agents to your PRs in that order (other people's PRs keep their
  place). A rebase or review waits while another of your PRs in the repo
  that changes a file in common (or whose files are unknown) has auto-merge
  on and is ready to merge, because that merge would undo it; fixing checks
  and answering feedback don't wait. In a repo that requires branches to be
  up to date, only the next PR to merge is updated, with GitHub's update
  branch (`update_method`, as your login, in repos `[auto_merge]` covers);
  the rest wait their turn instead of being updated after every merge.
- **Required checks only.** Where the base branch requires checks, only a
  failing required check starts a `ci` agent; another failing check doesn't
  block the merge, so it doesn't start one (the row still shows ❌, and `a`
  still offers `ci`). A repo that requires no checks can't tell them apart,
  so any failure counts there.
- **Shared failures.** A check that also fails on the base branch's latest
  commit, or fails on two or more of your PRs in the repo, has one cause
  outside each PR. Instead of a `ci` agent per PR, each fixing it again and
  conflicting with the others, a PR whose only failures are shared stops at
  "needs you", once for the group: `▸you: ci shared: OSV-Scanner (main
  too)`. A PR with a failure of its own still gets its agent.
- **Fixed on main.** A PR's checks only run again when the PR changes, so
  after the base branch is fixed the PRs would keep their old failures.
  Once the base branch passes a shared check again, gh-hud brings each PR
  still failing it up to date with GitHub's update-branch, as your gh login
  (no agent), and its checks rerun on top of the fix: `▸ci update: main
  fixed OSV-Scanner`. This needs `auto_merge` to cover the repo. Each PR
  commit is tried once per base commit. A conflict goes to a `rebase` as
  usual, and a PR that already had the fix gets a `ci` agent, since its
  failure is its own.

Each row shows its stage after the run tag:

| Tag | Meaning |
|---|---|
| `▸ci` | the next step, starting when a slot frees |
| `▸ci wait` | the step ran in the last 30 minutes; it waits before another round |
| `▸rebase after #12` | the step waits for #12, which changes the same files, to merge |
| `▸you: review 2/2 rounds` | the pipeline stopped (orange `◆`); the reason follows the task |
| `▸you: ci shared: Lint (3 PRs)` | the failing check is shared; one fix is needed, not one per PR |
| `▸ci fix in #1031` | a supervisor linked the shared failure to the PR that fixes it |
| `▸ci update: main fixed Lint` | main fixed the failing check; gh-hud updates the PR so its checks rerun |
| `▸merge` | auto-merge is on; GitHub merges when the rules pass |
| `▸merge updating` | next to merge in a repo that requires up-to-date branches; gh-hud updated it |
| `▸merge after #12` | behind main; it's updated once #12 has merged |

### Plan and your queue

`p` opens the Plan: what agents start next, in the order they start, and
what's running, waiting, stopped and merging below that. It draws the same
plan the auto pass follows, so the numbered rows start in that order as
slots free (the header shows the slots).

```
    3 start next  ·  slots local 2/3 · lap1 0/2 · lap2 0/4
    # task      item                    title                         status
  ── your queue ──────────────────────────────────────────────────────────────
    1 review    uf/dewey#177            fix(review): use the reply…   next free slot
    2 rebase    uf/unbound-force#1006   fix(doctor,scaffold): …       waits: ci agent on it
  ── auto, next ──────────────────────────────────────────────────────────────
    3 review    uf/unbound-force#1004   fix(review-dispatch): …       next free slot
  ── waiting ─────────────────────────────────────────────────────────────────
      rebase    uf/gaze#330             fix(cli): …                   after #326
  ── needs you ───────────────────────────────────────────────────────────────
      review    uf/dewey#163            feat: resolve bare compiled…  2/2 rounds
```

Your queue goes first. `+` on any dashboard row, or on a Plan row, queues an
agent task (on the dashboard it asks which task; in the `a` menu, `+` queues
the selected task instead of starting it now). Queue as many as you like:

- **Order:** `J`/`K` move the selected queued task later or earlier, `x`
  removes it.
- **Starting:** a queued task starts as if you had pressed `a` when a slot
  frees, so it runs even where the auto pipeline would wait or has stopped
  (out of rounds, held for a merge, a shared failure). One agent per PR and
  the machines' slots still apply: a queued task on a PR with an agent
  working waits for it. If it can't start (no idle checkout, a dirty one),
  it stays queued with the reason, flashed once.
- **Precedence:** a PR with a task in your queue gets nothing else
  automatically, and auto-merge doesn't arm or merge it, until that task has
  started.
- **Persistence:** the queue is saved in `queue.json` in the state dir, so it
  survives a restart. Only the gh-hud that owns dispatch edits it.

### Supervisor cases and decisions

Some cases need judgment the rules can't make. gh-hud lists them in
`cases.json` in the state dir and takes answers from files dropped in the
state dir's `decisions/` folder. A supervisor (an agent, or you) reads the
cases and writes decisions. The owning gh-hud checks each decision against
the case as it stands on its next pass, applies it or refuses it, and
removes the file. A refused decision is kept as `<name>.refused` with the
reason. Decisions only hold or order work. They can't start an agent,
merge, push or post.

There is one kind of case:

- **`shared_failure`**: a required check failing on main or on several of
  your PRs. Its id is `shared_failure|owner/repo|check`.

A decision answers it in one of two ways:

- **`link_fix`** names the open PR that fixes the check, for example
  `{"case": "shared_failure|acme/app|OSV-Scanner", "kind": "link_fix",
  "fix_pr": 1031}`.
  - The group's PRs wait for it (`▸ci fix in #1031`) instead of stopping.
  - The fix PR goes first in its repo's merge train if it's yours.
  - Once it merges, the PRs are updated so their checks rerun.
- **`escalate`** says the supervisor can't decide, for example
  `{"case": "…", "kind": "escalate", "summary": "No open PR fixes the
  advisory", "options": ["bump effect", "ignore it"]}`. The PRs stop with
  its summary (`▸you: ci supervisor: No open PR fixes…`).

A decision lapses when its case closes or a linked fix PR stops being
open. An escalation also lapses when its case's facts change: the PRs in
the group, or main's commit. A PR with a failure of its own still gets its
`ci` agent whatever the decision. The fix PR must be one gh-hud can see,
which means it's yours, waiting on your review, or mentions you.

### The supervisor agent

gh-hud can answer cases itself. With the supervisor on, the owning gh-hud
starts a headless, read-only opencode run whenever a case opens with no
decision:

```toml
[supervisor]
enabled = true
mode = "propose"   # or "act"
model = ""         # provider/model; empty uses opencode's default
timeout_secs = 600
```

- **Where it runs.** On the hub's machine, outside the slots, one run at a
  time, in an empty folder under the state dir (`supervisor/work`).
- **What it can do.** It runs as the `hud-supervisor` agent, which gh-hud
  ships and writes to `supervisor/opencode/agents/`. gh-hud points
  `OPENCODE_CONFIG_DIR` there and turns off project config, so the agent is
  the same whatever repo the case is in. Its tools are denied by default:
  it can read files and run read-only `gh pr`, `gh run` and
  `gh issue view` commands, and nothing else. Your global opencode config,
  plugins and guards still load.
- **Keys.** The shell that starts it loads `~/.config/agent-env` for the
  model keys, then unsets `UF_DIVISOR_GH_TOKEN` and `GH_TOKEN`.
  Key values never pass through gh-hud.
- **The brief.** The run gets the case's facts: the repo, the check, the
  failing PRs and whether main fails it too. It also gets the other open PRs
  gh-hud sees in the repo, and the two answers allowed. It ends with one
  fenced json block.
- **Checking the answer.** gh-hud checks the answer like a decision from
  the inbox. If it's refused, the agent gets one retry that says why. After
  that, gh-hud escalates for it: "the supervisor found no answer". Run
  logs are kept in `supervisor/runs/`.

What happens to an answer:

- An **escalation** applies at once. It only changes what the PRs say.
- A **`link_fix`** in `propose` mode waits for you. The case's PRs show
  `▸you: ci approve: fix in #1031?`. The Plan tab lists the case with the
  evidence, and `Y` approves or `N` rejects it. Other clients use the hub's
  `ApproveProposal` and `RejectProposal` calls. In `act` mode it applies as
  soon as it passes gh-hud's check.

While a run is going, the PRs show `▸ci supervisor looking`. A case gets
one run per set of facts. An approved, rejected or applied proposal holds
off another run until the case's PRs or main's commit change. Proposals
are kept in `proposals.json`.

## Configuring dispatch

Dispatch is configured in three layers, all in `config.toml`:

| Layer | Where | Decides |
|---|---|---|
| Tasks | `[dispatch.<task>]` | what each task runs (`command`), whether it starts itself (`auto`), and for which orgs (`orgs`) and items (`mine`) |
| Machines | top level (the hub) and `[[machines]]` (workers) | where agents may run, how many at once, which tasks each machine accepts, and how agents run there |
| Checkouts | ghws `@` instances on each machine | which repos a machine can take work for at all |

Task settings are global: one `command`, `auto`, `orgs` and `mine` per task, used
on every machine. Per-machine control is the `[[machines]] tasks`
allowlist; per-org control of auto-dispatch is the task's `orgs` list. A
machine only takes work for repos it has a ghws instance of, so which
orgs a worker serves is also decided by which workspaces you create on it.

### Tasks

| Task | Default command | Offered on (`a`, and what `auto` dispatches for) |
|---|---|---|
| `rebase` | `/uf.rebase-pr {num}` | any listed PR with a merge conflict |
| `ci` | `/uf.fix-ci {num}` | any listed PR with a failing required check (any failing check where none are required) |
| `feedback` | `/uf.address-feedback {num}` | your PRs with changes requested or unresolved review threads |
| `review` | `/uf.review-pr {num}` | PRs in the Queue (`8`): others' PRs ready for review, and your own in `unbound-force`. Also offered on your own PRs elsewhere, never auto-dispatched there. Your `unbound-force` PRs run as `uf-divisor` (see [Review identity](#review-identity)) |
| `issue` | `/uf.run-pipeline-plan #{num}` | issues assigned to you or mentioning you |

"Any listed PR" includes other people's PRs in Review and CI, so `auto` on
`rebase` or `ci` sends agents at PRs you don't own; limit it with `mine`
or `orgs`, or leave it manual.

All five tasks exist without any config. A `[dispatch.<task>]` table only
needs the keys it changes: `auto = true` alone keeps the default command.
`command` is the slash command the agent starts with; `{num}` becomes the
PR or issue number.

`auto = true` makes the task start itself on every refresh, without a
keypress, for each row that triggers it, as slots free up. A dispatch that
finds no slot or checkout is retried on later refreshes. A task never
starts twice for the same PR or issue state: it waits until the item
changes (new commits, comments, checks), and never restarts a ref within
30 minutes of its last start. For `review` this drains the Queue.

`orgs` limits `auto` to repos owned by the listed orgs or users
(case-insensitive). Empty or unset means every org.

`mine = true` limits `auto` to items that are yours: PRs you opened, and
issues assigned to you (not ones that only mention you). `feedback` only
ever triggers on your PRs, so it needs no `mine`; on `review` it means
auto never fires, since auto-review covers only others' PRs.

`orgs` and `mine` combine (both must match), and neither limits `a`: you
can still dispatch any task by hand on any PR or issue.

### Machines

The hub is configured at the top level: `max_concurrent` (default 2) is its
slot count, and it accepts every task. Each `[[machines]]` entry adds a
worker reached over ssh:

| Key | Default | Meaning |
|---|---|---|
| `name` | required | herdr machine label (`herdr machine add`); `local` is reserved for the hub |
| `ssh` | `name` | ssh target, when it differs from the label |
| `max_concurrent` | 2 | agents gh-hud runs there at once |
| `ghws_root` | `~/Projects/github` | ghws root on that machine |
| `tasks` | all | allowlist of tasks the machine accepts, e.g. `["review", "ci"]` |
| `enabled` | true | `false` takes the machine out of placement and polling; fleet rollouts still reach it (a drain asks its herdr directly) |
| `dispatch_mode` | top-level | `run` (headless) or `tui` for agents on this machine |

Placement ranks the machines that qualify for a dispatch (enabled,
reachable, task allowed, a free slot, not draining for a fleet rollout, an
idle checkout of the repo) by free-slot fraction, hub first on ties, then
config order. A PR or issue never runs on two machines at once.

### Examples

**Manual only (the default).** No `[dispatch]` tables: `a` offers every
task, nothing starts by itself.

**Auto-review the whole queue.**

```toml
[dispatch.review]
auto = true
```

**Auto-review one org's queue, review the rest by hand.**

```toml
[dispatch.review]
auto = true
orgs = ["fullsend-ai"]
```

**Fix your own PRs' conflicts and CI, never anyone else's.**

```toml
[dispatch.rebase]
auto = true
mine = true

[dispatch.ci]
auto = true
mine = true
```

**Implement issues assigned to you in one org.**

```toml
[dispatch.issue]
auto = true
mine = true
orgs = ["zero-dot-force"]
```

**Different auto scopes per task.** Fix CI and conflicts on every PR in one
org automatically, auto-review two orgs, and implement issues only on `a`:

```toml
orgs = ["fullsend-ai", "unbound-force", "zero-dot-force"]  # dashboard scope

[dispatch.ci]
auto = true
orgs = ["zero-dot-force"]

[dispatch.rebase]
auto = true
orgs = ["zero-dot-force"]

[dispatch.review]
auto = true
orgs = ["fullsend-ai", "unbound-force"]
```

**Address feedback on your PRs as soon as it arrives.** `feedback` only
ever triggers on your own PRs, so it needs no `orgs`:

```toml
[dispatch.feedback]
auto = true
```

**A different command.** Point a task at your own slash command
(`{num}` is replaced; anything else is passed through as written):

```toml
[dispatch.issue]
command = "/my-implement-issue #{num}"
```

**A hub that stays light, with workers doing the work.** The hub keeps
one slot for what you start by hand; the workers take most of the load:

```toml
max_concurrent = 1           # hub

[[machines]]
name = "lap1"
max_concurrent = 2

[[machines]]
name = "lap2"
max_concurrent = 4
```

**Machines with roles.** lap1 only reviews; lap2 takes everything but
issue implementation, which then lands on the hub or lap3:

```toml
[[machines]]
name = "lap1"
tasks = ["review"]

[[machines]]
name = "lap2"
tasks = ["review", "ci", "rebase", "feedback"]

[[machines]]
name = "lap3"
```

**A worker for one org.** Give it ghws instances of only that org; tasks
for other orgs' repos never go there because it has no checkout of them.

**Take a worker out for a while.** Nothing new is placed there (let its
running agents finish first):

```toml
[[machines]]
name = "lap2"
enabled = false
```

**Agents that can ask questions.** Run every agent in the opencode TUI so
the question tool and auto-question work, or only on one machine:

```toml
dispatch_mode = "tui"        # every machine

[[machines]]
name = "lap1"
dispatch_mode = "run"        # except lap1, headless
```

**Stash instead of refusing a dirty checkout.**

```toml
dirty = "stash"              # or "stash-tracked" to leave untracked files
```

## Auto-merge

gh-hud can turn on GitHub auto-merge for your own PRs, so GitHub merges each
one when its branch rules pass. GitHub's repo setting only makes auto-merge
available; something still has to switch it on for each PR. This is off by
default, and covers nothing until you list orgs or repos:

```toml
[auto_merge]
enabled = true
repos = ["unbound-force/dewey"]  # single repos; orgs = [...] covers whole orgs
method = "squash"                # squash (default), merge or rebase
hold_label = "hold"              # default
update_method = "merge"          # how the merge train updates the next PR: merge (default) or rebase
```

"Done" is whatever the branch rules on the base branch require, so that's
where approvals, checks and resolved threads are configured. gh-hud acts on a
PR only when all of these hold:

- you opened it, and it's in a covered repo
- it's not a draft, has no `hold_label` label and has no merge conflict
- the repo allows auto-merge
- the base branch's protection or rulesets require at least one approval
- no agent is working on it and no agent step is due (merge is the
  [pipeline's](#coordination) last step; turning auto-merge off is never held)

The last check is the safety net: on a branch with no such rule, auto-merge
has nothing to wait for. gh-hud also doesn't use `gh pr merge --auto`, which
merges on the spot when nothing is pending.

A PR that's already approved and `CLEAN` when gh-hud first sees it has nothing
left to wait for, so GitHub refuses auto-merge and gh-hud merges it directly.
Every call names the head commit it checked, so a newer push makes GitHub
refuse it.

To stop a PR, add the hold label or convert it to a draft: gh-hud turns
auto-merge off at the next refresh, so a merge can still land before then.
New PRs are only picked up while gh-hud runs; once auto-merge is on, GitHub
finishes the merge without it. Merges run as your `gh` login, not `uf-divisor`.

## Fleet rollout (GitOps)

`[fleet]` turns gh-hud into a GitOps coordinator for the machines it
dispatches on: it polls `repo`'s `branch` head, and when a new commit is
green it rolls every machine forward one at a time (config order, hub
always last) using each machine's `fleet-apply` script (fetch, ff-only,
`chezmoi apply --force`, health check). State lives in
`~/.local/state/gh-hud/fleet.json`, so a gh-hud restart resumes where it
left off; machines already at the target are simply current.

The CI gate (`require_ci = true`): the target commit must have at least one
check run, all completed, every conclusion green (`success`, `skipped`,
`neutral`). Pending checks wait; a failure (or zero checks) blocks the
target — shown in the view and notified once per sha.

Per machine the rollout walks: `current` → `draining` (marked so the
dispatch scheduler places no new agents there; waits for live gh-hud
records and working/blocked herdr agents to hit zero, asking a machine's herdr
directly when it has `enabled = false`, notifying once after
`max_wait` but never forcing) → `applying` (`fleet-apply <sha>`, 30m
timeout, cancellable) → `verifying` (`fleet-apply --check` plus `--status`
== target) → `current`, undrain, next machine. A failure records the last
~20 output lines, **pauses** the rollout, notifies, and leaves the machine
drained; an unreachable machine is skipped for the round and retried on the
next poll. After the local apply succeeds a banner reminds you to restart
gh-hud so the new binaries load.

`F` opens the fleet view: per-machine table (name, reachable, slots,
applied vs target sha, state, drain wait, last error) with target/CI/pause
status in the header. Keys: `p` pause/resume · `a` apply now (force a
poll) · `s` skip the drain wait (press twice — it applies with live agents
still running) · `r` retry the failed machine and resume · `F`/`esc`
close. The main header carries a compact fleet chip while a rollout is
active or failed.

## Config

`~/.config/gh-hud/config.toml` (all optional; `--config` picks another path).
With no `orgs`, searches cover all of GitHub.

```toml
orgs = ["my-org", "another-org"]  # limit PR/issue searches to these orgs
refresh_secs = 120         # auto-refresh interval
limit = 30                 # max items per search
my_prs_everywhere = false  # "Mine" ignores `orgs` and covers all of GitHub
watch_repos = []           # e.g. ["owner/repo"]: show failed Actions runs
known_bots = ["fullsend-ai-", "codecov", "github-actions", "qodo-code-", "dependabot", "renovate", "greenkeeper"]
url_click = "cmd+right"    # gesture → copy URL
ref_click = "shift+click"  # gesture → copy owner/repo#N
fetch_timeout_secs = 45    # per-section timeout
notify = true              # desktop notification when my PR becomes maintainer-ready
queue_window_hours = 48    # review queue: PRs updated within this window

# agent dispatch (see "Agent dispatch" above)
ghws_root = "~/Projects/github"  # ghws workspace root ($GHWS_ROOT default)
agent = "autonomy"         # opencode agent for headless dispatch (unattended: asks auto-reject)
runner = "herdr"           # dispatch runner: herdr (default) | local
dispatch_mode = "run"      # herdr runner: run (headless, default) | tui (agents can ask questions)
blocked_notify_secs = 180  # notify when a dispatched agent stays blocked this long (0 = off)
max_concurrent = 2         # max simultaneously dispatched agents (hub)
busy_window_secs = 900     # foreign session newer than this blocks dispatch
dirty = "refuse"           # dirty checkout policy: refuse | stash | stash-tracked
dispatch_instances_only = true  # only @ instance workspaces are targets
learnings_pr = true        # chore PR of untracked dewey learnings after a clean run (daily per checkout)
agent_watch_cmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e tail -n 200 -f {log}"
herdr_watch_cmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e herdr"  # {workspace} {pane} {machine}

# remote dispatch workers (herdr machine labels; ssh defaults to the name,
# key auth required). tasks is an optional allowlist; enabled defaults true.
[[machines]]
name = "lap1"
max_concurrent = 2         # ghws_root defaults to ~/Projects/github

[[machines]]
name = "lap2"
max_concurrent = 2
# ssh = "jflowers@lap2.example"     # ssh target when it differs from name
# ghws_root = "~/Projects/github"   # remote ghws root
# tasks = ["review", "ci"]          # task allowlist (empty = all tasks)
# enabled = true
# dispatch_mode = "tui"             # override the top-level dispatch_mode

# dispatchable tasks, one table each (see "Configuring dispatch" above).
# All five are built in with these commands; a table only needs the keys
# it changes. command: slash-command template ({num} = PR/issue number).
# auto: dispatch on refresh, no keypress. orgs: limit auto to these repo
# owners (empty = every org). mine: limit auto to your PRs and issues
# assigned to you. Neither limits a keypress dispatch.
[dispatch.rebase]          # any listed PR with a merge conflict
command = "/uf.rebase-pr {num}"
auto = false
orgs = []
mine = false

[dispatch.ci]              # any listed PR with failing checks
command = "/uf.fix-ci {num}"
auto = false

[dispatch.feedback]        # your PRs: changes requested or unresolved threads
command = "/uf.address-feedback {num}"
auto = false
max_rounds = 2             # auto runs per PR (negative = no limit)

[dispatch.review]          # Queue (8): PRs ready for review; also offered on
command = "/uf.review-pr {num}"  # your own PRs, auto there only in unbound-force
auto = false
max_rounds = 2             # auto runs per PR (negative = no limit)

[dispatch.issue]           # issues assigned to you or mentioning you; auto
command = "/uf.run-pipeline-plan #{num}"  # here implements every listed issue
auto = false

# Turn on GitHub auto-merge for your PRs (see "Auto-merge" above).
[auto_merge]
enabled = false            # run it at all
orgs = []                  # repo owners covered
repos = []                 # single owner/name repos covered
method = "squash"          # squash, merge or rebase
hold_label = "hold"        # a PR with this label stays open
update_method = "merge"    # merge train's branch update: merge or rebase

# How apps other than the TUI reach the hub (see "The hub" above).
[hub]
listen = "127.0.0.1:0"     # loopback only; "" turns TCP off

# GitOps fleet rollout (see "Fleet rollout (GitOps)" above). Machines need
# a fleet-apply script on PATH (chezmoi dotfiles checkout).
[fleet]
enabled = false            # run the coordinator at all
repo = "jflowers/dotfiles" # dotfiles repo whose branch head is the target
branch = "main"
poll = "5m"                # target poll interval
require_ci = true          # only roll out commits with green check runs
# order = ["lap1", "lap2", "local"]  # default: [[machines]] order, local last
max_wait = "4h"            # drain grace before notifying (never forces)
notify = true              # herdr push notifications (phone via Ovis)
```

## Development

CI (`.github/workflows/ci.yml`) runs on every pull request and on pushes to
`master`. It checks that `go mod tidy` leaves no diff, runs golangci-lint
(`.golangci.yml`), runs the tests with `-race`, and scans dependencies with
govulncheck. Pull requests test on Linux; pushes to `master` test on macOS
too, since hosted macOS runners can queue long enough to time out a PR check.
Run the same checks locally with:

```
make ci      # tidy, proto-check, lint, test, vuln
make lint    # needs golangci-lint v2 on PATH
make fmt     # gofmt + goimports
make proto   # regenerate the Go and Swift API code from proto/
make mac     # the Mac app (see "Mac app")
```

The hub's API is `proto/ghhud/v1/hub.proto` (Connect-RPC; see
`PLAN-serve.md`). A field added to the types the hub publishes needs a
proto field and a line in `internal/api/convert.go`: `api.TestRoundTrip`
fails until it has them.

Tests never read your git config: `internal/dispatch` points git at a
throwaway config of its own.

## Attribution

The PR enrichment logic (CI/review/thread/last-actor rules, bot masking, row
colors) is a Go port of Ralph Bean's `pr-list.sh`, shared with me by Adam
Scerra as part of his `pr-term` package. Thanks to both.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE).

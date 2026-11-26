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
- **Mine** — your open PRs in your orgs (set `my_prs_everywhere = true` for all of GitHub).
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
  where the author acted last, plus your own and Dependabot's
  `unbound-force` PRs until `uf-divisor` has reviewed the head commit
  (requires `orgs`; the sweep is disabled without them). A Dependabot PR is
  never a chore, whatever its title. Only repos `[dispatch.review]`'s
  `orgs` and `repos` cover enter the Queue (every repo in `orgs` when it
  sets neither), so it holds what review auto-dispatch would take. With
  `[dispatch.review] auto = true` these drain automatically as agent slots
  free up (see [Configuring dispatch](#configuring-dispatch)).

Your own sections (Mine, Review, Issues and the `by_label` issues) read
every match, page by page, up to 300 each: auto-merge, the merge train,
learnings approval and auto-dispatch act only on items the dashboard has.
The queue sweep, others' PRs in the agent review window, reads up to
`limit` (default 100), since it's the costly search: a page of 15 PRs is
about 17 GraphQL points. A section whose search matched more than it
read says so (`⚠ 1 cut short` in the footer, the count in the legend,
and `queue-sweep: 100 of 140 shown` on the apps' status line).

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
- The phone reaches the hub over your tailnet. `[hub] tailnet = "gh-hud"`
  makes the hub join it as its own device named `gh-hud` (it doesn't
  need the machine's Tailscale app running). The first start logs a
  login URL: open it and log in as yourself, not with a tagged auth key.
  The node's state is kept in `tailnet/` under the state directory. Only
  devices logged in as your tailnet user get through; tagged devices and
  devices shared in from other people are refused. The hub serves HTTPS
  on its tailnet name when HTTPS is on in your tailnet's admin console
  (DNS page: MagicDNS and HTTPS certificates), and plain HTTP otherwise.
- The Mac app on another Mac reaches the hub over your local network
  when `[hub] lan` names an address, e.g. `lan = "0.0.0.0:7377"` (a fixed
  port, so the other Macs keep finding it). Those Macs can't read
  `hub.json`, so they send the LAN token instead: the hub makes it once,
  keeps it in `lan-token` in the state directory (readable only by you)
  and keeps it across restarts. The hub logs where to reach it, such as
  `http://macbook.local:7377`, at start. It only answers private network
  addresses (your LAN, link-local, a VPN such as Tailscale), never the
  open internet. It's plain HTTP: anyone on the same network can read
  the traffic, token included, so turn it on only on a network you
  trust. Delete `lan-token` and restart the hub to make a new one.
- `[hub] agent_token = true` has the LAN listener also take a second,
  limited token for agents and scripts (`agent-token` in the state
  directory, made once, readable only by you). With it a caller can read
  the hub, dispatch, queue, enroll, reread or drop epics, drive the fleet
  and toolkit rollouts, start or cancel Dewey reindex passes, and prune
  branches, but can't approve epic plans or supervisor proposals, clear
  gates, or open windows on the hub: those stay with you. A toolkit
  rollout still ends in pin PRs that you merge. The hub logs each agent call that changes something, and each
  one it refuses. Copy the file to `~/.config/gh-hud/hub-token` on the
  agent's machine and use `gh-hud … -hub <address>`.
- A client that shows alerts as native notifications says so when it
  subscribes, and the hub stops posting its own osascript notifications
  while it's connected, so you aren't notified twice.
- `gh-hud --no-attach` runs a hub in-process even when one is serving
  (it follows the serving hub's dispatch, read-only); for debugging.

### Commands from a shell or another machine

These read and drive the running hub, like the dashboard does. Each takes
`-json` for output a script or an agent reads:

```
gh-hud status                     # machines, live agents, the plan, your queue, epics
gh-hud runs [-n N] [-ref R]       # recent runs, newest first
gh-hud choices acme/app#8         # tasks that can run on an item, and where
gh-hud dispatch acme/app#8 [-task issue]   # start one now (default: the suggested task)
gh-hud queue [add|rm] …           # your queue
gh-hud transcript <run|ref>       # a run's conversation (a ref: its newest run)
gh-hud pane <run|ref>             # the screen of a run's live pane
gh-hud dewey reindex [-force] <machine>|-all   # reindex the Dewey vaults there that need it now (see "Reindex the vaults now")
gh-hud workspaces [-machine M] [-problems]     # every fleet checkout: problem, uncommitted, local-only commits, last upkeep and reindex
gh-hud checkout pull|learnings|save|upkeep|reindex <machine> <checkout>   # act on one checkout now (see the Workspaces tab)
```

They, `gh-hud epic` and `gh-hud branches` use the hub on this machine's
socket. To reach a hub on another machine, name its `[hub] lan` address with
`-hub` (or `$GHHUD_HUB`) and keep its token in a file: `-token-file`,
`$GHHUD_TOKEN_FILE`, or `~/.config/gh-hud/hub-token`. `-hub mac` uses the
hub the Mac app on this Mac is set to, with the token the app keeps in the
Keychain (macOS asks before handing it over). A command reaches the hub you
named or fails; nothing falls back to another one.

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

`gh-hud serve` restarts itself when its binary is replaced, whether by
`go install` or a fleet apply. It waits until the new binary has stopped
changing for 30 seconds and `gh-hud -version` runs. It also waits until no
machine is applying a fleet rollout and no toolkit step is running. Then it
stops serving and execs the new binary in place, keeping the same pid and
terminal (or herdr pane). A binary that fails to run is skipped until it
changes again, so a broken build leaves the running hub alone. Set
`[hub] self_restart = false` to restart it yourself. A dashboard running
its own hub never restarts itself.

## Mac app

A Mac app, in the Dock and ⌘-Tab, with the dashboard in a window (it
opens at launch and when you click the Dock icon) and a menu bar icon
that shows what needs you, from the hub: agents waiting on
you, supervisor proposals and epic plans awaiting your approval, gates on
approved epics that only you can clear, reviews requested of you, and your
PRs ready for a maintainer. The icon shows the count; the menu lists them
(click one to open it on GitHub) with the hub's state and a Refresh
button. A proposal has Approve and Reject buttons, an epic plan has
Approve and a gate has Clear, the same as `Y`/`N`, `E` and `C` in the
dashboard's plan tab. A plan that went stale isn't offered: the hub reads
the epic again first. A gate is offered once everything its piece waits
on is done.

**Dashboard** in the menu (or the Dock icon) opens a window with the dashboard's items: the
filters (all, review requested, mine, issues, CI, inbox, ready, agent
queue) down the side with their counts, and the items as a table with
the same columns as the TUI. The search field matches repo, title and
`owner/repo#N`, like `/`. Double-click or Return opens an item on GitHub,
and the right-click menu opens or copies the links of the selected rows.
Titles carry the TUI's agent tags (`fix ● 5m`, `▸review`, `▸you: …`).

The agent commands work on the selected item, from the toolbar or the
right-click menu: **Start Agent…** (⌘D, the TUI's `a`) lists where each
task can start now and starts the one you pick; **Add to Queue…** (⇧⌘D,
`+`) queues one. When the item has a run, **Watch**, **Re-check** and
**Open Transcript** act on its newest run; Watch and the transcript open
a terminal on the hub's machine. A result shows along the bottom of the
window. The commands are off when the hub has no dispatcher or only
follows another gh-hud.

**Start Agent on URL…** (⇧⌘U) works on any open issue or PR, on the
dashboard or not: paste its link (or `owner/repo#N`; a link on the
clipboard is filled in). The hub reads it from GitHub and offers every
task that fits it, a PR's rebase, ci, feedback and review or an issue's
issue, the ones its state calls for marked **suggested**. It runs as a
Start Agent would, so `human_gate` and a free checkout of its repo still
apply.

With a dispatcher, the sidebar also has **Plan** (the TUI's `p`),
**Agents** (`6`) and **Workspaces** (`9`). Plan shows the open cases
(Approve or Reject a supervisor's proposal), the enrolled epics (Approve
Plan, Clear Gate on a gated piece, and Reread or Drop from the epic's
menu; **Enroll Epic** in the toolbar adds one), then what agents start
next in order: your queue, the auto steps due, and what's running,
waiting, needs you or is merging. A queued task moves earlier or later
(⌥⌘↑/⌥⌘↓) and ⌫ takes it off the queue; the right-click menu also
queues a row's task, starts an agent or opens the item. Agents lists the sessions and the runs behind them,
with the selected one's outcome below: where it ran, how long it took,
the reason it failed, what it left behind or pushed, its checkout's
Dewey index health after the run, and its final message. Double-click watches a session; the right-click menu re-checks
its run, opens its transcript or copies its ID or directory. **Show
Stale** adds sessions idle for over a day. Workspaces lists every
checkout a dispatch could use and whether the next run could start
there, with what's in the way (uncommitted files, a diverged default
branch, commits on no remote) below. Its Index column is each checkout's
Dewey index (`ok 2d 100%`, `old format`, `unindexed`, `reindexing…`, `—`
for a checkout that isn't a vault), in orange when the machine is at
fault and a reindex won't help; the detail lists each reindex reason,
what built the index, what it holds and the last reindex. Opening it reads the checkouts again
when they're over 30 seconds old, and Refresh (⌘R) refreshes whichever
pane is showing.

When the fleet is on, **Rollout** (the TUI's `F`) shows the target commit
and its CI, whether the rollout is moving, paused, blocked or failed, and
each machine's state, the commit it has, its agent slots, how long it has
waited for agents to drain, its Dewey reindex (`reindexing 3/12`, then
the summary) and its last error; the selected machine's failed and
skipped vaults are listed below. The toolbar pauses or resumes the
rollout, applies now, skips the drain wait (after asking), and retries
the failed machine. Reindex Dewey reindexes the vaults that need it on
the selected machine or every machine, or every vault with Force, after
asking, and cancels a pass.

With `[releases]` set (see "Config"), **Ship → Releases** lists every repo
[jflowers/ship](https://github.com/jflowers/ship) releases, read from its
`repos.yaml`: the latest release and how long ago it went out, the commits
on the default branch since it, the ship version `repos.yaml` records, and
the last release run (its failures are the sidebar's badge). Double-click
opens the release; the right-click menu opens its unreleased commits, its
last run or the repo, or copies `gh ship release <repo> ` to finish with a
tag. The hub reads it all every 15 minutes, and Refresh (⌘R) reads it now.

Its source is in `macos/` and it needs a hub running (see "The hub") with
TCP on, which is the default.

**On another Mac.** The app can use the hub on another Mac on your local
network. On the hub's Mac, set `[hub] lan = "0.0.0.0:7377"` and restart
the hub. It logs the addresses to use, and its LAN token is in
`~/.local/state/gh-hud/lan-token`. On the other Mac, open gh-hud →
Settings… (⌘,), choose **On another Mac on the network**, enter the
address (e.g. `macbook.local:7377`) and the token, and click Connect.
The token is kept in the Keychain. macOS asks once to let gh-hud reach
devices on your local network. Everything works the same except Watch
and Open Transcript, which open a terminal on the hub's Mac, so they're
off there. The hub on the other Mac keeps posting its own notifications
while this app is connected.

Each release publishes the app, notarized and built for Apple
silicon and Intel, to the GitHub release and to the Homebrew tap:

```
brew install --cask jflowers/tools/gh-hud
brew upgrade --cask gh-hud                 # after a new release
```

To cut a release, run the Release workflow from master with the tag
(`gh workflow run release.yml -f tag=v0.4.0`); don't push tags yourself.
It runs through [jflowers/ship](https://github.com/jflowers/ship)'s shared
workflows, which check the tag and that CI passed before creating it, and
lists the secrets it needs. To build it yourself:

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

It leaves the stapled app zipped as `build/gh-hud-app_VERSION.zip`.
`MAC_NOTARY_PROFILE` names another profile. A build signed with a
Developer ID now carries a secure timestamp, which notarization requires.

It builds with the Command Line Tools alone (no Xcode), for this Mac's
architecture; `MAC_ARCHS="arm64 x86_64"` builds both, which needs Xcode. Add it to
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

## Android app

A phone app (in progress, see `PLAN-android.md`) that shows what needs
you, as the Mac menu does: agents waiting on you, proposals, epic plans
and gates to approve or clear, reviews requested, and your PRs ready for
a maintainer. Tap a row to open it on GitHub; pull down to refresh.

It reaches the hub over your tailnet (`[hub] tailnet`, see "The hub"):
tap **Hub** and enter the URL the hub logs at start, like
`https://gh-hud.your-tailnet.ts.net`. It's connected while it's on
screen. Turn on **Notify me** there to stay connected while it's closed:
it notifies you of the hub's alerts (as the Mac app does) and of
proposals, epic plans and gates that start waiting on you. Android shows
this as a silent "gh-hud" notification saying whether the hub is there.
It starts again when the phone restarts or the app is updated.

To start an agent on any open issue or PR, tap **+** on Items and paste
its link, or share the link to gh-hud from your browser or the GitHub
app. As on the Mac (Start Agent on URL), it offers every task that fits,
the suggested ones marked.

Dewey indexes show where the TUI shows them: each checkout on
**Agents → Workspaces** has a `dewey` line (`ok 2d 100%`, `old format`,
`unindexed`, `no embeddings`, `reindexing…`), and its screen has the
verdict, each reason on its own line, what built the index and when, its
contents and the last reindex. A run's screen shows its after-run index
check. On **Fleet**, each machine lists its rollout's reindex and the
pass you asked for (`dewey pass: reindexing 3/12`, then the summary with
the vaults that failed or were skipped); **Reindex vaults now…** (every
machine, or one: tap its row) starts a pass after a confirmation, with
**Force** to reindex every vault, and **Cancel pass** stops one. A failed
automatic reindex or upkeep is a notification that opens the checkout.

**Install.** Each release (a `v*` tag) carries `gh-hud-VERSION.apk`, for
any phone or tablet on Android 10 or later. Open the release page on the
device, download the APK and open it; Android asks once to allow installs
from your browser (or Files). Installing a newer release's APK updates the
app in place, keeping its hub settings. Every release is signed with the
same release key. A build from `make android-install` is signed with the
Mac's debug key instead, so going from one to the other means uninstalling
first, which drops the hub URL and token.

Its source is in `android/`. The JDK and Android SDK tools come from the
repo's `mise.toml` (`mise install`); everything else from the Gradle
build. No Android Studio needed.

```
make android-sdk           # once per machine: SDK packages and license
make android               # lint, tests, debug APK
make android-install       # install on a phone over adb (USB debugging on)
make android-device-test   # the screen and notification tests, on that phone
make android-dev-hub       # point the phone's app at this Mac's hub over USB
make android-release       # the signed release APK (needs the release key, see the Makefile)
```

`make android-dev-hub` forwards the Mac's loopback hub to the phone (`adb
reverse`) and launches the app with the URL and token from `hub.json`,
so you can develop without the tailnet. Run it again after the hub
restarts.

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
`openspec`, …) onto its upstream; it never pushes one (only the
learnings PR below does), and an overlay
with uncommitted changes or unpushed commits refuses the dispatch. PR
tasks (rebase, ci, feedback) then check the PR head out with
`gh pr checkout --force`, so the agent starts on the PR exactly as it is
on GitHub; a local PR branch holding commits no remote has is refused
rather than reset.

git moves a submodule's recorded commit on a checkout or fast-forward but
leaves the submodule itself where it was, which reads as ` M <submodule>`.
So after each of its own moves (the preflight fast-forward, the PR
checkout, the return to the default branch after a run) gh-hud runs
`git submodule update --init --recursive` for the submodules it left
behind: uninitialized ones, and clean ones still at the commit recorded
before its move. The after-run inspection does the same for a submodule
the run's own commits moved (a rebase onto upstream) while it sat at the
run's starting commit. A submodule with changes in it, or checked out
somewhere else, is never touched and counts as left work.

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

Dewey's web-source cache (`.uf/dewey/cache/`) never counts either, in a
checkout or an overlay. Some repos track it, the fullsend-ai overlays
among them, and Dewey rewrites a cached page whenever it re-fetches a
source that changed upstream, so nobody made the change. Preflight and
the overlay sync put the cache back to what's committed before they
fast-forward.

Dewey learnings (`.uf/dewey/learnings/*`, written by agent sessions after
most runs) are the exception: untracked learnings never count as dirty or
as left work, since a new uniquely named file can't conflict with a
checkout or a fast-forward (a modified tracked learning still does). After
a clean run, gh-hud commits a checkout's untracked learnings on
`chore/dewey-learnings-<label>-<date>` cut from `upstream/<default>`,
pushes it to `origin` and opens a PR on the upstream repo, shown under
Pushed in the run view. A checkout has one learnings PR open at a time:
while the day's PR is open, new learnings wait for it; once it has merged
or closed, the next clean run opens another on the same day's branch name
with a suffix (`-2`, `-3`, …), so learnings don't wait for another day.
A failed push puts
the files back; set `learnings_pr = false` to only ignore them. Learnings
inside a stealth overlay the checkout links into get the same treatment on
the overlay's own repo: gh-hud commits them on the same day's branch, cut
from the overlay's upstream branch, pushes it to the overlay's `origin`
and opens a PR on the overlay's GitHub repo (listed under Pushed as
`overlay .uf/<repo> learnings PR …`). The overlay itself is never
switched: the commit is built aside, and the files leave the overlay only
once the push has succeeded. Other instances pick the merged learnings up
on their next preflight fast-forward.

Last, a clean run checks the checkout's Dewey index and records on the
run whether it needs a `dewey reindex` and why. The verdict is Dewey's
own, so **Dewey 3.9.0 or later** is required on the machine: since 3.9.0
the index records the Dewey version and index format that built it, and
`dewey status --json` says `reindexRecommended`, with
`reindexReasons`, when this Dewey writes a newer format
(unbound-force/dewey#224). The reasons the check records are:

- `unindexed`: no `graph.db` yet; the agent's first search builds one
  mid-run.
- Dewey's own reasons, verbatim, such as `index format 0, this Dewey
  writes 1` for an index built before 3.9.0.
- `no embeddings`: the index has blocks but no embeddings while Dewey's
  embedding model is available. When the model isn't available, that's
  a machine problem instead (`no embeddings, and the
  granite-embedding:30m embedding model isn't available`): a reindex
  wouldn't fix it.

A Dewey older than 3.9.0 has no verdict to give, so the check reports
the machine problem `dewey 3.8.0 is older than 3.9.0: can't tell whether
the index needs a reindex` rather than guessing. gh-hud no longer looks
for learnings or knowledge files changed since the index was built,
compares the Dewey binary against a recorded baseline, or probes the
embedder: Dewey resyncs learnings by content and prunes the pages of
deleted files itself (unbound-force/dewey#221, #222), and reports
whether its model is available (#225). The old `learnings changed`,
`knowledge changed` and `dewey upgraded` reasons are gone, and so is
`dewey-baselines.json` (the hub removes it).

`[dispatch] dewey_reindex` says what happens next: `"report"` (the
default) only records it, `"off"` skips the check, and `"auto"` reindexes
the checkout in the background once nothing else is using it. While that
runs the checkout is busy `reindexing` in the Workspaces tab and takes no
dispatch. The run is `dewey reindex` alone: since Dewey 3.9.0 it loads
learnings and knowledge stores itself (unbound-force/dewey#223), so the
warm-up `dewey serve` that used to follow it is gone. Afterwards gh-hud
reads `dewey status --json` once and records the Dewey version and index
format the index now carries. Each machine runs one reindex at a time,
and none starts in a checkout with a live agent, leftovers, or a held
`.uf/dewey/dewey.lock`. A flagged checkout that can't start now is
skipped, and the next clean run there flags it again. The Workspaces
tab's detail shows the last reindex: its outcome, how long it took, when,
why it ran and what the index records now. A failure is also a
notification and isn't retried. A verdict with only a machine problem,
such as no `dewey` binary or a Dewey older than 3.9.0, is never
reindexed, because a reindex wouldn't fix it.

The Workspaces tab's index column shows the same verdict, compactly
(`old format 2d 100%`, `unindexed`, `no embeddings`, `dewey too old`),
and the detail has Dewey's reasons in full, with the version and format
that built the index. The 2-minute poll only stats the vault (is there a
`graph.db`, and its mtime); `dewey status --json` runs on `r`, every 30
minutes, or when `graph.db` changed, and never in a checkout with a live
run. An index rebuilt since the last read shows `unchecked` until the
next one.

One reindex per machine holds on the machine itself, not only in
gh-hud's bookkeeping. Before a vault starts, gh-hud looks for a running
`dewey reindex` for any vault on that machine (`pgrep -f`, else `ps`),
whoever started it. If it finds one, the automatic reindex skips with
`another dewey reindex is already running on lap1 (pid N, vault …)`, and
a pass or rollout waits for it. That wait is bounded by the time limit
plus a few minutes. Past that, the machine's pass stops.

Each vault's reindex has a time limit, `[dispatch]
dewey_reindex_timeout` (default `"2h"`). The script on the machine
enforces the limit itself, without relying on the ssh session to carry a
cancel. It runs `dewey reindex`, and then the short `dewey status`, in
the background as a process group of its own (`set -m` job control,
which works the same in macOS and Linux shells, with no `setsid` or
GNU `timeout` needed). It records the group in
`.uf/dewey/gh-hud-reindex.pid`. At the deadline it sends the group
SIGTERM, then SIGKILL 10 seconds later, removes the pid file, and records
`timed out after 2h0m0s`. The status read gets at most a minute and
never fails the reindex. gh-hud waits two minutes longer than the script. If gh-hud stops waiting
first (a cancelled pass, an ssh drop, the hub shutting down), the script
stops its dewey on the way out. gh-hud then runs a short cleanup on the
machine that kills the group in the pid file, but only when that pid is
still the vault's `dewey reindex` or `status`. The result is recorded as
`cancelled: …` or `timed out: …`. If a dewey can't be confirmed gone,
the machine's pass stops with that failure, so no second dewey starts
beside it. The pid file isn't counted as leftover work.

#### Reindex the vaults now

Some Dewey releases change what an index stores, and only a reindex
updates an existing one. A pin that rolled out before the rollout
reindex (see "Fleet rollout") existed leaves vaults nothing reindexed.
Ask for a pass instead:

```
gh-hud dewey reindex lap1        # the vaults on lap1 that need it (local is the hub)
gh-hud dewey reindex -all        # every fleet machine, side by side
gh-hud dewey reindex -force lap1 # every vault on lap1, whatever its check says
gh-hud dewey status [lap1]       # reindexing 3/12, or the last summary
gh-hud dewey cancel lap1|-all
```

or, in the fleet view (`F`), `d` on the selected machine (`↑`/`↓`) or
`D` for every machine, each pressed twice (or `f` instead of the second
press for a forced pass), and `x` to cancel. A pass lists the machine's
vaults as the rollout does (every `@` instance's, plus base checkouts
with a `graph.db`) and checks each one as the teardown does. It
reindexes the ones that are unindexed, that Dewey recommends
reindexing, or that have no embeddings while the model is available,
and lists the rest as up to date; a vault whose check fails, or that
has only a machine problem (a Dewey older than 3.9.0, say), is skipped
with why. `-force` skips the check and reindexes every vault. Each
reindex goes through the automatic reindex's routine: busy
`reindexing`, the machine's one reindex at a time. It doesn't drain the machine, so a vault with a live agent,
leftovers or a held `dewey.lock` is skipped and listed with why, not
waited for. A `dewey reindex` already running on the machine is waited
for, and a dewey that can't be stopped stops the pass on that machine
(see the time limit above). The fleet view shows `dewey pass: reindexing 3/12` under the
machine and then the summary with every vault skipped or failed; each
vault's outcome is also its last reindex in the Workspaces tab. The end
of a pass is notified, and a failed vault raises the same alert as a
failed automatic reindex.

The machines are `[fleet] order`'s (the fleet must be on), since the
fleet's Dewey pin is why a pass is needed. A pass runs in the hub that
owns dispatch, detached from the command that asked; a hub restart ends
it. Running it again is safe: a vault already reindexed is up to date
(unless the pass is forced), and a skipped one gets another try. `dewey_reindex =
"off"` refuses a pass; `"report"` and `"auto"` allow it, since a person
asking is the switch `"report"` leaves to a person. The agent token can
start or cancel one too, so an agent you ask counts as that person.

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
| `review` | `uf-divisor` (Dependabot's PRs too) | you |
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
an approval, they won't auto-merge without one. Two kinds are handled for
you, in `unbound-force` only: gh-hud's own learnings PRs get a `uf-divisor`
approval with no agent (see [Auto-merge](#auto-merge)), and Dependabot's PRs
aren't chores, so they reach the Queue and get a bot review like yours. No task auto-dispatches on a
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

Dispatch panes also set `OPENCODE_SUPERVISOR_MODE=repair-only`, so
opencode-agent-supervisor runs only its tool-arg repair pass there. In full
mode its audit critiques abort and re-steer the child sessions a delegated
pipeline spawns (unbound-force/unbound-force#1199). Sessions you start by
hand keep the mode from the supervisor's config file.

Dispatch panes also turn on auto-question's headless mode
(`AUTO_QUESTION_HEADLESS=1`, plugin 0.4.0 or later): questions are answered
inside the tool call and no form ever shows, for every agent and subagent.
When a person must decide (a human gate, or an answer the profile won't
guess), the agent follows the playbook `AUTO_QUESTION_ESCALATION` names,
`~/.config/opencode/auto-question/escalation-gh-hud.md` (from dotfiles): it
comments on the issue or PR with the decision, pushes its work to a draft
with the `hold` label, and stops. gh-hud reads the plugin's `autoQuestion`
result from the transcript, so that run counts as failed with "a person must
decide": the PR or issue shows under Needs you, and an epic piece goes to
you without a supervisor review. A question asked in a subagent lands in
that subagent's own session, so gh-hud also reads the sessions the run
started through the `task` tool (and theirs, up to 12): a person needed
there makes the run need you too, even if the parent carried on. Older
plugin versions ignore both variables.

gh-hud only reads and changes **fleet checkouts**: the ghws `org@label`
instances of the top-level `orgs`, on every machine (any instance when
`orgs` is empty). Canonical org clones (`unbound-force/`) and other orgs
(`jflowers/`) belong to whoever uses the machine. Dispatch, the
Workspaces tab, session scans, Dewey reindexes and passes, branch
pruning and learnings PRs never see them, the hub's included. Changing
`orgs` takes a hub restart. The toolkit is the one exception: on the hub
it uses a repo's canonical clone as the git source for its scratch
worktrees and never changes that clone's working tree
(`dispatch_instances_only` no longer does anything). With `[[machines]]` configured, placement spreads across
the hub and the workers: a machine qualifies when it is enabled and
reachable (`herdr machine status`), allows the task (per-machine `tasks`
allowlist), takes the item's labels (`labels` / `exclude_labels`), has a
free slot (`max_concurrent`), and has a matching idle
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
per-machine slot usage and each machine's opencode version
(`local 1/2 oc 1.14.2 · lap1 0/2 oc 2.0.0 · lap2 —`, `—` = unreachable
or disabled), and the Agents tab lists the versions on its own line
(`local opencode 1.14.2 · lap1 opencode 2.0.0 · lap2 opencode ?`, `?` =
not known yet or opencode didn't answer). A worker's version is asked in
the same ssh round trip as its checkout scan, the hub's with a local
`opencode --version`, at most every 5 minutes each. Each run records the
version its machine had when it started; the run view shows it, and
`gh-hud status` / `gh-hud runs --json` and the hub API carry both.

`9` opens the Workspaces tab (its count is the number of ready
checkouts): every fleet checkout on the hub and each worker, with what the
next run there would hit. A checkout is ready unless an agent is running
in it, it has uncommitted files (untracked dewey learnings and Dewey's cache don't count),
it has no `upstream` remote, or its default branch has diverged from
upstream (preflight only fast-forwards). Being on another branch is fine:
preflight switches to the default branch. Notes show what cleaning up
would lose (commits on no remote, stashes) and learnings waiting for the
daily chore PR; the selected checkout's detail lists the files and
branches. One script per machine reads every checkout (no fetch, so the
upstream column is as of each checkout's last fetch), every 2 minutes and
on `r`; `y` copies the checkout's dir.

In the Mac app, a checkout's right-click menu starts [upkeep](#upkeep)'s
steps on it now, instead of waiting for the next sweep (#172); in the
Android app, the checkout's screen has them as buttons. Each runs on the
hub in the background, takes the machine's one upkeep/reindex slot, and
refuses a checkout with an agent in it. While one runs, the checkout's
state names it and how long it has run ("saving work to a draft PR · 2m";
a sweep's own shows as "upkeep" or "reindexing"), in the TUI, both apps
and `gh-hud workspaces`. Its outcome is the checkout's last upkeep (or
reindex) in the detail pane:

- **Fast-Forward** the default branch (and submodules); nothing else.
- **Open Learnings PR**: the checkout's and its overlays' learnings PRs,
  on a clean checkout on its default branch (`learnings_pr` on).
- **Save Work to a Draft PR**: commits everything but untracked learnings
  on a `wip/` branch (a finished run's own, else
  `wip/<label>/saved-<time>`), pushes it to your fork, opens a draft PR
  from it, and puts the checkout back on its default branch. A default
  branch ahead of upstream is backed up and reset as upkeep does.
- **Keep Up Now**: a full upkeep, acting even when `[upkeep] mode` only
  reports.
- **Reindex Dewey**: the checkout's vault (not with `dewey_reindex =
  "off"`).
- **Discard Uncommitted Changes**: resets tracked edits and deletes
  untracked files, keeping untracked learnings, Dewey's cache, ignored
  files and every commit.

The ones that push, open PRs or throw work away ask first. The agent token can't call
them.

From a shell, `gh-hud workspaces` lists the same rows (`-json` for a
script), and `gh-hud checkout <action> <machine> <checkout>` starts one of
these on a checkout: `pull`, `learnings`, `save`, `upkeep`, `reindex` or
`discard`.
`<machine>` is `local` for the hub; `<checkout>` is its name as the list
shows it (its directory's last two elements, like
`unbound-force@chai/dewey` or `fullsend-ai/agents`), its directory, or its
workspace name or the directory's last element when that names one
checkout. The list cuts a long last upkeep or reindex short; `-json` has
it whole. It doesn't ask first, and it needs the hub's LAN token, not the
agent token.

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
second_opinion = true          # default; needs the auto-question plugin
second_opinion_models = []     # e.g. ["deepseek/deepseek-v4"]; see below
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
- **Second opinion.** With the
  [auto-question](https://github.com/jflowers/opencode-auto-question)
  plugin (0.3.0 or later) in your opencode config, the agent calls its
  `second_opinion` tool before an answer gh-hud may apply without you: a
  case's `link_fix`, or a piece's `retry` verdict. It passes the question,
  the options and only the evidence, never its own reasoning. If the
  second opinion picks another option or isn't confident, the agent
  escalates (or answers `human`) and says what each concluded. gh-hud writes
  `supervisor/opencode/auto-question.jsonc`, which turns the tool on for
  supervisor runs only, with the plugin's guidance pass off for the
  supervisor (your global guidance is written for coding agents). The
  engines, keys and models come from your own auto-question config.
  `second_opinion_models` become the plugin's
  `llm.alternateModels` for the `hud-supervisor` agent, used when the
  plugin's `llm.model` is the supervisor's own model, so the second opinion
  comes from a different model. `second_opinion = false` turns it off.
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

### What a ci run found

A `ci` agent sometimes works out that a failure isn't the PR's to fix: main
is broken, or another PR already fixes it. gh-hud keeps that conclusion as
a finding and acts on it, instead of stopping the PR at "needs you" because
the run changed nothing.

- **From the agent.** A run can end its last message with one line:
  `gh-hud-finding: {"kind": "blocked_by_base", "check": "Build", "why": "main's run fails the same test"}`
  or `gh-hud-finding: {"kind": "fixed_by", "pr": 1031}`.
- **From the supervisor.** With the supervisor on, a `ci` run that pushed
  nothing and left no finding line is read by the supervisor agent. It gets
  the PR's failing checks, what main fails, the other open PRs in the repo
  and the agent's last message. It answers `blocked_by_base`, `fixed_by` or
  `none`. While it reads, the PR shows `▸ci supervisor reading the run`.
- **Checked like a decision.** A `blocked_by_base` must name one of the
  checks the PR was failing; the check can be left out when there was only
  one. A `fixed_by` must name another PR that gh-hud sees open. A refused
  finding is noted on the run and the PR stops as before.
- **What it does.** The PR waits, `▸ci main broken: Build` or
  `▸ci fix in #1031`. It doesn't need you, no ci agent starts, and it doesn't
  merge.
- **When it lapses.** Each run records what it started from: the PR's head,
  main's commit and the failing checks. The finding lapses when any of
  those change, or when the `fixed_by` PR is no longer open. Then the run
  that changed nothing no longer stops the PR, so `ci` can run again. A
  `blocked_by_base` check counts as seen failing on main, so once main
  passes it, the PR is brought up to date with main (no agent), as for a
  shared failure.

The run view (the TUI, the Mac and Android agent detail, and
`gh-hud runs -json`) shows the finding, or why there is none.

### Drafted review replies

With the supervisor on, it drafts replies to the review threads waiting
on you. A draft is never posted unless you approve it.

- **When.** A PR of yours has unresolved review threads opened by a person
  whose last comment isn't yours. gh-hud's own `feedback` step has stopped
  at "needs you" (it ran and changed nothing, or ran out of rounds), or
  `feedback` isn't an auto task. A draft never races the feedback agent.
  The Plan tab lists the PR as a case, `✎ acme/app#12 review threads`.
- **The draft.** The supervisor reads each thread in full, and may read the
  PR and its diff. It drafts the replies you'd post, in the first person.
  It leaves out any thread that needs a code change first or a decision
  only you can make, and says so in its summary. It never says a change
  was made unless the diff shows it.
- **Approving.** The Plan tab shows each reply in full under the comment it
  answers. The PR says `▸you: feedback approve 2 drafted replies?`. `Y`
  posts them as you (the Mac and Android apps: Post as You), and `N` posts
  nothing. On approval, gh-hud reads the threads again. It posts a reply
  only to a thread that is still unresolved and has no new comment since
  the draft, and tells you what it left out. The `act` mode doesn't apply:
  replies always wait for you.
- **When it lapses.** A draft holds for the PR as it was: its head, its
  updated time and its threads waiting on you. When any of these changes
  (a push, a new comment or review), the draft is dropped. A new one is
  drafted if threads still wait. A PR gets one draft per state, whether you
  approve or reject it.

### Epics

An epic is an issue that tracks a larger piece of work done through other
issues. gh-hud reads one as a plan and keeps each piece's status current:

```
gh-hud epic https://github.com/acme/app/issues/100   # enroll it; reading takes a few minutes
gh-hud epic list                                     # plans, pieces and status
gh-hud epic approve acme/app#100                     # approve the current plan
gh-hud epic reread acme/app#100                      # read it again
gh-hud epic clear acme/app#100 #103                  # clear a piece's gate
gh-hud epic finish acme/app#100 [-y]                 # show a finished epic's summary; -y posts it and closes it
gh-hud epic drop acme/app#100                        # stop tracking it
```

These talk to the running hub. The Plan tab shows each epic, its pieces
still to do (running and ready first). `E` approves the plan that waits
for you, `C` clears the gate of the selected piece, and `D` (twice)
finishes an epic whose pieces are all closed.

- **Reading.** gh-hud doesn't count on one format. It merges what it finds:
  - **Prose.** The read-only supervisor agent (see "The supervisor
    agent") reads the body and the issues and PRs it mentions. It proposes
    the pieces, their phases, what each waits on, gates only a person can
    clear (say, "a second refresh proves repetition"), and the rules every
    piece must respect. gh-hud refuses refs the body doesn't mention, PRs
    as pieces, and cycles. A refused answer is retried once, in the same
    agent session, with the reason.
  - **Blocked-by links** between the pieces, from GitHub.
  - **Sub-issues.** Every GitHub sub-issue of the epic is a piece. When
    the body mentions nothing but its sub-issues, the plan is built from
    them and their blocked-by links alone, in GitHub's order, without the
    supervisor (so it works with the supervisor off). When the body
    mentions other issues too, the supervisor reads it as above, is told
    the sub-issues are pieces, and any it leaves out are added. Adding or
    removing a sub-issue counts as an edit (see Edits).
  - **Task lists.** Each checkbox line in the body (`- [ ] #12 the parser`,
    `- [x] acme/lib#4`, nested or not) names a piece: the first issue on
    the line. Lines whose first ref is a PR aren't pieces. When the body
    mentions nothing but its task-list issues and sub-issues, the plan is
    built from them alone, sub-issues first and then the list in order,
    with no supervisor read. Otherwise the supervisor reads the body as
    above, and any task-list issue it neither placed nor skipped on purpose
    is added. The boxes don't matter: a ticked box is still a piece, and
    its status comes from GitHub.
- **Status** comes from GitHub on every refresh, never from the body's
  checkboxes:

  | Mark | State | Meaning |
  |---|---|---|
  | ✓ | done | Closed as completed, or merged. |
  | ✗ | dropped | Closed without being completed. |
  | ▸ | in progress | An open PR closes it. |
  | ▶ | running | An agent is on it and no PR is open yet (`on chai`). |
  | ● | ready | Nothing it waits on is left. |
  | ◌ | waiting | It waits on another piece. |
  | ⏸ | gated | It waits on a condition only you can clear. |
  | ! | blocked | It waits on a piece that was dropped. |
- **Approving** a plan is consent to start its ready pieces, within the
  epic's caps. A plan has an ID, so you approve the plan you saw.
- **Edits.** When the epic's body or its sub-issues change, nothing more
  starts and gh-hud reads it again on its own. The reader gets its previous plan and keeps
  what the edit didn't touch as it was. Then gh-hud compares the new plan
  with the one you approved:
  - **The same,** or **only reworded:** a gate's or a rule's text changed,
    and nothing else. It stays approved, the pieces carry on, and a notice
    lists what was reworded.
  - **Changed:** a piece added or removed, a phase or a wait changed, a
    gate added or removed, or a rule added or removed. It waits for your
    approval again, and the Plan tab, `gh-hud epic list` and the apps show
    what changed (`± #1056 waits on nothing (was #1029)`).

  Rewording is told apart by text: a rule counts as reworded when it
  shares at least half its words with an old one.
- **Progress.** gh-hud keeps an approved epic's progress in one comment on
  the epic, edited in place as pieces move: a tally and a row per piece.
  It isn't part of the plan, and the reader never sees comments. Progress
  written into the body would count as an edit, so piece agents are told
  not to. A progress note that has to be in the body goes in a block the
  reader skips:

  ```
  <!-- gh-hud:status -->
  #938: the unification shipped in #582; only the wording fix remains.
  <!-- /gh-hud:status -->
  ```

  `status_comment = false` under `[epics]` turns the comment off.
- **Gates.** A gated piece waits until you clear its gate
  (`gh-hud epic clear`, or `C` in the Plan tab), saying the condition
  holds. A cleared gate stays cleared across rereads while the piece has
  the same gate.

**Running an approved epic.** Its pieces get rows in the Plan tab, and the
auto pass starts them like any other step:

- **Next.** A ready piece is next (`next free slot · epic acme/app#100`)
  while fewer than `max_active` of the epic's pieces are active. Active
  means an agent is on it or a PR for it is open. Past that, it waits
  (`epic: 2 active (max 2)`).
- **What a piece runs.** The `issue` task. With `/uf.run-pipeline-plan`,
  gh-hud writes a one-item plan file for the piece in `/tmp/gh-hud-epics/`
  on the machine that takes it, and passes that instead of `#N`. The
  item's description carries the epic's title, the piece's phase and the
  epic's rules, so the agent knows the constraints. It also skips the
  command's question about how to group bare issues.
- **Once a PR opens,** the piece leaves the epic's rows and the PR goes
  through the usual pipeline: ci, feedback, review, the merge train. The
  piece is done when its issue closes.
- **When a run leaves no PR.** gh-hud keeps a report of the run: where it
  ran, its status, the agent's last message and why it failed.
  `gh-hud epic list` shows it under the piece. It waits until GitHub has
  been asked about the piece since the run ended, so a PR the run opened
  is seen first. Then, with the supervisor on, it asks the read-only
  supervisor agent for a verdict:
  - **retry:** another run can get it done. The next run starts without
    waiting, and its plan file carries what went wrong and the
    supervisor's hint. It goes to the checkout the earlier run left its
    work in, when that checkout still holds it, and carries on from there:
    nothing is stashed, reset or switched, and the plan file lists what
    was left. Any other task, or another issue, still refuses that
    checkout as dirty. This holds for any `issue` run of the same item,
    not only an epic's.
  - **human:** a person has to act first (a question only you can answer,
    a decision, a permission, an unclear issue). The piece needs you
    (`epic: <why>`). A run you start on it yourself (`a` in the Plan tab),
    or a PR for it, moves it on.

  Without the supervisor, or when its review fails, the next run waits 15
  minutes (`epic: ran, no PR yet`).
- **Holding.** The `hold` label (auto-merge's `hold_label`) or the
  `human-gate` label (`[dispatch] human_gate_label`) on a piece holds it:
  it needs you (`held: the hold label`) and never starts, and its slot
  goes to the next ready piece. On the epic issue itself it holds the
  whole epic: none of its pieces start (`epic held: …`), runs already
  going carry on, and the epic's line says so. gh-hud reads the labels on
  every status check and says once when an epic is held and when it's
  let go; remove the label to let it go.
- **When it stops.** A piece gets `max_runs` runs after approval. Once out
  of runs it's yours (`epic: 2 runs, still no PR`). Gated and blocked
  pieces need you too. A piece that can't start says why once:
  `epic acme/app#100: acme/app#2 didn't start: …`.
- **One at a time in the same area.** The planner gives each piece the
  files or directories it expects the piece to change, when the epic or
  the issue makes that plain (`gh-hud epic list` shows them in brackets). A
  ready piece waits while another piece of the epic is working in an
  overlapping area: one is running, or has an open PR. The row says
  `epic: same area as #12 (internal/index)`. Paths overlap when they're
  equal or one is inside the other, and only within one repo. Once a piece
  has a PR, the PR's changed files replace its expected area. A piece with
  no area never waits. Areas aren't part of the plan you approve, so a
  reread that only changes them keeps your approval.
- **The run budget.** All of an epic's pieces together get
  `max_total_runs` runs after approval (default 20). Once those are used,
  no piece starts. The ready ones need you
  (`epic: run budget spent (20/20 runs)`), runs already going carry on, and
  gh-hud says so once. To grant another budget, approve the plan again: `E`
  in the Plan tab, `gh-hud epic approve <ref>`, or Approve Again in the
  apps. As with any approval, the counts start over, per piece too. The
  epic's line shows the runs used, for example `7/20 runs`.

**Re-planning.** When a piece's run leaves no PR and the supervisor reads
it, the supervisor can also propose a change to the plan:

- **drop**: the piece isn't needed. For example, another PR already did it,
  or another piece made it moot. The piece needs you
  (`epic: approve dropping it? …`). Approving comments on its issue with
  the reason and closes it as not planned, as you, so it reads as dropped.
  Rejecting keeps it, and it needs you.
- **follow-up**: the run turned up new work the epic needs, such as a
  prerequisite or a bug. This can come with any verdict. Approving opens an
  issue for it in the piece's repo, as you, and adds it as a sub-issue of
  the epic. The next read picks it up as a piece. That read keeps the plan
  approved when the follow-ups you approved are all that's new.

Each proposal waits for you. Nothing changes on GitHub without your
approval, and the agent token can't approve. The Plan tab lists the pending
ones under their epic (`Y` approves and `N` rejects when only one is
pending), and so does `gh-hud epic list`, with
`gh-hud epic accept <id>` and `gh-hud epic reject <id>`. The Mac and
Android apps show them with Approve and Reject.

**Finishing.** When every piece of the approved plan is done or dropped,
the epic is finished: nothing is left to run. A piece blocked by a dropped
one isn't finished; it needs you. gh-hud says so once (`epic acme/app#100:
all 4 pieces are finished`) and drafts the comment it would close the epic
with: the tally, each piece done with the PR that did it, and each piece
dropped with why. The Plan tab, `gh-hud epic finish <ref>` and the apps
show that draft. Finishing posts it as you and closes the epic as
completed, so it waits for you: `D` twice in the Plan tab, `-y` on the
command, or Finish Epic in the apps, each for the plan you saw. The agent
token can't. A finish whose close fails can be retried without posting the
summary twice. An epic closed on GitHub stops the same way, without a
summary (`closed on GitHub`). A closed epic stays listed, starts nothing
and isn't checked again; `gh-hud epic drop` removes it.

```toml
[epics]
max_active = 2   # pieces of one epic active at once
max_runs = 2     # runs per piece before it's left to you
max_total_runs = 20  # runs for all of an epic's pieces; then approve again (negative: no limit)
status_comment = true  # keep an approved epic's progress in a comment on it
```

Epics are kept in `epics.json` in the state dir.

### Pruning branches

Agents push PR branches to your fork and open the PRs upstream. GitHub
deletes neither copy once the PR is done: the upstream repo's "delete head
branches" skips forks, and git never deletes a local branch. So gh-hud
does, in the dispatch checkouts (instances, on the hub and the workers).
A local branch is pruned when:

- a merged or closed PR on the checkout's upstream repo has it as head,
  from the checkout's `origin` repo, and no open PR does;
- its tip is the PR's last head or an ancestor of it, so no local commit
  is lost;
- it isn't checked out in any worktree, isn't the default branch, and no
  run is live in the checkout.

The forks the checkouts push to (their `origin`, when you own it) are
swept on GitHub as a whole, so a branch no checkout has anymore goes too:
a fork branch is deleted when its PR on the parent is merged or closed (no
open PR), it still points at the PR's last head, and no checkout has it
checked out. The daily `chore/dewey-learnings-…` branches follow the same
rule.

The sweep also runs `git worktree prune` in each idle checkout: a worktree
an agent made and whose folder is gone leaves a record that keeps its
branch checked out (so it can't be pruned); git drops those records, and
nothing else.

The same sweep keeps each fork's default branch in step with its parent's:
it fast-forwards it when the fork has no commits of its own there, and
leaves alone a fork that has. `gh-hud branches` lists those forks; the
hub's notice after a sweep only reports what it pruned or synced, and what
failed, so a fork kept for the same reason every time isn't announced
every 6 hours.

```sh
gh-hud branches          # what would be pruned or synced (-v for each branch)
gh-hud branches -apply   # prune them and sync the forks
```

```toml
[branches]
prune = true        # the hub prunes on its own every 6 hours (default off)
sync_forks = true   # and fast-forwards the forks' default branches (default off)
```

### Upkeep

Fleet checkouts used to catch up with upstream only when a dispatch's
preflight ran in them, so idle ones drifted: behind by hundreds of
commits, Dewey indexes describing old trees, and drifted submodules
reading as dirty. The hub keeps them current now, fast-forward only.

After each Workspaces read (every 2 minutes), the dispatch owner keeps up
each idle fleet checkout it hasn't kept up in the last hour, one checkout
at a time per machine. A Dewey pass or rollout keeps a checkout up just
before it reindexes it too, so the index describes the current tree.

On the workers, upkeep also keeps the fleet orgs' plain ghws dirs current
(`~/Projects/github/<org>/<repo>` next to the `<org>@<label>` instances,
for each org in `orgs`): ghws clones new instances from them, so a stale
plain dir means a stale new instance. They get the same upkeep as a fleet
checkout, backups included (their branches are named for the org, e.g.
`backup/<org>/<default>-<time>`), but no agent ever runs in one: they are
never dispatch targets, and the Workspaces tab lists them as `upkeep
only`, never ready. The hub's plain dirs are its user's, and gh-hud
leaves them alone.

Each upkeep:

1. fetches the remote the checkout syncs from (`upstream`, else
   `origin`);
2. puts Dewey's cache churn back (see above);
3. on another branch with nothing left (no uncommitted files, no commits
   no remote has), switches back to the default branch;
4. on the default branch, fast-forwards it and brings submodules along
   (the same rule as preflight); on a branch with work, moves only the
   local default-branch ref;
5. with `learnings_pr` on and the checkout clean on its default branch,
   opens the learnings PRs a clean run would (the checkout's and its
   overlays'), so learnings in a checkout no run uses again still go
   upstream; the day's-branch rules are the same, and an open PR waits;
6. records what it did and what it left as the checkout's last upkeep,
   shown in the Workspaces tab's detail pane (`upkeep failed` in the row
   when it failed).

It also clears what keeps a checkout from current, never losing
anything (#158):

- **A finished run's leftovers** (uncommitted files, commits no remote
  has) wait a day for a retry of the same item, which resumes there.
  After that, or as soon as the item is closed or merged, upkeep commits
  everything on `wip/<label>/<task>-<num>-<started>`, pushes it to
  `origin` (your fork), and puts the checkout back on its default branch.
  The run's record keeps where the work went, and a later dispatch of the
  item is told.
- **A default branch with commits upstream doesn't have** is pushed to
  `backup/<label>/<default>-<time>` on `origin` and reset to upstream's.

Branch pruning (`[branches] prune`, `gh-hud branches`) deletes these
branches from the fork again once they're no longer needed: a `wip/`
branch as soon as its item is closed or merged, and either kind after
`keep_backups` days, counted from the time in its name. One a checkout has
checked out stays.

Untracked learnings and Dewey's cache stay. Work no gh-hud run recorded
(someone working by hand) is left as it is. Backups only ever go to a
fork: a checkout without an `upstream` remote, or whose `origin` is the
upstream repo, is left as it is. A push that fails changes nothing, and
the failed upkeep raises an alert (once per checkout and error).
Everything else upkeep leaves shows as "left" in the last upkeep. Like a
reindex, an upkeep holds its machine's one maintenance slot (so upkeeps
and Dewey reindexes never overlap on a machine) and marks the checkout
busy, so nothing is dispatched there meanwhile, and it never touches a
checkout with a live agent or a held `dewey.lock`.

```toml
[upkeep]
mode = "auto"      # "report" (default): fetch and record what it would do (backups too); "off": nothing
keep_backups = 14  # days branch pruning keeps upkeep's wip/ and backup/ branches on the forks
```

#### Removing what isn't the fleet's

A worker can carry checkouts nothing uses: other orgs' workspaces
(`jflowers/`, `Opencode-DCP/`) next to the fleet's instances. With
`remove_unmanaged = true` on a `[[machines]]` entry, that worker's upkeep
sweep (at most hourly) removes every directory under its `ghws_root`
except:

- fleet instances (`org@label` of an org in `orgs`);
- the fleet orgs' plain dirs (`unbound-force/`), which ghws clones new
  instances from;
- the machine's `keep` paths: a workspace dir, or one repo in it (for
  example an opencode plugin the machine loads from a checkout);
- hidden entries and plain files.

A repo is removed only when it, and every repo under it, holds nothing
that exists nowhere else: no uncommitted files (Dewey's cache churn aside)
and no commits, stashes or tags no remote has. A repo that does, or a dir
that isn't a git repo, is kept, and the upkeep alert names it. One a process
is working in waits for the next sweep. A workspace dir goes once nothing
in it is kept. `[upkeep] mode` applies: `report` lists what it would remove,
`auto` removes it. The Workspaces tab shows each such worker's last
removal as an `unmanaged` row.

The hub never removes anything. It isn't a `[[machines]]` entry, the switch
is refused for a loopback `ssh`, and the removal refuses to run on a host
named like the hub.

```toml
[[machines]]
name = "lap1"
remove_unmanaged = true
keep = ["Opencode-DCP/opencode-dynamic-context-pruning"]
```

## Configuring dispatch

Dispatch is configured in three layers, all in `config.toml`:

| Layer | Where | Decides |
|---|---|---|
| Tasks | `[dispatch.<task>]` | what each task runs (`command`), whether it starts itself (`auto`), and for which orgs and repos (`orgs`, `repos`) and items (`mine`); `[[dispatch.issue.by_label]]` gives labelled issues their own command |
| Machines | top level (the hub) and `[[machines]]` (workers) | where agents may run, how many at once, which tasks and which labelled items each machine accepts, and how agents run there |
| Checkouts | ghws `@` instances on each machine | which repos a machine can take work for at all |

Task settings are global: one `command`, `auto`, `orgs`, `repos` and `mine` per task, used
on every machine. Per-machine control is the `[[machines]] tasks`
allowlist; per-org and per-repo control of auto-dispatch is the task's `orgs` and `repos` lists. A
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

`orgs` limits `auto` to repos owned by the listed orgs or users, and
`repos` to single `owner/name` repos (both case-insensitive). A repo is
covered when its owner is in `orgs` or it is in `repos`; with both empty
or unset, every repo is. To auto-review one org whole and only two repos
of another: `orgs = ["unbound-force"]`, `repos = ["fullsend-ai/fullsend",
"fullsend-ai/agents"]`.

`mine = true` limits `auto` to items that are yours: PRs you opened, and
issues assigned to you (not ones that only mention you). `feedback` only
ever triggers on your PRs, so it needs no `mine`; on `review` it means
auto never fires, since auto-review covers only others' PRs.

`orgs`/`repos` and `mine` combine (both must match), and none limits `a`: you
can still dispatch any task by hand on any PR or issue.

### Machines

The hub is configured at the top level: `max_concurrent` (default 2) is its
slot count, it accepts every task, and top-level `labels` and
`exclude_labels` are its label rules (below). Each `[[machines]]` entry
adds a worker reached over ssh:

| Key | Default | Meaning |
|---|---|---|
| `name` | required | herdr machine label (`herdr machine add`); `local` is reserved for the hub |
| `ssh` | `name` | ssh target, when it differs from the label |
| `max_concurrent` | 2 | agents gh-hud runs there at once |
| `ghws_root` | `~/Projects/github` | ghws root on that machine |
| `tasks` | all | allowlist of tasks the machine accepts, e.g. `["review", "ci"]` |
| `labels` | any | the machine only takes PRs and issues with at least one of these labels |
| `exclude_labels` | none | the machine never takes a PR or issue with any of these labels |
| `enabled` | true | `false` takes the machine out of placement and polling; fleet rollouts still reach it (a drain asks its herdr directly) |
| `agents` | true | `false` says the machine runs no agents and has no herdr (a services box); a fleet drain counts it idle without asking herdr. Needs `enabled = false` |
| `dispatch_mode` | top-level | `run` (headless) or `tui` for agents on this machine |

Placement ranks the machines that qualify for a dispatch (enabled,
reachable, task allowed, label rules accepting the item, a free slot, not
draining for a fleet rollout, an idle checkout of the repo) by free-slot
fraction, hub first on ties, then config order. A PR or issue never runs on
two machines at once.

Label rules match the item's GitHub labels, ignoring case (both PRs and
issues carry theirs, up to 20; an epic piece outside the refresh uses
the labels its epic's status check last read, and while none has read
them it waits with `epic: labels unknown …` if any machine has label
rules). `exclude_labels` wins over `labels`; a
label in both lists is a config error. An item no machine's rules take
doesn't start: the Plan (`p`) shows its auto step as waiting, and a queued
one with the reason, e.g. `no machine takes label opencode-v2-migration:
local excludes opencode-v2-migration, lap1 excludes
opencode-v2-migration`; the `a` menu, `gh-hud choices` and
`gh-hud dispatch` say the same. When a machine takes it by label but is
busy, the reason lists each machine (`lap1 excludes …, lap2 full (4 of 4
running)`). A dispatch at one named machine whose rules refuse the item is
refused with the rule. The rules reload live with the config.

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

**A different command for labelled issues.** Some issues ask for a
different workflow, not a feature: a scheduled `dewey-distill` issue should
run `/dewey-distill-auto`, not the implementation pipeline. Per-label rules
on the `issue` task replace its command, and can turn `auto` on for just
those issues:

```toml
[dispatch.issue]
command = "/uf.run-pipeline-plan #{num}"   # ordinary issues: on `a` only

[[dispatch.issue.by_label]]
label = "dewey-distill"                    # matched case-insensitively
command = "/dewey-distill-auto #{num}"
auto = true                                # optional; else the task's auto
```

The first matching rule wins, in config order. `orgs`, `mine` and
`max_rounds` still come from `[dispatch.issue]`. The Plan tab names the rule
on the row (`next free slot · dewey-distill`), and the agent menu shows the
command that will run. Rules on other tasks, or without a label or command,
stop the config from loading, naming the entry. gh-hud also searches your
`orgs` for open issues carrying each rule's label, assigned or not, since a
scheduled workflow opens its issue as `GITHUB_TOKEN`, which can't assign
you. With no `orgs` it doesn't: that search would cover all of GitHub, and
anyone could label an issue into your agents. Leave `mine` off
`[dispatch.issue]` for an unassigned labelled issue to start itself.

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

**Machines by label (a mixed opencode fleet).** While the fleet runs two
opencode majors, send v2-migration issues only to the machine that runs
v2 verification, and keep them off the others (the hub included):

```toml
exclude_labels = ["opencode-v2-migration"]   # hub (top level)

[[machines]]
name = "lap2"
labels = ["opencode-v2-migration"]           # only takes items with one of these

[[machines]]
name = "lap1"
exclude_labels = ["opencode-v2-migration"]   # never takes items with these
```

The header and the Agents tab show which opencode each machine runs.

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

- you or Dependabot opened it, and it's in a covered repo
- it's not a draft, has no `hold_label` or `human_gate_label` label and has
  no merge conflict
- the repo allows auto-merge
- the base branch's protection or rulesets require at least one approval
- no agent is working on it and no agent step is due (merge is the
  [pipeline's](#coordination) last step; turning auto-merge off is never held)
- its last gh-hud run didn't fail or leave work behind (unpushed commits,
  uncommitted changes); a later clean run or a re-inspection clears that

The last check is the safety net: on a branch with no such rule, auto-merge
has nothing to wait for. gh-hud also doesn't use `gh pr merge --auto`, which
merges on the spot when nothing is pending.

**Learnings PRs** (gh-hud's own `chore/dewey-learnings-…` branches) are
chores, so no agent reviews them and nothing would approve them. In
`unbound-force`, gh-hud approves one as `uf-divisor` itself, with no agent,
once every changed file is under `.uf/dewey/learnings/` (added or modified,
not renamed in). The approval names the head commit it checked, so a new
push gets a new one. A held, human-gated, drafted or conflicted learnings PR
isn't approved, and a PR on that branch that changes anything else is left
to you, reported once. The token comes from `agent-env`, as for a bot review.

**Dependabot PRs** get a real `uf-divisor` review through the Queue, then
auto-merge like yours, for every bump size. They stay out of the merge
train, since a push to Dependabot's branch stops Dependabot managing it:
when the branch rules hold one as behind its base, gh-hud comments
`@dependabot rebase` instead, once per head commit.

**Cancelled check runs.** A workflow with cancel-in-progress concurrency can
start two sets of runs on one push and cancel one of them. gh-hud reads a
cancelled run as passing, but GitHub counts it as failing, so the PR sits
`BLOCKED` with auto-merge on and green checks, and at the head of a merge
train it stops the whole repo. When one of the PRs gh-hud merges is
approved, up to date, `BLOCKED`, has nothing failing or running, and has
cancelled runs on its head, gh-hud re-runs those workflow runs, once per
head commit, and says so in its notices.

A PR that's already approved and `CLEAN` when gh-hud first sees it has nothing
left to wait for, so GitHub refuses auto-merge and gh-hud merges it directly.
Every call names the head commit it checked, so a newer push makes GitHub
refuse it.

To stop a PR, add the hold label or convert it to a draft: gh-hud turns
auto-merge off at the next refresh, so a merge can still land before then.
A run that fails or leaves work turns it off the same way.

A PR or issue a person must handle (a Dewey curation PR, say) gets the
`human-gate` label (`human_gate_label` at the top level of the config to
rename it). gh-hud then sends it no agent of any task, by hand, from your
queue or automatically, shows it as `needs you: human gate`, and never
merges it. `hold` only means "don't merge yet": agents still work on it.
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
next poll. After the local apply succeeds, a banner says gh-hud restarts
into the new binary once idle (see "The hub"). With `self_restart = false`
it reminds you to restart it.

When an apply changes the machine's Dewey binary (`mise which dewey`,
else `command -v dewey`, compared before and after; mise's install path
carries the version), the machine goes `applying` → `reindexing 7/15` →
`verifying`: still drained, it checks every Dewey vault in its ghws `@`
instances, plus the base checkouts that have a `graph.db`, with the new
Dewey, and reindexes the ones the check flags (unindexed, or Dewey
recommends a reindex because the new Dewey writes a newer index format),
one at a time, under the same rules as the automatic reindex (busy
`reindexing`, none with a live agent, leftovers or a held `dewey.lock`).
A pin bump that doesn't change the index format reindexes nothing. A
vault that fails, is skipped or can't be checked is listed under the
machine in the fleet view and notified once, and the rollout goes on.
This follows `[dispatch] dewey_reindex`: only `"auto"` reindexes;
`"report"` (the default) checks the vaults, says how many need a
reindex and touches nothing; `"off"` doesn't look. When the binary
can't be read before or after the apply, the view says the change is
unknown and nothing is reindexed. The hub is still last, so its own
checkouts are reindexed after its own apply.

Once a machine's apply verifies, and while it is still drained, gh-hud
runs `mise prune --yes` there from the home directory (#86). Each pin
bump leaves the old version installed, so without it a machine piles up
dozens of versions and several GB. mise removes only versions that no
tracked config or tool stub selects, and keeps any a running process
was started from, so a checkout's own pins and the hub's running gh-hud
keep theirs. The fleet view shows what it did under the machine ("mise
prune: removed 12 versions, freed 2.7 GB"). A machine without mise, or
a prune that fails, is only a note there: the rollout goes on and
nothing is notified.

`F` opens the fleet view: per-machine table (name, reachable, slots,
applied vs target sha, state, drain wait, last error) with target/CI/pause
status in the header. Keys: `p` pause/resume · `a` apply now (force a
poll) · `s` skip the drain wait (press twice — it applies with live agents
still running) · `r` retry the failed machine and resume · `↑`/`↓`
select a machine · `d` reindex its Dewey vaults that need it, `D` on every
machine (press twice, or `f` for a forced pass of every vault; see
"Reindex the vaults now") · `x` cancel its
pass · `F`/`esc` close. The main header carries a compact fleet chip while a rollout is
active or failed.

## Toolkit scaffolds

A tool release (uf, replicator, gaze, dewey) reaches the machines through the
fleet rollout above, once its mise pin is bumped. The files each tool writes
*into* a repo with its `init` command come from the binary, so they only
change when someone re-runs init at the new version and lands the result.
`gh-hud toolkit scaffold` does that for every repo in a manifest (issue #74;
the rest of the rollout story is epic #78).

```sh
gh-hud toolkit list                          # tools, fleet pins, targets
gh-hud toolkit scaffold --dry-run            # every tool at its fleet pin
gh-hud toolkit scaffold uf@0.22.0 --repo unbound-force/dewey
```

For each target, it:

1. Adds a scratch worktree (under the state dir's `toolkit/`) of the
   canonical ghws clone at `upstream`'s default branch. Agents' `@`
   instances and the clone's working tree are never touched.
2. Runs each tool's init there in manifest order, with the binary at the
   exact version (`mise install` then `mise which --tool`), not whatever is
   on `PATH` or pinned by the repo. A version defaults to the fleet pin in
   `~/.config/mise/config.toml`.
3. If anything changed, it commits on `chore/toolkit-<tool>-<version>…`,
   pushes to `origin` (your fork), and opens one PR on the repo for all
   the tools. When the change touches agent commands or definitions
   (`hold_paths`), the PR gets the `hold` label (created if missing), so
   auto-merge waits for your look. Otherwise the usual pipeline takes it:
   bot review, auto-merge and the `ci` task.
4. An **overlay** target (a private uf stealth mirror, such as fullsend-ai's)
   gets the commit pushed straight to its default branch, or to the chore
   branch when held, for you to merge. When that remote is on GitHub, a
   held branch also gets a PR (labelled `hold`) to look at and merge.

It refuses to go backwards. Commits carry `Toolkit-Scaffold: <tool>
<version>` trailers, and PR titles name the versions. A version older than
the newest open or merged one is refused. So is a change that deletes far
more than it adds (over 500 lines and more than twice what it adds), unless
every tool run is newer than what the repo holds, since then the shrink is
the release's own change (gaze 2.4.0 cut its reporter agent by 500 lines).
What the repo holds is the version gh-hud last scaffolded there, else the
fleet's pin before this run (a rollout passes the pin it replaces). A shrink
with nothing saying the repo is older usually means it was scaffolded from
something newer (`--allow-shrink` lets that through). An existing chore branch is reported
with its PR, not redone. The exit code is 1 when any target failed or was
refused.

The manifest is `~/.config/gh-hud/toolkit.toml`:

```toml
hold_paths = [".opencode/command/", ".opencode/commands/", ".opencode/agent/", ".opencode/agents/"] # the default
hold_label = "hold"                                    # the default

[[tool]]                       # runs in this order
name = "uf"
release = "unbound-force/unbound-force"                # release links, and its own repo
mise = "go:github.com/unbound-force/unbound-force/cmd/unbound-force"
bin = "unbound-force"
init = ["init"]                                        # the default

[[target]]
repo = "unbound-force/dewey"
tools = ["uf", "replicator", "gaze", "dewey"]
args.gaze = ["--ci", "--gaze-version", "{version}"]    # {version} is the release tag

[[target]]
overlay = "fullsend-ai/.uf/fullsend"                   # relative to the ghws root
name = "fullsend-ai/fullsend (overlay)"
tools = ["uf"]
args.uf = ["--lang", "go"]
```

A tool never scaffolds its own repo: a target whose `repo` is the tool's
`release` skips that tool and runs the rest. The tool's repo holds the
source of what its init writes. gaze, for one, checks that its `.opencode`
copies match its embedded assets, so a scaffold there would only break
that check. In the example, `dewey` skips `unbound-force/dewey`.

A tool without `mise` and `bin` (an opencode plugin) only has a pin. Name
the file that pins it in the dotfiles repo, and the pin itself:

```toml
[[tool]]
name = "auto-question"
release = "jflowers/opencode-auto-question"
npm = "opencode-auto-question"          # latest version from npm, not GitHub releases
pin_file = "dot_config/opencode/opencode.jsonc.tmpl"
pin = "opencode-auto-question@{version}"
applied_file = "~/.config/opencode/opencode.jsonc"
```

Mise tools default to `pin_file = "dot_config/mise/config.toml"`,
`pin = '"{mise}" = "{version}"'` and `applied_file = "~/.config/mise/config.toml"`.

### Toolkit rollouts

With a manifest in place, the hub also rolls releases out (issue #75; epic
#78). `T` opens the toolkit view: each tool's pin on the dotfiles branch,
the version on the hub's machine (the fleet's last), the latest release
(`⬆` when it's newer), and how many targets carry the pin. Below are the
rollouts, with the selected one's PRs.

Select a tool with a newer release and press `u` twice. The hub then:

1. **Pins it.** It opens a dotfiles PR that bumps the pin, on `[fleet]`'s
   repo and branch, cut in a scratch worktree of the chezmoi source. You
   merge it, or auto-merge does once it's approved. The fleet coordinator
   rolls it out to the machines.
2. **Scaffolds.** Once the pin merges, it runs `gh-hud toolkit scaffold` for
   that tool on every target: one PR per repo, held when agent commands or
   definitions change. A plugin has no scaffold step.
3. **Tracks.** It follows the scaffold PRs and the hub's applied pin. The
   rollout is done when every PR merged or closed and the hub has the pin.
   Failed and refused targets, held PRs and failing CI show as waiting on
   you.

**New releases are proposed.** Each status check (every 30 minutes, or `x`)
looks at every tool's latest release. One newer than the fleet's pin
becomes a proposed rollout (`◆`) in `T`, with its release notes, and a
notification on the Mac and the phone. Nothing starts until you approve it:
`a` twice on the proposal (or `u` twice on the tool). `d` declines it, and
that release isn't proposed again; `u` still rolls it out. A newer release
replaces an older proposal, and a proposal goes once the pin catches up
some other way. Nothing is proposed for a tool while it rolls out.

Other keys: `c` cancels a rollout (its PRs stay open), `R` retries a failed
or cancelled one (scaffolding only the targets that failed), `d` dismisses a
finished one, `o` opens the PR waiting on you (a proposal's release, or a tool's releases), and `x`
checks releases and versions now (otherwise every 30 minutes). Rollouts
survive restarts (`~/.local/state/gh-hud/toolkit.json`), and notifications
mark the pin PR, the merge, the finish and failures. The same is on the
command line:

**In the apps.** The Mac app's **Toolkit → Tools** pane and the phone's
**Tools** place show the same: each tool's pin and latest release (◆ when
one waits for you; open it for the release notes, **Approve** and
**Decline**), and the rollouts, each with its repos (landed, open, held,
refused, failed) and a link to its PR. **Roll Out** starts a tool's latest
release; **Cancel**, **Retry** and **Dismiss** act on a rollout; **Check
Now** is `x`. A repo that waits on you (held, refused, failed, or failing
CI) is also in the list of what needs you, under "Tool rollouts waiting on
you", and notifies; it opens its PR, or the Tools view when there is none.
So does a rollout's open pin PR ("Merge the uf 0.22.0 pin"), since nothing
goes on until you merge it; the row opens that PR.

```sh
gh-hud toolkit status
gh-hud toolkit rollout uf          # the latest release; or uf@0.22.0
gh-hud toolkit approve|decline uf@0.22.0   # a proposed release
gh-hud toolkit cancel|retry|dismiss uf@0.22.0
gh-hud toolkit check
```

The toolkit runs when `~/.config/gh-hud/toolkit.toml` exists at hub start:

```toml
[toolkit]
enabled = true                              # unset: on when the manifest exists
manifest = "~/.config/gh-hud/toolkit.toml"  # the default
dotfiles = "~/.local/share/chezmoi"         # the default: a clone of [fleet]'s repo
```

## Config

`~/.config/gh-hud/config.toml` (all optional; `--config` picks another path).
With no `orgs`, searches cover all of GitHub.

The hub checks the file every few seconds (and on `r`) and applies a
change without a restart, saying what changed:
`config reloaded: supervisor.enabled false→true, max_concurrent 2→3`.
- **Needs a restart:** `ghws_root`, `runner`, `[hub]` and `[fleet]`. A
  reload keeps their old values and says so until you restart the hub.
  Runs already on a machine you remove carry on; it just gets no new
  work.
- **A file that doesn't load** (a typo, a bad value) leaves the hub on
  the config it has. The error shows in the dashboard's footer, the Mac
  menu and the serve log until you fix it.
- **Keys nothing reads** are warned about the same way. A `[table]`
  header written above top-level settings turns them into the table's
  keys, so put top-level settings first.
- **The dashboard's own display settings** (`url_click`, `ref_click`, and
  the like) are read when it starts.

```toml
orgs = ["my-org", "another-org"]  # limit PR/issue searches to these orgs; also the fleet: gh-hud only manages their org@label checkouts
refresh_secs = 120         # auto-refresh interval
limit = 100                # queue-sweep PRs to read (others' PRs, the costly search); your own sections read up to 300
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
# labels = ["big"]                          # hub: only items with one of these labels (empty = any)
# exclude_labels = ["opencode-v2-migration"] # hub: never items with any of these
busy_window_secs = 900     # foreign session newer than this blocks dispatch
dirty = "refuse"           # dirty checkout policy: refuse | stash | stash-tracked
learnings_pr = true        # chore PR of untracked dewey learnings after a clean run (one open at a time per checkout and overlay)
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
# labels = ["opencode-v2-migration"]          # only items with one of these labels (empty = any)
# exclude_labels = ["opencode-v2-migration"]  # never items with any of these; case-insensitive

[dispatch]
dewey_reindex = "report"   # after a clean run, a Dewey index that needs a reindex: report | auto (reindex when idle) | off (also refuses `gh-hud dewey reindex`)
dewey_reindex_timeout = "2h"  # one vault's reindex; the machine kills dewey past it (CPU-only embedding is slow)

# dispatchable tasks, one table each (see "Configuring dispatch" above).
# All five are built in with these commands; a table only needs the keys
# it changes. command: slash-command template ({num} = PR/issue number).
# auto: dispatch on refresh, no keypress. orgs: limit auto to these repo
# owners; repos: and these owner/name repos (both empty = every repo).
# mine: limit auto to your PRs and issues assigned to you. None limits a
# keypress dispatch.
[dispatch.rebase]          # any listed PR with a merge conflict
command = "/uf.rebase-pr {num}"
auto = false
orgs = []
repos = []
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

# [[dispatch.issue.by_label]]  # labelled issues run their own command
# label = "dewey-distill"      # first match wins; case-insensitive
# command = "/dewey-distill-auto #{num}"
# auto = true                  # optional; overrides the task's auto

# Turn on GitHub auto-merge for your PRs (see "Auto-merge" above).
[auto_merge]
enabled = false            # run it at all
orgs = []                  # repo owners covered
repos = []                 # single owner/name repos covered
method = "squash"          # squash, merge or rebase
hold_label = "hold"        # a PR with this label stays open
update_method = "merge"    # merge train's branch update: merge or rebase

# Keep idle fleet checkouts current, fast-forward only (see "Upkeep" above).
[upkeep]
mode = "report"            # off, report (record what it would do) or auto
keep_backups = 14          # days before branch pruning deletes upkeep's wip/ and backup/ fork branches

# The Mac app's Releases pane (see "Mac app" above): what jflowers/ship
# releases and where each repo stands. Off unless manifest is set.
[releases]
manifest = "jflowers/ship" # ship's repos.yaml: owner/repo, or owner/repo/path
poll = "15m"               # how often the hub reads it all again (the default)

# How apps other than the TUI reach the hub (see "The hub" above).
[hub]
listen = "127.0.0.1:0"     # loopback only; "" turns TCP off
tailnet = ""               # join your tailnet as this device name, for the phone
lan = ""                   # e.g. "0.0.0.0:7377": the Mac app on other Macs on your network
agent_token = false        # with lan: also take a limited agent-token (no approvals or gates)

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
make proto-breaking  # check proto/ still works with clients built from master
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

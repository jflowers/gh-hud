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
`r` refresh · `a` agent actions · `6` Agents tab · `9` Workspaces tab ·
`F` fleet rollout · `1-8` filter section · `0` all · `/` search · `?` help · `q` quit.

## Sections (header tabs, keys 1-8)

- **Review** — open PRs in your orgs where your review is requested or you're assigned
- **Mine** — your open PRs in your orgs (set `my_prs_everywhere = true` for all of GitHub)
- **Issues** — open issues assigned to you or mentioning you
- **CI** — failing checks on open PRs, plus failed runs on `watch_repos`
- **Inbox** — unread notifications (mentions, comments, review requests, CI activity)
- **Ready** (7) — your PRs that are done on your side and wait only on a human
  maintainer: not draft, checks green or absent, no conflicts, no unresolved
  threads, not yet approved, you acted last. Marked `◆` green; a macOS desktop
  notification fires when a PR newly enters this state (`notify = true`).
- **Queue** (8) — other humans' PRs ready for an agent review: updated within
  `queue_window_hours`, checks settled, no conflicts, author acted last
  (requires `orgs`; the sweep is disabled without them). With
  `[dispatch.review] auto = true` these drain automatically: the dispatcher
  reviews queued PRs as agent slots free up, retrying on later refreshes when
  busy, and skips a PR until it changes (fingerprint dedupe).

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

## Agent dispatch

`a` on a row proposes dispatching an autonomous opencode session against a
ghws checkout of that repo (workspace root: `$GHWS_ROOT`, default
`~/Projects/github`; instance whose branch matches the PR's head branch is
preferred). The offered task follows the row's signals: merge conflict →
rebase, failing CI → fix, changes requested on your PR → address feedback,
review requested → review, issue → implement. Your own PRs also offer
review on request (never auto-dispatched; `/uf.review-pr` posts the
verdict as a comment, since GitHub rejects approving your own PR). The
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

Sessions run headless, so the configured `agent` (default `autonomy`, from
[opencode-auto-question](https://github.com/jflowers/opencode-auto-question))
must not depend on a human: under `opencode run` a permission `ask` is
auto-rejected and ends the run, while `deny` returns an error the agent can
work around. The `build` agent inherits interactive `ask` rules.
Dispatch panes also export `OPENCODE_PERMISSION` with `external_directory`
denied outside the temp dirs. opencode merges it over the config files, so
it reaches subagents the dispatched agent starts (review-council reviewers
don't inherit the autonomy agent's rules) and an outside path is an error
rather than a prompt; sessions you start yourself keep the config's `ask`.

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
unreachable or disabled). Tasks with `auto = true`
dispatch themselves on refresh without a keypress; the review queue
(`8`) drains itself this way up to `max_concurrent` agents.

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
records and working/blocked herdr agents to hit zero, notifying once after
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
queue_window_hours = 48    # review queue sweep: others' PRs updated within this window

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

# dispatchable tasks, one table each (all five are built in with these
# commands; a table only needs the keys it changes, so `auto = true` alone
# keeps the default command). command is a slash-command template ({num} =
# PR/issue number). auto = true dispatches on every refresh, no keypress,
# for each row that triggers the task, at most once per item state (a
# ref is retried 30 minutes after its last start, or when it changes).
[dispatch.rebase]          # any listed PR with a merge conflict
command = "/uf.rebase-pr {num}"
auto = false

[dispatch.ci]              # any listed PR with failing checks
command = "/uf.fix-ci {num}"
auto = false

[dispatch.feedback]        # your PRs: changes requested or unresolved threads
command = "/uf.address-feedback {num}"
auto = false

[dispatch.review]          # Queue (8): others' PRs ready for review; also
command = "/uf.review-pr {num}"  # offered on your own PRs, never auto there
auto = false

[dispatch.issue]           # issues assigned to you or mentioning you; auto
command = "/uf.run-pipeline-plan #{num}"  # here implements every listed issue
auto = false

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

## Attribution

The PR enrichment logic (CI/review/thread/last-actor rules, bot masking, row
colors) is a Go port of Ralph Bean's `pr-list.sh`, shared with me by Adam
Scerra as part of his `pr-term` package. Thanks to both.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE).

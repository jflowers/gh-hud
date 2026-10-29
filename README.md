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
`r` refresh · `a` agent actions · `6` agent sessions · `1-5` filter section ·
`0` all · `/` search · `?` help · `q` quit.

## Sections (header tabs, keys 1-5)

- **Review** — open PRs in your orgs where your review is requested or you're assigned
- **Mine** — your open PRs in your orgs (set `my_prs_everywhere = true` for all of GitHub)
- **Issues** — open issues assigned to you or mentioning you
- **CI** — failing checks on open PRs, plus failed runs on `watch_repos`
- **Inbox** — unread notifications (mentions, comments, review requests, CI activity)

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
review requested → review, issue → implement. The dispatcher never assumes
workspace state: it refuses busy checkouts (a live herdr agent or a foreign
opencode session updated within `busy_window_secs`), applies your `dirty`
policy to uncommitted files, and fast-forward syncs the default branch to
`upstream` before launching.

By default (`runner = "herdr"`) dispatch goes through a local herdr
server: one herdr workspace per ghws workspace (created lazily, reused,
never closed), one tab per dispatch labeled `<task> <repo>#<num>`, agent
run in that tab's pane. If the server can't be reached, dispatch falls
back to a detached local `opencode run --command …` with the configured
agent. Set `runner = "local"` to skip herdr entirely.

`6` lists opencode sessions across workspaces: `enter` watches one (herdr
sessions focus the workspace and attach the herdr TUI in a Ghostty window
via `herdr_watch_cmd`; local ones tail the session log via
`agent_watch_cmd`), `y` copies the session id. Live agent state shows in
the attention slot: `●` teal = running, `◆` orange = blocked on a
question or approval, `◆` red = dispatch failed. Tasks with `auto = true`
dispatch themselves on refresh without a keypress.

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

# agent dispatch (see "Agent dispatch" above)
ghws_root = "~/Projects/github"  # ghws workspace root ($GHWS_ROOT default)
agent = "build"            # opencode agent for headless dispatch
runner = "herdr"           # dispatch runner: herdr (default) | local
max_concurrent = 2         # max simultaneously dispatched agents
busy_window_secs = 900     # foreign session newer than this blocks dispatch
dirty = "refuse"           # dirty checkout policy: refuse | stash | stash-tracked
agent_watch_cmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e tail -n 200 -f {log}"
herdr_watch_cmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e herdr"  # {workspace} {pane}

# dispatchable tasks: command is a slash-command template ({num} = PR/issue
# number). Tasks: rebase, feedback, review, ci, issue.
[dispatch.rebase]
command = "/uf.rebase-pr {num}"
auto = false               # true = auto-dispatch on refresh, no keypress
```

## Attribution

The PR enrichment logic (CI/review/thread/last-actor rules, bot masking, row
colors) is a Go port of Ralph Bean's `pr-list.sh`, shared with me by Adam
Scerra as part of his `pr-term` package. Thanks to both.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE).

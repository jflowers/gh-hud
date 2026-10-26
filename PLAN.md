# gh-hud — approved plan (implementation record)

Goal: a GitHub attention dashboard for iTerm2 — PRs, issues, Actions runs,
comments/notifications needing attention — with click-to-open and
modifier+click-to-copy.

Decisions (approved by Jay):

- **Approach**: Go TUI (Bubble Tea + Lipgloss), single binary, no tmux.
- **Scope**: orgs configurable (empty = all of GitHub); "my PRs" optionally
  everywhere.
- **Sections**: PRs needing my review, my PRs, my issues/mentions, Actions
  runs, comments/notifications — unified into one attention-ranked list.
- **Copy formats**: ⌘-right-click → URL; ⇧-click → `owner/repo#N`
  (originally ⌥/⌃-click, but iTerm2 never forwards those to apps).
- **Location**: `~/Projects/github/jflowers/gh-hud`, module
  `github.com/jflowers/gh-hud`, Apache-2.0.

Architecture:

- `internal/gh` — thin `gh` CLI wrapper (GraphQL + REST, cached user).
- `internal/model` — fetchers per section → unified `Item`; enrichment is a
  Go port of pr-list.sh jq logic (CI rollup, review emojis, bot masking,
  last actor, waiting-on, threads, merge state, colors as row styles).
  Notifications merge into matching rows by URL (reason tags).
- `internal/ui` — custom table (not bubbles/table) for row hit-testing and
  hover; adaptive columns by terminal width; Tokyo Night palette.
- `internal/clipboard` — pbcopy + `open`.
- `cmd/gh-hud` — TUI (`--config`, `--version`) and `--once` plain table.

Verification: `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`,
`gh-hud --once` smoke test, manual iTerm2 click checklist
(click, ⌘-click, ⌘-right-click, ⇧-click).

Phases:
1. Core: PRs + review queue, table, mouse/keyboard, auto-refresh. (done)
2. Feeds: issues, notifications dedupe, watch-repo runs. (done)
3. Later ideas: hide-until-changed state, mark-read on GitHub, detail pane.

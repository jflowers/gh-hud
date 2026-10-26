# gh-hud — approved plan (implementation record)

Goal: a GitHub attention dashboard for iTerm2 — PRs, issues, Actions runs,
comments/notifications needing attention — with click-to-open and
modifier+click-to-copy.

Decisions (approved by Jay):

- **Approach**: Go TUI (Bubble Tea + Lipgloss), single binary, no tmux.
- **Scope**: orgs configurable, starting with `fullsend-ai`, `unbound-force`,
  `zero-dot-force`; "my PRs" optionally everywhere.
- **Sections**: PRs needing my review, my PRs, my issues/mentions, Actions
  runs, comments/notifications — unified into one attention-ranked list.
- **Copy formats**: Alt+click → URL; Ctrl+click → `owner/repo#N`;
  right-click → `owner/repo#N`; Cmd+click → iTerm native OSC-8 open.
- **Location**: `~/Projects/github/jflowers/gh-hud`, module
  `github.com/jflowers/gh-hud`, kept private (pr-list.sh has no license).

Architecture:

- `internal/gh` — thin `gh` CLI wrapper (GraphQL + REST, cached user).
- `internal/model` — fetchers per section → unified `Item`; enrichment is a
  Go port of pr-list.sh jq logic (CI rollup, review emojis, bot masking,
  last actor, waiting-on, threads, merge state, colors as row styles).
  Notifications merge into matching rows by URL (`‹reason›` badges).
- `internal/ui` — custom table (not bubbles/table) for row hit-testing,
  hover, per-cell OSC-8 links; adaptive columns by terminal width
  (≥110 full, ≥90 drop CC/BOT, <90 drop MG/LAST/AUTHOR too).
- `internal/clipboard` — pbcopy + `open`.
- `cmd/gh-hud` — TUI (`--config`, `--version`) and `--once` plain table.

Verification: `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`,
`gh-hud --once` smoke test, manual iTerm2 click checklist
(plain/alt/ctrl/cmd/right-click).

Phases:
1. Core: PRs + review queue, table, mouse/keyboard, auto-refresh. (done)
2. Feeds: issues, notifications dedupe, watch-repo runs. (done)
3. Later ideas: hide-until-changed state, mark-read on GitHub, detail pane.

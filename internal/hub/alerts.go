// Package hub is gh-hud's engine: it fetches, dispatches, and decides what
// deserves an alert, independent of any one client. The TUI is its first
// client (see PLAN-hub.md).
package hub

import (
	"fmt"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/notify"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

// AlertTitle is the title every gh-hud desktop alert carries.
const AlertTitle = "gh-hud"

// AlertType says what an alert is about.
type AlertType int

const (
	// AlertReady: one of my PRs became ready for a human maintainer.
	AlertReady AlertType = iota
	// AlertBlocked: a dispatched agent has waited on a human too long.
	AlertBlocked
	// AlertRunFinished: a dispatched run ended.
	AlertRunFinished
	// AlertToolRelease: a tool released, and its rollout waits for you.
	AlertToolRelease
	// AlertToolkitTarget: a repo in a tool rollout waits for you: its
	// scaffold was held for a look, refused, failed, or fails CI.
	AlertToolkitTarget
	// AlertReindexFailed: an automatic Dewey reindex of a checkout failed.
	AlertReindexFailed
)

// Alert is one thing worth interrupting the operator for. It stays
// structured so each client can present it natively; Text is the
// plain-text rendering desktop notifications use.
type Alert struct {
	Type    AlertType
	Ref     string
	Run     *dispatch.Run     // AlertRunFinished
	Session *dispatch.Session // AlertBlocked
	Text    string
}

// Alerter turns successive refreshes into alerts, each at most once. It
// remembers what it has seen between calls and is not safe for concurrent
// use.
type Alerter struct {
	notify       bool
	blockedAfter time.Duration // 0 disables blocked alerts

	// ready: refs of my maintainer-ready PRs (nil until seeded).
	ready map[string]bool
	// blocked: session IDs already alerted for the current block.
	blocked map[string]bool
	// finished: IDs of runs seen finished (nil until seeded).
	finished map[string]bool
	// proposed: IDs of proposed toolkit rollouts (nil until seeded).
	proposed map[string]bool
	// targets: toolkit targets waiting on you, by ToolkitTargetKey (nil
	// until seeded).
	targets map[string]bool
	// reindexFails: failed reindexes already alerted, by checkout and time.
	reindexFails map[string]bool
}

// NewAlerter builds an Alerter from the notify settings in cfg.
func NewAlerter(cfg *config.Config) *Alerter {
	return &Alerter{
		notify:       cfg.Notify,
		blockedAfter: time.Duration(cfg.BlockedNotifySecs) * time.Second,
	}
}

// SetConfig takes on reloaded notify settings, keeping what it has seen.
func (a *Alerter) SetConfig(cfg *config.Config) {
	a.notify = cfg.Notify
	a.blockedAfter = time.Duration(cfg.BlockedNotifySecs) * time.Second
}

// Items alerts for each of my PRs that just became ready for a human
// maintainer review. The first fetch after startup only seeds the seen
// set, so pre-existing ready PRs stay quiet.
func (a *Alerter) Items(user string, items []model.Item) []Alert {
	if !a.notify || user == "" {
		return nil
	}
	ready := map[string]bool{}
	for _, it := range items {
		if it.Cats&model.CatReady != 0 {
			if ref := it.Ref(); ref != "" {
				ready[ref] = true
			}
		}
	}
	prev := a.ready
	a.ready = ready
	if prev == nil {
		return nil
	}
	var out []Alert
	for _, it := range items {
		ref := it.Ref()
		if !ready[ref] || prev[ref] {
			continue
		}
		prev[ref] = true // one alert per ref even if it appears twice
		out = append(out, Alert{Type: AlertReady, Ref: ref, Text: ref + " ready for maintainer review"})
	}
	return out
}

// Sessions alerts once per block when a gh-hud-dispatched agent has been
// blocked longer than the configured limit. Shorter blocks are the
// auto-question plugin deciding; a long one means the question went to a
// human.
func (a *Alerter) Sessions(sessions []dispatch.Session, now time.Time) []Alert {
	if !a.notify || a.blockedAfter <= 0 {
		return nil
	}
	still := map[string]bool{}
	var out []Alert
	for _, s := range sessions {
		if !s.Blocked || s.Task == "" || s.BlockedSince.IsZero() {
			continue
		}
		still[s.ID] = a.blocked[s.ID]
		blockedFor := now.Sub(s.BlockedSince)
		if still[s.ID] || blockedFor < a.blockedAfter {
			continue
		}
		still[s.ID] = true
		where := "local"
		if s.Machine != "" {
			where = s.Machine
		}
		sess := s
		out = append(out, Alert{
			Type:    AlertBlocked,
			Ref:     s.Ref,
			Session: &sess,
			Text: fmt.Sprintf("%s %s needs input (blocked %s on %s, pane %s)",
				s.Task, s.Ref, blockedFor.Round(time.Minute), where, s.PaneID),
		})
	}
	a.blocked = still
	return out
}

// Runs alerts once per run that finished since the previous call (runs as
// Dispatcher.Runs returns them). The first call only seeds the seen set,
// so runs that ended before gh-hud started stay quiet. The seen set is
// kept current even with alerts off.
func (a *Alerter) Runs(runs []dispatch.Run) []Alert {
	seen := map[string]bool{}
	for _, r := range runs {
		if r.Status != dispatch.RunRunning && r.Status != dispatch.RunBlocked {
			seen[r.ID] = true
		}
	}
	prev := a.finished
	a.finished = seen
	if prev == nil || !a.notify {
		return nil
	}
	var out []Alert
	for _, r := range runs {
		if !seen[r.ID] || prev[r.ID] {
			continue
		}
		run := r
		out = append(out, Alert{Type: AlertRunFinished, Ref: r.Ref, Run: &run, Text: finishedText(r)})
	}
	return out
}

// Toolkit alerts once per toolkit rollout newly proposed since the
// previous call, and once per repo in a rollout that newly waits on you
// (again when it gets a PR to look at). The first call only seeds, so
// what already waited when gh-hud started stays quiet.
func (a *Alerter) Toolkit(snap toolkit.Snapshot) []Alert {
	seen := map[string]bool{}
	waiting := map[string]bool{}
	for _, r := range snap.Rollouts {
		if r.Stage == toolkit.StageProposed {
			seen[r.ID] = true
		}
		for _, t := range ToolkitWaiting(r) {
			waiting[ToolkitTargetKey(r, t)] = true
		}
	}
	prev, prevWaiting := a.proposed, a.targets
	a.proposed, a.targets = seen, waiting
	if prev == nil || !a.notify {
		return nil
	}
	var out []Alert
	for _, r := range snap.Rollouts {
		if !seen[r.ID] || prev[r.ID] {
			continue
		}
		text := fmt.Sprintf("%s %s released (the fleet runs %s): its rollout waits for your go-ahead", r.Tool, r.Version, r.From)
		out = append(out, Alert{Type: AlertToolRelease, Ref: r.ID, Text: text})
	}
	for _, r := range snap.Rollouts {
		for _, t := range ToolkitWaiting(r) {
			if prevWaiting[ToolkitTargetKey(r, t)] {
				continue
			}
			ref := r.ID
			if repo, num, err := model.ParseItemRef(t.URL); err == nil {
				ref = fmt.Sprintf("%s#%d", repo, num)
			}
			out = append(out, Alert{Type: AlertToolkitTarget, Ref: ref, Text: toolkitTargetText(r, t)})
		}
	}
	return out
}

// Workspaces alerts once per failed automatic Dewey reindex that ended
// after since (the hub's start: a reindex runs in the hub, so one that
// ended before it was announced by the hub that ran it). It doesn't
// retry: a reindex that fails again later is a new alert.
func (a *Alerter) Workspaces(ws []dispatch.CheckoutHealth, since time.Time) []Alert {
	if a.reindexFails == nil {
		a.reindexFails = map[string]bool{}
	}
	var out []Alert
	for _, h := range ws {
		r := h.LastReindex
		if r == nil || !r.Failed() || r.At.Before(since) {
			continue
		}
		key := h.Machine + "|" + h.Dir + "|" + r.At.String()
		if a.reindexFails[key] {
			continue
		}
		a.reindexFails[key] = true
		if !a.notify {
			continue
		}
		where := h.Workspace
		if h.Machine != "" {
			where = h.Machine + ":" + where
		}
		out = append(out, Alert{Type: AlertReindexFailed, Ref: where,
			Text: "Dewey reindex failed in " + where + ": " + truncate(r.Err, 120)})
	}
	return out
}

// ToolkitWaiting is a rollout's repos that wait on you. A cancelled
// rollout's don't: you stopped it.
func ToolkitWaiting(r toolkit.Rollout) []toolkit.TargetRun {
	if r.Stage == toolkit.StageCancelled || r.Stage == toolkit.StageProposed {
		return nil
	}
	var out []toolkit.TargetRun
	for _, t := range r.Targets {
		if t.NeedsYou() != "" {
			out = append(out, t)
		}
	}
	return out
}

// ToolkitTargetKey names a waiting repo by why it waits and where to
// look, so a held branch that gets a PR is news again.
func ToolkitTargetKey(r toolkit.Rollout, t toolkit.TargetRun) string {
	return r.ID + " " + t.Target + " " + t.NeedsYou() + " " + t.URL
}

func toolkitTargetText(r toolkit.Rollout, t toolkit.TargetRun) string {
	head := fmt.Sprintf("%s %s on %s", r.Tool, r.Version, t.Target)
	switch why := t.NeedsYou(); why {
	case "held":
		if t.URL != "" {
			return head + " is held for a look: merge its PR once it looks right"
		}
		return head + " is held on " + t.Branch + ": merge it after a look"
	case "CI failing":
		return head + ": the scaffold PR's CI fails"
	default:
		if t.Detail != "" {
			return head + " " + why + ": " + truncate(t.Detail, 120)
		}
		return head + " " + why
	}
}

func finishedText(r dispatch.Run) string {
	text := fmt.Sprintf("%s %s %s %s on %s", r.Status.Glyph(), r.Task, r.Ref, r.Status.Words(), r.Where())
	switch {
	case r.Status == dispatch.RunFailed && r.Reason != "":
		text += ": " + truncate(r.Reason, 80)
	case r.Status == dispatch.RunLeftWork:
		text += fmt.Sprintf(": %d item(s) — press i on the row", len(r.Left))
	case r.Status == dispatch.RunUnknown && r.Reason != "":
		text += ": " + truncate(r.Reason, 80)
	}
	return text
}

// truncate cuts s to at most w display columns, ending in an ellipsis
// when cut.
func truncate(s string, w int) string {
	return runewidth.Truncate(s, w, "…")
}

// Post sends each alert to n.
func Post(n notify.Notifier, alerts []Alert) {
	if n == nil {
		return
	}
	for _, a := range alerts {
		n.Notify(AlertTitle, a.Text)
	}
}

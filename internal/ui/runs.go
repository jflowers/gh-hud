package ui

// Dispatch runs in the dashboard: a run tag on each PR/issue row, a desktop
// notification when a run finishes, and a run detail view (i) with the
// outcome, the agent's final message, what it pushed, and what it left
// behind.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

// runTagWindow is how long a successful or unknown run keeps its row tag;
// failures and leftovers keep theirs for runAlertWindow (or until a newer
// run replaces them).
const (
	runTagWindow   = 24 * time.Hour
	runAlertWindow = 7 * 24 * time.Hour
)

type reinspectMsg struct {
	run dispatch.Run
	err error
}

// runGlyph maps a run status to its glyph and color.
func runGlyph(s dispatch.RunStatus) (string, lipgloss.Color) {
	switch s {
	case dispatch.RunRunning:
		return "●", cTeal
	case dispatch.RunBlocked:
		return "◆", cOrange
	case dispatch.RunOK:
		return "✓", cGreen
	case dispatch.RunFailed:
		return "✗", cRed
	case dispatch.RunLeftWork:
		return "!", cOrange
	}
	return "?", cYellow
}

// runWords is the status in words.
func runWords(s dispatch.RunStatus) string {
	switch s {
	case dispatch.RunRunning:
		return "running"
	case dispatch.RunBlocked:
		return "blocked, needs input"
	case dispatch.RunOK:
		return "succeeded"
	case dispatch.RunFailed:
		return "failed"
	case dispatch.RunLeftWork:
		return "left work behind"
	}
	return "outcome unknown"
}

// latestRuns maps ref → its newest run (runs arrive newest first).
func latestRuns(runs []dispatch.Run) map[string]dispatch.Run {
	out := map[string]dispatch.Run{}
	for _, r := range runs {
		if _, ok := out[r.Ref]; !ok && r.Ref != "" {
			out[r.Ref] = r
		}
	}
	return out
}

// showRunTag reports whether a row still shows its latest run.
func showRunTag(r dispatch.Run, now time.Time) bool {
	end := r.StartedAt
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	switch r.Status {
	case dispatch.RunOK, dispatch.RunUnknown:
		return now.Sub(end) < runTagWindow
	case dispatch.RunFailed, dispatch.RunLeftWork:
		return now.Sub(end) < runAlertWindow
	}
	return true
}

// runTag is the row tag text: "<task> <glyph> <age>".
func runTag(r dispatch.Run, now time.Time) (string, lipgloss.Color) {
	g, c := runGlyph(r.Status)
	at := r.StartedAt
	if r.FinishedAt != nil {
		at = *r.FinishedAt
	}
	return fmt.Sprintf("%s %s %s", r.Task, g, model.RelativeTime(at, now)), c
}

// runAt is where a run ran: "lap1:ws" or "local:ws".
func runAt(r dispatch.Run) string {
	mach := r.Machine
	if mach == "" {
		mach = "local"
	}
	return mach + ":" + r.Workspace
}

// notifyFinished posts a desktop notification once per run that finished
// since the previous agents refresh. The first refresh only seeds the seen
// set, so runs that ended before gh-hud started stay quiet.
func (m *Model) notifyFinished(runs []dispatch.Run) {
	seen := map[string]bool{}
	for _, r := range runs {
		if r.Status != dispatch.RunRunning && r.Status != dispatch.RunBlocked {
			seen[r.ID] = true
		}
	}
	prev := m.finishedSeen
	m.finishedSeen = seen
	if prev == nil || !m.cfg.Notify {
		return
	}
	for _, r := range runs {
		if !seen[r.ID] || prev[r.ID] {
			continue
		}
		g, _ := runGlyph(r.Status)
		text := fmt.Sprintf("%s %s %s %s on %s", g, r.Task, r.Ref, runWords(r.Status), runAt(r))
		switch {
		case r.Status == dispatch.RunFailed && r.Reason != "":
			text += ": " + truncatePlain(r.Reason, 80)
		case r.Status == dispatch.RunLeftWork:
			text += fmt.Sprintf(": %d item(s) — press i on the row", len(r.Left))
		case r.Status == dispatch.RunUnknown && r.Reason != "":
			text += ": " + truncatePlain(r.Reason, 80)
		}
		desktopNotify(text)
	}
}

// openRun opens the run detail view for a run ID.
func (m *Model) openRun(id string) bool {
	for _, r := range m.runs {
		if r.ID == id {
			m.runView = &r
			m.runScroll = 0
			return true
		}
	}
	return false
}

// openRowRun opens the latest run of the selected dashboard row.
func (m *Model) openRowRun() {
	it, ok := m.selected()
	if !ok {
		return
	}
	r, ok := latestRuns(m.runs)[it.Ref()]
	if !ok {
		m.flash("no agent runs for " + displayRef(it))
		return
	}
	m.openRun(r.ID)
}

// refreshRunView keeps an open run view current across agent refreshes.
func (m *Model) refreshRunView() {
	if m.runView == nil {
		return
	}
	for _, r := range m.runs {
		if r.ID == m.runView.ID {
			m.runView = &r
			return
		}
	}
}

func (m *Model) handleRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.runView
	switch msg.String() {
	case "esc", "q", "i":
		m.runView = nil
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.runScroll++
	case "k", "up":
		if m.runScroll > 0 {
			m.runScroll--
		}
	case "g", "home":
		m.runScroll = 0
	case "w", "enter":
		if err := m.disp.WatchRun(*r); err != nil {
			m.flash("watch failed: " + err.Error())
		} else {
			m.flash("watching " + string(r.Task) + " " + r.Ref)
		}
	case "t":
		if err := m.disp.OpenTranscript(*r); err != nil {
			m.flash("transcript failed: " + err.Error())
		} else {
			m.flash("opening transcript " + r.SessionID)
		}
	case "y":
		if r.SessionID == "" {
			m.flash("no session id for this run")
		} else if err := clipboard.Copy(r.SessionID); err != nil {
			m.flash("copy failed: " + err.Error())
		} else {
			m.flash("copied " + r.SessionID)
		}
	case "r":
		d, id := m.disp, r.ID
		m.flash("re-checking " + runAt(*r) + "…")
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			run, err := d.Reinspect(ctx, id)
			return reinspectMsg{run: run, err: err}
		}
	}
	return m, nil
}

// runLines renders the run detail body (before scrolling).
func (m *Model) runLines(r dispatch.Run, w int) []string {
	fg := lipgloss.NewStyle().Foreground(cFg)
	key := lipgloss.NewStyle().Foreground(cComment).Width(11)
	h := lipgloss.NewStyle().Foreground(cMagenta).Bold(true)
	dim := lipgloss.NewStyle().Foreground(cComment)
	now := time.Now()

	g, c := runGlyph(r.Status)
	field := func(k, v string) string { return key.Render(k) + fg.Render(truncatePlain(v, w-11)) }
	lines := []string{
		lipgloss.NewStyle().Foreground(c).Bold(true).Render(g+" "+runWords(r.Status)) +
			dim.Render("  "+string(r.Task)+" "+r.Ref),
		"",
		field("where", runAt(r)+"  "+r.Dir),
	}
	started := r.StartedAt.Local().Format("Jan 2 15:04") + "  (" + model.RelativeTime(r.StartedAt, now) + " ago)"
	lines = append(lines, field("started", started))
	if r.FinishedAt != nil {
		lines = append(lines, field("took", shortDur(r.FinishedAt.Sub(r.StartedAt))))
	}
	if r.Auto {
		lines = append(lines, field("trigger", "auto-dispatch"))
	}
	if r.SessionID != "" {
		lines = append(lines, field("session", r.SessionID))
	}
	if r.Branch != "" {
		b := r.Branch
		if r.Cleaned {
			b += "  (returned to the default branch)"
		}
		lines = append(lines, field("branch", b))
	}
	if r.Reason != "" {
		lines = append(lines, key.Render("reason")+lipgloss.NewStyle().Foreground(cRed).Render(truncatePlain(r.Reason, w-11)))
	}

	if len(r.Left) > 0 {
		lines = append(lines, "", h.Foreground(cOrange).Render("Left behind")+dim.Render("  commit, push or discard it, then press r to re-check"))
		for _, l := range r.Left {
			lines = append(lines, lipgloss.NewStyle().Foreground(cOrange).Render("  "+truncatePlain(l, w-2)))
		}
	}
	if len(r.Pushed) > 0 {
		lines = append(lines, "", h.Render("Pushed"))
		for _, p := range r.Pushed {
			lines = append(lines, fg.Render("  "+truncatePlain(p, w-2)))
		}
	}
	if r.Summary != "" {
		lines = append(lines, "", h.Render("Final message"))
		for _, l := range wrapText(r.Summary, w-2) {
			lines = append(lines, fg.Render("  "+l))
		}
	} else if r.FinishedAt != nil {
		lines = append(lines, "", dim.Render("no final message recorded"))
	}
	return lines
}

func (m *Model) runViewText() string {
	r := *m.runView
	w := m.width - 12
	if w > 110 {
		w = 110
	}
	if w < 30 {
		w = 30
	}
	body := m.runLines(r, w)
	// viewport: border(2) + padding(2) + footer(2)
	rows := m.height - 6
	if rows < 5 {
		rows = 5
	}
	if max := len(body) - rows; m.runScroll > max {
		m.runScroll = max
	}
	if m.runScroll < 0 {
		m.runScroll = 0
	}
	comment := lipgloss.NewStyle().Foreground(cComment)
	view := body[m.runScroll:]
	if len(view) > rows {
		view = append(view[:rows-1:rows-1], comment.Render(fmt.Sprintf("  ↓ %d more (j)", len(body)-m.runScroll-rows+1)))
	}
	keys := "j/k scroll · t transcript · y copy session · r re-check · esc close"
	if r.Status == dispatch.RunRunning || r.Status == dispatch.RunBlocked || r.PaneID == "" {
		keys = "w watch · " + keys
	}
	view = append(view, "", comment.Render(keys))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(1, 3).
		Render(strings.Join(view, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// wrapText word-wraps s to width w, keeping its line breaks.
func wrapText(s string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for width(word) > w {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				r := []rune(word)
				out = append(out, string(r[:w]))
				word = string(r[w:])
			}
			switch {
			case line == "":
				line = word
			case width(line)+1+width(word) > w:
				out = append(out, line)
				line = word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

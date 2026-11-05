package ui

// The Agents tab (6): a full-width table of dispatched runs and opencode
// sessions, with the selected one's detail (outcome, reason, leftovers,
// pushes, final message) in a pane below it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

// agent table column widths (the name column takes the rest)
const (
	agTaskW   = 9
	agMachW   = 6
	agWsW     = 26
	agStatusW = 9
	agTookW   = 5
	agAgeW    = 4
)

// runningAgents counts live sessions, for the tab count.
func (m *Model) runningAgents() int {
	n := 0
	for _, s := range m.agents {
		if s.Running {
			n++
		}
	}
	return n
}

// sessionRun is the dispatch run behind a session, if any.
func (m *Model) sessionRun(s dispatch.Session) (dispatch.Run, bool) {
	if s.RunID == "" {
		return dispatch.Run{}, false
	}
	for _, r := range m.runs {
		if r.ID == s.RunID {
			return r, true
		}
	}
	return dispatch.Run{}, false
}

// shortStatus is a run status in at most agStatusW columns.
func shortStatus(s dispatch.RunStatus) string {
	switch s {
	case dispatch.RunLeftWork:
		return "left work"
	case dispatch.RunUnknown:
		return "unknown"
	}
	return string(s)
}

// agentGlyph, status text and color for a session row.
func (m *Model) agentCells(s dispatch.Session) (glyph string, color lipgloss.Color, status, took string) {
	if r, ok := m.sessionRun(s); ok {
		g, c := runGlyph(r.Status)
		end := time.Now()
		if r.FinishedAt != nil {
			end = *r.FinishedAt
		}
		return g, c, shortStatus(r.Status), shortDur(end.Sub(r.StartedAt))
	}
	switch {
	case s.Blocked:
		return "◆", cOrange, "blocked", ""
	case s.Running:
		st := s.State
		if st == "" {
			st = "running"
		}
		return "●", cTeal, st, ""
	case s.Failed:
		return "✗", cRed, "failed", ""
	case s.Unknown:
		return "?", cYellow, "unknown", ""
	}
	return "○", cDim, "", ""
}

// agentName is the row's name: ref plus the PR/issue title for a
// dispatch, the session title otherwise.
func (m *Model) agentName(s dispatch.Session) string {
	if s.Ref == "" {
		return s.Title
	}
	if m.res != nil {
		for _, it := range m.res.Items {
			if it.Ref() == s.Ref {
				return displayRef(it) + "  " + it.Title
			}
		}
	}
	return s.Ref
}

// agentsLayout splits the screen below the tab bar: table rows and the
// detail pane (0 when the terminal is too short for one).
func (m *Model) agentsLayout() (rows, detail int) {
	spare := m.height - 4 // tab bar, column header, separator, footer
	if spare < 14 {
		if spare < 3 {
			spare = 3
		}
		return spare, 0
	}
	detail = spare * 45 / 100
	if detail > 18 {
		detail = 18
	}
	return spare - detail, detail
}

func (m *Model) agentsView() string {
	fg := lipgloss.NewStyle().Foreground(cFg)
	comment := lipgloss.NewStyle().Foreground(cComment)
	dim := lipgloss.NewStyle().Foreground(cDim)
	w := m.width
	nameW := w - (2 + 2 + agTaskW + 1 + agMachW + 1 + agWsW + 1 + agStatusW + 1 + agTookW + 1 + agAgeW + 1)
	if nameW < 16 {
		nameW = 16
	}

	list := m.visibleAgents()
	rows, detailH := m.agentsLayout()
	lines := []string{m.headerBar(), dim.Render(
		"    " + padPlain("task", agTaskW, false) + " " + padPlain("agent", nameW, false) + " " +
			padPlain("where", agMachW, false) + " " + padPlain("workspace", agWsW, false) + " " +
			padPlain("status", agStatusW, false) + " " + padPlain("took", agTookW, true) + " " + padPlain("age", agAgeW, true))}

	start, end := 0, len(list)
	if len(list) > rows {
		start = m.agentsSel - rows/2
		if start < 0 {
			start = 0
		}
		if start > len(list)-rows {
			start = len(list) - rows
		}
		end = start + rows
	}
	if len(list) == 0 {
		lines = append(lines, comment.Render("    no agent sessions"))
	}
	now := time.Now()
	for i := start; i < end; i++ {
		s := list[i]
		glyph, color, status, took := m.agentCells(s)
		task := string(s.Task)
		if task == "" {
			task = "—"
		}
		mach := s.Machine
		if mach == "" {
			mach = "local"
		}
		ws := s.Workspace
		if ws == "" {
			ws = "—"
		}
		row := padPlain(task, agTaskW, false) + " " + padPlain(m.agentName(s), nameW, false) + " " +
			padPlain(mach, agMachW, false) + " " + padPlain(ws, agWsW, false) + " "
		st := padPlain(status, agStatusW, false)
		tail := " " + padPlain(took, agTookW, true) + " " + padPlain(model.RelativeTime(s.UpdatedAt, now), agAgeW, true)
		marker := "  "
		rowStyle, stStyle := fg, lipgloss.NewStyle().Foreground(color)
		if i == m.agentsSel {
			marker = "▶ "
			rowStyle = rowStyle.Background(cSel).Bold(true)
			stStyle = stStyle.Background(cSel)
		}
		if s.Task == "" && !s.Running {
			rowStyle = rowStyle.Foreground(cComment)
		}
		lines = append(lines, marker+lipgloss.NewStyle().Foreground(color).Render(glyph)+" "+
			rowStyle.Render(row)+stStyle.Render(st)+rowStyle.Render(tail))
	}
	for len(lines) < 2+rows {
		lines = append(lines, "")
	}

	// separator: position and scroll hints
	pos := ""
	if len(list) > 0 {
		pos = fmt.Sprintf(" %d/%d ", m.agentsSel+1, len(list))
	}
	if start > 0 {
		pos += fmt.Sprintf("↑ %d ", start)
	}
	if end < len(list) {
		pos += fmt.Sprintf("↓ %d ", len(list)-end)
	}
	if hidden := len(m.agents) - len(list); hidden > 0 {
		pos += fmt.Sprintf("· %d stale hidden (a) ", hidden)
	}
	sepW := w - width(pos) - 3
	if sepW < 0 {
		sepW = 0
	}
	lines = append(lines, dim.Render("──"+pos+strings.Repeat("─", sepW)))

	if detailH > 0 {
		var body []string
		if m.agentsSel < len(list) {
			body = m.agentDetail(list[m.agentsSel], w-4)
		}
		if len(body) > detailH {
			body = append(body[:detailH-1:detailH-1], comment.Render("… i opens the full run view"))
		}
		for _, l := range body {
			lines = append(lines, "  "+l)
		}
		for len(lines) < 3+rows+detailH {
			lines = append(lines, "")
		}
	}

	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.agentsFooter())
	return strings.Join(lines, "\n")
}

// agentDetail is the detail pane body for a session: the run view's
// lines for a dispatch, the session's identity otherwise.
func (m *Model) agentDetail(s dispatch.Session, w int) []string {
	if r, ok := m.sessionRun(s); ok {
		return m.runLines(r, w)
	}
	fg := lipgloss.NewStyle().Foreground(cFg)
	key := lipgloss.NewStyle().Foreground(cComment).Width(11)
	field := func(k, v string) string { return key.Render(k) + fg.Render(truncatePlain(v, w-11)) }
	_, c, status, _ := m.agentCells(s)
	if status == "" {
		status = "not running"
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(c).Bold(true).Render(truncatePlain(s.Title, w)),
		"",
		field("status", status),
		field("dir", s.Directory),
		field("session", s.ID),
		field("updated", s.UpdatedAt.Local().Format("Jan 2 15:04")+"  ("+model.RelativeTime(s.UpdatedAt, time.Now())+" ago)"),
	}
	if s.Note != "" {
		lines = append(lines, field("note", s.Note))
	}
	return lines
}

func (m *Model) agentsFooter() string {
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		if strings.Contains(m.statusMsg, "failed") {
			return lipgloss.NewStyle().Foreground(cRed).Render(" ✗ " + m.statusMsg)
		}
		return lipgloss.NewStyle().Foreground(cGreen).Render(" ✓ " + m.statusMsg)
	}
	toggle := "show stale"
	if m.agentsAll {
		toggle = "hide stale"
	}
	return chips(m.width, "j/k", "move", "enter", "watch", "i", "run view", "t", "transcript", "y", "copy id",
		"r", "refresh", "a", toggle, "0-8", "tabs", "esc", "dashboard")
}

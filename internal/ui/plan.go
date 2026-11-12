package ui

// The Plan tab (p): what agents work on next, in the order they start —
// your queue first, then the auto steps due in merge-train order — and
// below that what's running, waiting, stopped and merging. It draws the
// same plan the auto pass walks (dispatch.Plan), so the order shown is the
// order things start.
//
// Keys: + queues the row's task, J/K move a queued task later/earlier,
// x removes it, a opens the agent menu, enter opens the PR.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

// plan table column widths (the title column takes the rest)
const (
	plPosW    = 3
	plTaskW   = 9
	plRefW    = 30
	plStatusW = 38
)

// planRows is the current plan (nil without dispatch or a fetch).
func (m *Model) planRows() []dispatch.PlanRow {
	if m.disp == nil || m.res == nil {
		return nil
	}
	return m.disp.Plan(m.res.Items, m.runs)
}

// planStarting counts the rows the next pass tries to start, for the tab.
func (m *Model) planStarting() int {
	n := 0
	for _, r := range m.planRows() {
		if r.Starts() {
			n++
		}
	}
	return n
}

// planGroup is the heading a row sits under.
func planGroup(r dispatch.PlanRow) string {
	if r.Queued > 0 {
		return "your queue"
	}
	switch r.Stage.Kind {
	case dispatch.StageNext:
		return "auto, next"
	case dispatch.StageRunning:
		return "running"
	case dispatch.StageWait:
		return "waiting"
	case dispatch.StageNeedsYou:
		return "needs you"
	case dispatch.StageMerge:
		return "merging"
	}
	return ""
}

// planStatus is a row's status text and color.
func planStatus(r dispatch.PlanRow) (string, lipgloss.Color) {
	if r.Queued > 0 {
		switch {
		case r.Missing:
			return r.Note, cDim
		case r.Stage.Kind == dispatch.StageRunning:
			return r.Note, cDim
		case r.Note == "next free slot", r.Note == "starting":
			return r.Note, cGreen
		default:
			return r.Note, cRed
		}
	}
	switch r.Stage.Kind {
	case dispatch.StageNext:
		return "next free slot", cGreen
	case dispatch.StageRunning:
		return "running", cTeal
	case dispatch.StageWait:
		return r.Stage.Reason, cDim
	case dispatch.StageNeedsYou:
		return r.Stage.Reason, cOrange
	case dispatch.StageMerge:
		if r.Stage.Reason != "" {
			return r.Stage.Reason, cGreen
		}
		return "auto-merge on", cGreen
	}
	return "", cDim
}

func (m *Model) planView() string {
	fg := lipgloss.NewStyle().Foreground(cFg)
	dim := lipgloss.NewStyle().Foreground(cDim)
	comment := lipgloss.NewStyle().Foreground(cComment)
	w := m.width
	titleW := w - (2 + plPosW + 1 + plTaskW + 1 + plRefW + 1 + 1 + plStatusW)
	if titleW < 12 {
		titleW = 12
	}

	rows := m.planRows()
	if m.planSel >= len(rows) {
		m.planSel = max(len(rows)-1, 0)
	}

	p := paint{}
	summary := fmt.Sprintf("    %d start next", m.planStarting())
	if u := machineUsageText(p, m.machineUsage); u != "" {
		summary += "  ·  slots " + u
	}
	if m.follower != "" {
		summary += "  ·  read-only"
	}
	lines := []string{m.headerBar(), comment.Render(summary), dim.Render(
		"  " + padPlain("#", plPosW, true) + " " + padPlain("task", plTaskW, false) + " " +
			padPlain("item", plRefW, false) + " " + padPlain("title", titleW, false) + " " + "status")}

	// Lay the rows out with group headings, then scroll to keep the
	// selection in view.
	type line struct {
		text string
		row  int // -1 for a heading
	}
	var body []line
	group, pos := "", 0
	for i, r := range rows {
		if g := planGroup(r); g != group {
			group = g
			body = append(body, line{dim.Render("  ── " + g + " " + strings.Repeat("─", max(w-8-width(g), 0))), -1})
		}
		num := ""
		if r.Queued > 0 || r.Stage.Kind == dispatch.StageNext {
			pos++
			num = fmt.Sprint(pos)
		}
		status, sc := planStatus(r)
		task := string(r.Task)
		if task == "" {
			task = "—"
		}
		cells := padPlain(num, plPosW, true) + " " + padPlain(task, plTaskW, false) + " " +
			padPlain(planRef(r.Item, plRefW), plRefW, false) + " " +
			padPlain(truncatePlain(r.Item.Title, titleW), titleW, false) + " "
		st := truncatePlain(status, plStatusW)
		marker := "  "
		rowStyle, stStyle := fg, lipgloss.NewStyle().Foreground(sc)
		if r.Stage.Kind == dispatch.StageWait || r.Stage.Kind == dispatch.StageMerge || r.Missing {
			rowStyle = comment
		}
		if i == m.planSel {
			marker = "▶ "
			rowStyle = rowStyle.Background(cSel).Bold(true)
			stStyle = stStyle.Background(cSel)
		}
		body = append(body, line{marker + rowStyle.Render(cells) + stStyle.Render(st), i})
	}
	if len(rows) == 0 {
		body = append(body, line{comment.Render("    nothing queued, due, running or waiting"), -1})
	}

	avail := max(m.height-len(lines)-1, 1)
	start := 0
	if len(body) > avail {
		selAt := 0
		for j, l := range body {
			if l.row == m.planSel {
				selAt = j
			}
		}
		start = min(max(selAt-avail/2, 0), len(body)-avail)
	}
	for j := start; j < len(body) && j < start+avail; j++ {
		lines = append(lines, body[j].text)
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.planFooter())
	return strings.Join(lines, "\n")
}

func (m *Model) planFooter() string {
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		if strings.Contains(m.statusMsg, "failed") || strings.Contains(m.statusMsg, "can't") {
			return lipgloss.NewStyle().Foreground(cRed).Render(" ✗ " + m.statusMsg)
		}
		return lipgloss.NewStyle().Foreground(cGreen).Render(" ✓ " + m.statusMsg)
	}
	return chips(m.width, "j/k", "move", "+", "queue", "J/K", "later/earlier", "x", "unqueue",
		"a", "agent", "enter", "open", "0-9", "tabs", "esc", "dashboard")
}

// planRef fits an item's ref in w columns, shortening the owner to its
// initials before cutting anything, so the number always shows.
func planRef(it model.Item, w int) string {
	ref := displayRef(it)
	if width(ref) <= w {
		return ref
	}
	if owner, rest, ok := strings.Cut(ref, "/"); ok {
		ref = initials(owner) + "/" + rest
	}
	if width(ref) <= w {
		return ref
	}
	if name, num, ok := strings.Cut(ref, "#"); ok {
		return truncatePlain(name, w-width(num)-1) + "#" + num
	}
	return truncatePlain(ref, w)
}

// planItemURL is a row's link; a queued PR missing from the refresh only
// has its ref, and GitHub redirects /issues/N to a PR.
func planItemURL(r dispatch.PlanRow) string {
	if r.Item.URL != "" {
		return r.Item.URL
	}
	if r.Item.Repo == "" || r.Item.Num == 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", r.Item.Repo, r.Item.Num)
}

func (m *Model) handlePlanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.planRows()
	var cur dispatch.PlanRow
	have := m.planSel < len(rows)
	if have {
		cur = rows[m.planSel]
	}
	switch msg.String() {
	case "esc", "q", "p":
		m.planMode = false
	case "ctrl+c":
		return m, tea.Quit
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m, m.switchTab(msg.String())
	case "?":
		m.help = true
	case "j", "down":
		if m.planSel < len(rows)-1 {
			m.planSel++
		}
	case "k", "up":
		if m.planSel > 0 {
			m.planSel--
		}
	case "g", "home":
		m.planSel = 0
	case "G", "end":
		m.planSel = max(len(rows)-1, 0)
	case "enter", "o":
		if have {
			it := cur.Item
			it.URL = planItemURL(cur)
			if it.URL != "" {
				m.open(it)
			}
		}
	case "a":
		if have && !cur.Missing {
			m.openActionFor(cur.Item, false)
		}
	case "+":
		if !have {
			break
		}
		if cur.Queued > 0 {
			m.flash("already queued")
			break
		}
		if cur.Task == "" {
			m.openActionFor(cur.Item, true)
			break
		}
		m.queueTask(cur.Task, cur.Item)
		m.planSel = m.queueLen() - 1
	case "J", "K":
		if !have || cur.Queued == 0 {
			m.flash("only your queued tasks move: + queues this one")
			break
		}
		delta := 1
		if msg.String() == "K" {
			delta = -1
		}
		if i, err := m.disp.MoveQueued(cur.Task, cur.Item.Ref(), delta); err != nil {
			m.flash("move failed: " + err.Error())
		} else {
			m.planSel = i
		}
	case "x", "delete", "backspace":
		if !have || cur.Queued == 0 {
			break
		}
		if err := m.disp.Unqueue(cur.Task, cur.Item.Ref()); err != nil {
			m.flash("unqueue failed: " + err.Error())
		} else {
			m.flash("unqueued " + string(cur.Task) + " " + cur.Item.Ref())
		}
	}
	return m, nil
}

// queueTask adds a task to your queue and says so.
func (m *Model) queueTask(task dispatch.Task, it model.Item) {
	if err := m.disp.Enqueue(task, it); err != nil {
		m.flash("can't queue: " + err.Error())
		return
	}
	m.flash(fmt.Sprintf("queued %s %s (#%d): p shows the plan", task, it.Ref(), m.queueLen()))
}

func (m *Model) queueLen() int {
	if m.disp == nil {
		return 0
	}
	return len(m.disp.Queue())
}

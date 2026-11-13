package ui

// The Plan tab (p): what agents work on next, in the order they start —
// your queue first, then the auto steps due in merge-train order — and
// below that what's running, waiting, stopped and merging. It draws the
// same plan the auto pass walks (dispatch.Plan), so the order shown is the
// order things start.
//
// Above the rows are the open cases (a check failing on several of your
// PRs) with what the supervisor is doing about them, and the enrolled
// epics with their pieces still to do.
//
// Keys: + queues the row's task, J/K move a queued task later/earlier,
// x removes it, a opens the agent menu, enter opens the PR, Y/N approve or
// reject the supervisor's proposal for the selected PR's case, E approves
// the epic plan waiting for approval.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/model"
)

// plan table column widths (the title column takes the rest)
const (
	plPosW    = 3
	plTaskW   = 9
	plRefW    = 30
	plStatusW = 38
)

// planRows is the current plan, from the hub (nil without dispatch or a
// fetch).
func (m *Model) planRows() []dispatch.PlanRow { return m.plan }

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
	if r.Epic != "" && (r.Stage.Kind == dispatch.StageNext || r.Stage.Kind == dispatch.StageRunning) {
		what := map[dispatch.StageKind]string{dispatch.StageNext: "next free slot", dispatch.StageRunning: "running"}[r.Stage.Kind]
		return what + " · epic " + r.Epic, map[dispatch.StageKind]lipgloss.Color{dispatch.StageNext: cGreen, dispatch.StageRunning: cTeal}[r.Stage.Kind]
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

	cases := append(m.caseLines(w), m.epicLines(w)...)

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

	lines = append(lines, cases...)
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
		if m.statusBad() {
			return lipgloss.NewStyle().Foreground(cRed).Render(" ✗ " + m.statusMsg)
		}
		return lipgloss.NewStyle().Foreground(cGreen).Render(" ✓ " + m.statusMsg)
	}
	if m.epicWaiting() != nil {
		return chips(m.width, "E", "approve epic plan", "j/k", "move", "+", "queue", "x", "unqueue",
			"a", "agent", "enter", "open", "esc", "dashboard")
	}
	if m.pendingCount() > 0 {
		return chips(m.width, "Y", "approve", "N", "reject", "j/k", "move", "+", "queue", "x", "unqueue",
			"a", "agent", "enter", "open", "esc", "dashboard")
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
		if pos := m.queueTask(cur.Task, cur.Item); pos > 0 {
			m.planSel = pos - 1
		}
	case "J", "K":
		if !have || cur.Queued == 0 {
			m.flash("only your queued tasks move: + queues this one")
			break
		}
		delta := 1
		if msg.String() == "K" {
			delta = -1
		}
		if i, err := m.backend.MoveQueued(ctx(), cur.Task, cur.Item.Ref(), delta); err != nil {
			m.flash("move failed: " + err.Error())
		} else {
			m.sync()
			m.planSel = i
		}
	case "Y", "N":
		c, ok := m.proposalFor(cur, have)
		if !ok {
			m.flash("no proposal to settle: select a PR that says approve")
			break
		}
		id := c.Proposal.ID
		if msg.String() == "Y" {
			if err := m.backend.ApproveProposal(ctx(), id); err != nil {
				m.flash("approve failed: " + err.Error())
			} else {
				m.sync()
				m.flash("approved: " + c.Proposal.Decision.Summary)
			}
			break
		}
		if err := m.backend.RejectProposal(ctx(), id); err != nil {
			m.flash("reject failed: " + err.Error())
		} else {
			m.sync()
			m.flash("rejected; " + c.Repo + " " + c.Check + " stays with you")
		}
	case "E":
		e := m.epicWaiting()
		if e == nil {
			m.flash("no epic plan waits for approval (gh-hud epic list)")
			break
		}
		if err := m.backend.ApproveEpic(ctx(), e.Ref, e.Plan.ID); err != nil {
			m.flash("approve failed: " + err.Error())
		} else {
			m.sync()
			m.flash("approved the plan for " + e.Ref)
		}
	case "x", "delete", "backspace":
		if !have || cur.Queued == 0 {
			break
		}
		if err := m.backend.Unqueue(ctx(), cur.Task, cur.Item.Ref()); err != nil {
			m.flash("unqueue failed: " + err.Error())
		} else {
			m.sync()
			m.flash("unqueued " + string(cur.Task) + " " + cur.Item.Ref())
		}
	}
	return m, nil
}

// queueTask adds a task to your queue, says so, and returns its position
// (0 when it failed).
func (m *Model) queueTask(task dispatch.Task, it model.Item) int {
	pos, err := m.backend.Enqueue(ctx(), task, it.URL)
	if err != nil {
		m.flash("can't queue: " + err.Error())
		return 0
	}
	m.sync()
	m.flash(fmt.Sprintf("queued %s %s (#%d): p shows the plan", task, it.Ref(), pos))
	return pos
}

// pendingCount is how many supervisor proposals wait for you.
func (m *Model) pendingCount() int {
	n := 0
	for _, c := range m.cases {
		if c.Proposal != nil {
			n++
		}
	}
	return n
}

// proposalFor is the case whose proposal Y/N settle: the selected PR's,
// else the only one waiting.
func (m *Model) proposalFor(cur dispatch.PlanRow, have bool) (dispatch.Case, bool) {
	var only []dispatch.Case
	for _, c := range m.cases {
		if c.Proposal == nil {
			continue
		}
		only = append(only, c)
		if have && cur.Item.Repo == c.Repo && (slices.Contains(c.PRs, cur.Item.Num) || cur.Item.Num == c.Proposal.Decision.FixPR) {
			return c, true
		}
	}
	if len(only) == 1 {
		return only[0], true
	}
	return dispatch.Case{}, false
}

// caseLines draws the open cases above the plan rows; none when there
// are none.
func (m *Model) caseLines(w int) []string {
	if len(m.cases) == 0 {
		return nil
	}
	dim := lipgloss.NewStyle().Foreground(cDim)
	fg := lipgloss.NewStyle().Foreground(cFg)
	out := []string{dim.Render("  ── cases " + strings.Repeat("─", max(w-12, 0)))}
	for _, c := range m.cases {
		where := fmt.Sprintf("%d PRs", len(c.PRs))
		if c.OnBase {
			where += ", main too"
		}
		head := "  ⚑ " + truncatePlain(c.Repo+" "+c.Check, 44) + "  " + where
		status, color := caseStatus(c)
		room := max(w-width(head)-3, 10)
		out = append(out, fg.Render(head)+"   "+lipgloss.NewStyle().Foreground(color).Render(truncatePlain(status, room)))
		if c.Proposal != nil && c.Proposal.Decision.Summary != "" {
			out = append(out, dim.Render("      "+truncatePlain(strings.Join(strings.Fields(c.Proposal.Decision.Summary), " "), max(w-8, 10))))
		}
	}
	return out
}

// caseStatus is what a case's supervisor line says.
func caseStatus(c dispatch.Case) (string, lipgloss.Color) {
	switch {
	case c.Proposal != nil:
		return "supervisor proposes " + caseReason(c.Proposal.Decision) + ": Y approve · N reject", cOrange
	case c.Decision != nil:
		return caseReason(*c.Decision) + " (" + c.Decision.By + ")", cDim
	case c.Supervising:
		return "supervisor looking…", cTeal
	}
	return "needs you", cOrange
}

func caseReason(d dispatch.Decision) string {
	if d.Kind == dispatch.DecideLinkFix {
		return fmt.Sprintf("fix in #%d", d.FixPR)
	}
	return "escalated: " + strings.Join(strings.Fields(d.Summary), " ")
}

// epicWaiting is the one epic whose plan waits for approval, or nil when
// none or several do (gh-hud epic approve picks among several).
func (m *Model) epicWaiting() *epic.Epic {
	var found *epic.Epic
	for i := range m.epics {
		e := &m.epics[i]
		if e.State == epic.StatePlanned && e.Plan != nil && !e.Plan.Stale {
			if found != nil {
				return nil
			}
			found = e
		}
	}
	return found
}

// epicShown is how many open pieces an epic lists before "+N more".
const epicShown = 6

var epicMark = map[epic.NodeState]string{
	epic.NodeDone: "✓", epic.NodeDropped: "✗", epic.NodeInProgress: "▸", epic.NodeReady: "●",
	epic.NodeWaiting: "◌", epic.NodeGated: "⏸", epic.NodeBlocked: "!", epic.NodeUnknown: "?",
}

// epicLines draws the enrolled epics: a line each, then their pieces still
// to do, ready and in progress first.
func (m *Model) epicLines(w int) []string {
	if len(m.epics) == 0 {
		return nil
	}
	dim := lipgloss.NewStyle().Foreground(cDim)
	fg := lipgloss.NewStyle().Foreground(cFg)
	out := []string{dim.Render("  ── epics " + strings.Repeat("─", max(w-12, 0)))}
	for _, e := range m.epics {
		head := "  ◆ " + truncatePlain(e.Ref+"  "+e.Title, max(w/2, 30))
		status, color := epicStatus(e)
		out = append(out, fg.Render(head)+"   "+lipgloss.NewStyle().Foreground(color).Render(truncatePlain(status, max(w-width(head)-3, 10))))
		if e.Plan == nil {
			continue
		}
		repo, _, _ := epic.ParseRef(e.Ref)
		var open []epic.Node
		for _, n := range e.Plan.Nodes {
			if n.State != epic.NodeDone && n.State != epic.NodeDropped {
				open = append(open, n)
			}
		}
		rank := map[epic.NodeState]int{epic.NodeReady: 0, epic.NodeInProgress: 1, epic.NodeGated: 2, epic.NodeBlocked: 3, epic.NodeWaiting: 4, epic.NodeUnknown: 5}
		slices.SortStableFunc(open, func(a, b epic.Node) int { return rank[a.State] - rank[b.State] })
		for i, n := range open {
			if i == epicShown {
				out = append(out, dim.Render(fmt.Sprintf("      +%d more (gh-hud epic list)", len(open)-epicShown)))
				break
			}
			line := fmt.Sprintf("      %s %s %s", epicMark[n.State], epic.Short(repo, n.Ref), n.Title)
			if n.Note != "" {
				line += "  · " + n.Note
			}
			style := dim
			if n.State == epic.NodeReady || n.State == epic.NodeInProgress {
				style = fg
			}
			out = append(out, style.Render(truncatePlain(line, max(w-2, 10))))
		}
	}
	return out
}

// epicStatus is an epic's line: where its plan stands and its counts.
func epicStatus(e epic.Epic) (string, lipgloss.Color) {
	switch e.State {
	case epic.StateReading:
		return "reading the plan…", cTeal
	case epic.StateError:
		return "can't read it: " + e.Error, cRed
	}
	c := e.Counts()
	counts := fmt.Sprintf("%d/%d done, %d ready, %d in progress", c[epic.NodeDone], len(e.Plan.Nodes), c[epic.NodeReady], c[epic.NodeInProgress])
	switch {
	case e.Plan.Stale:
		return "the epic changed: reread it · " + counts, cOrange
	case e.State == epic.StatePlanned:
		return "plan waits for you: E approves · " + counts, cOrange
	}
	return "approved · " + counts, cGreen
}

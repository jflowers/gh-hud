package ui

// The Workspaces tab (9): every checkout a dispatch could use, on the hub
// and the workers, with whether the next run could start there and what
// a cleanup would lose. The selected checkout's detail is below the table.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

const (
	wsStale = 30 * time.Second // opening the tab refreshes older data
)

// workspace table column widths (the notes column takes the rest)
const (
	wsMachW   = 6
	wsNameW   = 24
	wsRepoW   = 16
	wsBranchW = 22
	wsSyncW   = 8
	wsStateW  = 20
)

// readyCheckouts counts checkouts the next run could use, for the tab count.
func (m *Model) readyCheckouts() int {
	n := 0
	for _, h := range m.ws {
		if h.Ready() {
			n++
		}
	}
	return n
}

// wsCells is a checkout's glyph, color and state text.
func wsCells(h dispatch.CheckoutHealth) (glyph string, color lipgloss.Color, state string) {
	switch {
	case h.Unmanaged && h.Upkeeping:
		return "●", cTeal, "removing unmanaged"
	case h.Unmanaged && h.LastUpkeep != nil && h.LastUpkeep.Failed():
		return "◆", cYellow, "kept work"
	case h.Unmanaged:
		return "·", cComment, "unmanaged removal"
	case h.Err != "":
		return "✗", cRed, h.Err
	case h.Busy != "":
		// the row already names the repo; keep the PR/issue number
		ref := h.Busy
		if i := strings.LastIndex(ref, "#"); i >= 0 {
			ref = ref[i:]
		}
		return "●", cTeal, "running " + ref
	case h.Reindexing, h.Upkeeping:
		return "●", cTeal, h.Problem()
	case h.NoUpstream:
		return "◆", cYellow, h.Problem()
	case h.Problem() == "upkeep only":
		// a worker's plain dir: kept current for new instances, no runs
		return "◇", cDim, h.Problem()
	case !h.Ready():
		return "✗", cRed, h.Problem()
	}
	return "✓", cGreen, "ready"
}

// wsSync renders the default branch against upstream: ↓behind ↑ahead,
// "=" when even, "—" when unknown.
func wsSync(h dispatch.CheckoutHealth) string {
	if h.Behind < 0 || h.Ahead < 0 {
		return "—"
	}
	var parts []string
	if h.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", h.Behind))
	}
	if h.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", h.Ahead))
	}
	if len(parts) == 0 {
		return "="
	}
	return strings.Join(parts, " ")
}

// wsNotes lists what is not blocking but worth knowing.
func wsNotes(h dispatch.CheckoutHealth) string {
	var parts []string
	if n := h.LocalOnlyCommits(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d local-only", n))
	}
	if h.Stashes > 0 {
		parts = append(parts, fmt.Sprintf("%d stash", h.Stashes))
	}
	if h.Learnings > 0 {
		parts = append(parts, fmt.Sprintf("%d learnings", h.Learnings))
	}
	if r := h.LastReindex; r != nil && r.Failed() && !h.Reindexing {
		parts = append(parts, "reindex failed")
	}
	if u := h.LastUpkeep; u != nil && u.Failed() && !h.Upkeeping && !h.Unmanaged {
		parts = append(parts, "upkeep failed")
	}
	return strings.Join(parts, " · ")
}

func repoName(repo string) string {
	if _, name, ok := strings.Cut(repo, "/"); ok {
		return name
	}
	return repo
}

func (m *Model) workspacesView() string {
	comment := lipgloss.NewStyle().Foreground(cComment)
	dim := lipgloss.NewStyle().Foreground(cDim)
	fg := lipgloss.NewStyle().Foreground(cFg)
	w := m.width
	notesW := w - (4 + wsMachW + 1 + wsNameW + 1 + wsRepoW + 1 + wsBranchW + 1 + wsSyncW + 1 + wsStateW + 1 + wsIndexW + 1)
	if notesW < 0 {
		notesW = 0
	}
	now := time.Now()

	rows, detailH := m.agentsLayout()
	lines := []string{m.headerBar(), dim.Render(
		"    " + padPlain("where", wsMachW, false) + " " + padPlain("workspace", wsNameW, false) + " " +
			padPlain("repo", wsRepoW, false) + " " + padPlain("branch", wsBranchW, false) + " " +
			padPlain("upstream", wsSyncW, false) + " " + padPlain("state", wsStateW, false) + " " +
			padPlain("index", wsIndexW, false) + " " + padPlain("notes", notesW, false))}

	list := m.ws
	start, end := 0, len(list)
	if len(list) > rows {
		start = m.wsSel - rows/2
		if start < 0 {
			start = 0
		}
		if start > len(list)-rows {
			start = len(list) - rows
		}
		end = start + rows
	}
	if len(list) == 0 {
		msg := "    no checkouts"
		if m.wsLoading {
			msg = "    reading checkouts on every machine…"
		}
		lines = append(lines, comment.Render(msg))
	}
	for i := start; i < end; i++ {
		h := list[i]
		glyph, color, state := wsCells(h)
		mach := h.Machine
		if mach == "" {
			mach = "local"
		}
		branch := h.Branch
		if h.Default != "" && branch != h.Default {
			branch += " ←"
		}
		row := padPlain(mach, wsMachW, false) + " " + padPlain(h.Workspace, wsNameW, false) + " " +
			padPlain(repoName(h.Repo), wsRepoW, false) + " " + padPlain(branch, wsBranchW, false) + " " +
			padPlain(wsSync(h), wsSyncW, false) + " "
		st := padPlain(state, wsStateW, false)
		idxText, idxColor := wsIndex(h, now)
		idx := " " + padPlain(idxText, wsIndexW, false)
		tail := " " + padPlain(wsNotes(h), notesW, false)
		marker := "  "
		rowStyle, stStyle, tailStyle := fg, lipgloss.NewStyle().Foreground(color), comment
		idxStyle := lipgloss.NewStyle().Foreground(idxColor)
		if i == m.wsSel {
			marker = "▶ "
			rowStyle = rowStyle.Background(cSel).Bold(true)
			stStyle = stStyle.Background(cSel)
			idxStyle = idxStyle.Background(cSel)
			tailStyle = tailStyle.Background(cSel)
		}
		lines = append(lines, marker+lipgloss.NewStyle().Foreground(color).Render(glyph)+" "+
			rowStyle.Render(row)+stStyle.Render(st)+idxStyle.Render(idx)+tailStyle.Render(tail))
	}
	for len(lines) < 2+rows {
		lines = append(lines, "")
	}

	// separator: position, tallies, freshness
	pos := ""
	if len(list) > 0 {
		pos = fmt.Sprintf(" %d/%d ", m.wsSel+1, len(list))
	}
	if start > 0 {
		pos += fmt.Sprintf("↑ %d ", start)
	}
	if end < len(list) {
		pos += fmt.Sprintf("↓ %d ", len(list)-end)
	}
	busy, others := 0, 0
	for _, h := range list {
		switch {
		case h.Unmanaged:
			others++ // a worker's removal row, not a checkout
		case h.Busy != "" || h.Reindexing || h.Upkeeping:
			busy++
		}
	}
	if len(list) > others {
		ready := m.readyCheckouts()
		pos += fmt.Sprintf("· %d ready · %d running · %d blocked ", ready, busy, len(list)-others-ready-busy)
	}
	switch {
	case m.wsLoading:
		pos += "· refreshing… "
	case !m.wsAt.IsZero():
		if ago := model.RelativeTime(m.wsAt, time.Now()); ago == "now" {
			pos += "· read just now "
		} else {
			pos += "· read " + ago + " ago "
		}
	}
	sepW := w - width(pos) - 3
	if sepW < 0 {
		sepW = 0
	}
	lines = append(lines, dim.Render("──"+pos+strings.Repeat("─", sepW)))

	if detailH > 0 {
		var body []string
		if m.wsSel < len(list) {
			body = wsDetail(list[m.wsSel], w-4)
		}
		if len(body) > detailH {
			body = append(body[:detailH-1:detailH-1], comment.Render("…"))
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
	lines = append(lines, m.workspacesFooter())
	return strings.Join(lines, "\n")
}

// wsDetail is the detail pane body for a checkout.
func wsDetail(h dispatch.CheckoutHealth, w int) []string {
	fg := lipgloss.NewStyle().Foreground(cFg)
	comment := lipgloss.NewStyle().Foreground(cComment)
	key := lipgloss.NewStyle().Foreground(cComment).Width(12)
	field := func(k, v string) string { return key.Render(k) + fg.Render(truncatePlain(v, w-12)) }
	_, color, state := wsCells(h)
	where := h.Workspace
	if h.Machine != "" {
		where = h.Machine + ":" + where
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(color).Bold(true).Render(truncatePlain(where+"  "+h.Repo, w)),
		"",
	}
	if h.Unmanaged {
		return append(lines, unmanagedDetail(h, field)...)
	}
	switch {
	case h.Ready() && h.Default != "" && h.Branch != h.Default:
		state += " — a run here switches to " + h.Default + " and fast-forwards it"
	case h.Ready():
		state += " — a run here fast-forwards " + h.Default + " to upstream"
	case h.UpkeepOnly:
		state += " — ghws clones new instances from it: upkeep keeps it current, no run starts here"
	case len(h.Dirty) > 0 && h.Busy == "":
		state += " — a run here is refused until these are committed or discarded"
	case h.Ahead > 0 && h.Behind > 0:
		state += fmt.Sprintf(" — %s can't fast-forward to upstream/%s", h.Default, h.Default)
	}
	lines = append(lines, field("state", state), field("dir", h.Dir))
	if h.Branch != "" {
		b := h.Branch
		if h.Default != "" && h.Default != h.Branch {
			b += "   (default " + h.Default + ")"
		}
		lines = append(lines, field("branch", b))
	}
	if h.Behind >= 0 {
		lines = append(lines, field("upstream", fmt.Sprintf("%s is %d behind, %d ahead of upstream/%s (as of the last fetch)",
			h.Default, h.Behind, h.Ahead, h.Default)))
	}
	for _, kv := range wsIndexDetail(h, time.Now()) {
		lines = append(lines, field(kv[0], kv[1]))
	}
	if len(h.Dirty) > 0 {
		lines = append(lines, field("uncommitted", fmt.Sprintf("%d file(s)", len(h.Dirty))))
		for _, l := range h.Dirty {
			lines = append(lines, comment.Render(truncatePlain("            "+l, w)))
		}
	}
	if len(h.LocalOnly) > 0 {
		lines = append(lines, field("local-only", fmt.Sprintf("%d commit(s) on no remote; cleaning up loses them", h.LocalOnlyCommits())))
		for _, l := range h.LocalOnly {
			b, n, _ := strings.Cut(l, "\t")
			lines = append(lines, comment.Render(truncatePlain("            "+b+"  "+n, w)))
		}
	}
	if h.Stashes > 0 {
		lines = append(lines, field("stashes", fmt.Sprint(h.Stashes)))
	}
	if h.Learnings > 0 {
		lines = append(lines, field("learnings", fmt.Sprintf("%d untracked; the next clean run opens the daily chore PR", h.Learnings)))
	}
	if h.Reindexing {
		lines = append(lines, field("dewey", "reindexing the index; no dispatch here until it ends"))
	}
	if r := h.LastReindex; r != nil {
		lines = append(lines, field("last reindex", reindexLine(*r, time.Now())))
	}
	if h.Upkeeping {
		lines = append(lines, field("upkeep", "fetching and fast-forwarding; no dispatch here until it ends"))
	}
	if u := h.LastUpkeep; u != nil {
		lines = append(lines, field("last upkeep", truncatePlain(upkeepLine(*u, time.Now()), w-14)))
	}
	return lines
}

// unmanagedDetail is the detail pane body for a worker's removal of what
// isn't the fleet's (remove_unmanaged): what it removed (or would), what
// it kept because it holds work found nowhere else, what it left.
func unmanagedDetail(h dispatch.CheckoutHealth, field func(k, v string) string) []string {
	lines := []string{field("root", h.Dir)}
	if h.Upkeeping {
		lines = append(lines, field("state", "removing what isn't the fleet's"))
	}
	u := h.LastUpkeep
	if u == nil {
		return lines
	}
	lines = append(lines, field("last run", u.At.Local().Format("Jan 2 15:04")+" ("+model.RelativeTime(u.At, time.Now())+" ago)"))
	verb := "removed"
	if u.Report {
		verb = "to remove"
	}
	for _, d := range u.Did {
		lines = append(lines, field(verb, strings.TrimPrefix(d, "remove ")))
	}
	if u.Err != "" {
		lines = append(lines, field("kept", u.Err))
	}
	for _, l := range u.Left {
		lines = append(lines, field("left", l))
	}
	if len(u.Did) == 0 && u.Err == "" && len(u.Left) == 0 {
		lines = append(lines, field("state", "nothing to remove"))
	}
	return lines
}

// upkeepLine renders a last upkeep for the detail pane: "fast-forwarded
// main by 12, Nov 19 09:30 (5m ago)".
func upkeepLine(u dispatch.UpkeepResult, now time.Time) string {
	return u.String() + ", " + u.At.Local().Format("Jan 2 15:04") + " (" + model.RelativeTime(u.At, now) + " ago)"
}

// reindexLine renders a last reindex for the detail pane: "ok in 3m12s,
// Nov 18 09:30 (2h ago), for index format 0, this Dewey writes 1; now
// format 1 by 3.9.0".
func reindexLine(r dispatch.ReindexResult, now time.Time) string {
	s := r.String() + ", " + r.At.Local().Format("Jan 2 15:04") + " (" + model.RelativeTime(r.At, now) + " ago)"
	if len(r.Reasons) > 0 {
		s += ", for " + strings.Join(r.Reasons, ", ")
	}
	if r.DeweyVersion != "" || r.IndexFormat > 0 {
		s += "; now " + indexBuiltBy(r.DeweyVersion, r.IndexFormat)
	}
	return s
}

// indexBuiltBy names what built an index: "format 1 by 3.9.0".
func indexBuiltBy(version string, format int) string {
	s := fmt.Sprintf("format %d", format)
	if version != "" {
		s += " by " + version
	}
	return s
}

func (m *Model) workspacesFooter() string {
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		if m.statusBad() {
			return lipgloss.NewStyle().Foreground(cRed).Render(" ✗ " + m.statusMsg)
		}
		return lipgloss.NewStyle().Foreground(cGreen).Render(" ✓ " + m.statusMsg)
	}
	return chips(m.width, "j/k", "move", "y", "copy dir", "r", "refresh", "0-9", "tabs", "esc", "dashboard")
}

func (m *Model) handleWorkspacesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "9":
		m.wsMode = false
	case "0", "1", "2", "3", "4", "5", "6", "7", "8":
		return m, m.switchTab(msg.String())
	case "?":
		m.help = true
	case "F":
		if m.fleetOn {
			m.wsMode = false
			m.openFleet()
		}
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.wsSel < len(m.ws)-1 {
			m.wsSel++
		}
	case "k", "up":
		if m.wsSel > 0 {
			m.wsSel--
		}
	case "g", "home":
		m.wsSel = 0
	case "G", "end":
		if len(m.ws) > 0 {
			m.wsSel = len(m.ws) - 1
		}
	case "y":
		if m.wsSel < len(m.ws) {
			dir := m.ws[m.wsSel].Dir
			if err := clipboard.Copy(dir); err != nil {
				m.flash("copy failed: " + err.Error())
			} else {
				m.flash("copied " + dir)
			}
		}
	case "r":
		if m.dispatchOn && !m.wsLoading {
			m.flash("reading checkouts…")
			m.refreshWorkspaces()
		}
	}
	return m, nil
}

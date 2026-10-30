package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// rowsStartY is the screen line of the first item row (header bar + column
// header precede it). app.go uses it for mouse hit-testing.
const rowsStartY = 2

const (
	kindW  = 6
	stateW = 5
	numW   = 6
	ageW   = 4
	thW    = 4
)

// layout holds the column widths for a given terminal width.
type layout struct {
	termW   int
	repoW   int
	whoW    int // 0 = hidden
	threads bool
	titleW  int
}

func layoutFor(termW int) layout {
	l := layout{termW: termW, threads: true}
	switch {
	case termW >= 140:
		l.repoW, l.whoW = 26, 12
	case termW >= 120:
		l.repoW, l.whoW = 22, 12
	case termW >= 100:
		l.repoW, l.whoW = 20, 10
	case termW >= 80:
		l.repoW = 18
	default:
		l.repoW, l.threads = 14, false
	}
	// gutter+sp, kind+sp, attn+sp, state+sp, num+2sp, repo+sp, title, age+trailing
	fixed := 2 + kindW + 1 + 2 + stateW + 1 + numW + 2 + l.repoW + 1 + 1 + ageW + 1
	if l.threads {
		fixed += 1 + thW
	}
	if l.whoW > 0 {
		fixed += 1 + l.whoW
	}
	l.titleW = termW - fixed
	if l.titleW < 10 {
		l.titleW = 10
	}
	return l
}

// ---------------------------------------------------------------------------
// rows

// agentMark is the per-repo agent state for the attention slot.
type agentMark struct {
	running bool
	blocked bool
	failed  bool
}

type rowCtx struct {
	me     string
	bots   *model.BotFilter
	now    time.Time
	agents map[string]agentMark
}

func renderRepo(p paint, repo string, w int) string {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return p.text(cSubtle, padPlain(repo, w, false))
	}
	if width(repo) > w {
		owner = initials(owner)
	}
	if room := w - width(owner) - 1; width(name) > room {
		name = truncatePlain(name, room)
	}
	used := width(owner) + 1 + width(name)
	return p.text(cDim, owner) + p.text(cDim, "/") + p.text(cSubtle, name) + p.space(w-used)
}

func renderTitle(p paint, it model.Item, w int) string {
	var tags []string
	tagW := 0
	for _, r := range visibleReasons(it) {
		tags = append(tags, p.text(reasonColor(r), r))
		tagW += width(r) + 1
	}
	if it.Kind == model.KindRun && it.RunBranch != "" {
		s := it.RunBranch
		if it.RunEvent != "" {
			s += " · " + it.RunEvent
		}
		tags = append(tags, p.text(cComment, s))
		tagW += width(s) + 1
	}
	if tagW > w/2 {
		tags, tagW = nil, 0
	}
	if tagW > 0 {
		tagW++ // extra gap between title and tags
	}

	title := truncatePlain(it.Title, w-tagW)
	ts := p.fg(cFg)
	switch {
	case it.Draft:
		ts = p.fg(cDim).Italic(true)
	case it.OtherAppr, it.Quiet():
		ts = p.fg(cComment)
	}
	out := ts.Render(title)
	used := width(title)
	if len(tags) > 0 {
		out += p.space(2) + strings.Join(tags, p.space(1))
		used += tagW + 1
	}
	return out + p.space(w-used)
}

func ageColor(age string) lipgloss.Color {
	switch {
	case age == "now", strings.HasSuffix(age, "m") && !strings.HasSuffix(age, "mo"):
		return cCyan
	case strings.HasSuffix(age, "h"):
		return cSubtle
	default:
		return cDim
	}
}

func renderRow(it model.Item, l layout, rc rowCtx, selected, hover bool) string {
	p := paint{}
	switch {
	case selected:
		p.bg = cSel
	case hover:
		p.bg = cHover
	}
	sp := p.space(1)
	var b strings.Builder

	// gutter
	if selected {
		b.WriteString(p.text(cBlue, "▌"))
	} else {
		b.WriteString(sp)
	}
	b.WriteString(sp)

	// kind badge
	label, color := kindBadge(it)
	if it.Quiet() {
		label, color = "later", cDim
	}
	b.WriteString(p.fg(color).Bold(selected).Render(padPlain(label, kindW, false)))
	b.WriteString(sp)

	// attention marker: agent state outranks ready and waiting-on-you
	if mark := rc.agents[it.Repo]; mark.blocked {
		b.WriteString(p.text(cOrange, "◆"))
	} else if mark.running {
		b.WriteString(p.text(cTeal, "●"))
	} else if mark.failed {
		b.WriteString(p.text(cRed, "◆"))
	} else if it.Cats&model.CatReady != 0 {
		b.WriteString(p.text(cGreen, "◆"))
	} else if it.WaitingOn == "me" {
		b.WriteString(p.text(cYellow, "◆"))
	} else {
		b.WriteString(sp)
	}
	b.WriteString(sp)

	// state cluster: ci review merge
	if it.Kind == model.KindPR || it.Kind == model.KindRun {
		g, c := ciGlyph(it.CI)
		b.WriteString(p.text(c, g))
		b.WriteString(sp)
		g, c = reviewGlyph(it.Review)
		b.WriteString(p.text(c, g))
		b.WriteString(sp)
		g, c = mergeGlyph(it.Merge)
		b.WriteString(p.text(c, g))
	} else {
		b.WriteString(p.space(stateW))
	}
	b.WriteString(sp)

	// #N — the only hyperlinked cell (Cmd+click in iTerm2)
	if it.Num > 0 {
		num := fmt.Sprintf("#%d", it.Num)
		if width(num) > numW {
			num = truncatePlain(num, numW)
		}
		b.WriteString(p.space(numW - width(num)))
		b.WriteString(hyperlink(it.URL, p.text(cDim, "#")+p.text(cSubtle, num[1:])))
	} else {
		b.WriteString(p.space(numW))
	}
	b.WriteString(p.space(2))

	b.WriteString(renderRepo(p, it.Repo, l.repoW))
	b.WriteString(sp)
	b.WriteString(renderTitle(p, it, l.titleW))

	if l.threads {
		b.WriteString(sp)
		// Human threads are what need a reply; bot-only threads show dim.
		n, c := it.Thread, cYellow
		if n == 0 {
			n, c = it.BotThread, cDim
		}
		t := ""
		switch {
		case n > 9:
			t = "✎9+"
		case n > 0:
			t = fmt.Sprintf("✎%d", n)
		}
		b.WriteString(p.text(c, padPlain(t, thW, false)))
	}

	if l.whoW > 0 {
		b.WriteString(sp)
		who := rc.bots.Mask(it.LastActor)
		switch {
		case who == "":
			b.WriteString(p.space(l.whoW))
		case who == rc.me:
			b.WriteString(p.text(cDim, padPlain("you", l.whoW, false)))
		default:
			b.WriteString(p.text(cTeal, padPlain(who, l.whoW, false)))
		}
	}

	b.WriteString(sp)
	age := model.RelativeTime(it.UpdatedAt, rc.now)
	b.WriteString(p.text(ageColor(age), padPlain(age, ageW, true)))
	b.WriteString(sp)

	out := b.String()
	if pad := l.termW - width(out); pad > 0 {
		out += p.space(pad)
	}
	return out
}

func headerRow(l layout) string {
	d := lipgloss.NewStyle().Foreground(cDim)
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(d.Render(padPlain("kind", kindW, false)))
	b.WriteString("   ")
	b.WriteString(d.Render(padPlain("state", stateW, false)))
	b.WriteString(" ")
	b.WriteString(d.Render(padPlain("#", numW, true)))
	b.WriteString("  ")
	b.WriteString(d.Render(padPlain("repo", l.repoW, false)))
	b.WriteString(" ")
	b.WriteString(d.Render(padPlain("title", l.titleW, false)))
	if l.threads {
		b.WriteString(" ")
		b.WriteString(d.Render(padPlain("✎", thW, false)))
	}
	if l.whoW > 0 {
		b.WriteString(" ")
		b.WriteString(d.Render(padPlain("who", l.whoW, false)))
	}
	b.WriteString(" ")
	b.WriteString(d.Render(padPlain("age", ageW, true)))
	return b.String()
}

// ---------------------------------------------------------------------------
// header bar

type tab struct {
	label string
	cat   model.Cats
	color lipgloss.Color
	key   string
}

var tabs = []tab{
	{"All", 0, cFg, "0"},
	{"Review", model.CatRQ, cYellow, "1"},
	{"Mine", model.CatMy, cBlue, "2"},
	{"Issues", model.CatISS, cGreen, "3"},
	{"CI", model.CatCI, cRed, "4"},
	{"Inbox", model.CatNT, cMagenta, "5"},
	{"Ready", model.CatReady, cGreen, "7"},
	{"Queue", model.CatQueue, cCyan, "8"},
}

func shortDur(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	default:
		return fmt.Sprintf("%dh", s/3600)
	}
}

func (m *Model) headerBar() string {
	p := paint{bg: cSurface}
	sp := p.space(1)

	var left strings.Builder
	left.WriteString(lipgloss.NewStyle().Background(cBlue).Foreground(cInk).Bold(true).Render(" gh-hud "))
	left.WriteString(sp)
	if u := m.me(); u != "" {
		left.WriteString(p.text(cSubtle, u))
		left.WriteString(p.space(3))
	}

	var items []model.Item
	if m.res != nil {
		items = m.res.Items
	}
	for _, t := range tabs {
		n := 0
		for _, it := range items {
			if t.cat == 0 || it.Cats&t.cat != 0 {
				n++
			}
		}
		active := m.filter == t.cat
		tp := p
		if active {
			tp = paint{bg: cTab}
		}
		labelColor := lipgloss.TerminalColor(cComment)
		if active {
			labelColor = cFg
		}
		countColor := lipgloss.TerminalColor(t.color)
		if n == 0 {
			countColor = cDim
		}
		left.WriteString(tp.space(1))
		left.WriteString(tp.fg(labelColor).Bold(active).Render(t.label))
		left.WriteString(tp.space(1))
		left.WriteString(tp.text(countColor, fmt.Sprint(n)))
		left.WriteString(tp.space(1))
		left.WriteString(sp)
	}

	var right []string
	if m.searchText != "" && !m.searchMode {
		right = append(right, p.text(cCyan, "/"+m.searchText))
	}
	if m.res != nil && len(m.res.Errors) > 0 {
		right = append(right, p.text(cRed, fmt.Sprintf("⚠ %d failed", len(m.res.Errors))))
	}
	every := shortDur(time.Duration(m.cfg.RefreshSecs) * time.Second)
	switch {
	case m.fetching:
		right = append(right, p.text(cYellow, "⟳ refreshing"))
	case !m.lastFetch.IsZero():
		right = append(right, p.text(cComment,
			fmt.Sprintf("updated %s ago · every %s", shortDur(time.Since(m.lastFetch)), every)))
	}
	r := strings.Join(right, p.text(cDim, "  ·  "))
	if r != "" {
		r += sp
	}

	l := left.String()
	gap := m.width - width(l) - width(r)
	if gap < 1 {
		r, gap = "", m.width-width(l)
	}
	if gap < 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(l)
	}
	return l + p.space(gap) + r
}

// ---------------------------------------------------------------------------
// footer

func chips(maxW int, pairs ...string) string {
	key := lipgloss.NewStyle().Foreground(cBlue)
	lbl := lipgloss.NewStyle().Foreground(cDim)
	out := " "
	for i := 0; i+1 < len(pairs); i += 2 {
		c := key.Render(pairs[i]) + " " + lbl.Render(pairs[i+1])
		if width(out)+width(c)+3 > maxW {
			break
		}
		if i > 0 {
			out += "   "
		}
		out += c
	}
	return out
}

func (m *Model) footer() string {
	if m.searchMode {
		return lipgloss.NewStyle().Foreground(cCyan).Render(" /"+m.searchText+"█") +
			lipgloss.NewStyle().Foreground(cDim).Render("   enter keeps · esc clears")
	}
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		if strings.Contains(m.statusMsg, "failed") {
			return lipgloss.NewStyle().Foreground(cRed).Render(" ✗ " + m.statusMsg)
		}
		return lipgloss.NewStyle().Foreground(cGreen).Render(" ✓ " + m.statusMsg)
	}
	return chips(m.width,
		"click", "open",
		gestureLabel(m.cfg.URLClick), "copy url",
		gestureLabel(m.cfg.RefClick), "copy ref",
		"y/Y", "ref/url",
		"r", "refresh",
		"a", "agent",
		"6", "agents",
		"0-8", "filter",
		"/", "find",
		"?", "help",
		"q", "quit",
	)
}

// ---------------------------------------------------------------------------
// help

func (m *Model) helpView() string {
	k := lipgloss.NewStyle().Foreground(cBlue).Width(18)
	d := lipgloss.NewStyle().Foreground(cFg)
	h := lipgloss.NewStyle().Foreground(cMagenta).Bold(true)
	g := func(glyph string, c lipgloss.Color, desc string) string {
		return lipgloss.NewStyle().Foreground(c).Width(7).Render(glyph) + d.Render(desc)
	}
	line := func(key, desc string) string { return k.Render(key) + d.Render(desc) }

	left := []string{
		h.Render("Mouse"),
		line("click", "open in browser"),
		line(gestureLabel(m.cfg.URLClick), "copy URL"),
		line(gestureLabel(m.cfg.RefClick), "copy owner/repo#N"),
		line("wheel", "scroll"),
		"",
		h.Render("Keys"),
		line("j k ↑ ↓", "move"),
		line("g G", "top / bottom"),
		line("enter o", "open"),
		line("y  Y", "copy ref / URL"),
		line("0-8", "All Review Mine Issues CI Inbox Ready Queue"),
		line("/", "find (esc clears)"),
		line("r", "refresh now"),
		line("a", "agent actions for row"),
		line("6", "agent sessions"),
		line("q", "quit"),
	}
	right := []string{
		h.Render("Glyphs"),
		g("◆", cYellow, "waiting on you"),
		g("◆", cGreen, "your PR ready for maintainer review"),
		g("◆", cCyan, "queued for agent review"),
		g("●", cTeal, "agent active on repo"),
		g("◆", cRed, "agent dispatch failed"),
		g("◆", cOrange, "agent blocked (needs input)"),
		g("✓", cGreen, "CI passing / approved"),
		g("✗", cRed, "CI failing / changes requested"),
		g("●", cYellow, "CI running / mixed reviews"),
		g("!", cOrange, "you requested changes"),
		g("○", cDim, "no checks"),
		g("⊘", cOrange, "merge conflict"),
		g("✎", cYellow, "unresolved human review threads"),
		g("✎", cDim, "unresolved bot threads only"),
		g("later", cDim, "quiet issue (no recent activity)"),
	}
	if m.res != nil && len(m.res.Errors) > 0 {
		right = append(right, "", h.Foreground(cRed).Render("Errors"))
		for _, e := range m.res.Errors {
			right = append(right, lipgloss.NewStyle().Foreground(cRed).Render(
				truncatePlain(e.Error(), 60)))
		}
	}

	// Stack the sections when they fit; otherwise put them side by side.
	stacked := strings.Join(append(append(append([]string{}, left...), ""), right...), "\n")
	body := stacked
	padV := 1
	if lipgloss.Height(stacked)+4 > m.height {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			strings.Join(left, "\n"), "      ", strings.Join(right, "\n"))
		if lipgloss.Height(body)+4 > m.height {
			padV = 0
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(padV, 3).
		Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// ---------------------------------------------------------------------------
// agent modals

func (m *Model) actionView() string {
	title := lipgloss.NewStyle().Foreground(cMagenta).Bold(true)
	key := lipgloss.NewStyle().Foreground(cBlue).Width(10)
	sel := lipgloss.NewStyle().Foreground(cFg).Bold(true)
	comment := lipgloss.NewStyle().Foreground(cComment)

	ref := m.actionItem.Ref()
	if ref == "" {
		ref = m.actionItem.URL
	}
	lines := []string{
		title.Render(" dispatch agent ") + comment.Render(ref+" · "+truncatePlain(m.actionItem.Title, 48)),
		"",
	}
	for i, a := range m.actions {
		marker := "  "
		task := key.Render(string(a.task))
		if i == m.actionSel {
			marker = sel.Render("▶ ")
			task = sel.Render(string(a.task))
		}
		lines = append(lines, marker+task+comment.Render(" "+a.cmd+"  ·  "+a.co.Workspace))
	}
	lines = append(lines, "", comment.Render("enter dispatch · j/k move · esc cancel"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) agentsView() string {
	title := lipgloss.NewStyle().Foreground(cMagenta).Bold(true)
	sel := lipgloss.NewStyle().Foreground(cFg).Bold(true)
	fg := lipgloss.NewStyle().Foreground(cFg)
	comment := lipgloss.NewStyle().Foreground(cComment)

	list := m.visibleAgents()
	hidden := len(m.agents) - len(list)
	sub := "dispatched, live, and recent sessions"
	if m.agentsAll {
		sub = "all opencode sessions in ghws workspaces"
	}
	lines := []string{
		title.Render(" agent sessions ") + comment.Render(sub),
		"",
	}
	if len(list) == 0 {
		lines = append(lines, comment.Render("no agent sessions"))
	}
	// viewport: border(2) + padding(2) + header(2) + footer(2) + scroll hints(2)
	maxRows := m.height - 10
	if maxRows < 3 {
		maxRows = 3
	}
	start, end := 0, len(list)
	if len(list) > maxRows {
		start = m.agentsSel - maxRows/2
		if start < 0 {
			start = 0
		}
		if start > len(list)-maxRows {
			start = len(list) - maxRows
		}
		end = start + maxRows
	}
	if start > 0 {
		lines = append(lines, comment.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		s := list[i]
		glyph, color := "○", cDim
		task := s.Task
		switch {
		case s.Running:
			glyph, color = "●", cTeal
		case s.Blocked:
			glyph, color = "◆", cOrange
		case s.Failed:
			glyph, color = "◆", cRed
		case task != "":
			glyph, color = "✓", cGreen
		}
		if task == "" {
			task = "—"
		}
		name := s.Ref
		if name == "" {
			name = truncatePlain(s.Title, 32)
		}
		ws := s.Workspace
		if ws == "" {
			ws = "—"
		}
		age := model.RelativeTime(s.UpdatedAt, time.Now())
		marker := "  "
		if i == m.agentsSel {
			marker = sel.Render("▶ ")
		}
		g := lipgloss.NewStyle().Foreground(color).Render(glyph)
		info := ws
		if s.State != "" {
			info += "  ·  " + s.State
		}
		info += "  ·  " + age
		lines = append(lines, marker+g+" "+comment.Render(padPlain(string(task), 9, false))+" "+fg.Render(name)+
			comment.Render("  ·  "+info))
	}
	if end < len(list) {
		lines = append(lines, comment.Render(fmt.Sprintf("  ↓ %d more", len(list)-end)))
	}
	toggle := "a show all"
	if m.agentsAll {
		toggle = "a hide stale"
	} else if hidden > 0 {
		toggle = fmt.Sprintf("a show %d stale", hidden)
	}
	lines = append(lines, "", comment.Render("enter watch · y copy id · r refresh · "+toggle+" · esc close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// ---------------------------------------------------------------------------
// View

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting gh-hud…"
	}
	if m.fetchErr != "" {
		msg := lipgloss.NewStyle().Foreground(cRed).Bold(true).Render("gh-hud could not reach GitHub") +
			"\n\n" + lipgloss.NewStyle().Foreground(cFg).Render(m.fetchErr) +
			"\n\n" + lipgloss.NewStyle().Foreground(cDim).Render("check `gh auth status` · press q to quit")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}
	if m.help {
		return m.helpView()
	}
	if m.actionMode {
		return m.actionView()
	}
	if m.agentsMode {
		return m.agentsView()
	}

	l := layoutFor(m.width)
	lines := []string{m.headerBar(), headerRow(l)}
	maxRows := m.rowsFit()

	switch {
	case m.res == nil:
		lines = append(lines, "", lipgloss.NewStyle().Foreground(cComment).Render("   fetching from GitHub…"))
	default:
		vis := m.visible()
		if len(vis) == 0 {
			msg := "✓ nothing needs your attention"
			if m.filter != 0 || m.searchText != "" {
				msg = "nothing matches this filter"
			}
			lines = append(lines, "", lipgloss.NewStyle().Foreground(cGreen).Render("   "+msg))
		}
		rc := rowCtx{me: m.me(), bots: m.bots, now: time.Now(), agents: m.agentMarks()}
		end := m.offset + maxRows
		if end > len(vis) {
			end = len(vis)
		}
		for i := m.offset; i < end; i++ {
			lines = append(lines, renderRow(vis[i], l, rc, i == m.cursor, i == m.hover))
		}
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.footer())
	return strings.Join(lines, "\n")
}

// PlainTable renders every item once, for --once (no selection, no paging).
func PlainTable(res *model.FetchResult, cfg *config.Config, termW int, now time.Time) string {
	l := layoutFor(termW)
	rc := rowCtx{me: res.User, bots: model.NewBotFilter(cfg.KnownBots), now: now}
	var b strings.Builder
	b.WriteString(headerRow(l))
	b.WriteString("\n")
	if len(res.Items) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(cGreen).Render("  ✓ nothing needs your attention"))
		b.WriteString("\n")
	}
	for _, it := range res.Items {
		b.WriteString(strings.TrimRight(renderRow(it, l, rc, false, false), " "))
		b.WriteString("\n")
	}
	for _, e := range res.Errors {
		b.WriteString(lipgloss.NewStyle().Foreground(cRed).Render("⚠ " + e.Error()))
		b.WriteString("\n")
	}
	return b.String()
}

// Package ui implements the gh-hud terminal dashboard.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

type tickMsg time.Time

type fetchedMsg struct {
	res *model.FetchResult
}

type agentsMsg struct {
	sessions []dispatch.Session
	err      error
}

type dispatchDoneMsg struct {
	task   string
	ref    string
	ws     string
	runner string
	err    error
}

type autoDoneMsg []string

// actionChoice is one dispatchable task for the row under the cursor.
type actionChoice struct {
	task dispatch.Task
	cmd  string
	co   ghws.Checkout
}

// Model is the bubbletea model for the dashboard.
type Model struct {
	cfg    *config.Config
	client *gh.Client
	bots   *model.BotFilter
	disp   *dispatch.Dispatcher
	idx    *ghws.Index

	res       *model.FetchResult
	fetching  bool
	lastFetch time.Time
	fetchErr  string

	cursor        int
	offset        int
	hover         int
	prevCursorURL string

	filter     model.Cats
	searchMode bool
	searchText string
	help       bool

	actionMode bool
	actionSel  int
	actionItem model.Item
	actions    []actionChoice

	agentsMode bool
	agents     []dispatch.Session
	agentsSel  int
	lastAgents time.Time

	statusMsg string
	statusAt  time.Time

	width, height int
}

// New builds the dashboard model. disp and idx may be nil (dispatch off).
func New(cfg *config.Config, client *gh.Client, disp *dispatch.Dispatcher, idx *ghws.Index) *Model {
	// No OSC-8 links in the TUI: iTerm2 would open ⌘-clicked links itself
	// *and* report the click to us (double tab), and it swallows ⌘-right-click
	// on links. The app handles every click itself; --once keeps links.
	hyperlinks = false
	return &Model{
		cfg:    cfg,
		client: client,
		bots:   model.NewBotFilter(cfg.KnownBots),
		disp:   disp,
		idx:    idx,
		hover:  -1,
	}
}

// Init starts the first fetch and the clock.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchCmd(m.cfg, m.client), tick()}
	if m.disp != nil {
		cmds = append(cmds, m.fetchAgents())
	}
	return tea.Batch(cmds...)
}

func fetchCmd(cfg *config.Config, c *gh.Client) tea.Cmd {
	return func() tea.Msg {
		return fetchedMsg{model.FetchAll(context.Background(), c, cfg)}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles clock ticks, fetch results, keys, and mouse.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		if !m.fetching && m.res != nil &&
			time.Since(m.lastFetch) >= time.Duration(m.cfg.RefreshSecs)*time.Second {
			m.fetching = true
			m.prevCursorURL = m.cursorURL()
			cmds = append(cmds, fetchCmd(m.cfg, m.client))
		}
		if m.disp != nil && time.Since(m.lastAgents) >= 30*time.Second {
			m.lastAgents = time.Now()
			cmds = append(cmds, m.fetchAgents())
		}
		return m, tea.Batch(cmds...)

	case fetchedMsg:
		m.fetching = false
		m.res = msg.res
		m.lastFetch = time.Now()
		if msg.res.User == "" {
			for _, e := range msg.res.Errors {
				if e.Section == "user" {
					m.fetchErr = e.Err.Error()
				}
			}
		} else {
			m.fetchErr = ""
			m.restoreCursor()
		}
		if m.disp != nil && msg.res.User != "" {
			return m, tea.Batch(autoCmd(m, msg.res.Items), m.fetchAgents())
		}
		return m, nil

	case agentsMsg:
		if msg.err == nil {
			m.agents = msg.sessions
			if m.agentsSel >= len(m.agents) {
				m.agentsSel = len(m.agents) - 1
			}
			if m.agentsSel < 0 {
				m.agentsSel = 0
			}
		}
		return m, nil

	case dispatchDoneMsg:
		if msg.err != nil {
			m.flash(fmt.Sprintf("dispatch %s %s failed: %s", msg.task, msg.ref, msg.err))
		} else {
			flash := fmt.Sprintf("dispatched %s %s in %s", msg.task, msg.ref, msg.ws)
			if msg.runner != "" {
				flash += " via " + msg.runner
			}
			m.flash(flash)
		}
		return m, m.fetchAgents()

	case autoDoneMsg:
		if len(msg) > 0 {
			m.flash(strings.Join(msg, " · "))
			return m, m.fetchAgents()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampOffset()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(tea.MouseEvent(msg))

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.fetchErr != "" {
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.actionMode {
		return m.handleActionKey(msg)
	}
	if m.agentsMode {
		return m.handleAgentsKey(msg)
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if m.searchMode {
		switch msg.String() {
		case "esc":
			m.searchMode = false
			m.searchText = ""
			m.clampCursor()
		case "enter":
			m.searchMode = false
		case "backspace":
			r := []rune(m.searchText)
			if len(r) > 0 {
				m.searchText = string(r[:len(r)-1])
			}
			m.clampCursor()
		case "ctrl+c":
			return m, tea.Quit
		default:
			if s := msg.String(); len([]rune(s)) == 1 {
				m.searchText += s
				m.clampCursor()
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor++
		m.clampCursor()
	case "k", "up":
		m.cursor--
		m.clampCursor()
	case "g":
		m.cursor = 0
		m.offset = 0
	case "G":
		if n := len(m.visible()); n > 0 {
			m.cursor = n - 1
		}
		m.clampCursor()
	case "enter", "o":
		if it, ok := m.selected(); ok {
			m.open(it)
		}
	case "y":
		if it, ok := m.selected(); ok {
			m.copyRef(it)
		}
	case "Y":
		if it, ok := m.selected(); ok {
			m.copyURL(it)
		}
	case "r":
		if !m.fetching {
			m.fetching = true
			m.flash("refreshing…")
			m.prevCursorURL = m.cursorURL()
			return m, fetchCmd(m.cfg, m.client)
		}
	case "a":
		m.openAction()
	case "6":
		if m.disp != nil {
			m.agentsMode = true
			m.agentsSel = 0
			return m, m.fetchAgents()
		}
	case "1":
		m.setFilter(model.CatRQ)
	case "2":
		m.setFilter(model.CatMy)
	case "3":
		m.setFilter(model.CatISS)
	case "4":
		m.setFilter(model.CatCI)
	case "5":
		m.setFilter(model.CatNT)
	case "0":
		m.setFilter(0)
	case "/":
		m.searchMode = true
	case "?":
		m.help = true
	}
	return m, nil
}

func (m *Model) handleMouse(e tea.MouseEvent) (tea.Model, tea.Cmd) {
	if logMouse(e) {
		return m, nil
	}
	if m.help || m.actionMode || m.agentsMode || m.fetchErr != "" {
		return m, nil
	}
	if e.IsWheel() {
		switch e.Button {
		case tea.MouseButtonWheelUp:
			m.offset -= 3
		case tea.MouseButtonWheelDown:
			m.offset += 3
		}
		m.clampOffset()
		return m, nil
	}
	if e.Action == tea.MouseActionMotion {
		m.hover = m.rowAt(e.Y)
		return m, nil
	}
	if e.Action == tea.MouseActionPress {
		idx := m.rowAt(e.Y)
		if idx < 0 {
			return m, nil
		}
		m.cursor = idx
		m.ensureVisible()
		it := m.visible()[idx]
		switch g := gestureOf(e); {
		case g == m.cfg.URLClick:
			m.copyURL(it)
		case g == m.cfg.RefClick:
			m.copyRef(it)
		case e.Button == tea.MouseButtonLeft:
			m.open(it)
		case e.Button == tea.MouseButtonRight:
			m.copyRef(it)
		}
	}
	return m, nil
}

// gestureOf names a press the way config gestures are spelled. iTerm2
// reports ⌘ as the meta (alt) bit, since real ⌥-clicks never reach the app.
func gestureOf(e tea.MouseEvent) string {
	g := ""
	switch {
	case e.Alt:
		g = "cmd+"
	case e.Shift:
		g = "shift+"
	case e.Ctrl:
		g = "ctrl+"
	}
	switch e.Button {
	case tea.MouseButtonLeft:
		return g + "click"
	case tea.MouseButtonRight:
		return g + "right"
	}
	return g + "other"
}

// rowAt maps a screen row to an index into the visible items (-1 if none).
func (m *Model) rowAt(y int) int {
	if m.res == nil {
		return -1
	}
	maxRows := m.rowsFit()
	if maxRows < 1 {
		return -1
	}
	idx := m.offset + (y - rowsStartY)
	if y < rowsStartY || idx < 0 || idx >= len(m.visible()) || idx >= m.offset+maxRows {
		return -1
	}
	return idx
}

func (m *Model) rowsFit() int {
	return m.height - 3 // status + header + footer
}

func (m *Model) visible() []model.Item {
	if m.res == nil {
		return nil
	}
	var out []model.Item
	q := strings.ToLower(m.searchText)
	for _, it := range m.res.Items {
		if m.filter != 0 && it.Cats&m.filter == 0 {
			continue
		}
		if q != "" {
			hay := strings.ToLower(it.Repo + " " + it.Title + " " + it.Ref())
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

func (m *Model) me() string {
	if m.res != nil {
		return m.res.User
	}
	return ""
}

func (m *Model) setFilter(f model.Cats) {
	if m.filter == f {
		m.filter = 0
	} else {
		m.filter = f
	}
	m.clampCursor()
}

func (m *Model) flash(msg string) {
	m.statusMsg = msg
	m.statusAt = time.Now()
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	if n == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor > n-1 {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureVisible()
}

func (m *Model) ensureVisible() {
	maxRows := m.rowsFit()
	if maxRows < 1 {
		maxRows = 1
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+maxRows {
		m.offset = m.cursor - maxRows + 1
	}
	m.clampOffset()
}

func (m *Model) clampOffset() {
	n := len(m.visible())
	maxRows := m.rowsFit()
	if maxRows < 1 {
		maxRows = 1
	}
	if m.offset > n-maxRows {
		m.offset = n - maxRows
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) selected() (model.Item, bool) {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return model.Item{}, false
	}
	return vis[m.cursor], true
}

func (m *Model) cursorURL() string {
	if it, ok := m.selected(); ok {
		return it.URL
	}
	return ""
}

// restoreCursor keeps the cursor on the same item across refreshes.
func (m *Model) restoreCursor() {
	if m.prevCursorURL != "" {
		for i, it := range m.visible() {
			if it.URL == m.prevCursorURL {
				m.cursor = i
				m.ensureVisible()
				break
			}
		}
	}
	m.prevCursorURL = ""
	m.clampCursor()
}

func displayRef(it model.Item) string {
	if r := it.Ref(); r != "" {
		return r
	}
	return it.URL
}

func (m *Model) open(it model.Item) {
	if err := clipboard.OpenURL(it.URL); err != nil {
		m.flash("open failed: " + err.Error())
		return
	}
	m.flash("opened " + displayRef(it))
}

func (m *Model) copyURL(it model.Item) {
	if err := clipboard.Copy(it.URL); err != nil {
		m.flash("copy failed: " + err.Error())
		return
	}
	m.flash("copied URL " + it.URL)
}

func (m *Model) copyRef(it model.Item) {
	ref := displayRef(it)
	if err := clipboard.Copy(ref); err != nil {
		m.flash("copy failed: " + err.Error())
		return
	}
	m.flash("copied " + ref)
}

// fetchAgents polls agent session status (nil cmd when dispatch is off).
func (m *Model) fetchAgents() tea.Cmd {
	d := m.disp
	if d == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sessions, err := d.Sessions(ctx)
		return agentsMsg{sessions: sessions, err: err}
	}
}

// autoCmd runs allowlisted auto-dispatch for the freshly fetched items.
func autoCmd(m *Model, items []model.Item) tea.Cmd {
	d := m.disp
	if d == nil {
		return nil
	}
	ix := m.idx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return autoDoneMsg(d.AutoDispatch(ctx, items, ix))
	}
}

func (m *Model) handleActionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.actionMode = false
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.actionSel < len(m.actions)-1 {
			m.actionSel++
		}
	case "k", "up":
		if m.actionSel > 0 {
			m.actionSel--
		}
	case "enter", "o":
		m.actionMode = false
		if m.actionSel < len(m.actions) {
			choice := m.actions[m.actionSel]
			m.flash("dispatching " + string(choice.task) + " " + m.actionItem.Ref() + "…")
			return m, m.dispatchCmd(choice)
		}
	}
	return m, nil
}

func (m *Model) handleAgentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "6":
		m.agentsMode = false
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.agentsSel < len(m.agents)-1 {
			m.agentsSel++
		}
	case "k", "up":
		if m.agentsSel > 0 {
			m.agentsSel--
		}
	case "y":
		if m.agentsSel < len(m.agents) {
			id := m.agents[m.agentsSel].ID
			if err := clipboard.Copy(id); err != nil {
				m.flash("copy failed: " + err.Error())
			} else {
				m.flash("copied " + id)
			}
		}
	case "enter", "o":
		if m.agentsSel < len(m.agents) {
			s := m.agents[m.agentsSel]
			if err := m.disp.Watch(s); err != nil {
				m.flash("watch failed: " + err.Error())
			} else {
				m.flash("watching " + s.Title)
			}
		}
	case "r":
		return m, m.fetchAgents()
	}
	return m, nil
}

// openAction builds the dispatch menu for the selected row.
func (m *Model) openAction() {
	if m.disp == nil || m.idx == nil {
		m.flash("dispatch unavailable")
		return
	}
	it, ok := m.selected()
	if !ok {
		return
	}
	tasks := dispatch.InferTasks(it)
	m.actionItem = it
	m.actions = nil
	m.actionSel = 0
	for _, task := range tasks {
		cmd, ok := m.disp.Configured(task)
		if !ok {
			continue
		}
		cos := m.idx.Resolve(it.Repo, it.HeadBranch)
		if len(cos) == 0 {
			continue
		}
		m.actions = append(m.actions, actionChoice{task: task, cmd: cmd, co: cos[0]})
		if len(m.actions) >= 8 {
			break
		}
	}
	if len(m.actions) == 0 {
		if len(tasks) == 0 {
			m.flash("no agent actions for this row")
		} else {
			m.flash("no ghws checkout for " + it.Repo)
		}
		return
	}
	m.actionMode = true
}

func (m *Model) dispatchCmd(c actionChoice) tea.Cmd {
	d := m.disp
	it := m.actionItem
	co := c.co
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rec, err := d.Dispatch(ctx, c.task, it, co)
		return dispatchDoneMsg{task: string(c.task), ref: it.Ref(), ws: co.Workspace, runner: rec.Runner, err: err}
	}
}

// agentMarks maps repo → agent state for the per-row attention markers.
func (m *Model) agentMarks() map[string]agentMark {
	if m.idx == nil || len(m.agents) == 0 {
		return nil
	}
	marks := make(map[string]agentMark)
	for _, s := range m.agents {
		repo := m.idx.RepoFor(s.Directory)
		if repo == "" {
			continue
		}
		mk := marks[repo]
		if s.Running {
			mk.running = true
		}
		if s.Blocked {
			mk.blocked = true
		}
		if s.Failed {
			mk.failed = true
		}
		marks[repo] = mk
	}
	return marks
}

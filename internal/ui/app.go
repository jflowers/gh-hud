// Package ui implements the gh-hud terminal dashboard.
package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
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
	runs     []dispatch.Run
	usage    []dispatch.MachineUsage
	err      error
}

type workspacesMsg []dispatch.CheckoutHealth

type dispatchDoneMsg struct {
	task    string
	ref     string
	ws      string
	machine string
	runner  string
	err     error
}

type autoDoneMsg []string

// actionChoice is one dispatchable task for the row under the cursor.
type actionChoice struct {
	task   dispatch.Task
	cmd    string
	target string // "workspace" locally, "machine:workspace" remotely
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
	agentsAll  bool // show stale sessions too (default: only relevant ones)
	lastAgents time.Time

	// wsMode is the Workspaces tab (9): every dispatchable checkout's
	// health, refreshed every wsRefresh (one ssh per worker) or on r.
	wsMode    bool
	ws        []dispatch.CheckoutHealth
	wsSel     int
	wsAt      time.Time
	wsLoading bool

	// fleetMode is the GitOps rollout view (F); fleet is nil when [fleet]
	// is disabled. fleetSnap is refreshed from the coordinator on tick.
	fleetMode     bool
	fleet         *fleet.Coordinator
	fleetSnap     fleet.Snapshot
	skipWaitArmed bool
	skipWaitAt    time.Time

	// machineUsage is the per-machine slot line for the status area.
	machineUsage []dispatch.MachineUsage

	// readySeen dedupes maintainer-ready notifications: keyed by ref,
	// rebuilt each fetch; the first fetch only seeds it.
	readySeen map[string]bool
	// blockedSeen dedupes long-blocked agent notifications: keyed by
	// session ID, dropped once the agent is no longer blocked.
	blockedSeen map[string]bool

	// runs are the dispatch runs, newest first (see runs.go); runView is
	// the open run detail view. finishedSeen dedupes finish notifications
	// (nil until the first agents refresh seeds it).
	runs         []dispatch.Run
	runView      *dispatch.Run
	runScroll    int
	finishedSeen map[string]bool

	statusMsg string
	statusAt  time.Time

	width, height int
}

// New builds the dashboard model. disp, idx, and fleetCoord may be nil
// (dispatch / fleet off).
func New(cfg *config.Config, client *gh.Client, disp *dispatch.Dispatcher, idx *ghws.Index, fleetCoord *fleet.Coordinator) *Model {
	// No OSC-8 links in the TUI: iTerm2 would open ⌘-clicked links itself
	// *and* report the click to us (double tab), and it swallows ⌘-right-click
	// on links. The app handles every click itself; --once keeps links.
	hyperlinks = false
	m := &Model{
		cfg:    cfg,
		client: client,
		bots:   model.NewBotFilter(cfg.KnownBots),
		disp:   disp,
		idx:    idx,
		fleet:  fleetCoord,
		hover:  -1,
	}
	m.refreshFleetSnap()
	return m
}

// refreshFleetSnap copies the coordinator's state for rendering.
func (m *Model) refreshFleetSnap() {
	if m.fleet != nil {
		m.fleetSnap = m.fleet.Snapshot()
	}
}

// Init starts the first fetch and the clock.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchCmd(m.cfg, m.client), tick()}
	if m.disp != nil {
		cmds = append(cmds, m.fetchAgents(), m.fetchWorkspaces())
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
		if m.disp != nil && time.Since(m.wsAt) >= wsRefresh {
			cmds = append(cmds, m.fetchWorkspaces())
		}
		if m.fleet != nil {
			m.refreshFleetSnap()
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
		m.notifyReady(msg.res)
		if m.disp != nil && msg.res.User != "" {
			return m, tea.Batch(autoCmd(m, msg.res.Items), m.fetchAgents())
		}
		return m, nil

	case agentsMsg:
		if msg.err == nil {
			m.agents = msg.sessions
			m.runs = msg.runs
			m.machineUsage = msg.usage
			m.notifyBlocked(msg.sessions)
			m.notifyFinished(msg.runs)
			m.refreshRunView()
			if n := len(m.visibleAgents()); m.agentsSel >= n {
				m.agentsSel = n - 1
			}
			if m.agentsSel < 0 {
				m.agentsSel = 0
			}
		}
		return m, nil

	case workspacesMsg:
		m.ws, m.wsAt, m.wsLoading = msg, time.Now(), false
		if m.wsSel >= len(m.ws) {
			m.wsSel = len(m.ws) - 1
		}
		if m.wsSel < 0 {
			m.wsSel = 0
		}
		return m, nil

	case dispatchDoneMsg:
		if msg.err != nil {
			m.flash(fmt.Sprintf("dispatch %s %s failed: %s", msg.task, msg.ref, msg.err))
		} else {
			flash := fmt.Sprintf("dispatched %s %s in %s", msg.task, msg.ref, msg.ws)
			if msg.machine != "" {
				flash += " on " + msg.machine
			}
			if msg.runner != "" {
				flash += " via " + msg.runner
			}
			m.flash(flash)
		}
		return m, m.fetchAgents()

	case reinspectMsg:
		if msg.err != nil {
			m.flash("re-check failed: " + msg.err.Error())
			return m, nil
		}
		if len(msg.run.Left) == 0 {
			m.flash("re-check: nothing left behind in " + runAt(msg.run))
		} else {
			m.flash(fmt.Sprintf("re-check: %d item(s) still left in %s", len(msg.run.Left), runAt(msg.run)))
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
	if m.help {
		m.help = false
		return m, nil
	}
	if m.runView != nil {
		return m.handleRunKey(msg)
	}
	if m.actionMode {
		return m.handleActionKey(msg)
	}
	if m.agentsMode {
		return m.handleAgentsKey(msg)
	}
	if m.wsMode {
		return m.handleWorkspacesKey(msg)
	}
	if m.fleetMode {
		return m.handleFleetKey(msg)
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
	case "i":
		if m.disp != nil {
			m.openRowRun()
		}
	case "F":
		if m.fleet != nil {
			m.fleetMode = true
			m.skipWaitArmed = false
			m.refreshFleetSnap()
		}
	case "6", "9":
		return m, m.switchTab(msg.String())
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
	case "7":
		m.setFilter(model.CatReady)
	case "8":
		m.setFilter(model.CatQueue)
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
	if m.help || m.actionMode || m.agentsMode || m.wsMode || m.fleetMode || m.fetchErr != "" {
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

// fetchAgents polls agent session status and machine slot usage (nil cmd
// when dispatch is off).
func (m *Model) fetchAgents() tea.Cmd {
	d := m.disp
	if d == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sessions, err := d.Sessions(ctx)
		// Runs and slot usage get their own deadline: a slow session scan
		// must not expire the herdr calls that say which agents are live.
		live, cancelLive := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelLive()
		usage := d.MachineUsage(live)
		return agentsMsg{sessions: sessions, runs: d.Runs(live), usage: usage, err: err}
	}
}

// autoCmd runs allowlisted auto-dispatch for the freshly fetched items.
func autoCmd(m *Model, items []model.Item) tea.Cmd {
	d := m.disp
	if d == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return autoDoneMsg(d.AutoDispatch(ctx, items))
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

// agentRecentWindow is how long an idle, undispatched session stays in the
// agents panel before it is hidden as stale.
const agentRecentWindow = 24 * time.Hour

// visibleAgents is the agents panel list: by default only sessions gh-hud
// dispatched, live/blocked/failed ones, and anything touched recently.
func (m *Model) visibleAgents() []dispatch.Session {
	if m.agentsAll {
		return m.agents
	}
	cutoff := time.Now().Add(-agentRecentWindow)
	var out []dispatch.Session
	for _, s := range m.agents {
		if s.Task != "" || s.Running || s.Blocked || s.Failed || s.UpdatedAt.After(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

func (m *Model) handleAgentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	list := m.visibleAgents()
	switch msg.String() {
	case "esc", "q", "6":
		m.agentsMode = false
	case "0", "1", "2", "3", "4", "5", "7", "8", "9":
		return m, m.switchTab(msg.String())
	case "?":
		m.help = true
	case "F":
		if m.fleet != nil {
			m.agentsMode = false
			m.fleetMode = true
			m.skipWaitArmed = false
			m.refreshFleetSnap()
		}
	case "t":
		if m.agentsSel < len(list) {
			r, ok := m.sessionRun(list[m.agentsSel])
			if !ok {
				m.flash("not a gh-hud dispatch")
			} else if err := m.disp.OpenTranscript(r); err != nil {
				m.flash("transcript failed: " + err.Error())
			} else {
				m.flash("opening transcript " + r.SessionID)
			}
		}
	case "ctrl+c":
		return m, tea.Quit
	case "a":
		m.agentsAll = !m.agentsAll
		m.agentsSel = 0
	case "j", "down":
		if m.agentsSel < len(list)-1 {
			m.agentsSel++
		}
	case "k", "up":
		if m.agentsSel > 0 {
			m.agentsSel--
		}
	case "g", "home":
		m.agentsSel = 0
	case "G", "end":
		if len(list) > 0 {
			m.agentsSel = len(list) - 1
		}
	case "y":
		if m.agentsSel < len(list) {
			id := list[m.agentsSel].ID
			if err := clipboard.Copy(id); err != nil {
				m.flash("copy failed: " + err.Error())
			} else {
				m.flash("copied " + id)
			}
		}
	case "enter", "o":
		if m.agentsSel < len(list) {
			s := list[m.agentsSel]
			if err := m.disp.Watch(s); err != nil {
				m.flash("watch failed: " + err.Error())
			} else {
				m.flash("watching " + s.Title)
			}
		}
	case "i":
		if m.agentsSel < len(list) {
			if id := list[m.agentsSel].RunID; id == "" || !m.openRun(id) {
				m.flash("not a gh-hud dispatch")
			}
		}
	case "r":
		return m, m.fetchAgents()
	}
	return m, nil
}

// switchTab opens the tab bound to key from the Agents or Workspaces tab
// (or, for 6 and 9, from the dashboard): a view tab, or a dashboard filter.
func (m *Model) switchTab(key string) tea.Cmd {
	m.agentsMode, m.wsMode = false, false
	switch key {
	case "6":
		if m.disp == nil {
			return nil
		}
		m.agentsMode = true
		m.agentsSel = 0
		return m.fetchAgents()
	case "9":
		if m.disp == nil {
			return nil
		}
		m.wsMode = true
		m.wsSel = 0
		if time.Since(m.wsAt) >= wsStale {
			return m.fetchWorkspaces()
		}
		return nil
	}
	for _, t := range tabs {
		if t.key == key {
			m.filter = t.cat
			m.clampCursor()
		}
	}
	return nil
}

// handleFleetKey drives the fleet rollout view. `s` (skip the drain wait)
// needs a second press within skipWaitWindow — there is no confirm-modal
// pattern in the codebase, so the double press is the confirmation.
const skipWaitWindow = 5 * time.Second

func (m *Model) handleFleetKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "F":
		m.fleetMode = false
		m.skipWaitArmed = false
	case "ctrl+c":
		return m, tea.Quit
	case "p":
		if m.fleet != nil {
			paused := !m.fleetSnap.Paused
			m.fleet.SetPaused(paused)
			if paused {
				m.flash("fleet rollout paused")
			} else {
				m.flash("fleet rollout resumed")
			}
		}
	case "a":
		if m.fleet != nil {
			m.fleet.ApplyNow()
			m.flash("fleet poll forced")
		}
	case "s":
		if m.fleet == nil {
			return m, nil
		}
		if m.skipWaitArmed && time.Since(m.skipWaitAt) < skipWaitWindow {
			m.skipWaitArmed = false
			m.fleet.SkipWait()
			m.flash("skipping drain wait — applying with live agents")
		} else {
			m.skipWaitArmed = true
			m.skipWaitAt = time.Now()
			m.flash("press s again to skip the drain wait")
		}
	case "r":
		if m.fleet != nil {
			m.fleet.Retry()
			m.flash("fleet retrying failed machine")
		}
	}
	m.refreshFleetSnap()
	return m, nil
}

// openAction builds the dispatch menu for the selected row.
func (m *Model) openAction() {
	if m.disp == nil {
		m.flash("dispatch unavailable")
		return
	}
	it, ok := m.selected()
	if !ok {
		return
	}
	tasks := dispatch.MenuTasks(it)
	m.actionItem = it
	m.actions = nil
	m.actionSel = 0
	for _, choice := range m.disp.MenuChoices(it) {
		m.actions = append(m.actions, actionChoice{task: choice.Task, cmd: choice.Cmd, target: choice.Target})
		if len(m.actions) >= 8 {
			break
		}
	}
	if len(m.actions) == 0 {
		if len(tasks) == 0 {
			m.flash("no agent actions for this row")
		} else {
			m.flash("no idle ghws checkout for " + it.Repo)
		}
		return
	}
	m.actionMode = true
}

func (m *Model) dispatchCmd(c actionChoice) tea.Cmd {
	d := m.disp
	it := m.actionItem
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rec, err := d.Dispatch(ctx, c.task, it, false)
		return dispatchDoneMsg{task: string(c.task), ref: it.Ref(), ws: rec.Workspace, machine: rec.Machine, runner: rec.Runner, err: err}
	}
}

// notifyReady sends a macOS desktop notification for each of my PRs that
// just became ready for a human maintainer review. The first fetch after
// startup only seeds the seen-set so pre-existing ready PRs stay quiet.
func (m *Model) notifyReady(res *model.FetchResult) {
	if !m.cfg.Notify || res.User == "" {
		return
	}
	ready := map[string]bool{}
	for _, it := range res.Items {
		if it.Cats&model.CatReady != 0 {
			if ref := it.Ref(); ref != "" {
				ready[ref] = true
			}
		}
	}
	if m.readySeen == nil {
		m.readySeen = ready
		return
	}
	for ref := range ready {
		if !m.readySeen[ref] {
			desktopNotify(fmt.Sprintf("%s ready for maintainer review", ref))
		}
	}
	m.readySeen = ready
}

// notifyBlocked sends a desktop notification once per block when a
// gh-hud-dispatched agent has been blocked longer than
// blocked_notify_secs. Shorter blocks are the auto-question plugin
// deciding; a long one means the question went to a human.
func (m *Model) notifyBlocked(sessions []dispatch.Session) {
	if !m.cfg.Notify || m.cfg.BlockedNotifySecs <= 0 {
		return
	}
	limit := time.Duration(m.cfg.BlockedNotifySecs) * time.Second
	still := map[string]bool{}
	for _, s := range sessions {
		if !s.Blocked || s.Task == "" || s.BlockedSince.IsZero() {
			continue
		}
		still[s.ID] = m.blockedSeen[s.ID]
		if still[s.ID] || time.Since(s.BlockedSince) < limit {
			continue
		}
		still[s.ID] = true
		where := "local"
		if s.Machine != "" {
			where = s.Machine
		}
		desktopNotify(fmt.Sprintf("%s %s needs input (blocked %s on %s, pane %s)",
			s.Task, s.Ref, time.Since(s.BlockedSince).Round(time.Minute), where, s.PaneID))
	}
	m.blockedSeen = still
}

// desktopNotify posts a macOS notification via osascript, fire-and-forget
// (a variable so tests can capture notifications).
var desktopNotify = func(text string) {
	script := fmt.Sprintf("display notification %q with title \"gh-hud\"", text)
	cmd := exec.Command("osascript", "-e", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

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
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
)

type tickMsg time.Time

// snapshotMsg and eventMsg carry what the hub publishes; ok is false once
// the subscription has ended.
type snapshotMsg struct {
	snap hub.Snapshot
	ok   bool
}

type eventMsg struct {
	ev hub.Event
	ok bool
}

type dispatchDoneMsg struct {
	task    string
	ref     string
	ws      string
	machine string
	runner  string
	err     error
}

// actionChoice is one dispatchable task for the row under the cursor.
type actionChoice struct {
	task   dispatch.Task
	cmd    string
	target string // "workspace" locally, "machine:workspace" remotely
}

// Model is the bubbletea model for the dashboard.
type Model struct {
	cfg     *config.Config
	backend hub.Backend // nil only in tests that render one frame
	bots    *model.BotFilter
	// dispatchOn and fleetOn say whether the hub has a dispatcher and a
	// fleet coordinator; every action on them goes through backend.
	dispatchOn bool
	fleetOn    bool

	// snaps and events are the hub subscription (see Init); seq is the
	// last snapshot applied.
	snaps  <-chan hub.Snapshot
	events <-chan hub.Event
	seq    uint64
	// stages is each item's pipeline stage by URL, plan the Plan tab's
	// rows, and queue your queue, all from the hub.
	stages map[string]dispatch.Stage
	plan   []dispatch.PlanRow
	queue  []dispatch.QueueEntry
	// cases are what the rules can't settle, with the supervisor's
	// proposals (the Plan tab shows them).
	cases []dispatch.Case
	// epics are the enrolled epics (the Plan tab shows them).
	epics []epic.Epic

	res       *model.FetchResult
	fetching  bool
	lastFetch time.Time
	fetchErr  string

	cursor int
	offset int
	hover  int

	filter     model.Cats
	searchMode bool
	searchText string
	help       bool

	actionMode  bool
	actionQueue bool // the menu queues the task instead of starting it
	actionSel   int
	actionItem  model.Item
	actions     []actionChoice

	// planMode is the Plan tab (p): what starts next, and your queue.
	planMode bool
	planSel  int

	agentsMode bool
	agents     []dispatch.Session
	agentsSel  int
	agentsAll  bool // show stale sessions too (default: only relevant ones)

	// wsMode is the Workspaces tab (9): every dispatchable checkout's
	// health, refreshed by the hub every couple of minutes or on r.
	wsMode    bool
	ws        []dispatch.CheckoutHealth
	wsSel     int
	wsAt      time.Time
	wsLoading bool

	// fleetMode is the GitOps rollout view (F), shown when fleetOn.
	fleetMode     bool
	fleetSnap     fleet.Snapshot
	skipWaitArmed bool
	skipWaitAt    time.Time

	// machineUsage is the per-machine slot line for the status area.
	machineUsage []dispatch.MachineUsage
	// follower is set when another gh-hud owns dispatch: this one shows
	// runs but never dispatches, merges or notifies. Its value is the
	// owner's pid ("?" when unknown).
	follower string

	// runs are the dispatch runs, newest first (see runs.go); runView is
	// the open run detail view.
	runs      []dispatch.Run
	runView   *dispatch.Run
	runScroll int

	statusMsg string
	statusAt  time.Time

	width, height int
}

// New builds the dashboard model over a hub, which it reads state from and
// sends every command to.
func New(cfg *config.Config, backend hub.Backend) *Model {
	// No OSC-8 links in the TUI: iTerm2 would open ⌘-clicked links itself
	// *and* report the click to us (double tab), and it swallows ⌘-right-click
	// on links. The app handles every click itself; --once keeps links.
	hyperlinks = false
	m := &Model{
		cfg:     cfg,
		backend: backend,
		bots:    model.NewBotFilter(cfg.KnownBots),
		hover:   -1,
	}
	m.sync()
	return m
}

// sync applies the hub's current state now, so a command's effect shows
// before its snapshot arrives (and the next key acts on fresh rows).
func (m *Model) sync() {
	if m.backend != nil {
		m.applySnapshot(m.backend.Snapshot())
	}
}

// ctx is the context for backend calls made from the UI loop.
func ctx() context.Context { return context.Background() }

// Init subscribes to the hub and starts the clock (which only re-renders
// ages and expires the status line; the hub does the refreshing).
func (m *Model) Init() tea.Cmd {
	if m.backend == nil {
		return tick()
	}
	m.snaps, m.events = m.backend.Subscribe(context.Background())
	return tea.Batch(m.waitSnapshot(), m.waitEvent(), tick())
}

func (m *Model) waitSnapshot() tea.Cmd {
	ch := m.snaps
	return func() tea.Msg {
		s, ok := <-ch
		return snapshotMsg{snap: s, ok: ok}
	}
}

func (m *Model) waitEvent() tea.Cmd {
	ch := m.events
	return func() tea.Msg {
		e, ok := <-ch
		return eventMsg{ev: e, ok: ok}
	}
}

// applySnapshot copies the hub's state into the view, keeping the cursor
// on the same item when the list changes.
func (m *Model) applySnapshot(s hub.Snapshot) {
	if s.Seq != 0 && s.Seq <= m.seq {
		return
	}
	m.seq = s.Seq
	keep := m.cursorURL()
	newRes := s.Res != m.res
	m.res, m.fetching, m.lastFetch, m.fetchErr = s.Res, s.Fetching, s.FetchedAt, s.FetchErr
	m.agents, m.runs, m.machineUsage, m.follower = s.Sessions, s.Runs, s.Usage, s.Follower
	m.ws, m.wsAt, m.wsLoading = s.Workspaces, s.WorkspacesAt, s.WorkspacesLoading
	m.stages, m.plan, m.queue = s.Stages, s.Plan, s.Queue
	m.cases, m.epics = s.Cases, s.Epics
	m.dispatchOn, m.fleetOn = s.Dispatch, s.Fleet != nil
	if s.Fleet != nil {
		m.fleetSnap = *s.Fleet
	}
	if newRes && m.res != nil && m.fetchErr == "" {
		m.restoreCursor(keep)
	}
	m.refreshRunView()
	m.agentsSel = clampSel(m.agentsSel, len(m.visibleAgents()))
	m.wsSel = clampSel(m.wsSel, len(m.ws))
}

func clampSel(sel, n int) int {
	if sel >= n {
		sel = n - 1
	}
	if sel < 0 {
		sel = 0
	}
	return sel
}

// Hub triggers. They only ask; the result arrives as a snapshot. They
// return no command so call sites read `return m, m.refreshAgents()`.

func (m *Model) refreshAgents() tea.Cmd {
	if m.backend != nil && m.dispatchOn {
		_ = m.backend.RefreshAgents(ctx())
	}
	return nil
}

func (m *Model) refreshWorkspaces() {
	if m.backend != nil && m.dispatchOn {
		m.wsLoading = true // until the hub says otherwise
		_ = m.backend.RefreshWorkspaces(ctx())
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles clock ticks, fetch results, keys, and mouse.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tick()

	case snapshotMsg:
		if !msg.ok {
			return m, nil
		}
		m.applySnapshot(msg.snap)
		return m, m.waitSnapshot()

	case eventMsg:
		if !msg.ok {
			return m, nil
		}
		if msg.ev.Kind == hub.EventNotice {
			m.flash(msg.ev.Text)
		}
		return m, m.waitEvent()

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
		return m, nil // the hub refreshes agents after a dispatch

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
	if m.planMode {
		return m.handlePlanKey(msg)
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
		if !m.fetching && m.backend != nil {
			m.fetching = true // until the hub says otherwise
			m.flash("refreshing…")
			_ = m.backend.Refresh(ctx())
		}
	case "a":
		m.openAction()
	case "+":
		if it, ok := m.selected(); ok && m.dispatchOn {
			m.openActionFor(it, true)
		}
	case "p":
		return m, m.switchTab("p")
	case "i":
		if m.dispatchOn {
			m.openRowRun()
		}
	case "F":
		m.openFleet()
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
	if m.help || m.actionMode || m.planMode || m.agentsMode || m.wsMode || m.fleetMode || m.fetchErr != "" {
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

// statusBad reports whether the status line says something went wrong
// (drawn red with ✗ rather than green with ✓).
func (m *Model) statusBad() bool {
	for _, w := range []string{"failed", "can't", "lost the hub"} {
		if strings.Contains(m.statusMsg, w) {
			return true
		}
	}
	return false
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

// restoreCursor puts the cursor back on the item at url after the list
// changed (it stays at its index when that item is gone).
func (m *Model) restoreCursor(url string) {
	if url != "" {
		for i, it := range m.visible() {
			if it.URL == url {
				m.cursor = i
				m.ensureVisible()
				break
			}
		}
	}
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
			if m.actionQueue {
				m.queueTask(choice.task, m.actionItem)
				return m, nil
			}
			m.flash("dispatching " + string(choice.task) + " " + m.actionItem.Ref() + "…")
			return m, m.dispatchCmd(choice)
		}
	case "+":
		m.actionMode = false
		if m.actionSel < len(m.actions) {
			m.queueTask(m.actions[m.actionSel].task, m.actionItem)
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
		if m.fleetOn {
			m.agentsMode = false
			m.openFleet()
		}
	case "t":
		if m.agentsSel < len(list) {
			r, ok := m.sessionRun(list[m.agentsSel])
			if !ok {
				m.flash("not a gh-hud dispatch")
			} else if err := m.backend.OpenTranscript(ctx(), r.ID); err != nil {
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
			if err := m.backend.Watch(ctx(), s.ID); err != nil {
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
		return m, m.refreshAgents()
	}
	return m, nil
}

// switchTab opens the tab bound to key from the Agents or Workspaces tab
// (or, for 6 and 9, from the dashboard): a view tab, or a dashboard filter.
func (m *Model) switchTab(key string) tea.Cmd {
	m.agentsMode, m.wsMode, m.planMode = false, false, false
	switch key {
	case "p":
		if !m.dispatchOn {
			return nil
		}
		m.planMode = true
		m.planSel = 0
		return nil
	case "6":
		if !m.dispatchOn {
			return nil
		}
		m.agentsMode = true
		m.agentsSel = 0
		return m.refreshAgents()
	case "9":
		if !m.dispatchOn {
			return nil
		}
		m.wsMode = true
		m.wsSel = 0
		if time.Since(m.wsAt) >= wsStale && !m.wsLoading {
			m.refreshWorkspaces()
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

// openFleet opens the fleet rollout view (when the fleet is on).
func (m *Model) openFleet() {
	if m.fleetOn {
		m.fleetMode = true
		m.skipWaitArmed = false
		m.sync()
	}
}

func (m *Model) handleFleetKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// fleetDo runs a fleet control and flashes done, or why it failed.
	fleetDo := func(err error, done string) {
		if err != nil {
			m.flash("fleet: " + err.Error() + " (failed)")
			return
		}
		m.flash(done)
		m.sync()
	}
	switch msg.String() {
	case "esc", "q", "F":
		m.fleetMode = false
		m.skipWaitArmed = false
	case "ctrl+c":
		return m, tea.Quit
	case "p":
		if m.fleetSnap.Paused {
			fleetDo(m.backend.FleetPause(ctx(), false), "fleet rollout resumed")
		} else {
			fleetDo(m.backend.FleetPause(ctx(), true), "fleet rollout paused")
		}
	case "a":
		fleetDo(m.backend.FleetApplyNow(ctx()), "fleet poll forced")
	case "s":
		if m.skipWaitArmed && time.Since(m.skipWaitAt) < skipWaitWindow {
			m.skipWaitArmed = false
			fleetDo(m.backend.FleetSkipWait(ctx()), "skipping drain wait — applying with live agents")
		} else {
			m.skipWaitArmed = true
			m.skipWaitAt = time.Now()
			m.flash("press s again to skip the drain wait")
		}
	case "r":
		fleetDo(m.backend.FleetRetry(ctx()), "fleet retrying failed machine")
	}
	return m, nil
}

// openAction builds the dispatch menu for the selected row.
func (m *Model) openAction() {
	if !m.dispatchOn {
		m.flash("dispatch unavailable")
		return
	}
	if it, ok := m.selected(); ok {
		m.openActionFor(it, false)
	}
}

// openActionFor builds the agent menu for an item. With queue set the menu
// adds the chosen task to your queue, which needs no free checkout yet.
func (m *Model) openActionFor(it model.Item, queue bool) {
	if !m.dispatchOn {
		m.flash("dispatch unavailable")
		return
	}
	tasks := dispatch.MenuTasks(it)
	m.actionItem = it
	m.actionQueue = queue
	m.actions = nil
	m.actionSel = 0
	if queue {
		opts, err := m.backend.QueueChoices(ctx(), it.URL)
		if err != nil {
			m.flash("can't queue: " + err.Error())
			return
		}
		for _, t := range opts {
			m.actions = append(m.actions, actionChoice{task: t, target: "your queue"})
		}
		if len(m.actions) == 0 {
			m.flash("no agent tasks to queue for this row")
			return
		}
		m.actionMode = true
		return
	}
	choices, err := m.backend.MenuChoices(ctx(), it.URL)
	if err != nil {
		m.flash("agent menu failed: " + err.Error())
		return
	}
	for _, choice := range choices {
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
	b := m.backend
	it := m.actionItem
	return func() tea.Msg {
		rec, err := b.Dispatch(ctx(), c.task, it.URL)
		return dispatchDoneMsg{task: string(c.task), ref: it.Ref(), ws: rec.Workspace, machine: rec.Machine, runner: rec.Runner, err: err}
	}
}
